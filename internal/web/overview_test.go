package web

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// The overview exists to answer one question before anything else: what should
// I do right now. These cover the three shapes that answer takes.
func TestOverviewSortsWorkByWhoseMoveItIs(t *testing.T) {
	projectID := uuid.New()

	view := &OverviewView{
		Project:  &domain.Project{ID: projectID, Name: "Pollstar"},
		Strategy: &domain.Strategy{ID: uuid.New(), Name: "Q3 launch"},
		HopCount: 4,
		NeedsYou: []OverviewItem{
			{Kind: "Pick a winner", Title: "Choose a sign-in approach",
				Note: "Compare the finished variations and pick the one to merge, or ask for more.",
				Href: "/p/x/inputs/1", Tone: domain.ToneWaiting},
			{Kind: "Hop", Title: "rate-limiting", Note: "Out of candidates",
				Href: "/p/x/hops/2", Tone: domain.ToneWarning},
		},
		InFlight: []OverviewItem{
			{Kind: "Hop", Title: "user-onboarding", Note: "Building 3 variations",
				Href: "/p/x/hops/3", Tone: domain.ToneProgress},
		},
		Deployment: DeploymentSummary{
			Configured: true, Channel: "container → Fly.io", Href: "/p/x/deployment",
			Status: domain.StatusView{Label: "Ready for demos", Tone: domain.ToneSuccess},
		},
		Cost: &StrategyCostView{
			SpentUSD: 41.82, BudgetUSD: 120,
			ElapsedFraction: 0.61, HasPeriod: true,
		},
	}

	body := renderPageForTest(t, "overview.html", map[string]interface{}{
		"ProjectID": projectID.String(), "View": view,
	})

	for _, want := range []string{
		"Pollstar", "Needs you", "Choose a sign-in approach", "Out of candidates",
		"Mendel is working on", "user-onboarding",
		"Ready for demos", "container → Fly.io",
		"$41.82", "of $120 budgeted",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("overview missing %q", want)
		}
	}

	// The count beside "Needs you" is the number the reader acts on; getting it
	// from a different list than the one rendered would be worse than omitting it.
	if !strings.Contains(body, `badge badge-waiting">2<`) {
		t.Error("the needs-you count should match the list beneath it")
	}
}

// Nothing waiting and nothing running is a real state, and two empty lists is a
// bad way to say it.
func TestOverviewSaysWhenNothingIsHappening(t *testing.T) {
	projectID := uuid.New()
	body := renderPageForTest(t, "overview.html", map[string]interface{}{
		"ProjectID": projectID.String(),
		"View": &OverviewView{
			Project:  &domain.Project{ID: projectID, Name: "Pollstar"},
			Strategy: &domain.Strategy{ID: uuid.New(), Name: "Q3"},
		},
	})

	if !strings.Contains(body, "Nothing is waiting on you") {
		t.Error("an empty needs-you list should say so")
	}
	if !strings.Contains(body, "Nothing is in flight") {
		t.Error("a project with nothing running should say so rather than showing two empty cards")
	}
	if strings.Contains(body, "Mendel is working on") {
		t.Error("the in-flight card should be omitted entirely when there is nothing in it")
	}
}

// A brand-new project has no strategy at all. The onboarding ribbon carries the
// page, and nothing below it should error on the missing pieces.
func TestOverviewRendersBeforeSetup(t *testing.T) {
	projectID := uuid.New()
	body := renderPageForTest(t, "overview.html", map[string]interface{}{
		"ProjectID": projectID.String(),
		"OnboardingRibbon": ribbonView(domain.OnboardingLifecycle(domain.OnboardingState{})),
		"View": &OverviewView{
			Project:    &domain.Project{ID: projectID, Name: "Fresh"},
			Deployment: summarizeDeployment("/p/x/deployment", nil, nil),
		},
	})

	if !strings.Contains(body, "Describe the project") {
		t.Error("a project before setup should show the onboarding ribbon")
	}
	// Without a strategy there is no roadmap to be quiet about, so the
	// nothing-in-flight panel must not fire and tell them to open one.
	if strings.Contains(body, "Nothing is in flight") {
		t.Error("a project that has not been set up is not idle; it is unstarted")
	}
}
