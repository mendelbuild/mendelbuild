package domain

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func ptr(s string) *string { return &s }

// ackRequirement is an acknowledgement whose instructions name the deployment.
func ackRequirement(name string) VariationRequirement {
	return VariationRequirement{
		ID:           uuid.New(),
		Kind:         RequirementKindAcknowledgement,
		Name:         name,
		Instructions: ptr("Add " + DeployURLPlaceholder + "/auth/callback to Authorized redirect URIs."),
	}
}

func TestEvaluateRequirementsSecrets(t *testing.T) {
	stored := VariationRequirement{Kind: RequirementKindSecret, Name: "GOOGLE_CLIENT_SECRET"}
	absent := VariationRequirement{Kind: RequirementKindSecret, Name: "STRIPE_KEY"}

	ev := RequirementEvidence{EnvVarNames: map[string]bool{"GOOGLE_CLIENT_SECRET": true}}
	got := EvaluateRequirements([]VariationRequirement{stored, absent}, ev, "https://app.fly.dev")

	if !got[0].Met {
		t.Error("a secret with a stored value should be met")
	}
	if !got[1].Blocking() {
		t.Error("a secret with no stored value should block the deploy")
	}
}

// A deployment's URL is part of what was acknowledged, not incidental to it.
// Registering the demo's redirect URI says nothing about production's.
func TestAcknowledgementIsPerResolvedValue(t *testing.T) {
	req := ackRequirement("google-redirect-uri")
	demoURL := "https://pong-demo.fly.dev"
	prodURL := "https://pong.fly.dev"

	ev := RequirementEvidence{Acknowledged: map[uuid.UUID]map[string]bool{
		req.ID: {req.ResolvedInstructions(demoURL): true},
	}}

	atDemo := EvaluateRequirements([]VariationRequirement{req}, ev, demoURL)
	if !atDemo[0].Met {
		t.Error("the acknowledged URL should be met")
	}

	atProd := EvaluateRequirements([]VariationRequirement{req}, ev, prodURL)
	if atProd[0].Met {
		t.Error("acknowledging the demo URL must not vouch for production's")
	}
	if !atProd[0].Blocking() {
		t.Error("an unacknowledged production URL should block the deploy")
	}
	if !strings.Contains(atProd[0].ResolvedValue, prodURL) {
		t.Errorf("instructions should name the production URL, got %q", atProd[0].ResolvedValue)
	}
}

// Cloud Run assigns a hash at deploy time and GKE a LoadBalancer IP after
// provisioning, so there is no URL to acknowledge before the first deploy.
// Blocking on one would make the demo unstartable.
func TestUrlDependentAcknowledgementDefersWithoutURL(t *testing.T) {
	req := ackRequirement("google-redirect-uri")

	got := EvaluateRequirements([]VariationRequirement{req}, RequirementEvidence{}, "")
	if !got[0].Deferred {
		t.Fatal("an acknowledgement naming the deploy URL should defer when there is no URL")
	}
	if got[0].Blocking() {
		t.Error("a deferred requirement must not block the first deploy")
	}

	// Once the deployment exists, it is an ordinary unmet requirement.
	got = EvaluateRequirements([]VariationRequirement{req}, RequirementEvidence{}, "https://x.run.app")
	if got[0].Deferred || !got[0].Blocking() {
		t.Error("with a URL known, the acknowledgement should block until confirmed")
	}
}

// An acknowledgement that does not name the deployment — "enable the People
// API" — is answerable before anything is deployed.
func TestAcknowledgementWithoutURLIsJudgedImmediately(t *testing.T) {
	req := VariationRequirement{
		ID:           uuid.New(),
		Kind:         RequirementKindAcknowledgement,
		Name:         "enable-people-api",
		Instructions: ptr("Enable the People API for your GCP project."),
	}

	got := EvaluateRequirements([]VariationRequirement{req}, RequirementEvidence{}, "")
	if got[0].Deferred {
		t.Error("an acknowledgement independent of the URL should not defer")
	}
	if !got[0].Blocking() {
		t.Error("it should block until confirmed")
	}
}

func TestResolvedInstructionsKeepsPlaceholderWithoutURL(t *testing.T) {
	req := ackRequirement("google-redirect-uri")
	if got := req.ResolvedInstructions(""); !strings.Contains(got, DeployURLPlaceholder) {
		t.Errorf("with no URL the placeholder should survive rather than leaving a hole: %q", got)
	}
}
