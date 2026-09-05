package web

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/assigner"
)

func experimentFixture() ExperimentDeployment {
	return ExperimentDeployment{
		Name:     "exp-checkout",
		Hostname: "app.example.com",
		Arms: []ArmDeployment{
			{Slug: assigner.MainlineSlug, Backend: "pong-game-prod", Weight: 50},
			{Slug: "a", Image: "gcr.io/p/pong:a", Weight: 25},
			{Slug: "b", Image: "gcr.io/p/pong:b", Weight: 25},
		},
		Secure: true,
	}
}

// The match has to find one cookie inside a header full of them, anchored at
// both ends of the value.
//
// An unanchored match for mendel_arm=a also fires on mendel_arm=ab, which would
// route one Arm's traffic into another. That failure does not look like a bug --
// it looks like a surprising experiment result, which is far worse.
func TestCookieMatchCannotCatchANeighbouringArm(t *testing.T) {
	re := regexp.MustCompile(cookieMatch("a"))

	for _, header := range []string{
		"mendel_arm=a",
		"sid=xyz; mendel_arm=a",
		"mendel_arm=a; theme=dark",
		"sid=xyz; mendel_arm=a; theme=dark",
		"sid=xyz;mendel_arm=a;theme=dark",
	} {
		if !re.MatchString(header) {
			t.Errorf("arm a should match %q", header)
		}
	}

	for _, header := range []string{
		"mendel_arm=ab",                 // a longer slug beginning with a
		"mendel_arm=ba",                 // and one ending with it
		"sid=xyz; mendel_arm=ab",
		"other_mendel_arm=a",            // a different cookie whose name ends the same
		"mendel_arm=0",
		"sid=mendel_arm=a",              // the text appearing inside another value
		"",
	} {
		if re.MatchString(header) {
			t.Errorf("arm a must not match %q", header)
		}
	}
}

func TestEveryArmGetsARuleAndTheFallbackIsTheAssigner(t *testing.T) {
	m, err := experimentFixture().Manifest()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, slug := range []string{assigner.MainlineSlug, "a", "b"} {
		// Rendered as a YAML double-quoted scalar, so the backslashes in the
		// regex are escaped. Comparing against the quoted form is what the file
		// actually has to contain.
		if !strings.Contains(m, fmt.Sprintf("%q", cookieMatch(slug))) {
			t.Errorf("no route rule matches arm %q", slug)
		}
	}

	// The escaping has to survive YAML, or the Gateway is handed a regex that
	// means something else. In a double-quoted scalar \\s is one backslash and
	// an s, which is what \s in the regex needs to arrive as.
	if !strings.Contains(m, `\\s*`) {
		t.Error("the regex is not escaped for a YAML double-quoted scalar")
	}

	// A visitor with no cookie is assigned by the response that serves them:
	// weighted backends, each setting the cookie naming itself. Verified against
	// a real Envoy Gateway -- twelve requests, every cookie matching the arm that
	// served it.
	fallback := m[strings.LastIndex(m, "  - backendRefs:"):]
	if strings.Contains(fallback, "type: RegularExpression") {
		t.Error("the fallback carries a match, so it is not the least specific rule")
	}
	for _, slug := range []string{assigner.MainlineSlug, "a", "b"} {
		if !strings.Contains(fallback, assigner.CookieName+"="+slug) {
			t.Errorf("an unassigned visitor can never be placed in arm %q", slug)
		}
	}
	// A cookie the browser will not send back assigns nobody: every request
	// would look unassigned and no visitor would stay in an Arm.
	if !strings.Contains(fallback, "Max-Age=") || !strings.Contains(fallback, "Path=/") {
		t.Error("the assignment cookie is not durable enough to keep a visitor in one arm")
	}
}

// Mainline is the code that was already there. Redeploying it under a new name
// would make the control a new thing, and the comparison would be against
// something nobody had been running.
func TestMainlineIsNotRedeployed(t *testing.T) {
	m, err := experimentFixture().Manifest()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if strings.Contains(m, "exp-checkout-0") {
		t.Error("a Deployment was rendered for mainline")
	}
	if !strings.Contains(m, "name: pong-game-prod") {
		t.Error("mainline's rule does not point at the Service it already has")
	}
	// The Variations, by contrast, are deployed.
	for _, name := range []string{"exp-checkout-a", "exp-checkout-b"} {
		if !strings.Contains(m, "name: "+name) {
			t.Errorf("no Deployment for %s", name)
		}
	}
}

// Everything is labelled with the experiment, so teardown can find what it made
// without knowing what each object is.
func TestResourcesCarryTheExperimentLabel(t *testing.T) {
	m, err := experimentFixture().Manifest()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if n := strings.Count(m, "mendel-experiment: exp-checkout"); n < 4 {
		t.Errorf("only %d objects carry the experiment label; teardown finds objects by it", n)
	}
}

func TestManifestRefusesWhatCannotBeDeployed(t *testing.T) {
	for name, mutate := range map[string]func(*ExperimentDeployment){
		"no hostname":        func(d *ExperimentDeployment) { d.Hostname = "" },
		"no name":            func(d *ExperimentDeployment) { d.Name = "" },
		// Relative weights, so they need not total anything in particular --
		// Envoy normalises them. All zero is the case that cannot work: no
		// visitor could ever be assigned.
		"every share is zero": func(d *ExperimentDeployment) {
			for i := range d.Arms {
				d.Arms[i].Weight = 0
			}
		},
		"a negative share": func(d *ExperimentDeployment) { d.Arms[1].Weight = -1 },
		"two arms share a slug": func(d *ExperimentDeployment) { d.Arms[2].Slug = d.Arms[1].Slug },
		"no mainline":        func(d *ExperimentDeployment) { d.Arms[0].Slug = "c" },
		"mainline no backend": func(d *ExperimentDeployment) { d.Arms[0].Backend = "" },
		"arm with no image":  func(d *ExperimentDeployment) { d.Arms[1].Image = "" },
		"nothing to compare": func(d *ExperimentDeployment) { d.Arms = d.Arms[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			d := experimentFixture()
			mutate(&d)
			if _, err := d.Manifest(); err == nil {
				t.Errorf("%s was accepted", name)
			}
		})
	}
}

// The two layers, and which does what.
//
// GKE's Gateway matches headers Exact only, so it cannot pick one cookie out of
// the several a visitor carries -- cookie matching is impossible on it. But it
// holds the reserved address, terminates TLS and carries the Certificate Manager
// map, none of which Envoy Gateway can be given, because it reads certificates
// from Kubernetes Secrets and Certificate Manager will not export a private key.
//
// So the Arm routes must attach to the Envoy Gateway and the front door must
// stay on GKE's. Getting that backwards produces a deployment that looks right
// and serves every visitor mainline.
func TestArmRoutesAttachToEnvoyAndTheFrontDoorStaysOnGKE(t *testing.T) {
	m, err := experimentFixture().Manifest()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// The Gateway that does the matching is not GKE's class.
	if !strings.Contains(m, "gatewayClassName: "+ExperimentGatewayClass) {
		t.Error("no Gateway of a class that can match cookies")
	}
	if strings.Contains(m, "gke-l7-global-external-managed") {
		t.Error("Arm matching was attached to a class that matches headers Exact only")
	}

	// The Arm rules hang off the experiment Gateway.
	armRoute := m[strings.Index(m, "\nkind: HTTPRoute\nmetadata:\n  name: exp-checkout\n"):]
	if !strings.Contains(armRoute, "name: "+ExperimentGatewayName) {
		t.Error("the Arm routes do not attach to the experiment Gateway")
	}

	// The front door is not rendered here at all: the production route already
	// exists and is repointed, because a second route for that host would lose
	// the age tie-break and never serve anything.
	if strings.Contains(m, "hostnames:\n  - app.example.com\nspec") {
		t.Error("rendered a competing route for the production hostname")
	}
	// The route reaches Envoy across a namespace boundary, which Gateway API
	// refuses without a grant.
	if !strings.Contains(m, "kind: ReferenceGrant") {
		t.Error("no grant, so the production route may not reference the proxy")
	}
	if !strings.Contains(m, "namespace: "+ExperimentProxyNamespace) {
		t.Error("the grant is not in the namespace the proxy runs in")
	}
}

// Envoy Gateway names its proxy Service after the Gateway plus a hash, which is
// not a name anything can reference. Mendel puts a fixed name in front of the
// same pods so the front door has something stable to point at.
func TestProductionReachesTheProxyByGrantNotBySelector(t *testing.T) {
	m, err := experimentFixture().Manifest()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// A Service selects pods in its own namespace only, and Envoy runs its
	// proxies in its own. The first version of this created one in mendel-apps
	// selecting those pods: it applied cleanly, reported ResolvedRefs=True
	// because the Service existed, had no endpoints, and pointed production
	// traffic at nothing.
	if strings.Contains(m, "mendel-experiment-edge") {
		t.Error("rendered a Service that can never have endpoints")
	}
	if !strings.Contains(m, "kind: ReferenceGrant") {
		t.Error("nothing permits the production route to reach the proxy")
	}
}
