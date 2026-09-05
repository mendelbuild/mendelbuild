package web

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bhs/mendelbuild/internal/domain"
)

// A settings area nothing links to is a settings area nobody finds.
func TestExperimentsTabIsReachable(t *testing.T) {
	s := &Server{}
	s.setupRoutes()

	var hasPage, hasSave bool
	chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		switch {
		case method == http.MethodGet && strings.HasSuffix(route, "/experiments"):
			hasPage = true
		case method == http.MethodPost && strings.HasSuffix(route, "/experiments/verify-datastore"):
			hasSave = true
		}
		return nil
	})
	if !hasPage {
		t.Error("no route serves the live-traffic experiments settings page")
	}
	if !hasSave {
		t.Error("the verification datastore cannot be saved")
	}

	tabs := readTemplateFile(t, "partials.html")
	if !strings.Contains(tabs, `/experiments">Live-traffic experiments`) {
		t.Error("the settings tabs do not link to the experiments area")
	}
}

func TestExperimentsPageRenders(t *testing.T) {
	obs := domain.ExperimentObservation{
		GatewayAPI:           domain.FactFalse,
		EnableGatewayCommand: "gcloud container clusters update c --location l --gateway-api=standard --project p",
		ProdHostname:         domain.FactTrue,
		ProdHost:             "app.example.com",
		ProdHTTPS:            domain.FactTrue,
		VerifyDatastore:      domain.FactFalse,
	}
	steps := domain.ExperimentReadiness(obs)
	headline, blocked := domain.ExperimentHeadline(steps)

	var out strings.Builder
	err := parsePageTemplate("project_experiments.html").ExecuteTemplate(&out, "page-content", map[string]interface{}{
		"SettingsTab": "experiments", "ProjectID": "abc", "Steps": steps,
		"Headline": headline, "Blocked": blocked, "Checking": false,
		"CheckedLabel": "just now", "Observation": obs,
		"DatastoreVar": VerifyDatastoreVar, "Success": false, "Error": "",
	})
	if err != nil {
		t.Fatalf("experiments page does not render: %v", err)
	}
	html := out.String()

	// The remedy for a missing Gateway API is a command, and it has to be
	// copyable rather than transcribed.
	for _, want := range []string{
		"--gateway-api=standard",
		`id="gateway-command"`,
		`data-copy="gateway-command"`,
		"/static/js/copy-button.js",
		"Verification datastore",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

// The command only appears when it is the remedy. Showing it beside a cluster
// that already has Gateway API invites someone to run it looking for a problem
// that is not there.
func TestGatewayCommandOnlyShownWhenItIsTheRemedy(t *testing.T) {
	obs := domain.ExperimentObservation{
		GatewayAPI:           domain.FactTrue,
		EnableGatewayCommand: "gcloud container clusters update c --location l --gateway-api=standard --project p",
		ProdHostname:         domain.FactTrue, ProdHTTPS: domain.FactTrue,
		VerifyDatastore: domain.FactTrue, VerifyReachable: domain.FactTrue,
	}
	steps := domain.ExperimentReadiness(obs)
	headline, blocked := domain.ExperimentHeadline(steps)

	var out strings.Builder
	if err := parsePageTemplate("project_experiments.html").ExecuteTemplate(&out, "page-content", map[string]interface{}{
		"SettingsTab": "experiments", "ProjectID": "abc", "Steps": steps,
		"Headline": headline, "Blocked": blocked, "Checking": false,
		"CheckedLabel": "just now", "Observation": obs,
		"DatastoreVar": VerifyDatastoreVar, "Success": false, "Error": "",
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out.String(), "--gateway-api=standard") {
		t.Error("the enable command is offered to a cluster that already has Gateway API")
	}
}

// The remedy for GKE's Exact-only header matching is a second controller, and
// the page has to say so — the property is invisible otherwise, since Gateway
// API being "on" looks like success.
func TestInstallControllerRemedyIsOfferedWhenNeeded(t *testing.T) {
	obs := domain.ExperimentObservation{
		GatewayAPI:            domain.FactTrue, // on, and still not enough
		CookieMatching:        domain.FactFalse,
		InstallControllerHint: "helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm -n envoy-gateway-system --create-namespace",
		ProdHostname:          domain.FactTrue, ProdHTTPS: domain.FactTrue,
		SchemaChanges: domain.FactFalse,
	}
	html := renderExperimentsPage(t, obs)

	for _, want := range []string{"envoyproxy/gateway-helm", `id="install-controller"`, `data-copy="install-controller"`} {
		if !strings.Contains(html, want) {
			t.Errorf("page is missing %q", want)
		}
	}

	// And it disappears once the controller is there, so nobody runs it looking
	// for a problem that is not present.
	obs.CookieMatching = domain.FactTrue
	if strings.Contains(renderExperimentsPage(t, obs), "envoyproxy/gateway-helm") {
		t.Error("the install command is offered to a cluster that already has a controller")
	}
}

func renderExperimentsPage(t *testing.T, obs domain.ExperimentObservation) string {
	t.Helper()
	steps := domain.ExperimentReadiness(obs)
	headline, blocked := domain.ExperimentHeadline(steps)

	var out strings.Builder
	if err := parsePageTemplate("project_experiments.html").ExecuteTemplate(&out, "page-content", map[string]interface{}{
		"SettingsTab": "experiments", "ProjectID": "abc", "Steps": steps,
		"Headline": headline, "Blocked": blocked, "Checking": false,
		"CheckedLabel": "just now", "Observation": obs,
		"DatastoreVar": VerifyDatastoreVar, "Success": false, "Error": "",
	}); err != nil {
		t.Fatalf("experiments page does not render: %v", err)
	}
	return out.String()
}

// Mendel installs the controller when it can, and says who must when it cannot.
//
// A user who has to run kubectl themselves before their first experiment has
// been handed a prerequisite, not a product. But "cannot" is a real case, and it
// is asked of the cluster rather than assumed either way — so there are three
// states here and the page must not collapse them into two.
func TestInstallIsOfferedAsAnActionWhenMendelCanDoIt(t *testing.T) {
	obs := domain.ExperimentObservation{
		GatewayAPI: domain.FactTrue, CookieMatching: domain.FactFalse,
		CanInstallController:  domain.FactTrue,
		InstallControllerHint: installControllerCommand(),
		ProdHostname:          domain.FactTrue, ProdHTTPS: domain.FactTrue,
		SchemaChanges: domain.FactFalse,
	}
	html := renderExperimentsPage(t, obs)
	if !strings.Contains(html, "/experiments/install-controller") {
		t.Error("Mendel can install it and did not offer to")
	}
	if strings.Contains(html, "cluster-admin can") {
		t.Error("the page asked a person to run kubectl that Mendel could run itself")
	}

	// Refused: the command, and who has to run it.
	obs.CanInstallController = domain.FactFalse
	refused := renderExperimentsPage(t, obs)
	if strings.Contains(refused, `action="/p/abc/experiments/install-controller"`) {
		t.Error("an install button was offered that Mendel's credentials would refuse")
	}
	for _, want := range []string{"kubectl apply --server-side", "cluster-admin can"} {
		if !strings.Contains(refused, want) {
			t.Errorf("a refused install does not show %q", want)
		}
	}

	// Unknown is its own state: Mendel has not tried, and says so rather than
	// claiming its credentials were refused.
	obs.CanInstallController = domain.FactUnknown
	unknown := renderExperimentsPage(t, obs)
	if !strings.Contains(unknown, "could not establish whether") {
		t.Error("an undetermined permission was reported as a refusal")
	}
}

// The controller version is pinned, and the pin is what the install applies. A
// controller that changed under a running experiment could change how traffic is
// routed mid-comparison.
func TestControllerVersionIsPinned(t *testing.T) {
	url := envoyGatewayManifestURL()
	if !strings.Contains(url, EnvoyGatewayVersion) {
		t.Errorf("the manifest URL does not carry the pinned version: %s", url)
	}
	if strings.Contains(url, "latest") {
		t.Error("the install follows 'latest', so a release could change routing mid-experiment")
	}
	if !strings.Contains(installControllerCommand(), "--server-side") {
		t.Error("the command is not a server-side apply, so re-running it conflicts")
	}
}

// kubectl warns on stderr for cluster-scoped resources and answers on stdout.
// Reading them together makes the answer match neither word, so Mendel would
// decide it could not tell and never offer to install — on exactly the clusters
// where it can.
func TestCanIVerdictIsReadFromStdoutAlone(t *testing.T) {
	exitErr := errors.New("exit status 1")

	for name, tc := range map[string]struct {
		stdout string
		err    error
		want   domain.Fact
	}{
		"plain yes":              {"yes\n", nil, domain.FactTrue},
		"plain no":               {"no\n", exitErr, domain.FactFalse},
		"yes with trailing blank": {"yes\n\n", nil, domain.FactTrue},
		"could not ask":          {"", errors.New("connection refused"), domain.FactUnknown},
		"unexpected answer":      {"maybe\n", nil, domain.FactUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			if got := interpretCanI(tc.stdout, tc.err); got != tc.want {
				t.Errorf("interpretCanI(%q) = %v, want %v", tc.stdout, got, tc.want)
			}
		})
	}
}

// "Not set" and "could not tell" are different answers to different questions,
// and the page shows them differently on purpose.
//
// GetProjectEnvVar reports a missing row as an error, so reading any error as
// unknown told the user Mendel had a problem when the truth was that they had
// not set one yet — the exact confusion Fact exists to prevent, reintroduced one
// layer above it.
func TestMissingDatastoreReadsAsMissingNotAsAFailure(t *testing.T) {
	absent := domain.ExperimentObservation{
		GatewayAPI: domain.FactTrue, CookieMatching: domain.FactTrue,
		ProdHostname: domain.FactTrue, ProdHTTPS: domain.FactTrue,
		SchemaChanges: domain.FactTrue, VerifyDatastore: domain.FactFalse,
	}
	steps := domain.ExperimentReadiness(absent)
	for _, s := range steps {
		if !strings.Contains(s.Name, "non-production datastore") {
			continue
		}
		if s.State != domain.StepYourMove {
			t.Errorf("an unset datastore should be the user's move, got %q", s.State)
		}
		if strings.Contains(s.Detail, "could not") {
			t.Errorf("an unset datastore was reported as a failure to look: %q", s.Detail)
		}
	}
}
