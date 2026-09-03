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
			{Slug: assigner.MainlineSlug, Backend: "pong-game-prod"},
			{Slug: "a", Image: "gcr.io/p/pong:a"},
			{Slug: "b", Image: "gcr.io/p/pong:b"},
		},
		Allocation:    `{"salt":"s","key":{"source":"cookie","name":"sid"},"arms":[]}`,
		AssignerImage: "gcr.io/p/mendel-assigner:1",
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

	// Everything with no Arm cookie must reach the assigner, or a first-time
	// visitor is served by whichever rule happens to catch them.
	if !strings.Contains(m, "exp-checkout-assigner") {
		t.Error("nothing routes an unassigned visitor to the assigner")
	}
	assignerRule := strings.LastIndex(m, "name: exp-checkout-assigner")
	lastArmRule := strings.LastIndex(m, "type: RegularExpression")
	if assignerRule < lastArmRule {
		t.Error("the fallback is listed before an Arm rule; it must be last")
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

func TestAllocationTravelsWithTheExperiment(t *testing.T) {
	d := experimentFixture()
	m, err := d.Manifest()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(m, "allocation.json: |") {
		t.Error("the assigner has no allocation to read")
	}
	if !strings.Contains(m, `"salt":"s"`) {
		t.Error("the allocation did not reach the ConfigMap")
	}
	// Mounted, or the file the assigner reads is not there and it refuses to
	// start -- which is correct behaviour and a confusing way to find this out.
	if !strings.Contains(m, "mountPath: /etc/mendel") {
		t.Error("the allocation is not mounted into the assigner")
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
		"no allocation":      func(d *ExperimentDeployment) { d.Allocation = "" },
		"no assigner image":  func(d *ExperimentDeployment) { d.AssignerImage = "" },
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
