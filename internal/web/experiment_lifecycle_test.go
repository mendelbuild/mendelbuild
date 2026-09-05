package web

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"

	"github.com/bhs/mendelbuild/internal/assigner"
)

// The experiment must not add a second route for the production hostname.
//
// Gateway API ranks matches by path specificity, then method, then header count,
// and breaks the remaining tie with the older route. The production route is
// always older, so a second one for the same host would be applied, reported
// healthy, and never serve a single request -- the same silent-success shape
// this area has produced three times already.
func TestExperimentAddsNoSecondRouteForTheProductionHost(t *testing.T) {
	m, err := experimentFixture().Manifest()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Exactly one HTTPRoute object. Counted at the start of a line, because
	// "kind: HTTPRoute" also appears indented inside the ReferenceGrant's from
	// clause, where it names what is being permitted rather than declaring one.
	n := 0
	for _, line := range strings.Split(m, "\n") {
		if line == "kind: HTTPRoute" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("rendered %d HTTPRoutes; a second one for the same host would never take effect", n)
	}
	route := m[strings.Index(m, "kind: HTTPRoute"):]
	if !strings.Contains(route, "name: "+ExperimentGatewayName) {
		t.Error("the experiment's route does not attach to the experiment gateway")
	}
	if strings.Contains(route, "name: "+gatewayName+"\n") {
		t.Error("the experiment attached a route to the edge gateway, which already serves this host")
	}
}

// Stopping has to restore exactly what was there, so the two directions must
// agree about which object they are moving.
func TestStopPointsTheRouteBackAtItsOwnBackend(t *testing.T) {
	// The production deploy names its route and its Service identically, which
	// is what makes restoring a matter of naming the route again rather than
	// remembering something.
	app := prodAppName(sanitizeAppName("pong example"))
	if app == "" {
		t.Fatal("no production app name")
	}
	if strings.ContainsAny(app, " _") {
		t.Errorf("%q is not usable as a Kubernetes object name", app)
	}
}

// Mainline's weight has to reach the route, or every unassigned visitor lands in
// a treatment arm and the control is never served to anybody new.
func TestMainlineTakesItsShareOfNewVisitors(t *testing.T) {
	m, err := experimentFixture().Manifest()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	fallback := m[strings.LastIndex(m, "  - backendRefs:"):]

	if !strings.Contains(fallback, "name: pong-game-prod") {
		t.Error("mainline is not among the backends new visitors can be assigned to")
	}
	if !strings.Contains(fallback, assigner.CookieName+"="+assigner.MainlineSlug) {
		t.Error("a visitor assigned to mainline is never told so, and would be reassigned every request")
	}
	// Weights reach the route; without them Envoy splits evenly whatever the
	// allocation says.
	for _, want := range []string{"weight: 50", "weight: 25"} {
		if !strings.Contains(fallback, want) {
			t.Errorf("the allocation did not reach the route: missing %q", want)
		}
	}
}

// The edge gateway must be told how to health check the proxy, or it decides the
// proxy is down and serves 503 to everybody.
//
// Its default check probes the traffic port with no Host header, and Envoy
// answers 404 to a request matching no route -- correct of Envoy, fatal here.
// Observed on a real cluster: the backend sat UNHEALTHY and every request got
// 503 until the check was pointed at Envoy's own readiness port, after which
// twenty-four visitors split 9/9/6 across three arms.
func TestProxyIsHealthCheckedOnItsReadinessPort(t *testing.T) {
	src, err := os.ReadFile("experiment_lifecycle.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	if !strings.Contains(body, "HealthCheckPolicy") {
		t.Error("nothing configures the edge gateway's health check, so it will serve 503")
	}
	if !strings.Contains(body, "requestPath: /ready") {
		t.Error("the health check does not target Envoy's readiness endpoint")
	}
	if envoyReadinessPort == 80 || envoyReadinessPort == 10080 {
		t.Errorf("the health check port %d is a traffic port; Envoy answers 404 there",
			envoyReadinessPort)
	}
	// And it has to happen before traffic is repointed, or production is sent to
	// a backend the load balancer already believes is down.
	if strings.Index(body, "healthCheckProxy") > strings.Index(body, "Pointing production traffic") {
		t.Error("the health check is configured after the repoint, so production would 503 first")
	}
}

// Stopping must work whatever state the route is in.
//
// The first version always emitted a remove for the backend's namespace and
// tolerated the failure by matching kubectl's error text. The text was guessed
// and wrong -- kubectl says only "The request is invalid: the server rejected
// our request due to an error in our request" -- so stopping failed on its first
// step and left the experiment running with no way out of it from the page.
func TestStopDoesNotDependOnAnErrorMessage(t *testing.T) {
	src, err := os.ReadFile("experiment_lifecycle.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	if strings.Contains(body, "remove operation does not apply") {
		t.Error("stopping still depends on matching kubectl's error text")
	}
	// It reads the route before patching it, so the patch describes a change
	// from what is actually there rather than from what was assumed.
	if !strings.Contains(body, "routeBackendNamespace") {
		t.Error("the patch is built without looking at the route it patches")
	}
	if strings.Index(body, "routeBackendNamespace(ctx, route)") > strings.Index(body, `"op":"remove"`) {
		t.Error("the route is read after the remove is decided, which is not a decision")
	}
}

// Pressing Stop and being returned to a page still offering Stop is
// indistinguishable from the button not working — which is how it was read.
//
// Two things were wrong. The work takes minutes and there was no state for
// "part way through", so the page could only show what was true before the
// button was pressed. And the page polls a fingerprint that covered readiness
// only, so an experiment moving from running to stopped changed nothing it was
// watching, and it never reloaded.
func TestInProgressExperimentOffersNoButtonAndSaysWhy(t *testing.T) {
	for status, expect := range map[domain.ExperimentStatus]string{
		domain.ExperimentStarting: "Building an image for each arm",
		domain.ExperimentStopping: "Returning traffic to mainline",
	} {
		html := renderExperimentsPageWithStatus(t, status)

		if strings.Contains(html, "/start\"") || strings.Contains(html, "/stop\"") {
			t.Errorf("%s: an action was offered while the last one is still running", status)
		}
		if !strings.Contains(html, expect) {
			t.Errorf("%s: the page does not say what is happening", status)
		}
		if !strings.Contains(html, "updates itself when it finishes") {
			t.Errorf("%s: the page does not say it will update", status)
		}
	}

	// A settled experiment gets its button back.
	settled := renderExperimentsPageWithStatus(t, domain.ExperimentRunning)
	if !strings.Contains(settled, "/stop\"") {
		t.Error("a running experiment offers no way to stop it")
	}
}

func renderExperimentsPageWithStatus(t *testing.T, status domain.ExperimentStatus) string {
	t.Helper()
	obs := domain.ExperimentObservation{
		GatewayAPI: domain.FactTrue, CookieMatching: domain.FactTrue,
		ProdHostname: domain.FactTrue, ProdHTTPS: domain.FactTrue,
		SchemaChanges: domain.FactFalse,
	}
	steps := domain.ExperimentReadiness(obs)
	headline, blocked := domain.ExperimentHeadline(steps)

	var out strings.Builder
	if err := parsePageTemplate("project_experiments.html").ExecuteTemplate(&out, "page-content", map[string]interface{}{
		"SettingsTab": "experiments", "ProjectID": "abc", "Steps": steps,
		"Headline": headline, "Blocked": blocked, "Checking": false,
		"CheckedLabel": "just now", "Observation": obs, "Ready": true,
		"DatastoreVar": VerifyDatastoreVar, "Success": false, "Error": "",
		"Fingerprint": "x", "Candidates": nil,
		"Experiments": []*domain.Experiment{{
			ID: uuid.New(), Status: status,
			Arms: []domain.ExperimentArm{{Slug: "0", AllocationWeight: 50}},
		}},
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return out.String()
}

// A failure has to reach the product, not just the log.
//
// Four separate failures in this area left no trace a user could see: the page
// simply offered the button again, which reads as the button not working. The
// log had the cause every time, and the log is not somewhere a person running
// an experiment on their own application can go.
func TestFailureIsReportedInTermsTheUserAskedIn(t *testing.T) {
	start := domain.ReportStartFailure(
		`error: the namespace from the provided object "envoy-gateway-system" does not match`)

	// The first question is what happened to their traffic, and the error never
	// answers it.
	if !strings.Contains(start.Effect, "Nothing changed") {
		t.Errorf("a failed start does not say what became of production: %q", start.Effect)
	}
	if start.Yours {
		t.Error("a failed start blamed the user for something Mendel did")
	}
	// The cause is kept, but it is not the message.
	if !strings.Contains(start.Detail, "envoy-gateway-system") {
		t.Error("the technical cause was discarded; whoever maintains Mendel needs it")
	}
	if strings.Contains(start.Summary, "namespace") {
		t.Error("the summary leads with a kubectl error rather than with what happened")
	}

	// Stopping is the case where the reassuring version would be a lie: traffic
	// is returned first, so a failure before that leaves the experiment serving.
	early := domain.ReportStopFailure("could not point pong-game-prod at pong-game-prod", false)
	if !strings.Contains(early.Effect, "still be split") {
		t.Errorf("a stop that never returned traffic implies it did: %q", early.Effect)
	}
	late := domain.ReportStopFailure("deleting resources failed", true)
	if strings.Contains(late.Effect, "still be split") {
		t.Errorf("a stop that did return traffic alarms about it anyway: %q", late.Effect)
	}
}
