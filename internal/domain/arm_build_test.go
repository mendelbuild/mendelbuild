package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func variationArm() ExperimentArm {
	v := uuid.New()
	built := time.Now().Add(-2 * time.Hour)
	return ExperimentArm{
		ID: uuid.New(), VariationID: &v, Slug: "faster-checkout",
		SourceCommit: "1111111111111111111111111111111111111111",
		Image:        "gcr.io/p/app:1", BuiltAt: &built,
	}
}

// A branch Mendel could not read is not a branch that has moved.
//
// The distinction Fact exists for, applied to builds. Folding "could not look"
// into "stale" tells somebody to rebuild an arm that is already current -- which
// wastes a build and, mid-experiment, changes what its participants see for no
// reason at all. The opposite fold is worse: it reports an arm as up to date on
// the strength of never having checked.
func TestAnUnreadableBranchIsUnknownAndNotStale(t *testing.T) {
	b := DescribeArmBuild(variationArm(), "", true)

	if b.Freshness != ArmFreshnessUnknown {
		t.Errorf("freshness = %q, want %q", b.Freshness, ArmFreshnessUnknown)
	}
	if b.Stale() {
		t.Error("an unread branch was reported as stale, which asks for a needless rebuild")
	}
	if b.Freshness == ArmCurrent {
		t.Error("an unread branch was reported as current, which claims a check that never happened")
	}
}

func TestFreshnessAgainstABranchHead(t *testing.T) {
	arm := variationArm()

	current := DescribeArmBuild(arm, arm.SourceCommit, true)
	if current.Freshness != ArmCurrent || current.Stale() {
		t.Errorf("an arm built from the head should be current, got %q", current.Freshness)
	}

	stale := DescribeArmBuild(arm, "2222222222222222222222222222222222222222", true)
	if stale.Freshness != ArmStale || !stale.Stale() {
		t.Errorf("an arm built from something else should be stale, got %q", stale.Freshness)
	}
	// The sentence has to name both commits, because "stale" alone does not tell
	// anybody whether the change they are looking for is the missing one.
	if !strings.Contains(stale.Detail, "1111111") || !strings.Contains(stale.Detail, "2222222") {
		t.Errorf("the detail should name what is running and what the branch has: %q", stale.Detail)
	}
}

// Never built is its own state. An arm with no image is not an arm serving old
// code -- nothing is serving from it at all -- and "rebuild this" is the wrong
// instruction for an experiment that has not been started.
func TestAnArmThatWasNeverBuiltSaysSo(t *testing.T) {
	arm := variationArm()
	arm.SourceCommit, arm.Image, arm.BuiltAt = "", "", nil

	b := DescribeArmBuild(arm, "2222222222222222222222222222222222222222", false)
	if b.Freshness != ArmNeverBuilt {
		t.Errorf("freshness = %q, want %q", b.Freshness, ArmNeverBuilt)
	}
	if b.Stale() {
		t.Error("an arm that was never built cannot be behind")
	}
}

// An arm serving traffic with no recorded commit is unrecorded, not never built.
//
// An empty SourceCommit means Mendel has no record. It does not mean no image
// exists, and an arm answering requests is proof that one does -- so reporting
// it as "not built yet, starting the experiment builds it" describes the
// opposite of what is happening, on a page whose entire purpose is to say what
// visitors are seeing.
//
// This is reachable in normal running and not only on rows predating the
// columns: recording the build is deliberately not allowed to fail the build,
// since the image exists whether or not Mendel managed to write it down.
func TestAServingArmWithNoRecordIsUnrecordedRatherThanNeverBuilt(t *testing.T) {
	arm := variationArm()
	arm.SourceCommit, arm.Image, arm.BuiltAt = "", "", nil

	b := DescribeArmBuild(arm, "2222222222222222222222222222222222222222", true)
	if b.Freshness != ArmBuildUnrecorded {
		t.Errorf("freshness = %q, want %q", b.Freshness, ArmBuildUnrecorded)
	}
	if b.Stale() {
		t.Error("an arm whose build was never recorded cannot be known to be behind")
	}
	// The sentence has to say it is serving, or a reader takes "no record" to
	// mean "nothing is running".
	if !strings.Contains(b.Detail, "Serving traffic") {
		t.Errorf("the detail does not say the arm is serving: %q", b.Detail)
	}
	// And it has to name the way out, which is the same button the page already
	// offers: a rebuild records what it produced.
	if !strings.Contains(b.Detail, "rebuilding") {
		t.Errorf("the detail does not say how to get the record back: %q", b.Detail)
	}
}

// The two ways of having no answer stay apart, and neither is stale.
//
// Unknown is "Mendel could not read the branch"; unrecorded is "Mendel read the
// branch and has nothing to compare it against". Both must refuse to guess: the
// stale badge is the one that says visitors are missing your change, and saying
// that without evidence is the failure this type exists to prevent.
func TestNeitherKindOfNotKnowingIsReportedAsStale(t *testing.T) {
	unrecorded := variationArm()
	unrecorded.SourceCommit, unrecorded.Image, unrecorded.BuiltAt = "", "", nil

	for name, b := range map[string]ArmBuild{
		"branch unreadable": DescribeArmBuild(variationArm(), "", true),
		"build unrecorded":  DescribeArmBuild(unrecorded, "abcdef1234", true),
	} {
		if b.Stale() {
			t.Errorf("%s was reported as stale", name)
		}
		if b.Freshness == ArmCurrent {
			t.Errorf("%s was reported as current", name)
		}
	}

	// And they are not the same state, because the remedies differ: one waits
	// on a remote, the other is fixed by a rebuild.
	if DescribeArmBuild(variationArm(), "", true).Freshness ==
		DescribeArmBuild(unrecorded, "abcdef1234", true).Freshness {
		t.Error("an unreadable branch and an unrecorded build report identically")
	}
}

// Mainline is not built by the experiment: it keeps the Deployment the ordinary
// production deploy made. Asking whether it is stale is asking about production,
// which is a different page's question -- and reporting it stale here would put
// a rebuild button on the one arm that must not be rebuilt, since rebuilding the
// control makes it a new thing and the comparison is then against something
// nobody had been running.
func TestMainlineIsNeverJudgedStale(t *testing.T) {
	b := DescribeArmBuild(ExperimentArm{ID: uuid.New(), Slug: MainlineSlug}, "abc1234", true)
	if b.Stale() || b.Freshness != ArmCurrent {
		t.Errorf("mainline freshness = %q, want %q", b.Freshness, ArmCurrent)
	}
}
