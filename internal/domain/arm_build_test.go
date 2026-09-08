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
	b := DescribeArmBuild(variationArm(), "")

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

	current := DescribeArmBuild(arm, arm.SourceCommit)
	if current.Freshness != ArmCurrent || current.Stale() {
		t.Errorf("an arm built from the head should be current, got %q", current.Freshness)
	}

	stale := DescribeArmBuild(arm, "2222222222222222222222222222222222222222")
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

	b := DescribeArmBuild(arm, "2222222222222222222222222222222222222222")
	if b.Freshness != ArmNeverBuilt {
		t.Errorf("freshness = %q, want %q", b.Freshness, ArmNeverBuilt)
	}
	if b.Stale() {
		t.Error("an arm that was never built cannot be behind")
	}
}

// Mainline is not built by the experiment: it keeps the Deployment the ordinary
// production deploy made. Asking whether it is stale is asking about production,
// which is a different page's question -- and reporting it stale here would put
// a rebuild button on the one arm that must not be rebuilt, since rebuilding the
// control makes it a new thing and the comparison is then against something
// nobody had been running.
func TestMainlineIsNeverJudgedStale(t *testing.T) {
	b := DescribeArmBuild(ExperimentArm{ID: uuid.New(), Slug: MainlineSlug}, "abc1234")
	if b.Stale() || b.Freshness != ArmCurrent {
		t.Errorf("mainline freshness = %q, want %q", b.Freshness, ArmCurrent)
	}
}
