package domain

// Lifecycle vocabulary shared by Hops, Variations, and Decisions.
//
// The rule this file exists to enforce: templates must never switch on a raw
// status string. Status enums are storage details that conflate several
// independent concerns (a Variation's status mixes build progress, runtime
// state, and adjudication outcome). Presentation needs a stable, plain-English
// model instead, so every entity maps its status(es) into a Ribbon here and
// templates render only the Ribbon.
//
// See hop_lifecycle.go, variation_lifecycle.go, and decision_lifecycle.go for
// the per-entity mappings.

// Tone classifies a stage or ribbon for visual treatment. Templates map Tone to
// color; they must not derive color from status directly.
type Tone string

const (
	ToneNeutral  Tone = "neutral"  // Nothing has happened yet, or not applicable
	ToneProgress Tone = "progress" // Work is underway; nothing is needed from the user
	ToneWaiting  Tone = "waiting"  // Blocked on the user
	ToneSuccess  Tone = "success"
	ToneWarning  Tone = "warning"
	ToneFailure  Tone = "failure"
)

// StageState is a stage's position relative to where the entity actually is.
type StageState string

const (
	StageUpcoming StageState = "upcoming" // Not reached yet
	StageCurrent  StageState = "current"  // Where the entity is right now
	StageDone     StageState = "done"     // Completed successfully
	StageSkipped  StageState = "skipped"  // Not applicable to this entity
	StageFailed   StageState = "failed"   // Reached and failed
)

// Actor identifies who the system is waiting on. This is the single most
// useful fact for a newcomer: "is this my move?"
type Actor string

const (
	ActorMendel Actor = "mendel" // Mendel or an agent is working; just wait
	ActorYou    Actor = "you"    // A human needs to act
	ActorNobody Actor = "nobody" // Terminal; nothing further will happen
)

// Stage is one step within a Track.
type Stage struct {
	Key   string // Stable identifier, for CSS hooks and tests
	Label string // Plain English, written for someone who has not read the docs
	State StageState
	Tone  Tone
	Note  string // Optional detail, e.g. "Revision 2 of 3"
}

// Track is a named sequence of Stages. An entity may progress along several
// tracks at once — a Variation can be built, live, and not yet judged
// simultaneously — which is why a single linear stepper would misrepresent it.
type Track struct {
	Key        string
	Label      string
	Stages     []Stage
	Applicable bool   // When false, render dimmed; the track does not apply here
	Note       string // Optional explanation, especially when !Applicable
}

// Current returns the stage the track is presently at, or nil if the track has
// not started or is already finished.
func (t Track) Current() *Stage {
	for i := range t.Stages {
		if t.Stages[i].State == StageCurrent || t.Stages[i].State == StageFailed {
			return &t.Stages[i]
		}
	}
	return nil
}

// Ribbon is the complete renderable lifecycle of a single entity.
type Ribbon struct {
	Subject    string // "Hop", "Variation", "Decision"
	Headline   string // Plain-English statement of where things stand
	Tone       Tone
	Tracks     []Track
	WaitingOn  Actor
	NextAction string // What happens next, or what is needed from the user
}

// WaitingOnYou reports whether a human needs to act. Templates use this to
// decide whether to surface a call to action.
func (r Ribbon) WaitingOnYou() bool { return r.WaitingOn == ActorYou }

// Terminal reports whether the entity has reached an end state.
func (r Ribbon) Terminal() bool { return r.WaitingOn == ActorNobody }

// StatusLabel is the badge text: whose move it is, or — once nothing further
// will happen — how it ended.
//
// Terminal is not a synonym for success. A Variation whose build failed and one
// that was merged to main are both finished, and labelling them alike ("Complete")
// is the same class of bug as painting a failure green. Three outcomes are
// distinguished: it worked, it failed, or it was closed without either.
func (r Ribbon) StatusLabel() string {
	switch {
	case r.WaitingOn == ActorYou:
		return "Your move"
	case r.WaitingOn == ActorMendel:
		return "Mendel is working"
	case r.Tone == ToneFailure:
		return "Failed"
	case r.Tone == ToneSuccess:
		return "Complete"
	default:
		// Neutral terminal: eliminated, not selected, cancelled. Over, but
		// neither a success nor a failure.
		return "Closed"
	}
}

// StatusClass is the CSS modifier matching StatusLabel. Templates must style the
// badge from this rather than from WaitingOn, which cannot tell a terminal
// failure from a terminal success.
func (r Ribbon) StatusClass() string {
	switch r.StatusLabel() {
	case "Your move":
		return "you"
	case "Mendel is working":
		return "mendel"
	case "Failed":
		return "failed"
	case "Complete":
		return "done"
	default:
		return "closed"
	}
}

// Track returns the track with the given key, or nil.
func (r Ribbon) Track(key string) *Track {
	for i := range r.Tracks {
		if r.Tracks[i].Key == key {
			return &r.Tracks[i]
		}
	}
	return nil
}

// stageSeq builds a linear track by marking every stage before current as done,
// the stage at current as current, and the rest as upcoming. Pass current == -1
// for a track that has not started, or len(labels) for one that has finished.
func stageSeq(key, label string, keys, labels []string, current int, tone Tone) Track {
	stages := make([]Stage, len(keys))
	for i := range keys {
		st := Stage{Key: keys[i], Label: labels[i]}
		switch {
		case i < current:
			st.State, st.Tone = StageDone, ToneSuccess
		case i == current:
			st.State, st.Tone = StageCurrent, tone
		default:
			st.State, st.Tone = StageUpcoming, ToneNeutral
		}
		stages[i] = st
	}
	return Track{Key: key, Label: label, Stages: stages, Applicable: true}
}
