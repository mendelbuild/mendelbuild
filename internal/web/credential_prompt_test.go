package web

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// TestMissingCredentialsMessageNamesValuesAndPlace covers the wording a user
// sees when they pick a channel and try to validate before supplying its
// credentials.
//
// Naming the values is not enough on its own — the message that shipped did
// that and still left the user on a dead end, because it said nothing about
// where to put them.
func TestMissingCredentialsMessageNamesValuesAndPlace(t *testing.T) {
	msg := missingCredentialsMessage([]string{"GCP_PROJECT_ID", "GKE_ZONE"})

	for _, want := range []string{"GCP_PROJECT_ID", "GKE_ZONE", "Credentials", "validate again"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

// TestUnvalidatedChannelShowsMissingCredentials checks that the message lands on
// the deployment page through the callout the template already has for
// validation failures, rather than as a bare error page with no way back.
func TestUnvalidatedChannelShowsMissingCredentials(t *testing.T) {
	missing := []string{"GCP_PROJECT_ID", "GCP_SERVICE_ACCOUNT_KEY", "GKE_CLUSTER_NAME", "GKE_ZONE"}
	msg := missingCredentialsMessage(missing)
	now := time.Now()

	channel := &domain.ProjectDeploymentChannel{
		ID:                  uuid.New(),
		ProjectID:           uuid.New(),
		ArtifactKind:        domain.DeployArtifactKubernetes,
		CreatedAt:           now,
		DemoValidationError: &msg,
		HostingPlatform:     &domain.HostingPlatform{Slug: "gke", Name: "Google Kubernetes Engine"},
	}

	body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
		"ProjectID": uuid.New().String(),
		"Project":   &domain.Project{ID: uuid.New(), Name: "pong"},
		"Channel":   channel,
	})

	for _, want := range append(missing, "Credentials") {
		if !strings.Contains(body, want) {
			t.Errorf("deployment page does not surface %q", want)
		}
	}
}
