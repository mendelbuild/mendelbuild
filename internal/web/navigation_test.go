package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The navigation is information architecture made visible, so it gets the same
// treatment as the templates: rules, checked.
//
// The bug these exist to prevent actually happened. /costs was added as a page,
// linked from a single card, and never put in the nav or in navSection — so it
// was reachable by one route and highlighted nothing when you got there.

const testProject = "11111111-2222-3333-4444-555555555555"

// navLinks reads layout.html and returns each project-scoped nav destination as
// (href, the section it lights up).
func navLinks(t *testing.T) map[string]string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("templates", "layout.html"))
	if err != nil {
		t.Fatalf("reading layout: %v", err)
	}

	// Each nav link names the section it lights up and where it goes:
	//   <a class="nav-link{{if eq $nav "x"}} is-active{{end}}" href="...">
	section := regexp.MustCompile(`eq \$?\.?[Nn]av "([a-z]+)"`)
	href := regexp.MustCompile(`href="([^"]+)"`)

	out := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.Contains(line, `class="nav-link`) {
			continue
		}
		sm := section.FindStringSubmatch(line)
		hm := href.FindStringSubmatch(line)
		if sm == nil || hm == nil {
			continue
		}
		out[strings.ReplaceAll(hm[1], "{{.ProjectID}}", testProject)] = sm[1]
	}
	if len(out) == 0 {
		t.Fatal("found no nav links in layout.html; the rules below would pass vacuously")
	}
	return out
}

// Every destination in the nav must light itself up when you arrive at it.
// A link that highlights nothing leaves the reader with no answer to "where am
// I", which is the one question navigation exists to answer.
func TestEveryNavLinkHighlightsItself(t *testing.T) {
	for href, section := range navLinks(t) {
		if got := navSection(href); got != section {
			t.Errorf("nav link %q claims section %q but navSection says %q", href, section, got)
		}
	}
}

// Every page a person can reach must belong to a section the nav can show. A
// page mapping to "" renders with nothing highlighted, which is how /costs
// shipped as an orphan.
func TestEveryPageBelongsToANavSection(t *testing.T) {
	sections := map[string]bool{}
	for _, section := range navLinks(t) {
		sections[section] = true
	}

	// Every project-scoped page, with the section it should sit under. This
	// list is also the inventory: adding a page means adding a line here, which
	// is the moment to notice it has nowhere to live.
	pages := map[string]string{
		"/p/" + testProject:                       "overview",
		"/p/" + testProject + "/strategy":         "strategy",
		"/p/" + testProject + "/okr":              "strategy",
		"/p/" + testProject + "/roadmap":          "strategy",
		"/p/" + testProject + "/hops/abc":         "strategy",
		"/p/" + testProject + "/variations/abc":   "strategy",
		"/p/" + testProject + "/setup/okrs":       "strategy",
		"/p/" + testProject + "/inputs":           "inputs",
		"/p/" + testProject + "/inputs/abc":       "inputs",
		"/p/" + testProject + "/costs":            "costs",
		"/p/" + testProject + "/settings":         "settings",
		"/p/" + testProject + "/deployment":       "settings",
	}

	for path, want := range pages {
		got := navSection(path)
		if got != want {
			t.Errorf("navSection(%q) = %q, want %q", path, got, want)
		}
		if !sections[got] {
			t.Errorf("page %q maps to section %q, which is not in the nav", path, got)
		}
	}
}

// Objectives, the Hop DAG and the pages beneath them all belong to one
// Strategy. DESIGN.md section 2.1 says so, and the nav should agree: walking
// into a Variation must not make the Strategy tab go dark, because you have not
// left the Strategy.
func TestStrategyStaysLitAllTheWayDown(t *testing.T) {
	for _, path := range []string{
		"/p/" + testProject + "/strategy",
		"/p/" + testProject + "/okr",
		"/p/" + testProject + "/okr/objectives/abc",
		"/p/" + testProject + "/roadmap",
		"/p/" + testProject + "/hops/abc",
		"/p/" + testProject + "/variations/abc",
	} {
		if got := navSection(path); got != "strategy" {
			t.Errorf("navSection(%q) = %q, want %q", path, got, "strategy")
		}
	}
}

// Project-scoped pages must not be mistaken for the projects list, and vice
// versa. These share a prefix in the worst possible way: "/" is a prefix of
// everything.
func TestTopLevelRoutes(t *testing.T) {
	cases := map[string]string{
		"/":            "projects",
		"/new":         "projects",
		"/styleguide":  "",
		"/auth/login":  "",
	}
	for path, want := range cases {
		if got := navSection(path); got != want {
			t.Errorf("navSection(%q) = %q, want %q", path, got, want)
		}
	}
}

// Every page below the top level has to say where it sits and offer a way back
// up. Without this a reader who arrives from a link has no idea what contains
// what — which is most of what made the app hard to navigate.
func TestDetailPagesCarryABreadcrumb(t *testing.T) {
	needsCrumb := []string{
		"hop_detail.html",
		"variation_detail.html",
		"okr_editor.html",
		"input_request_credential.html",
		"input_request_hosting.html",
		"input_request_roadmap.html",
		"input_request_selection.html",
		"input_request_variation.html",
	}
	for _, name := range needsCrumb {
		body, err := os.ReadFile(filepath.Join("templates", name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		text := string(body)
		// The Decision pages get theirs from the shared decision-head partial.
		if strings.Contains(text, `class="breadcrumb"`) || strings.Contains(text, `"decision-head"`) {
			continue
		}
		t.Errorf("%s is a detail page with no breadcrumb", name)
	}
}
