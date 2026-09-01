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

// TestTrialTrackMatchesWhatTheHopAsksFor: a trial is not one fixed thing. A Hop
// that only wants a clickable demo never migrates data and never takes real
// traffic, so promising either would describe work that will not happen.
func TestTrialTrackMatchesWhatTheHopAsksFor(t *testing.T) {
	labelsOf := func(r Ribbon) string {
		var s string
		for _, st := range r.Track(VariationTrackTrial).Stages {
			s += st.Label + "|"
		}
		return s
	}

	demo := labelsOf(VariationLifecycle(
		&Variation{Status: VariationStatusActive}, nil, &Hop{RequiresDemo: true}))
	for _, banned := range []string{"live traffic", "migration", "Draining"} {
		if strings.Contains(demo, banned) {
			t.Errorf("demo-only trial mentions %q: %s", banned, demo)
		}
	}

	prod := labelsOf(VariationLifecycle(
		&Variation{Status: VariationStatusActive}, nil, &Hop{RequiresProduction: true}))
	if !strings.Contains(prod, "Serving live traffic") {
		t.Errorf(`a production trial should say "Serving live traffic": %s`, prod)
	}

	// Migrations are a detail of some deployments, so the word should surface
	// only while one is actually running.
	migrating := labelsOf(VariationLifecycle(
		&Variation{Status: VariationStatusMigrating}, nil, &Hop{RequiresProduction: true}))
	if !strings.Contains(migrating, "Applying data migrations") {
		t.Errorf("a migrating Variation should name the migration: %s", migrating)
	}
	if strings.Contains(prod, "migration") {
		t.Errorf("a Variation that is not migrating should not mention migrations: %s", prod)
	}
}

// TestStatusLabelNeverCallsFailureComplete: "Complete" is reserved for a
// terminal state that actually succeeded. Reporting a failed build as complete
// is the same class of bug as painting it green.
func TestStatusLabelNeverCallsFailureComplete(t *testing.T) {
	for _, st := range allVariationStatuses {
		r := VariationLifecycle(&Variation{Status: st}, nil, &Hop{})
		if r.Tone == ToneFailure && r.StatusLabel() == "Complete" {
			t.Errorf("variation %q failed but its badge reads Complete", st)
		}
		if r.StatusLabel() == "Complete" && r.Tone != ToneSuccess {
			t.Errorf("variation %q reads Complete with tone %q", st, r.Tone)
		}
	}
	for _, st := range allHopStatuses {
		r := HopLifecycle(&Hop{Status: st}, nil)
		if r.Tone == ToneFailure && r.StatusLabel() == "Complete" {
			t.Errorf("hop %q failed but its badge reads Complete", st)
		}
	}

	failed := VariationLifecycle(&Variation{Status: VariationStatusTerminated}, nil, &Hop{})
	if failed.StatusLabel() != "Failed" || failed.StatusClass() != "failed" {
		t.Errorf("a terminated Variation should be badged Failed, got %q/%q",
			failed.StatusLabel(), failed.StatusClass())
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

// A run that stopped at its spend ceiling has intact code. Offering to throw it
// away sits badly next to offering to fund it further, and the two were
// genuinely both drawn by the old template.
func TestPausedForBudgetOffersContinueNotRegenerate(t *testing.T) {
	paused := 3.5
	v := &Variation{Status: VariationStatusError, BudgetPausedUSD: &paused}
	got := VariationActions(v, true)

	if !got.Continue {
		t.Error("a run paused on cost should offer to continue")
	}
	if got.Regenerate {
		t.Error("a run paused on cost should not offer to discard its work")
	}
	if got.RetryWithFix {
		t.Error("nothing was diagnosed: the run ran out of money, it did not fail")
	}
}

// Adjudicated and live Variations are outcomes, not workbenches.
func TestSettledVariationsOfferNothing(t *testing.T) {
	for _, st := range []VariationStatus{
		VariationStatusMerged, VariationStatusSelected, VariationStatusRejected,
		VariationStatusPruned, VariationStatusActive, VariationStatusDraining,
		VariationStatusMigrating,
	} {
		if got := VariationActions(&Variation{Status: st}, true); got.Any() {
			t.Errorf("%s should offer nothing, got %+v", st, got)
		}
	}
}

// Mid-build there is nothing to revise and nothing to rebase; the one useful
// intervention is to stop it.
func TestCreatingOnlyOffersTerminate(t *testing.T) {
	got := VariationActions(&Variation{Status: VariationStatusCreating}, true)
	if !got.Terminate {
		t.Error("a running build should be stoppable")
	}
	if got.RequestChange || got.Regenerate || got.Rebase {
		t.Errorf("a running build should offer nothing else, got %+v", got)
	}
}

// RetryWithFix is only honest when something was actually diagnosed.
func TestRetryWithFixFollowsTheDiagnosis(t *testing.T) {
	v := &Variation{Status: VariationStatusTerminated}
	if VariationActions(v, false).RetryWithFix {
		t.Error("offered a fix when nothing was diagnosed")
	}
	if !VariationActions(v, true).RetryWithFix {
		t.Error("withheld a fix when one was diagnosed")
	}
}

// A run that stopped on cost sits in `blocked` or `error` like any other
// interrupted run, so the status column alone would have the ribbon tell the
// reader to go and find a credential that does not exist.
func TestBudgetPauseIsNotReportedAsBlockedOnInput(t *testing.T) {
	paused := 5.02
	for _, st := range []VariationStatus{VariationStatusBlocked, VariationStatusError} {
		v := &Variation{Status: st, BudgetPausedUSD: &paused}
		r := VariationLifecycle(v, nil, nil)

		if !strings.Contains(r.Headline, "spend ceiling") {
			t.Errorf("%s: headline %q does not mention the spend ceiling", st, r.Headline)
		}
		if strings.Contains(r.NextAction, "credential") {
			t.Errorf("%s: sends the reader after a credential that is not the problem", st)
		}
		if r.WaitingOn != ActorYou {
			t.Errorf("%s: a paused run is the user's move, got %q", st, r.WaitingOn)
		}
	}

	// Without the pause it must still read as blocked on input.
	ordinary := VariationLifecycle(&Variation{Status: VariationStatusBlocked}, nil, nil)
	if !strings.Contains(ordinary.NextAction, "credential") {
		t.Errorf("an ordinary blocked variation lost its explanation: %q", ordinary.NextAction)
	}
}

// Every InputRequest is created `needs_assignment` and nothing in the codebase
// ever moves it on, so that state means "nobody has looked at this yet" — which
// makes it the user's move, not Mendel's.
//
// Reporting it as Mendel's work is not a cosmetic error. It puts the request in
// the "Mendel is working on" list on the overview and tells the reader nothing
// is needed from them, about the only thing that will ever unblock it.
func TestUnroutedRequestIsTheUsersMove(t *testing.T) {
	ir := &InputRequest{
		Status: InputRequestStatusNeedsAssignment,
		Kind:   InputRequestKindCredentialRequest,
	}
	r := DecisionLifecycle(ir)

	if r.WaitingOn != ActorYou {
		t.Errorf("an unrouted request should be the user's move, got %q", r.WaitingOn)
	}
	if r.Tone != ToneWaiting {
		t.Errorf("tone = %q, want %q", r.Tone, ToneWaiting)
	}
	// The ask must be the specific one for its kind, not a generic "being routed".
	if r.NextAction != DecisionAsk(InputRequestKindCredentialRequest) {
		t.Errorf("NextAction = %q, want the credential ask", r.NextAction)
	}
}

// The corollary, stated as its own rule because it is the one that shows up on
// the overview: nothing that is still open may claim to be Mendel's problem
// unless Mendel is genuinely working on it.
func TestNoOpenRequestHidesFromTheUser(t *testing.T) {
	for _, st := range []InputRequestStatus{
		InputRequestStatusNeedsAssignment,
		InputRequestStatusAssigned,
		InputRequestStatusAccepted,
	} {
		r := DecisionLifecycle(&InputRequest{Status: st, Kind: InputRequestKindPassFail})
		if !r.WaitingOnYou() {
			t.Errorf("%s: an open request must surface as the user's move, got %q", st, r.WaitingOn)
		}
	}

	// And a resolved one must not.
	if DecisionLifecycle(&InputRequest{Status: InputRequestStatusResolved}).WaitingOnYou() {
		t.Error("a resolved request should not still be asking for something")
	}
}
