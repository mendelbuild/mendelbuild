package web

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// Things that must be true of every page, whatever it is about.
//
// Three of the four faults found in September 2026 were of this kind: a
// capability disappearing from pages that still rendered perfectly. Log out
// vanished from the whole app because the signed-in user stopped being stamped;
// the open-request count went the same way. No route changed, so a route check
// could not see either.
//
// These assert on what a reader can do rather than on what the markup says, so
// a redesign that moves every word leaves them alone. They render through
// renderPageFor -- the real chrome path -- because the faults were in the
// stamping, not in the templates.

// renderChrome renders a page the way a handler does, with a signed-in reader.
func renderChrome(t *testing.T, page, path string, data map[string]interface{}) string {
	t.Helper()
	user := &domain.User{ID: uuid.New(), Email: "ben@example.com"}
	req := httptest.NewRequest("GET", path, nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))

	rec := httptest.NewRecorder()
	// A server with no database: the chrome that matters here needs none.
	s := &Server{}
	if err := s.renderPageFor(rec, req, page, data); err != nil {
		t.Fatalf("rendering %s: %v", page, err)
	}
	return rec.Body.String()
}

// Whoever is signed in must be able to say who they are and to leave. This went
// missing on every page in the app at once, and nothing failed.
func TestSignedInReaderCanAlwaysLogOut(t *testing.T) {
	projectID := uuid.New().String()

	// One page per shape: a project page, the projects list, and a page whose
	// own data is absent, since a handler that fails to load its subject still
	// renders the chrome around it.
	for _, page := range []struct{ name, path string; data map[string]interface{} }{
		{"strategy.html", "/p/" + projectID + "/strategy", map[string]interface{}{"ProjectID": projectID}},
		{"dashboard.html", "/", map[string]interface{}{}},
		{"hop_detail.html", "/p/" + projectID + "/hops/x", map[string]interface{}{"ProjectID": projectID}},
	} {
		body := renderChrome(t, page.name, page.path, page.data)
		if !strings.Contains(body, "ben@example.com") {
			t.Errorf("%s does not say who is signed in", page.name)
		}
		if !strings.Contains(body, `href="/auth/logout"`) {
			t.Errorf("%s offers no way to log out", page.name)
		}
	}
}

// From inside a project, every one of its sections stays one click away. The
// nav is the only thing that guarantees this, so it is the only thing that has
// to be checked.
func TestEveryProjectPageOffersTheProjectSections(t *testing.T) {
	projectID := uuid.New().String()
	body := renderChrome(t, "strategy.html", "/p/"+projectID+"/strategy",
		map[string]interface{}{"ProjectID": projectID})

	for _, section := range []string{"", "/strategy", "/inputs", "/costs", "/settings"} {
		want := `href="/p/` + projectID + section + `"`
		if !strings.Contains(body, want) {
			t.Errorf("the nav does not reach %q", "/p/{id}"+section)
		}
	}
	// And out of the project entirely.
	if !strings.Contains(body, `href="/"`) {
		t.Error("no way back to the projects list")
	}
}

// A breadcrumb that leads nowhere is decoration. Every one must contain a link
// out of the page it is on.
//
// The weakest of the three, and worth saying so: it checks that a trail leads
// somewhere, not that it leads anywhere sensible. A breadcrumb pointing at the
// wrong ancestor passes. It costs four lines and catches the trail going inert,
// which is the failure that actually happens when markup is rearranged.
func TestBreadcrumbsLeadSomewhere(t *testing.T) {
	crumb := regexp.MustCompile(`(?s)<nav class="breadcrumb">(.*?)</nav>`)
	link := regexp.MustCompile(`<a\s+href=`)

	entries, err := os.ReadDir("templates")
	if err != nil {
		t.Fatalf("reading templates: %v", err)
	}
	found := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("templates", e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		for _, m := range crumb.FindAllStringSubmatch(string(body), -1) {
			found++
			if !link.MatchString(m[1]) {
				t.Errorf("%s has a breadcrumb with no link in it", e.Name())
			}
		}
	}
	if found == 0 {
		t.Fatal("found no breadcrumbs at all; this check has stopped checking anything")
	}
}
