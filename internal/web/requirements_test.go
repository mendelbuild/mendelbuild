package web

import (
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/google/uuid"
)

// Fly.io's hostname follows from the app name, so a redirect URI can be
// registered before the first deploy. The other platforms assign theirs at
// deploy time, and claiming otherwise would have the user register a URL the
// app never answers on.
func TestPredictedDeployURL(t *testing.T) {
	if got := predictedDeployURL("fly-io", "pong-game-0e30d7df"); got != "https://pong-game-0e30d7df.fly.dev" {
		t.Errorf("fly-io URL = %q", got)
	}
	for _, slug := range []string{"cloud-run", "gke", "something-new"} {
		if got := predictedDeployURL(slug, "pong-game"); got != "" {
			t.Errorf("%s should not claim to know the URL in advance, got %q", slug, got)
		}
	}
}

// The requirements section is what the user acts on when a demo is blocked, so
// it must render every state: a stored secret, one still needed, an
// acknowledgement carrying the real URL, and one waiting for a URL to exist.
func TestVariationDetailRendersRequirements(t *testing.T) {
	projectID := uuid.New()
	hop := &domain.Hop{ID: uuid.New(), Name: "auth-refactor", Commentary: "Wire up sign-in."}
	variation := &domain.Variation{ID: uuid.New(), HopID: hop.ID, Name: "google-oauth",
		Status: domain.VariationStatusPending}

	instructions := "Add " + domain.DeployURLPlaceholder + "/auth/callback to Authorized redirect URIs."
	console := "https://console.cloud.google.com/apis/credentials"
	deferredInstructions := "Add " + domain.DeployURLPlaceholder + "/logout to Authorized redirects."

	requirements := []domain.RequirementStatus{
		{
			Requirement: domain.VariationRequirement{ID: uuid.New(), Kind: domain.RequirementKindSecret,
				Name: "GOOGLE_CLIENT_SECRET", ConsoleURL: &console},
			Met: true,
		},
		{
			Requirement: domain.VariationRequirement{ID: uuid.New(), Kind: domain.RequirementKindSecret,
				Name: "GOOGLE_CLIENT_ID"},
		},
		{
			Requirement: domain.VariationRequirement{ID: uuid.New(), Kind: domain.RequirementKindAcknowledgement,
				Name: "google-redirect-uri", Instructions: &instructions, ConsoleURL: &console},
			ResolvedValue: strings.ReplaceAll(instructions, domain.DeployURLPlaceholder, "https://demo.fly.dev"),
		},
		{
			Requirement: domain.VariationRequirement{ID: uuid.New(), Kind: domain.RequirementKindAcknowledgement,
				Name: "google-logout-uri", Instructions: &deferredInstructions},
			Deferred: true,
		},
	}

	view := &VariationDetailView{
		Variation: variation,
		Hop:       hop,
		Ribbon:    domain.VariationLifecycle(variation, nil, hop),
		Requirements: &RequirementsPanel{
			Title:        "What This Needs to Run",
			Intro:        "This variation's code cannot function without the following.",
			ActionBase:   "/p/" + projectID.String() + "/variations/" + variation.ID.String() + "/requirements",
			DeferredNote: "Start the demo, then come back to confirm it.",
			Statuses:     requirements,
		},
		HasDeploymentChannel: true,
		IsDemoValidated:      true,
	}

	body := renderForTest(t, "variation_detail.html", projectID, view)

	for _, want := range []string{
		"What This Needs to Run",
		"GOOGLE_CLIENT_SECRET",
		"GOOGLE_CLIENT_ID",
		"https://demo.fly.dev/auth/callback", // the resolved instruction, not the placeholder
		"Not Ready to Run",                   // the demo panel defers to the section above
	} {
		if !strings.Contains(body, want) {
			t.Errorf("variation detail missing %q", want)
		}
	}

	if strings.Contains(body, domain.DeployURLPlaceholder) {
		t.Error("an unresolved {{deploy_url}} reached the page")
	}

	// Both acknowledgements are listed, but only the one whose URL is known
	// offers a button — there is nothing the user could confirm about the
	// other yet.
	if !strings.Contains(body, "google-logout-uri") {
		t.Error("a deferred acknowledgement should still be listed")
	}
	if n := strings.Count(body, "I've done this"); n != 1 {
		t.Errorf("expected exactly one actionable acknowledgement, got %d", n)
	}
}

// Production is gated on the merged variations' requirements, and production's
// redirect URI is a different string from any demo's. Without a panel on the
// deployment page there is nowhere to confirm it, and the deploy fails with
// "unconfirmed setup steps" and no way to act on it.
func TestDeploymentPageRendersProdRequirements(t *testing.T) {
	projectID := uuid.New()
	reqID := uuid.New()
	instructions := "Add " + domain.DeployURLPlaceholder + "/auth/callback to Authorized redirect URIs."

	panel := &RequirementsPanel{
		Title:        "What Production Needs to Run",
		Intro:        "The code merged to main cannot run in production without the following.",
		ActionBase:   "/p/" + projectID.String() + "/requirements",
		DeferredNote: "Deploy once, then come back to confirm it.",
		Statuses: []domain.RequirementStatus{{
			Requirement: domain.VariationRequirement{ID: reqID, Kind: domain.RequirementKindAcknowledgement,
				Name: "google-redirect-uri", Instructions: &instructions},
			ResolvedValue: strings.ReplaceAll(instructions, domain.DeployURLPlaceholder, "https://pong-prod.fly.dev"),
		}},
	}

	body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
		"Title":            "Deployment",
		"ProjectID":        projectID.String(),
		"Project":          &domain.Project{ID: projectID, Name: "pong"},
		"Channel":          validatedChannel(),
		"ProdRequirements": panel,
	})

	for _, want := range []string{
		"What Production Needs to Run",
		"google-redirect-uri",
		"https://pong-prod.fly.dev/auth/callback", // production's URI, not the demo's
		// The form must post to the project-scoped route; the variation-scoped
		// one is not reachable from this page.
		"/p/" + projectID.String() + "/requirements/" + reqID.String() + "/acknowledge",
		"I've done this",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("deployment page missing %q", want)
		}
	}
}

// A nil panel must render nothing rather than erroring the page, since most
// projects require nothing at all.
func TestDeploymentPageWithoutRequirements(t *testing.T) {
	projectID := uuid.New()
	body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
		"Title":            "Deployment",
		"ProjectID":        projectID.String(),
		"Project":          &domain.Project{ID: projectID, Name: "pong"},
		"Channel":          validatedChannel(),
		"ProdRequirements": (*RequirementsPanel)(nil),
	})
	if strings.Contains(body, "What Production Needs to Run") {
		t.Error("a project with no requirements should render no panel")
	}
	if !strings.Contains(body, "Deploy main to Production") {
		t.Error("the rest of the page should still render")
	}
}

// Nil-safety for the methods the templates call on a possibly-absent panel.
func TestRequirementsPanelNilIsHarmless(t *testing.T) {
	var p *RequirementsPanel
	if p.HasAny() {
		t.Error("nil panel has nothing to show")
	}
	if p.Blocked() {
		t.Error("nil panel blocks nothing")
	}
}
