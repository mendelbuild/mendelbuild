package domain

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// allHopStatuses / allVariationStatuses exist so that adding a status to the
// enum without teaching the lifecycle about it fails the build's tests rather
// than silently rendering "Unrecognized state" in the UI.
var allHopStatuses = []HopStatus{
	HopStatusPending, HopStatusActive, HopStatusSelecting,
	HopStatusCompleted, HopStatusRejected, HopStatusAbandoned,
}

var allVariationStatuses = []VariationStatus{
	VariationStatusCreating, VariationStatusPending, VariationStatusBlocked,
	VariationStatusMigrating, VariationStatusActive, VariationStatusDraining,
	VariationStatusError, VariationStatusTerminated, VariationStatusPruned,
	VariationStatusSelected, VariationStatusMerged, VariationStatusRejected,
}

var allDecisionStatuses = []InputRequestStatus{
	InputRequestStatusNeedsAssignment, InputRequestStatusAssigned,
	InputRequestStatusAccepted, InputRequestStatusResolved,
}

var allDecisionKinds = []InputRequestKind{
	InputRequestKindPassFail, InputRequestKindChooseOne, InputRequestKindChooseMany,
	InputRequestKindRoadmapReview, InputRequestKindVariationReview,
	InputRequestKindVariationSelection, InputRequestKindCredentialRequest,
	InputRequestKindManualSetup, InputRequestKindConfirmation,
	InputRequestKindHostingPlatform,
}

func TestHopLifecycleCoversEveryStatus(t *testing.T) {
	for _, st := range allHopStatuses {
		h := &Hop{Status: st}
		r := HopLifecycle(h, nil)
		if strings.Contains(r.Headline, "Unrecognized") {
			t.Errorf("HopStatus %q has no lifecycle mapping", st)
		}
		if r.Headline == "" || r.NextAction == "" {
			t.Errorf("HopStatus %q produced an empty headline or next action", st)
		}
		if len(r.Tracks) != 1 {
			t.Errorf("HopStatus %q: want 1 track, got %d", st, len(r.Tracks))
		}
	}
}

func TestVariationLifecycleCoversEveryStatus(t *testing.T) {
	for _, st := range allVariationStatuses {
		v := &Variation{Status: st}
		r := VariationLifecycle(v, []VariationRevision{}, nil)
		if strings.Contains(r.Headline, "Unrecognized") {
			t.Errorf("VariationStatus %q has no lifecycle mapping", st)
		}
		if r.Headline == "" || r.NextAction == "" {
			t.Errorf("VariationStatus %q produced an empty headline or next action", st)
		}
		if len(r.Tracks) != 4 {
			t.Errorf("VariationStatus %q: want 4 tracks, got %d", st, len(r.Tracks))
		}
	}
}

func TestDecisionLifecycleCoversEveryStatusAndKind(t *testing.T) {
	for _, st := range allDecisionStatuses {
		ir := &InputRequest{Status: st, Kind: InputRequestKindPassFail}
		r := DecisionLifecycle(ir)
		if strings.Contains(r.Headline, "Unrecognized") {
			t.Errorf("InputRequestStatus %q has no lifecycle mapping", st)
		}
	}
	for _, k := range allDecisionKinds {
		if label := DecisionKindLabel(k); label == string(k) {
			t.Errorf("InputRequestKind %q falls through to the raw enum", k)
		}
		if ask := DecisionAsk(k); ask == "" {
			t.Errorf("InputRequestKind %q has no plain-English ask", k)
		}
	}
}

// TestTerminatedIsNotSuccess guards the bug this package replaces: both detail
// templates mapped `terminated` to status-resolved, rendering a code/test
// failure in success green.
func TestTerminatedIsNotSuccess(t *testing.T) {
	v := &Variation{Status: VariationStatusTerminated}
	r := VariationLifecycle(v, nil, nil)
	if r.Tone == ToneSuccess {
		t.Errorf("terminated must not be ToneSuccess; got %q", r.Tone)
	}
	if r.Tone != ToneFailure {
		t.Errorf("terminated should read as a failure; got %q", r.Tone)
	}
}

// TestRefineDistinguishedFromFirstBuild is the reason the Refine track exists.
// handleRequestChange sets the Variation back to `creating`, so status alone
// cannot tell a first build from a revision in flight.
func TestRefineDistinguishedFromFirstBuild(t *testing.T) {
	v := &Variation{Status: VariationStatusCreating}

	first := VariationLifecycle(v, []VariationRevision{}, nil)
	if got := first.Track(VariationTrackRefine); got.Applicable {
		t.Error("a first build should not have an applicable Refine track")
	}
	if !strings.Contains(first.Headline, "Writing code") {
		t.Errorf("first build headline = %q", first.Headline)
	}

	revising := VariationLifecycle(v, []VariationRevision{
		{ID: uuid.New(), Status: VariationRevisionStatusCompleted},
		{ID: uuid.New(), Status: VariationRevisionStatusInProgress},
	}, nil)
	refine := revising.Track(VariationTrackRefine)
	if !refine.Applicable {
		t.Fatal("a revision in flight should make the Refine track applicable")
	}
	if refine.Current() == nil {
		t.Fatal("a revision in flight should have a current Refine stage")
	}
	if !strings.Contains(revising.Headline, "feedback") {
		t.Errorf("revision headline = %q, want it to mention feedback", revising.Headline)
	}

	// The whole point: the two states must not look alike.
	if first.Headline == revising.Headline {
		t.Error("a first build and a revision in flight render identically")
	}

	// Build is already finished when a revision is underway.
	build := revising.Track(VariationTrackBuild)
	if build.Current() != nil {
		t.Error("Build should be complete while a revision is in flight")
	}
}

func TestTrialTrackAppliesOnlyWhenRelevant(t *testing.T) {
	v := &Variation{Status: VariationStatusPending}

	noTrial := VariationLifecycle(v, nil, &Hop{})
	if noTrial.Track(VariationTrackTrial).Applicable {
		t.Error("a Hop requiring neither demo nor production should not show a Trial track")
	}

	wantsDemo := VariationLifecycle(v, nil, &Hop{RequiresDemo: true})
	if !wantsDemo.Track(VariationTrackTrial).Applicable {
		t.Error("a Hop requiring a demo should show a Trial track")
	}

	// Evidence beats configuration: a live Variation has plainly had a trial.
	live := VariationLifecycle(&Variation{Status: VariationStatusActive}, nil, &Hop{})
	if !live.Track(VariationTrackTrial).Applicable {
		t.Error("an active Variation must show a Trial track regardless of Hop flags")
	}
}

func TestHopHeadlineReflectsVariations(t *testing.T) {
	h := &Hop{ID: uuid.New(), Status: HopStatusActive}

	stuck := HopLifecycle(h, []Variation{
		{Status: VariationStatusTerminated},
		{Status: VariationStatusRejected},
	})
	if !stuck.WaitingOnYou() {
		t.Error("a Hop whose variations are all eliminated needs a human")
	}

	building := HopLifecycle(h, []Variation{
		{Status: VariationStatusCreating},
		{Status: VariationStatusPending},
	})
	if building.WaitingOnYou() {
		t.Error("a Hop still building variations should not be waiting on a human")
	}
	if !strings.Contains(building.Headline, "Building") {
		t.Errorf("building headline = %q", building.Headline)
	}
}

func TestTerminalRibbonsWaitOnNobody(t *testing.T) {
	terminal := map[HopStatus]bool{
		HopStatusCompleted: true, HopStatusRejected: true, HopStatusAbandoned: true,
	}
	for st, want := range terminal {
		r := HopLifecycle(&Hop{Status: st}, nil)
		if r.Terminal() != want {
			t.Errorf("HopStatus %q: Terminal() = %v, want %v", st, r.Terminal(), want)
		}
	}
	for _, st := range []HopStatus{HopStatusPending, HopStatusActive, HopStatusSelecting} {
		if HopLifecycle(&Hop{Status: st}, nil).Terminal() {
			t.Errorf("HopStatus %q should not be terminal", st)
		}
	}
}

func TestStageSeqHasAtMostOneCurrent(t *testing.T) {
	check := func(name string, tracks []Track) {
		for _, tr := range tracks {
			n := 0
			for _, s := range tr.Stages {
				if s.State == StageCurrent || s.State == StageFailed {
					n++
				}
			}
			if n > 1 {
				t.Errorf("%s track %q has %d current/failed stages, want at most 1", name, tr.Key, n)
			}
		}
	}
	for _, st := range allHopStatuses {
		check(string(st), HopLifecycle(&Hop{Status: st}, nil).Tracks)
	}
	for _, st := range allVariationStatuses {
		check(string(st), VariationLifecycle(&Variation{Status: st}, []VariationRevision{}, &Hop{RequiresDemo: true}).Tracks)
	}
}

func TestDecisionImportanceIsWords(t *testing.T) {
	cases := map[float64]string{0.9: "High", 0.5: "Medium", 0.1: "Low"}
	for score, want := range cases {
		if got := DecisionImportance(score); got != want {
			t.Errorf("DecisionImportance(%v) = %q, want %q", score, got, want)
		}
	}
}
