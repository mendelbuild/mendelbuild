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
		SetupScript:      "export GCP_PROJECT=<YOUR_PROJECT_ID_HERE>\ngcloud iam service-accounts create mendel-deployer",
		SetupScriptLines: markUpSetupScript("export GCP_PROJECT=<YOUR_PROJECT_ID_HERE>\n# a comment\ngcloud iam service-accounts create mendel-deployer"),
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
	if !strings.Contains(body, `data-copy="setup-script"`) {
		t.Error("no copy control points at the script")
	}

	// The line the user must edit has to be visually distinct, since pasting it
	// unchanged is the mistake the block exists to prevent.
	if !strings.Contains(body, "script-line-edit") {
		t.Error("the line needing an edit is not called out")
	}

	// What the copy button reads must be the script verbatim. If the highlighted
	// block were the source, the markup would travel with it.
	if strings.Contains(body, `id="setup-script" class=`) {
		t.Error("the copy source is the marked-up block rather than the raw script")
	}
}

// TestSetupScriptRendersSingleSpaced guards the spacing of the rendered block.
//
// Each line is its own block element, so the markup must not also carry a
// newline between them: a <pre> preserves that newline, the block starts a line
// of its own, and every script renders double-spaced. It reads as a styling
// nicety and is really a legibility bug -- a screen of commands at double
// spacing is half a screen of commands.
func TestSetupScriptRendersSingleSpaced(t *testing.T) {
	projectID := uuid.New()
	view := &InputRequestDetailView{
		InputRequest: &domain.InputRequest{
			ID: uuid.New(), ProjectID: projectID,
			Kind:   domain.InputRequestKindCredentialRequest,
			Title:  deploymentCredentialRequestTitle,
			Status: domain.InputRequestStatusNeedsAssignment,
		},
		SetupScript:      "export GCP_PROJECT=<YOUR_PROJECT_ID_HERE>\ngcloud services enable foo\n\ngcloud iam bar",
		SetupScriptLines: markUpSetupScript("export GCP_PROJECT=<YOUR_PROJECT_ID_HERE>\ngcloud services enable foo\n\ngcloud iam bar"),
	}

	body := renderForTest(t, "input_request_credential.html", projectID, view)

	start := strings.Index(body, `<pre class="script-body">`)
	end := strings.Index(body[start:], "</pre>") + start
	block := body[start:end]

	if strings.Contains(block, "</span>\n") {
		t.Error("a newline between line spans renders the script double-spaced")
	}
	if n := strings.Count(block, `class="script-line`); n != 4 {
		t.Errorf("rendered %d line spans, want 4", n)
	}
}

// TestSetupScriptMarkupFlagsOnlyTheEditableLine keeps the highlight on the one
// line it belongs to: a comment mentioning the placeholder explains it, and
// highlighting that too would point at a line there is nothing to do to.
func TestSetupScriptMarkupFlagsOnlyTheEditableLine(t *testing.T) {
	lines := markUpSetupScript("# set <YOUR_PROJECT_ID_HERE> below\nexport GCP_PROJECT=<YOUR_PROJECT_ID_HERE>\ngcloud services enable foo")

	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if lines[0].NeedsEdit {
		t.Error("the explanatory comment should not be flagged as needing an edit")
	}
	if !lines[0].Comment {
		t.Error("the comment should be marked as one")
	}
	if !lines[1].NeedsEdit {
		t.Error("the export line carrying the placeholder should be flagged")
	}
	if lines[2].NeedsEdit {
		t.Error("an ordinary command should not be flagged")
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

// TestDeploymentPageLeadsToTheSetupInstructions covers the route someone
// actually takes.
//
// Credentials are managed on the deployment page, so that is where a person goes
// when one is missing -- and nothing there led to the script that explains how to
// obtain it. Deleting a credential to "get the instructions back" left them on a
// page with none, which is how this was found.
func TestDeploymentPageLeadsToTheSetupInstructions(t *testing.T) {
	projectID := uuid.New()
	askID := uuid.New().String()

	body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
		"ProjectID": projectID.String(),
		"Project":   &domain.Project{ID: projectID, Name: "pong"},
		"Channel":   validatedChannel(),
		"RequiredCredentials": []RequiredCredentialView{
			{Name: "GCP_PROJECT_ID", IsConfigured: false},
		},
		"CredentialAskID": askID,
	})

	if !strings.Contains(body, "/inputs/"+askID) {
		t.Error("no link from the deployment page to the outstanding credential ask")
	}

	// With nothing outstanding there is no ask to link to, and a dead link would
	// be worse than none.
	quiet := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
		"ProjectID": projectID.String(),
		"Project":   &domain.Project{ID: projectID, Name: "pong"},
		"Channel":   validatedChannel(),
	})
	if strings.Contains(quiet, "Setup instructions") {
		t.Error("offered setup instructions with no outstanding ask")
	}
}
