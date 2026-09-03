// mendel-assigner places a visitor in an experiment Arm.
//
// It runs beside the user's application as the route's fallback: a request with
// no Arm cookie reaches it, it works out an Arm, sets the cookie and redirects
// back. Every request after that is routed by plain header matching and never
// touches this again.
//
// It reads its allocation from a file rather than from Mendel. Production
// traffic must not depend on Mendel being reachable -- if Mendel is down, the
// last-written allocation stands.
package main

import (
	"bytes"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/bhs/mendelbuild/internal/assigner"
)

func main() {
	var (
		addr     = flag.String("addr", ":8080", "address to serve on")
		path     = flag.String("allocation", "/etc/mendel/allocation.json", "file holding the allocation")
		insecure = flag.Bool("insecure-cookie", false,
			"omit Secure on the Arm cookie; only for a site served over plain http")
		poll = flag.Duration("poll", 10*time.Second, "how often to re-read the allocation")
	)
	flag.Parse()

	data, err := os.ReadFile(*path)
	if err != nil {
		log.Fatalf("cannot read the allocation at %s: %v", *path, err)
	}
	alloc, err := assigner.ParseAllocation(data)
	if err != nil {
		// Refusing to start is right: serving with no allocation would put every
		// visitor in mainline while looking healthy, which reads as an
		// experiment that found no effect.
		log.Fatalf("%v", err)
	}

	handler := assigner.NewHandler(alloc, !*insecure)
	go watch(*path, *poll, data, handler)

	log.Printf("assigning on %s from %s (%d arms)", *addr, *path, len(alloc.Arms))
	server := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

// watch re-reads the allocation when it changes.
//
// Polling rather than a filesystem watch: Kubernetes updates a mounted ConfigMap
// by swapping a symlink, which most watch APIs report inconsistently. Comparing
// the bytes is cheap, obvious, and cannot miss an update.
//
// A file that becomes unreadable or invalid is logged and ignored. The
// allocation already in memory is the last one known good, and continuing to
// serve it is better than either failing requests or falling back to something
// nobody chose.
func watch(path string, every time.Duration, current []byte, h *assigner.Handler) {
	for range time.Tick(every) {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("allocation unreadable, keeping the one in memory: %v", err)
			continue
		}
		if bytes.Equal(data, current) {
			continue
		}
		alloc, err := assigner.ParseAllocation(data)
		if err != nil {
			log.Printf("new allocation rejected, keeping the one in memory: %v", err)
			continue
		}
		current = data
		h.Set(alloc)
		log.Printf("allocation updated: %d arms", len(alloc.Arms))
	}
}
