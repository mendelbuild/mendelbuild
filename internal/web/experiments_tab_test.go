package web

import (
	"net/http"
	"net/http/httptest"
	"fmt"
	"context"
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
	// The manifest creates no GatewayClass, so a command that stops after the
	// apply leaves a controller running and claiming nothing -- an install that
	// looks complete and does nothing.
	if !strings.Contains(cmd, "kind: GatewayClass") {
		t.Errorf("the command never creates the gateway class:\n%s", cmd)
	}
	// And the Gateway API CRD errors are unavoidable when pasting the manifest
	// whole. Saying so is the difference between "expected noise" and "it
	// failed" for whoever reads the output.
	if !strings.Contains(cmd, "Expect errors") {
		t.Errorf("the command does not warn about the errors it will print:\n%s", cmd)
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

// The install must not fight the platform for the CRDs it already owns.
//
// Envoy Gateway bundles its own copy of all ten Gateway API definitions. On GKE
// those are installed and managed by kube-addon-manager, so applying them over
// the top fails twice: server-side apply reports a field-manager conflict, and
// Autopilot's enforce-gateway-standard-channel policy refuses the
// experimental-channel ones outright. Both were observed on a real cluster.
func TestInstallLeavesPlatformOwnedCRDsAlone(t *testing.T) {
	manifest := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: httproutes.gateway.networking.k8s.io
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: udproutes.gateway.networking.k8s.io
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: envoyproxies.gateway.envoyproxy.io
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: xbackends.gateway.networking.x-k8s.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: envoy-gateway
`
	out := installableManifest(manifest)

	for _, gone := range []string{"httproutes.gateway.networking.k8s.io", "udproutes.gateway.networking.k8s.io"} {
		if strings.Contains(out, gone) {
			t.Errorf("kept %s, which the platform owns", gone)
		}
	}
	// Only Envoy Gateway provides these, so dropping them would install a
	// controller that cannot start. Note x-k8s.io is a different group and must
	// survive, which a looser match on "gateway.networking" would not.
	for _, kept := range []string{
		"envoyproxies.gateway.envoyproxy.io",
		"xbackends.gateway.networking.x-k8s.io",
		"kind: Deployment",
	} {
		if !strings.Contains(out, kept) {
			t.Errorf("dropped %s, which nothing else provides", kept)
		}
	}
}

// install.yaml creates no GatewayClass -- Envoy Gateway leaves that to whoever
// installs it. Waiting for a class the manifest never creates would have waited
// forever on an install that had entirely succeeded.
func TestInstallCreatesTheGatewayClassItself(t *testing.T) {
	m := gatewayClassManifest()
	for _, want := range []string{
		"kind: GatewayClass",
		"name: " + ExperimentGatewayClass,
		"controllerName: gateway.envoyproxy.io/gatewayclass-controller",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("the gateway class manifest is missing %q", want)
		}
	}
}

// The manifest URL is pinned to a released version, so its contents cannot
// change and re-fetching it is pure waste.
//
// It was being fetched a great deal: the readiness check dry-runs the install
// while the controller is missing, so every refresh of the page downloaded four
// megabytes and filtered them again. That is why the page was slow to notice the
// controller arriving.
func TestManifestIsFetchedOnce(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n")
	}))
	defer server.Close()

	manifestCache.Lock()
	manifestCache.filtered = ""
	manifestCache.Unlock()
	t.Cleanup(func() {
		manifestCache.Lock()
		manifestCache.filtered = ""
		manifestCache.Unlock()
	})

	original := envoyGatewayManifestURLFn
	envoyGatewayManifestURLFn = func() string { return server.URL }
	t.Cleanup(func() { envoyGatewayManifestURLFn = original })

	for i := 0; i < 5; i++ {
		if _, err := fetchInstallManifest(context.Background()); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	if hits != 1 {
		t.Errorf("downloaded the pinned manifest %d times; it cannot change", hits)
	}
}
