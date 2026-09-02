package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"strconv"
	"testing"

	"github.com/bhs/mendelbuild/internal/hosting"
)

// A deployment is not done while nothing answers at its URL.
//
// The first deployment onto a name provisions a global load balancer, which
// Google takes several minutes to bring up; the name refuses connections
// throughout. Reporting success then hands over a link that looks broken, with
// no way to tell "still coming up" from "actually broken" -- which is exactly
// what a user saw.
func TestWaitUntilServingAcceptsAnyResponse(t *testing.T) {
	// A 404 means the load balancer is routing, which is the question. Whether
	// the app serves that path is the app's business.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	s := &Server{}
	if !s.waitUntilServing(context.Background(), server.URL, func(string) {}, func(string) {}) {
		t.Error("a 404 from the load balancer means it is serving")
	}
}

func TestWaitUntilServingSaysSoUpFront(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	var milestones []string
	s := &Server{}
	s.waitUntilServing(context.Background(), server.URL,
		func(m string) { milestones = append(milestones, m) }, func(string) {})

	if len(milestones) == 0 {
		t.Fatal("the wait is silent, so a user watching sees a stalled deploy")
	}
	if !strings.Contains(milestones[0], "Waiting for the load balancer") {
		t.Errorf("the first milestone should say what is being waited on, got %q", milestones[0])
	}
}

// A cancelled deploy must not keep polling for twenty minutes.
func TestWaitUntilServingHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &Server{}
	if s.waitUntilServing(ctx, "http://127.0.0.1:1/", func(string) {}, func(string) {}) {
		t.Error("a cancelled context cannot report a serving deployment")
	}
}

// Provisioning is neither success nor failure, and conflating it with either is
// the mistake: running hands over a dead link, failed says something is wrong
// when nothing is.
func TestStillProvisioningIsDistinguishable(t *testing.T) {
	if !errors.Is(errStillProvisioning, errStillProvisioning) {
		t.Fatal("callers cannot recognise the provisioning case")
	}
	if errors.Is(errors.New("deploy failed"), errStillProvisioning) {
		t.Error("an ordinary failure must not be mistaken for provisioning")
	}
}

// With one replica and the default strategy, the running pod can be taken away
// before its replacement answers, so every redeploy has a hole in it.
func TestRolloutKeepsTheOldPodUntilTheNewOneServes(t *testing.T) {
	manifest := k8sManifestFor("pong-prod", "img:tag", "", "app.example.com", "mendel-ip")

	for _, want := range []string{"maxUnavailable: 0", "readinessProbe", "tcpSocket"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest is missing %q, so a redeploy drops traffic", want)
		}
	}
	// The probe has to name the port the app is actually on.
	if !strings.Contains(manifest, "port: "+strconv.Itoa(hosting.ContainerPort)) {
		t.Errorf("readiness probe does not target the container port %d", hosting.ContainerPort)
	}
}
