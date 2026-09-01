package web

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// TestCredentialFormAsksForTheNamedValues covers the form a deployment channel
// puts in front of someone who has not supplied its credentials yet.
//
// The form used to offer one blank Name box with RENDER_API_KEY as its
// placeholder, whatever platform had actually been chosen — so a GKE user was
// shown an example from a platform they were not using and left to work out the
// other three names themselves.
func TestCredentialFormAsksForTheNamedValues(t *testing.T) {
	projectID := uuid.New()
	required := []string{"GCP_PROJECT_ID", "GCP_SERVICE_ACCOUNT_KEY", "GKE_CLUSTER_NAME", "GKE_ZONE"}

	view := &InputRequestDetailView{
		InputRequest: &domain.InputRequest{
			ID: uuid.New(), ProjectID: projectID,
			Kind:                 domain.InputRequestKindCredentialRequest,
			Title:                deploymentCredentialRequestTitle,
			Status:               domain.InputRequestStatusNeedsAssignment,
			RequiredCapabilities: required,
		},
		SetupScript: "PROJECT=your-project-id\ngcloud iam service-accounts create mendel-deployer",
	}

	body := renderForTest(t, "input_request_credential.html", projectID, view)

	for _, name := range required {
		if !strings.Contains(body, `name="value_`+name+`"`) {
			t.Errorf("form has no field for %s", name)
		}
	}
	if strings.Contains(body, "RENDER_API_KEY") {
		t.Error("form still shows the Render placeholder on a GKE request")
	}

	// The script is the part that leaves the page for a terminal, so it has to
	// arrive as something copyable rather than as prose.
	if !strings.Contains(body, `id="setup-script"`) {
		t.Error("setup script is not rendered as a copyable block")
	}
	if !strings.Contains(body, "gcloud iam service-accounts create mendel-deployer") {
		t.Error("setup script contents missing")
	}
}

// TestCredentialFormFallsBackToAFreeFormName keeps the older shape working for
// requests that never named what they wanted.
func TestCredentialFormFallsBackToAFreeFormName(t *testing.T) {
	projectID := uuid.New()

	view := &InputRequestDetailView{
		InputRequest: &domain.InputRequest{
			ID: uuid.New(), ProjectID: projectID,
			Kind:   domain.InputRequestKindCredentialRequest,
			Title:  "Provide the credential",
			Status: domain.InputRequestStatusNeedsAssignment,
		},
	}

	body := renderForTest(t, "input_request_credential.html", projectID, view)

	if !strings.Contains(body, `name="credential_name"`) {
		t.Error("a request naming nothing should still offer a name field")
	}
	if strings.Contains(body, `id="setup-script"`) {
		t.Error("no script was supplied, so none should be rendered")
	}
}
