package web

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// A live experiment must be operable from the page it belongs to.
//
// Asserted as reachability rather than as copy, per the lesson the orphaned-route
// and capability checks were written for: the recurring fault here is a feature
// becoming unreachable while every page still renders, and a test that pins the
// wording catches none of that while breaking on every rewrite.
//
// So this asks what a reader can *do*: which actions the page offers, and which
// it withholds.
func hopPageWithExperiment(t *testing.T, status domain.ExperimentStatus,
	mutate func(*ExperimentView)) (out string, projectID, experimentID, armID uuid.UUID) {
	t.Helper()

	projectID, experimentID, armID = uuid.New(), uuid.New(), uuid.New()
	hopID, variationID := uuid.New(), uuid.New()
	built := time.Now()

	ev := &ExperimentView{
		Experiment: &domain.Experiment{ID: experimentID, HopID: hopID, Status: status},
		ProdHost:   "app.pong.mendel.build",
		Arms: []ArmView{{
			Arm:   domain.ExperimentArm{ID: uuid.New(), Slug: domain.MainlineSlug, AllocationWeight: 50},
			Build: domain.DescribeArmBuild(domain.ExperimentArm{Slug: domain.MainlineSlug}, ""),
		}, {
			Arm: domain.ExperimentArm{ID: armID, VariationID: &variationID, Slug: "fast-a1b2c3",
				AllocationWeight: 50, SourceCommit: "aaaaaaaabbbbcccc", BuiltAt: &built},
			Variation: &domain.Variation{ID: variationID, Name: "fast"},
			Build: domain.DescribeArmBuild(domain.ExperimentArm{
				VariationID: &variationID, SourceCommit: "aaaaaaaabbbbcccc"}, "ddddddddeeeeffff"),
			Reach: "curl -si -H 'Cookie: mendel_arm=fast-a1b2c3' https://app.pong.mendel.build/",
		}},
	}
	if mutate != nil {
		mutate(ev)
	}

	var b strings.Builder
	err := parsePageTemplate("hop_detail.html").ExecuteTemplate(&b, "layout",
		map[string]interface{}{
			"Title": "Hop", "ProjectID": projectID.String(),
			"View": &HopDetailView{
				Hop:      &domain.Hop{ID: hopID, Name: "checkout"},
				Strategy: &domain.Strategy{Name: "growth"},
				Project:  &domain.Project{Name: "pong"},
				Ribbon:   ribbonView(domain.Ribbon{}),
				Experiment: ev,
			},
		})
	if err != nil {
		t.Fatalf("rendering the hop page: %v", err)
	}
	return b.String(), projectID, experimentID, armID
}

// Every action the experiment routes register is reachable from this page.
//
// The experiment used to be operated from the project settings tab, where the
// Hop had to be chosen out of a dropdown before anything could be done to it --
// the controls a page away from what they controlled. Moving them here is only
// an improvement if they all arrived, so this checks each one against the path
// its route is registered at.
func TestARunningExperimentIsFullyOperableFromItsHop(t *testing.T) {
	out, project, experiment, arm := hopPageWithExperiment(t, domain.ExperimentRunning, nil)

	base := "/p/" + project.String() + "/experiments/" + experiment.String()
	for what, path := range map[string]string{
		"stop it":                  base + "/stop",
		"sample the split":         base + "/sample",
		"rebase and rebuild all":   base + "/refresh",
		"rebase and rebuild one":   base + "/arms/" + arm.String() + "/refresh",
		"read it as data":          base + "/live.json",
	} {
		if !strings.Contains(out, path) {
			t.Errorf("a reader on the hop page cannot %s: no %s", what, path)
		}
	}

	// The header is the answer to "which version am I getting", and naming it is
	// the entire point -- it was in every response for the whole first
	// experiment and the product never mentioned it, so the first person to run
	// one gave each arm a different background colour to tell them apart.
	if !strings.Contains(out, ArmHeader) {
		t.Errorf("the page never names %s, so nobody learns how to tell which arm they are on", ArmHeader)
	}
	// And each arm says how to be seen deliberately, rather than by waiting to
	// be assigned to it and hoping.
	if !strings.Contains(out, "mendel_arm=fast-a1b2c3") {
		t.Error("no way to reach one arm on purpose")
	}
}

// Mainline is never offered a rebuild.
//
// It keeps the Deployment the ordinary production deploy made. Rebuilding it
// would make the control a new thing, and the comparison would then be against
// something nobody had been running.
func TestMainlineIsNotOfferedARebuild(t *testing.T) {
	out, project, experiment, _ := hopPageWithExperiment(t, domain.ExperimentRunning, nil)

	prefix := "/p/" + project.String() + "/experiments/" + experiment.String() + "/arms/"
	if n := strings.Count(out, prefix); n != 1 {
		t.Errorf("expected exactly one arm to be rebuildable, found %d", n)
	}
}

// Readiness not yet established is not the same as readiness established and
// fine. A page that showed an empty blocker list while Mendel had not looked
// would be reporting a clean bill of health it had never checked.
func TestUncheckedReadinessIsNotReported(t *testing.T) {
	out, _, _, _ := hopPageWithExperiment(t, domain.ExperimentStopped, func(v *ExperimentView) {
		v.Checking = true
	})

	view := &ExperimentView{Checking: true}
	if view.Ready() {
		t.Error("an unchecked project reported itself ready")
	}
	if !strings.Contains(out, "still checking") {
		t.Error("the page does not say that readiness is still being established")
	}
}

// The experiment reads at the width of the page, directly under the decision
// ribbon.
//
// It was first placed in the sidebar column beside Cost, which is a third of the
// page: the arms table was crushed to a few characters per cell, and the build
// verdict -- the column that says whether visitors are seeing your latest change
// -- wrapped to one word a line. A live experiment is the widest thing on this
// page and the most consequential, not an aside.
func TestTheExperimentIsFullWidthAndAboveTheColumns(t *testing.T) {
	out, _, _, _ := hopPageWithExperiment(t, domain.ExperimentRunning, nil)

	panel := strings.Index(out, "Live traffic")
	columns := strings.Index(out, `<div class="split">`)
	ribbon := strings.Index(out, "ribbon-headline")

	if panel < 0 || columns < 0 {
		t.Fatal("the page no longer has both an experiment panel and a two-column section")
	}
	if panel > columns {
		t.Error("the experiment is inside or below the two-column section, so it renders " +
			"at a third of the page width")
	}
	if ribbon >= 0 && panel < ribbon {
		t.Error("the experiment is above the decision ribbon, which is the page's headline")
	}
}

// A running experiment is not told that its start button is on the way.
//
// Readiness gates starting and nothing else. Rendered on a running experiment it
// read "the start button appears once it knows" beside a stop button and a badge
// saying "running" -- three claims, of which two were true.
func TestARunningExperimentIsNotToldAboutReadiness(t *testing.T) {
	out, _, _, _ := hopPageWithExperiment(t, domain.ExperimentRunning, func(v *ExperimentView) {
		v.Checking = true
		v.Blockers = []string{"Production answers at a name: Production has no hostname."}
	})

	if strings.Contains(out, "start button appears once it knows") {
		t.Error("a running experiment was told its start button is still coming")
	}
	if strings.Contains(out, "Not yet, because of the project") {
		t.Error("a running experiment was told it cannot start, which it already has")
	}
	// And it can still be stopped, which is the one thing readiness must never
	// stand in the way of.
	if !strings.Contains(out, "/stop") {
		t.Error("a running experiment cannot be stopped while readiness is unresolved")
	}
}
