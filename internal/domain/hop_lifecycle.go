package domain

import "fmt"

// Hop lifecycle: a single track, because a Hop really is sequential.
//
//	Planned -> Exploring -> Comparing -> Decided
//
// HopStatus has six values but only four meaningful positions; `completed`,
// `rejected`, and `abandoned` are three different outcomes of the same final
// position, so they share a stage and differ in Tone and Label.

const (
	HopTrackProgress = "progress"

	HopStagePlanned   = "planned"
	HopStageExploring = "exploring"
	HopStageComparing = "comparing"
	HopStageDecided   = "decided"
)

var (
	hopStageKeys   = []string{HopStagePlanned, HopStageExploring, HopStageComparing, HopStageDecided}
	hopStageLabels = []string{"Planned", "Exploring approaches", "Comparing results", "Decided"}
)

// variationCounts summarizes a Hop's Variations for headline purposes.
type variationCounts struct {
	total      int
	building   int // Code generation or revision underway
	blocked    int // Waiting on an InputRequest
	ready      int // Built, awaiting judgement
	live       int // Deployed and receiving traffic
	failed     int // Errored or terminated
	eliminated int // Pruned or rejected
	won        int // Selected or merged
}

func countVariations(vars []Variation) variationCounts {
	var c variationCounts
	c.total = len(vars)
	for _, v := range vars {
		switch v.Status {
		case VariationStatusCreating:
			c.building++
		case VariationStatusBlocked:
			c.blocked++
		case VariationStatusPending:
			c.ready++
		case VariationStatusMigrating, VariationStatusActive, VariationStatusDraining:
			c.live++
		case VariationStatusError, VariationStatusTerminated:
			c.failed++
		case VariationStatusPruned, VariationStatusRejected:
			c.eliminated++
		case VariationStatusSelected, VariationStatusMerged:
			c.won++
		}
	}
	return c
}

// stuck reports whether every Variation is out of the running, which leaves the
// Hop unable to progress without a human asking for more.
func (c variationCounts) stuck() bool {
	return c.total > 0 && c.failed+c.eliminated == c.total
}

// HopLifecycle maps a Hop (and, when available, its Variations) onto a Ribbon.
//
// vars may be nil. When it is, the Ribbon is still correct but the headline is
// coarser, since several distinct situations inside `active` — proposing,
// building, and stuck — are distinguishable only from the Variations.
func HopLifecycle(h *Hop, vars []Variation) Ribbon {
	c := countVariations(vars)

	r := Ribbon{Subject: "Hop"}

	switch h.Status {
	case HopStatusPending:
		r.Tracks = []Track{stageSeq(HopTrackProgress, "Hop progress", hopStageKeys, hopStageLabels, 0, ToneProgress)}
		r.Tone, r.WaitingOn = ToneNeutral, ActorMendel
		r.Headline = "Waiting on earlier Hops"
		r.NextAction = "This Hop starts on its own once the Hops it depends on have been decided."

	case HopStatusActive:
		r.Tracks = []Track{stageSeq(HopTrackProgress, "Hop progress", hopStageKeys, hopStageLabels, 1, ToneProgress)}
		r.Tone, r.WaitingOn = ToneProgress, ActorMendel
		switch {
		case vars == nil:
			r.Headline = "Exploring approaches"
			r.NextAction = "Mendel is working through candidate approaches for this Hop."
		case c.stuck():
			r.Tone, r.WaitingOn = ToneWarning, ActorYou
			r.Headline = "Out of candidates"
			r.NextAction = "Every variation has been eliminated or has failed. Request new variations to continue."
		case c.total == 0:
			r.Headline = "Proposing approaches"
			r.NextAction = "Mendel is drafting candidate approaches. You will be asked to review them before any code is written."
		case c.blocked > 0:
			r.Tone, r.WaitingOn = ToneWaiting, ActorYou
			r.Headline = plural(c.blocked, "variation is", "variations are") + " blocked"
			r.NextAction = "One or more variations need something from you — check the open decisions for this Hop."
		case c.building > 0:
			r.Headline = "Building " + plural(c.building, "variation", "variations")
			r.NextAction = "Mendel is writing code for the approved approaches. Nothing is needed from you yet."
		case c.ready > 0 || c.live > 0:
			r.Headline = plural(c.ready+c.live, "variation is", "variations are") + " ready"
			r.NextAction = "Mendel is preparing the comparison. You will be asked to pick a winner shortly."
		default:
			r.Headline = "Exploring approaches"
			r.NextAction = "Mendel is working through candidate approaches for this Hop."
		}

	case HopStatusSelecting:
		r.Tracks = []Track{stageSeq(HopTrackProgress, "Hop progress", hopStageKeys, hopStageLabels, 2, ToneWaiting)}
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "Ready for your decision"
		r.NextAction = "Compare the variations and pick a winner, or ask for more."

	case HopStatusCompleted:
		r.Tracks = []Track{hopTerminalTrack("Decided: winner merged", ToneSuccess)}
		r.Tone, r.WaitingOn = ToneSuccess, ActorNobody
		r.Headline = "Done — a winner was merged to main"
		r.NextAction = "Nothing further. Hops that depend on this one can now start."

	case HopStatusRejected:
		r.Tracks = []Track{hopTerminalTrack("Decided: all rejected", ToneFailure)}
		r.Tone, r.WaitingOn = ToneFailure, ActorNobody
		r.Headline = "Closed — every variation was rejected"
		r.NextAction = "Nothing further. Nothing from this Hop was merged."

	case HopStatusAbandoned:
		r.Tracks = []Track{hopTerminalTrack("Abandoned", ToneNeutral)}
		r.Tone, r.WaitingOn = ToneNeutral, ActorNobody
		r.Headline = "Cancelled before a winner was chosen"
		r.NextAction = "Nothing further. This Hop was cancelled."

	default:
		// Unknown status: degrade to something honest rather than rendering a
		// bare enum, which is the failure mode this package exists to prevent.
		r.Tracks = []Track{stageSeq(HopTrackProgress, "Hop progress", hopStageKeys, hopStageLabels, -1, ToneNeutral)}
		r.Tone, r.WaitingOn = ToneNeutral, ActorMendel
		r.Headline = fmt.Sprintf("Unrecognized state (%s)", h.Status)
		r.NextAction = "Mendel does not recognize this Hop's state. This is a bug worth reporting."
	}

	return r
}

// hopTerminalTrack builds a fully-completed track whose final stage carries the
// outcome, so `completed`, `rejected`, and `abandoned` read differently despite
// occupying the same position.
func hopTerminalTrack(finalLabel string, tone Tone) Track {
	t := stageSeq(HopTrackProgress, "Hop progress", hopStageKeys, hopStageLabels, 3, tone)
	last := &t.Stages[len(t.Stages)-1]
	last.Label = finalLabel
	last.State = StageDone
	if tone == ToneFailure {
		last.State = StageFailed
	}
	last.Tone = tone
	return t
}

// plural renders "1 variation" / "3 variations" given singular and plural forms.
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, pluralForm)
}
