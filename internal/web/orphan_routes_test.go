package web

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// No route may be registered that nothing refers to.
//
// Be clear about what this is and is not, because its first name claimed more
// than it does. It compares two lists that are already written down — the GET
// routes the router registers, and the paths the templates and handlers contain
// — and complains about anything in the first that is missing from the second.
//
// That is a reference check, closer to hunting dead code than to reachability.
// It does NOT establish that a person can get to a page. In particular it will
// not notice:
//
//   - a link inside a branch that never renders. Log out sits in {{if .User}},
//     and when .User stopped being set the link disappeared from every page in
//     the app while the template still plainly contained the path.
//   - a link only reachable from a page that is itself unreachable, since it
//     never asks how the linking page is arrived at.
//   - a capability lost without a route being lost. The open-request badge
//     vanished everywhere and no route changed; it is a span, not a page.
//
// Of the four such faults found in September 2026, it catches one: /costs,
// which shipped registered and linked from nothing. That is worth 150 lines and
// no dependencies, but it is one class of fault, not the family.
//
// Establishing the real property needs a crawl of rendered pages from the front
// door, which needs a seeded project, which needs fixtures that encode the data
// model. Worth doing once that model settles; see the note in memory about
// deferring frontend work until then.

// notLinked are routes deliberately reachable only by typing the address or by
// redirect. Each needs a reason: an unexplained entry here is how a route stops
// being unreachable-by-accident and becomes unreachable-on-purpose.
var notLinked = map[string]string{
	"/health":              "probed by the hosting platform, not by a person",
	"/version":             "probed after a deploy to confirm which build is live",
	"/styleguide":          "a developer surface, deliberately absent from the app's own navigation",
	"/p/{projectID}/debug": "a JSON diagnostic dump, read by hand when something is wrong",
}

func TestNoOrphanedRoutes(t *testing.T) {
	registered := registeredGETRoutes(t)
	linked := linkedPaths(t)

	var unreachable []string
	for _, route := range registered {
		if _, ok := notLinked[route]; ok {
			continue
		}
		if !linked[normalisePath(route)] {
			unreachable = append(unreachable, route)
		}
	}
	sort.Strings(unreachable)
	for _, route := range unreachable {
		t.Errorf("%s is registered but no template or handler refers to it; "+
			"it can only be reached by typing the address", route)
	}

	// An exemption for a route that no longer exists is a stale excuse, and the
	// next genuinely unreachable page would inherit it.
	for route := range notLinked {
		found := false
		for _, r := range registered {
			if r == route {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is exempted from reachability but is not a registered route", route)
		}
	}
}

// registeredGETRoutes walks the real router, so the list is what the app serves
// rather than what a test remembered to mention.
func registeredGETRoutes(t *testing.T) []string {
	t.Helper()
	s := &Server{}
	s.setupRoutes()

	var out []string
	err := chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != http.MethodGet {
			return nil
		}
		// Wildcards are file servers and API feeds, not pages.
		if strings.Contains(route, "*") || strings.HasPrefix(route, "/api/") ||
			strings.HasPrefix(route, "/auth/") {
			return nil
		}
		out = append(out, strings.TrimSuffix(route, "/"))
		return nil
	})
	if err != nil {
		t.Fatalf("walking routes: %v", err)
	}
	return out
}

var (
	// Anything that stands in for an ID: a chi parameter, a template action, or
	// a format verb.
	holeRE = regexp.MustCompile(`\{\{[^}]*\}\}|\{[^}]*\}|%[sdv]`)
	// Paths as they appear in templates and in Go.
	pathRE = regexp.MustCompile(`"(/[a-zA-Z0-9._/{}%$-]*)"`)
	// Where routes are declared rather than linked to.
	routeDeclRE = regexp.MustCompile(`\br\.(Get|Post|Put|Delete|Patch|Route|Handle|Method)\(`)
)

// normalisePath reduces a path to its shape, so /p/{projectID}/costs and
// /p/{{.ProjectID}}/costs are recognisably the same destination.
func normalisePath(p string) string {
	p = holeRE.ReplaceAllString(p, "*")
	return strings.TrimSuffix(p, "/")
}

// linkedPaths collects every path the templates and handlers emit. Templates
// carry most links; the rest are built in Go, where a page's primary action
// gets its URL.
func linkedPaths(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, dir := range []string{"templates", "."} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !(strings.HasSuffix(name, ".html") ||
				(strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go"))) {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}
			for _, line := range strings.Split(string(body), "\n") {
				// A route's own registration is not a link to it. Counting it
				// as one made this check pass for every route in the table,
				// which is every route there is -- the test looked green and
				// asserted nothing.
				if routeDeclRE.MatchString(line) {
					continue
				}
				for _, m := range pathRE.FindAllStringSubmatch(line, -1) {
					out[normalisePath(m[1])] = true
				}
			}
		}
	}
	return out
}
