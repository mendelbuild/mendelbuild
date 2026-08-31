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
	verdictKeys   = []string{"awaiting", "decided"}
	verdictLabels = []string{"Awaiting decision", "Decided"}
)

// trialShape describes what a trial actually consists of for a given Hop.
//
// A trial is not one fixed thing. A Hop that only wants a clickable demo never
// migrates data and never takes real traffic, so labelling its stages
// "Applying migrations" and "Serving live traffic" would promise work that will
// never happen — the sort of invented detail that makes the system harder to
// understand, not easier.
type trialShape struct {
	note       string // Distinguishes the two kinds in the track header
	keys       []string
	labels     []string
	production bool
}

func shapeOfTrial(h *Hop) trialShape {
	if h != nil && h.RequiresProduction {
		return trialShape{
			note:       "live traffic",
			keys:       []string{"deployed", "live", "drained"},
			labels:     []string{"Deployed", "Serving live traffic", "Traffic drained"},
			production: true,
		}
	}
	return trialShape{
		note:   "demo deployment",
		keys:   []string{"deployed", "running", "torndown"},
		labels: []string{"Deployed", "Demo available", "Torn down"},
	}
}

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
	trial := shapeOfTrial(h)
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

	// Running out of money is not the same as being blocked on a credential,
	// and the status column cannot tell them apart: handleVariationBudgetPause
	// leaves a paused run in `blocked` (or `error`, depending on where it
	// stopped). Checked before both, because both would otherwise claim it.
	case v.PausedForBudget():
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "Paused at its spend ceiling"
		r.NextAction = "Nothing went wrong — it ran out of budget before finishing, and the code " +
			"it wrote is intact. Continue it if the remaining work is worth more money."

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
		r.NextAction = "Mendel is preparing this variation's environment before the trial starts."

	case st == VariationStatusActive:
		r.Tone, r.WaitingOn = ToneProgress, ActorMendel
		if trial.production {
			r.Headline = "Serving live traffic"
			r.NextAction = "This variation is handling real traffic. Results are being gathered for the comparison."
		} else {
			r.Headline = "Running in the demo environment"
			r.NextAction = "This variation is deployed and clickable. Try it, or wait for the comparison."
		}

	case st == VariationStatusDraining:
		r.Tone, r.WaitingOn = ToneProgress, ActorMendel
		if trial.production {
			r.Headline = "Draining traffic"
			r.NextAction = "Traffic is being shifted away from this variation before it is cleaned up."
		} else {
			r.Headline = "Shutting down the demo"
			r.NextAction = "The demo deployment is being torn down."
		}

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
	shape := shapeOfTrial(h)
	reached := st == VariationStatusMigrating || st == VariationStatusActive || st == VariationStatusDraining
	wanted := h != nil && (h.RequiresDemo || h.RequiresProduction)

	seq := func(current int, tone Tone) Track {
		t := stageSeq(VariationTrackTrial, "Trial", shape.keys, shape.labels, current, tone)
		t.Note = shape.note
		return t
	}

	if !reached && !wanted {
		t := seq(-1, ToneNeutral)
		t.Applicable = false
		t.Note = "This Hop does not require a deployed trial"
		return t
	}

	switch st {
	case VariationStatusMigrating:
		// Migrations are a detail of getting deployed, and only some trials have
		// them, so the word appears only when one is actually running.
		t := seq(0, ToneProgress)
		t.Stages[0].Label = "Applying data migrations"
		return t
	case VariationStatusActive:
		return seq(1, ToneProgress)
	case VariationStatusDraining:
		t := seq(2, ToneProgress)
		if shape.production {
			t.Stages[2].Label = "Draining traffic"
		} else {
			t.Stages[2].Label = "Shutting down"
		}
		return t
	default:
		return seq(-1, ToneNeutral)
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

// VariationOffers is what a person may do to a Variation right now.
//
// This lives here, next to the lifecycle, for the reason the ribbon does: the
// answer is derived from status, revision history, and whether the run stopped
// on cost, and working that out inside a template meant the Variation page
// carried ten separate `eq .Status "..."` branches deciding which buttons to
// draw. Two of them disagreed about `blocked`.
//
// It says nothing about where the buttons go or what they are called; that is
// the template's business. It says only what is legitimate.
type VariationOffers struct {
	// RequestChange sends feedback back for revision. Available whenever there
	// is code to revise.
	RequestChange bool
	// Continue resumes a run that stopped at its spend ceiling. The work is
	// intact, so this is not a retry: it is the same run, funded further.
	Continue bool
	// Regenerate discards the work and builds the approach again from scratch.
	// Never offered alongside Continue — throwing away intact code because it
	// ran out of money is not a thing to offer next to resuming it.
	Regenerate bool
	// RetryWithFix rebuilds with the diagnosed failure fed back in.
	RetryWithFix bool
	// Rebase moves the variation onto the current main.
	Rebase bool
	// Terminate stops a generation that is stuck.
	Terminate bool
}

// Any reports whether there is anything at all to offer, so a template can omit
// the action region rather than render an empty one.
func (o VariationOffers) Any() bool {
	return o.RequestChange || o.Continue || o.Regenerate || o.RetryWithFix || o.Rebase || o.Terminate
}

// VariationActions decides what may be done to a Variation.
//
// canRetryFix reports whether a failure was diagnosed well enough to retry
// against it; that judgement needs the build logs and so is made by the caller.
func VariationActions(v *Variation, canRetryFix bool) VariationOffers {
	if v == nil {
		return VariationOffers{}
	}

	switch v.Status {
	case VariationStatusCreating:
		// Mid-build. Nothing to revise yet, and the only useful intervention is
		// to stop it.
		return VariationOffers{Terminate: true}

	case VariationStatusPending:
		return VariationOffers{RequestChange: true, Regenerate: true, Rebase: true}

	case VariationStatusError, VariationStatusTerminated, VariationStatusBlocked:
		if v.PausedForBudget() {
			return VariationOffers{RequestChange: true, Continue: true, Rebase: true}
		}
		return VariationOffers{
			RequestChange: true,
			Regenerate:    true,
			RetryWithFix:  canRetryFix,
			Rebase:        true,
		}

	default:
		// Live, drained, or adjudicated. These are outcomes, not workbenches:
		// offering "retry from scratch" on a merged winner would be an invitation
		// to undo a decision that has already been acted on.
		return VariationOffers{}
	}
}
