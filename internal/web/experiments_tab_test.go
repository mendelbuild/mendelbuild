package web

import (
	"errors"
	"net/http"
	"os"
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

func testClusterEnv() map[string]string {
	return map[string]string{
		"GCP_PROJECT_ID": "mendelpong", "GKE_CLUSTER_NAME": "pong-autopilot", "GKE_ZONE": "us-central1",
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
		InstallControllerHint: installControllerCommand(testClusterEnv()),
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
	for _, want := range []string{
		"apply --server-side", "--context gke_", "administrator access",
		`id="install-controller"`, `data-copy="install-controller"`, "/static/js/copy-button.js",
	} {
		if !strings.Contains(refused, want) {
			t.Errorf("a refused install does not show %q", want)
		}
	}

	// Unknown attempts rather than refuses. Mendel not being able to predict
	// the answer is a poor reason to hand the user a command Mendel could run:
	// the API server's refusal names the missing permission exactly, where
	// "could not tell" names nothing.
	obs.CanInstallController = domain.FactUnknown
	unknown := renderExperimentsPage(t, obs)
	if !strings.Contains(unknown, "/experiments/install-controller") {
		t.Error("an undetermined permission stopped Mendel from even trying")
	}
	if strings.Contains(unknown, "cluster-admin") {
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
	if !strings.Contains(installControllerCommand(testClusterEnv()), "--server-side") {
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

// The probe must ask about the verb the install actually uses.
//
// `kubectl apply --server-side` is a PATCH whatever the object's current state,
// so an account permitted to create but not to patch fails on the very first
// apply. GKE's container.developer is exactly such an account: asking about
// create returned yes and the install then failed on every RBAC object in the
// manifest.
func TestPermissionProbeAsksAboutPatch(t *testing.T) {
	src, err := os.ReadFile("experiment_controller.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `"auth", "can-i", "patch"`) {
		t.Error("the probe does not ask about patch, which is what apply --server-side does")
	}
	if strings.Contains(string(src), `"auth", "can-i", "create"`) {
		t.Error("the probe still asks about create, which the install never performs")
	}

	// And it has to cover what the manifest actually contains. Asking about two
	// of six is how a partial install gets attempted.
	for _, needed := range []string{
		"clusterroles", "clusterrolebindings", "roles", "rolebindings",
		"customresourcedefinitions", "mutatingwebhookconfigurations",
	} {
		found := false
		for _, r := range installNeeds {
			if r == needed {
				found = true
			}
		}
		if !found {
			t.Errorf("the probe does not ask about %s, which the manifest patches", needed)
		}
	}
}

// A person who has to learn about Homebrew before their first experiment will
// not have a first experiment. When Mendel cannot install the controller, the
// fallback must not assume a Kubernetes CLI on anyone's laptop.
func TestFallbackNeedsNothingInstalledLocally(t *testing.T) {
	obs := domain.ExperimentObservation{
		GatewayAPI: domain.FactTrue, CookieMatching: domain.FactFalse,
		CanInstallController:  domain.FactFalse,
		InstallControllerHint: installControllerCommand(testClusterEnv()),
		CloudShellURL:         cloudShellURL(testClusterEnv()),
		ProdHostname:          domain.FactTrue, ProdHTTPS: domain.FactTrue,
		SchemaChanges: domain.FactFalse,
	}
	html := renderExperimentsPage(t, obs)

	if !strings.Contains(html, "shell.cloud.google.com") {
		t.Error("no browser terminal offered; the fallback assumes a local kubectl")
	}
	if !strings.Contains(html, `rel="noopener noreferrer"`) {
		t.Error("the external link does not disclaim the opener")
	}
}

// A fresh Cloud Shell has no cluster configured, so a bare kubectl there reaches
// nothing. And on a laptop it reaches whatever the terminal was already pointed
// at, which for a Mendel operator is quite likely Mendel's own cluster. The
// command has to establish its own target either way.
func TestFallbackCommandEstablishesItsOwnTarget(t *testing.T) {
	cmd := installControllerCommand(testClusterEnv())

	if !strings.Contains(cmd, "get-credentials pong-autopilot --location us-central1 --project mendelpong") {
		t.Errorf("the command does not fetch credentials for the cluster: %s", cmd)
	}
	if !strings.Contains(cmd, "--context gke_mendelpong_us-central1_pong-autopilot") {
		t.Errorf("the apply does not name the cluster it means: %s", cmd)
	}
	if lines := strings.Split(strings.TrimSpace(cmd), "\n"); len(lines) != 2 {
		t.Errorf("expected exactly the two lines to paste, got %d:\n%s", len(lines), cmd)
	}

	// With nothing to name it degrades rather than inventing a cluster.
	if bare := installControllerCommand(nil); strings.Contains(bare, "get-credentials") {
		t.Errorf("a cluster was invented from nothing: %s", bare)
	}
}

// The readiness checks run behind the render, so a cold page says "Checking".
// Without a way to notice the answer arriving, it sits there until a person
// reloads — and an install started from this page takes a minute or two to
// change anything, which is exactly when someone is watching it.
func TestPageNoticesWhenTheAnswerArrives(t *testing.T) {
	html := renderExperimentsPage(t, domain.ExperimentObservation{
		GatewayAPI: domain.FactTrue, CookieMatching: domain.FactFalse,
		CanInstallController: domain.FactFalse, ProdHostname: domain.FactTrue,
	})

	for _, want := range []string{
		`id="experiment-readiness"`,
		`data-status="/p/abc/experiments/status"`,
		"data-fingerprint=",
		"/static/js/experiment-status.js",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page cannot watch for changes: missing %q", want)
		}
	}

	var registered bool
	s := &Server{}
	s.setupRoutes()
	chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet && strings.HasSuffix(route, "/experiments/status") {
			registered = true
		}
		return nil
	})
	if !registered {
		t.Error("nothing serves the status the page polls, so it would poll a 404 forever")
	}
}

// A fingerprint that ignored a field would leave the page stale on exactly the
// change the user is waiting for; one that included a timestamp would reload on
// a refresh that found nothing.
func TestFingerprintTracksEveryDisplayedFact(t *testing.T) {
	base := domain.ExperimentObservation{
		GatewayAPI: domain.FactTrue, CookieMatching: domain.FactFalse,
		CanInstallController: domain.FactFalse, ProdHostname: domain.FactTrue,
		ProdHTTPS: domain.FactTrue, VerifyDatastore: domain.FactFalse,
		ProdHost: "app.example.com",
	}

	if base.Fingerprint() != base.Fingerprint() {
		t.Fatal("the fingerprint is not stable, so the page would reload continuously")
	}

	for name, mutate := range map[string]func(*domain.ExperimentObservation){
		"gateway api":  func(o *domain.ExperimentObservation) { o.GatewayAPI = domain.FactFalse },
		"cookie match": func(o *domain.ExperimentObservation) { o.CookieMatching = domain.FactTrue },
		"may install":  func(o *domain.ExperimentObservation) { o.CanInstallController = domain.FactTrue },
		"hostname":     func(o *domain.ExperimentObservation) { o.ProdHostname = domain.FactFalse },
		"https":        func(o *domain.ExperimentObservation) { o.ProdHTTPS = domain.FactFalse },
		"datastore":    func(o *domain.ExperimentObservation) { o.VerifyDatastore = domain.FactTrue },
		"host name":    func(o *domain.ExperimentObservation) { o.ProdHost = "other.example.com" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if changed.Fingerprint() == base.Fingerprint() {
				t.Errorf("a change to %s does not change the fingerprint, so the page stays stale", name)
			}
		})
	}
}
