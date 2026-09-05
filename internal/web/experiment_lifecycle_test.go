package web

import (
	"os"
	"strings"
	"testing"

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
