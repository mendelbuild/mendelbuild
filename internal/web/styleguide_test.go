package web

import (
	"os"
	"strings"
	"testing"
)

// TestStyleguideRenders executes every component and every lifecycle specimen.
// A bad field path in a template only fails at execution time, so parsing alone
// would not catch it — and the styleguide is the one page that touches every
// component, which makes this the cheapest broad guard the package has.
//
// Set MENDEL_STYLEGUIDE_OUT to write the rendered page to a file and open it in
// a browser without standing up a database:
//
//	MENDEL_STYLEGUIDE_OUT=/tmp/sg/index.html go test ./internal/web/ -run Styleguide
func TestStyleguideRenders(t *testing.T) {
	body := renderPageForTest(t, "styleguide.html", map[string]interface{}{
		"Title":      "Styleguide",
		"Tones":      []string{"neutral", "progress", "waiting", "success", "warning", "failure"},
		"Lifecycles": lifecycleSpecimens(),
		"LogPanel":   styleguideLogPanel(),
	})

	if out := os.Getenv("MENDEL_STYLEGUIDE_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
			t.Fatalf("writing styleguide: %v", err)
		}
	}

	for _, want := range []string{
		"Mendel styleguide",
		// Every tone reaches the page.
		"badge-neutral", "badge-progress", "badge-waiting",
		"badge-success", "badge-warning", "badge-failure",
		// The lifecycle inventory is present and came from the real functions.
		"Out of candidates",           // Hop, active-but-stuck
		"Applying your feedback",      // Variation, revision in flight
		"Serving live traffic",        // Variation, production trial shape
		"Demo available",              // Variation, demo trial shape
		"Compare the finished variations", // Decision, variation_selection ask
		"Describe the project",        // Onboarding
	} {
		if !strings.Contains(body, want) {
			t.Errorf("styleguide missing %q", want)
		}
	}

	// The point of the exercise: an unrecognized status degrades to a sentence,
	// never to a bare enum sitting alone on the page.
	if !strings.Contains(body, "Unrecognized state") {
		t.Error("styleguide should exercise the unknown-status degradation path")
	}
}
