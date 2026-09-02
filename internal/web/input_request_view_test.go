package web

import (
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/domain"
)

// A request must never be shown another kind's decision form.
//
// The template defaulted to the roadmap review, so a manual setup step -- work
// that by definition happens somewhere Mendel cannot reach -- rendered "Approve
// and create the Hops", "Reject this roadmap" and "Edit the proposal directly
// (JSON)" over a request with no hops and nothing to approve. It failed silently,
// because a page rendering the wrong form still renders perfectly well.
//
// So: only the roadmap kind gets the roadmap view, and every kind's view exists
// and parses.
func TestEveryKindHasItsOwnView(t *testing.T) {
	for _, kind := range domain.AllInputRequestKinds() {
		name := templateForKind(kind)

		if name == "" {
			t.Errorf("%s has no view at all", kind)
			continue
		}
		if name == "input_request_roadmap.html" && kind != domain.InputRequestKindRoadmapReview {
			t.Errorf("%s renders the roadmap review, which offers to create Hops it does not have", kind)
		}

		// A view that does not parse is a 500 on a page nothing else covers.
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: %s does not parse: %v", kind, name, r)
				}
			}()
			parsePageTemplate(name)
		}()
	}
}

// The kinds that carry an action must not be quietly demoted to the view that
// offers none -- the opposite mistake, and just as silent.
func TestActionableKindsKeepTheirOwnView(t *testing.T) {
	for _, kind := range []domain.InputRequestKind{
		domain.InputRequestKindRoadmapReview,
		domain.InputRequestKindVariationReview,
		domain.InputRequestKindVariationSelection,
		domain.InputRequestKindCredentialRequest,
		domain.InputRequestKindMeasurement,
		domain.InputRequestKindHostingPlatform,
		domain.InputRequestKindManualSetup,
	} {
		if got := templateForKind(kind); got == "input_request_generic.html" {
			t.Errorf("%s has an action to offer but renders the do-nothing view", kind)
		}
	}
}

// Manual setup is closed by Mendel observing the work was done, so the page must
// not offer to approve it, reject it, or mark it off. A tick box would accept a
// record typed wrongly and surface the mistake much later as something else
// failing -- which is the reason these are observed rather than acknowledged.
func TestManualSetupOffersNoDecisionButtons(t *testing.T) {
	body := readTemplateFile(t, templateForKind(domain.InputRequestKindManualSetup))

	for _, forbidden := range []string{
		"Approve and create the Hops",
		"Reject this roadmap",
		"Edit the proposal directly",
		"mark it done",
		"/approve",
		"/reject",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the manual setup view offers %q, which it cannot honour", forbidden)
		}
	}
	if !strings.Contains(body, "Mendel checks this itself") {
		t.Error("the manual setup view should say how it closes, since nothing on it closes it")
	}
}

func readTemplateFile(t *testing.T, name string) string {
	t.Helper()
	body, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(body)
}
