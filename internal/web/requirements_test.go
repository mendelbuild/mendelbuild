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
		Variation:            variation,
		Hop:                  hop,
		Ribbon:               domain.VariationLifecycle(variation, nil, hop),
		Requirements:         requirements,
		RequirementsBlocked:  true,
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
