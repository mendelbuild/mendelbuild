package domain

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestDeployURLLimitation covers the URLs Mendel hands out and whether an
// identity provider will accept them.
//
// The GKE case is the one that bit: a demo is reached at its LoadBalancer's
// address, so Mendel told the user to register http://34.56.24.112/... with
// Google, which refuses raw IPs and refuses plain http. Nothing said so, and the
// refusal happened in Google's console.
func TestDeployURLLimitation(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		limited bool
		mention string
	}{
		{"fly gives a hostname over https", "https://pong-abc123.fly.dev", false, ""},
		{"cloud run likewise", "https://svc-745034772195.us-central1.run.app", false, ""},
		{"the GKE LoadBalancer address", "http://34.56.24.112", true, "bare IP"},
		{"an IP even over https", "https://34.56.24.112", true, "host name"},
		{"a hostname but plain http", "http://demo.example.com", true, "https"},
		{"localhost is exempt from both", "http://localhost:8080", false, ""},
		{"so is the loopback address", "http://127.0.0.1:8080", false, ""},
		{"nothing to judge yet", "", false, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DeployURLLimitation(c.url)
			if c.limited && got == "" {
				t.Fatalf("%s should be reported as unusable, got no limitation", c.url)
			}
			if !c.limited && got != "" {
				t.Fatalf("%s is usable, but got: %s", c.url, got)
			}
			if c.mention != "" && !strings.Contains(got, c.mention) {
				t.Errorf("message does not mention %q: %s", c.mention, got)
			}
		})
	}
}

// TestEvaluateRequirementsFlagsUnusableDeployURL checks the limitation reaches
// the status the page renders, and only for requirements that actually name the
// deployment.
func TestEvaluateRequirementsFlagsUnusableDeployURL(t *testing.T) {
	redirect := "Add " + DeployURLPlaceholder + "/auth/google/callback to Authorized redirect URIs."
	elsewhere := "Enable the Places API for the project."

	reqs := []VariationRequirement{
		{ID: uuid.New(), Kind: RequirementKindAcknowledgement, Name: "google-redirect-uri", Instructions: &redirect},
		{ID: uuid.New(), Kind: RequirementKindAcknowledgement, Name: "places-api", Instructions: &elsewhere},
	}
	ev := RequirementEvidence{EnvVarNames: map[string]bool{}, Acknowledged: map[uuid.UUID]map[string]bool{}}

	statuses := EvaluateRequirements(reqs, ev, "http://34.56.24.112")

	if statuses[0].Limitation == "" {
		t.Error("a redirect URI pointing at a bare IP should be flagged")
	}
	if statuses[1].Limitation != "" {
		t.Errorf("a requirement that never names the deployment should not be flagged: %s",
			statuses[1].Limitation)
	}

	// On a channel that hands out a hostname the same requirement is fine, so
	// the flag has to follow the URL rather than the requirement.
	onFly := EvaluateRequirements(reqs, ev, "https://pong-abc123.fly.dev")
	if onFly[0].Limitation != "" {
		t.Errorf("no limitation should be reported for an https hostname: %s", onFly[0].Limitation)
	}
}
