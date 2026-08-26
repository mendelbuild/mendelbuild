package domain

import "fmt"

// Variation lifecycle: four concurrent tracks, not one sequence.
//
//	Build   -> Refine -> Trial -> Verdict
//
// VariationStatus is a single column doing three unrelated jobs at once: build
// progress (creating/pending/blocked), runtime state (migrating/active/
// draining), and adjudication outcome (pruned/selected/merged/rejected). A
// Variation can legitimately be built AND live AND unjudged at the same moment,
// so rendering one linear stepper would misstate its position. Each concern
// gets its own track.
//
// Refine sits between Build and Trial because user-requested changes are a
// distinct phase, not a repeat of the first build. It needs its own track for a
// concrete reason: handleRequestChange sets the Variation back to `creating`,
// so a first build and a third revision are indistinguishable from the status
// column alone. The revision records are the only place that difference lives,
// which is why this function requires them.

const (
	VariationTrackBuild   = "build"
	VariationTrackRefine  = "refine"
	VariationTrackTrial   = "trial"
	VariationTrackVerdict = "verdict"
)

var (
	buildKeys     = []string{"generating", "built"}
	buildLabels   = []string{"Writing code", "Code ready"}
	refineKeys    = []string{"requested", "applying", "applied"}
	refineLabels  = []string{"Change requested", "Applying your feedback", "Feedback applied"}
	trialKeys     = []string{"migrating", "live", "drained"}
	trialLabels   = []string{"Applying migrations", "Live on traffic", "Traffic drained"}
	verdictKeys   = []string{"awaiting", "decided"}
	verdictLabels = []string{"Awaiting decision", "Decided"}
)

// revisionSummary describes the revision history of a Variation.
type revisionSummary struct {
	total     int
	inFlight  bool // A revision is pending or being applied right now
	failed    bool // The most recent revision failed
	completed int
}

func summarizeRevisions(revs []VariationRevision) revisionSummary {
	var s revisionSummary
	s.total = len(revs)
	for _, rev := range revs {
		switch rev.Status {
		case VariationRevisionStatusPending, VariationRevisionStatusInProgress:
			s.inFlight = true
		case VariationRevisionStatusCompleted:
			s.completed++
		case VariationRevisionStatusFailed:
			s.failed = true
		}
	}
	return s
}

// VariationLifecycle maps a Variation onto a multi-track Ribbon.
//
// revs carries the Variation's revision history and must be supplied for the
// Refine track to be accurate; pass nil only when the caller genuinely has no
// revision data, in which case Refine is reported as not applicable.
//
// h may be nil. It is used only to decide whether the Trial track applies,
// since a Variation belonging to a Hop that requires neither a demo nor
// production traffic will never be deployed.
func VariationLifecycle(v *Variation, revs []VariationRevision, h *Hop) Ribbon {
	rev := summarizeRevisions(revs)
	st := v.Status

	// A revision in flight masquerades as `creating`. Detect that first: the
	// code was already built once, so Build is done and Refine is the live track.
	refining := st == VariationStatusCreating && rev.inFlight

	r := Ribbon{Subject: "Variation"}
	r.Tracks = []Track{
		variationBuildTrack(st, refining),
		variationRefineTrack(revs, rev, refining),
		variationTrialTrack(st, h),
		variationVerdictTrack(st),
	}

	switch {
	case refining:
		r.Tone, r.WaitingOn = ToneProgress, ActorMendel
		r.Headline = fmt.Sprintf("Applying your feedback (revision %d)", rev.total)
		r.NextAction = "Mendel is revising the code with the change you requested. Nothing is needed from you."

	case st == VariationStatusCreating:
		r.Tone, r.WaitingOn = ToneProgress, ActorMendel
		r.Headline = "Writing code"
		r.NextAction = "Mendel is implementing this approach. It will be ready to review when the build finishes."

	case st == VariationStatusBlocked:
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "Blocked — needs something from you"
		r.NextAction = "This variation is waiting on an open decision, usually a credential. Resolve it to continue."

	case st == VariationStatusPending:
		r.Tone, r.WaitingOn = ToneProgress, ActorNobody
		r.Headline = "Built and awaiting comparison"
		r.NextAction = "The code is ready. You can request changes, or wait for the comparison against the other variations."
		if rev.total > 0 {
			r.Headline = fmt.Sprintf("Built, with %s applied", plural(rev.completed, "revision", "revisions"))
		}

	case st == VariationStatusError:
		// Documented as a retryable Mendel infrastructure failure, so this is a
		// warning rather than a dead end.
		r.Tone, r.WaitingOn = ToneWarning, ActorYou
		r.Headline = "Build failed — can be retried"
		r.NextAction = "Something in Mendel's infrastructure failed, not your code. Retry the build."

	case st == VariationStatusTerminated:
		// Not a success. Both detail templates currently render this green via
		// status-resolved; that mapping is wrong and this replaces it.
		r.Tone, r.WaitingOn = ToneFailure, ActorNobody
		r.Headline = "Failed — code or tests did not pass"
		r.NextAction = "This variation is out of the running and cannot be retried. Request a new variation instead."

	case st == VariationStatusMigrating:
		r.Tone, r.WaitingOn = ToneProgress, ActorMendel
		r.Headline = "Applying data migrations"
		r.NextAction = "Mendel is preparing this variation's environment before sending it traffic."

	case st == VariationStatusActive:
		r.Tone, r.WaitingOn = ToneProgress, ActorMendel
		r.Headline = "Live and receiving traffic"
		r.NextAction = "This variation is running. Results are being gathered for the comparison."

	case st == VariationStatusDraining:
		r.Tone, r.WaitingOn = ToneProgress, ActorMendel
		r.Headline = "Draining traffic"
		r.NextAction = "Traffic is being shifted away from this variation before it is cleaned up."

	case st == VariationStatusPruned:
		r.Tone, r.WaitingOn = ToneNeutral, ActorNobody
		r.Headline = "Eliminated during comparison"
		r.NextAction = "Nothing further. This variation was ruled out before the final decision."

	case st == VariationStatusRejected:
		r.Tone, r.WaitingOn = ToneNeutral, ActorNobody
		r.Headline = "Not selected"
		r.NextAction = "Nothing further. A different variation won this Hop."

	case st == VariationStatusMerged, st == VariationStatusSelected:
		r.Tone, r.WaitingOn = ToneSuccess, ActorNobody
		r.Headline = "Winner — merged to main"
		r.NextAction = "Nothing further. This variation's code is on main."

	default:
		r.Tone, r.WaitingOn = ToneNeutral, ActorMendel
		r.Headline = fmt.Sprintf("Unrecognized state (%s)", st)
		r.NextAction = "Mendel does not recognize this Variation's state. This is a bug worth reporting."
	}

	return r
}

func variationBuildTrack(st VariationStatus, refining bool) Track {
	switch {
	case refining:
		// Built once already; the current work belongs to Refine.
		return stageSeq(VariationTrackBuild, "Build", buildKeys, buildLabels, 2, ToneSuccess)
	case st == VariationStatusCreating:
		return stageSeq(VariationTrackBuild, "Build", buildKeys, buildLabels, 0, ToneProgress)
	case st == VariationStatusBlocked:
		t := stageSeq(VariationTrackBuild, "Build", buildKeys, buildLabels, 0, ToneWaiting)
		t.Stages[0].Label = "Blocked — needs input"
		return t
	case st == VariationStatusError:
		t := stageSeq(VariationTrackBuild, "Build", buildKeys, buildLabels, 0, ToneWarning)
		t.Stages[0].State, t.Stages[0].Label = StageFailed, "Build failed (retryable)"
		return t
	case st == VariationStatusTerminated:
		t := stageSeq(VariationTrackBuild, "Build", buildKeys, buildLabels, 0, ToneFailure)
		t.Stages[0].State, t.Stages[0].Label = StageFailed, "Code or tests failed"
		return t
	default:
		// Every remaining status implies the code was built successfully.
		return stageSeq(VariationTrackBuild, "Build", buildKeys, buildLabels, 2, ToneSuccess)
	}
}

func variationRefineTrack(revs []VariationRevision, rev revisionSummary, refining bool) Track {
	if revs == nil {
		return Track{
			Key: VariationTrackRefine, Label: "Refine",
			Stages:     stageSeq(VariationTrackRefine, "Refine", refineKeys, refineLabels, -1, ToneNeutral).Stages,
			Applicable: false,
			Note:       "Revision history not loaded",
		}
	}
	if rev.total == 0 {
		t := stageSeq(VariationTrackRefine, "Refine", refineKeys, refineLabels, -1, ToneNeutral)
		t.Applicable = false
		t.Note = "No changes requested yet"
		return t
	}

	var t Track
	switch {
	case refining:
		t = stageSeq(VariationTrackRefine, "Refine", refineKeys, refineLabels, 1, ToneProgress)
	case rev.failed:
		t = stageSeq(VariationTrackRefine, "Refine", refineKeys, refineLabels, 1, ToneFailure)
		t.Stages[1].State, t.Stages[1].Label = StageFailed, "Revision failed"
	default:
		t = stageSeq(VariationTrackRefine, "Refine", refineKeys, refineLabels, 3, ToneSuccess)
	}
	// Refine is a loop, not a one-shot step; the count is the useful signal.
	t.Note = plural(rev.total, "change requested", "changes requested")
	if c := t.Current(); c != nil {
		c.Note = fmt.Sprintf("Revision %d", rev.total)
	}
	return t
}

func variationTrialTrack(st VariationStatus, h *Hop) Track {
	reached := st == VariationStatusMigrating || st == VariationStatusActive || st == VariationStatusDraining
	wanted := h != nil && (h.RequiresDemo || h.RequiresProduction)

	if !reached && !wanted {
		t := stageSeq(VariationTrackTrial, "Trial", trialKeys, trialLabels, -1, ToneNeutral)
		t.Applicable = false
		t.Note = "This Hop does not require a deployed trial"
		return t
	}

	switch st {
	case VariationStatusMigrating:
		return stageSeq(VariationTrackTrial, "Trial", trialKeys, trialLabels, 0, ToneProgress)
	case VariationStatusActive:
		return stageSeq(VariationTrackTrial, "Trial", trialKeys, trialLabels, 1, ToneProgress)
	case VariationStatusDraining:
		t := stageSeq(VariationTrackTrial, "Trial", trialKeys, trialLabels, 2, ToneProgress)
		t.Stages[2].Label = "Draining traffic"
		return t
	default:
		return stageSeq(VariationTrackTrial, "Trial", trialKeys, trialLabels, -1, ToneNeutral)
	}
}

func variationVerdictTrack(st VariationStatus) Track {
	switch st {
	case VariationStatusMerged, VariationStatusSelected:
		t := stageSeq(VariationTrackVerdict, "Verdict", verdictKeys, verdictLabels, 1, ToneSuccess)
		t.Stages[1].Label, t.Stages[1].State = "Selected — merged to main", StageDone
		return t
	case VariationStatusRejected:
		t := stageSeq(VariationTrackVerdict, "Verdict", verdictKeys, verdictLabels, 1, ToneNeutral)
		t.Stages[1].Label, t.Stages[1].State = "Not selected", StageDone
		return t
	case VariationStatusPruned:
		t := stageSeq(VariationTrackVerdict, "Verdict", verdictKeys, verdictLabels, 1, ToneNeutral)
		t.Stages[1].Label, t.Stages[1].State = "Eliminated during comparison", StageDone
		return t
	case VariationStatusTerminated:
		t := stageSeq(VariationTrackVerdict, "Verdict", verdictKeys, verdictLabels, -1, ToneNeutral)
		t.Applicable = false
		t.Note = "Never judged — the build failed"
		return t
	case VariationStatusPending, VariationStatusMigrating, VariationStatusActive, VariationStatusDraining:
		return stageSeq(VariationTrackVerdict, "Verdict", verdictKeys, verdictLabels, 0, ToneProgress)
	default:
		// Still being built or blocked; judgement has not begun.
		return stageSeq(VariationTrackVerdict, "Verdict", verdictKeys, verdictLabels, -1, ToneNeutral)
	}
}
