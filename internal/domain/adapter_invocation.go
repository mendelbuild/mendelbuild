package domain

import (
	"time"

	"github.com/google/uuid"
)

// One run of a datastore adapter, which happens somewhere Mendel does not
// control and answers later.
//
// The asynchrony is the whole reason this is a stored thing rather than a
// function call. Mendel deploys a job into the project's own channel and the
// answer arrives over the network, so between asking and hearing there is a
// state that is neither answer — and reporting that state as a negative one is
// the mistake `Fact` and `DomainObservation.Known` both exist to prevent, made
// here at a different layer.

// AdapterInvocation is what was asked of an adapter and what came back.
type AdapterInvocation struct {
	ID        uuid.UUID `json:"id"`
	ProjectID uuid.UUID `json:"project_id"`

	// Phase is what this run was for: probe, admit, apply, withdraw, restore.
	Phase string `json:"phase"`

	// ExpiresAt bounds the token minted for this run. Short on purpose: a token
	// outliving its job is a standing credential for an endpoint that accepts
	// findings about a datastore.
	ExpiresAt time.Time `json:"expires_at"`

	// Instruction and Result are the JSON that crossed the wire, so a late or
	// malformed report is checked against the question as asked rather than
	// against a reconstruction of it, and the record stays readable after the Go
	// types have moved on.
	//
	// Stored as JSONB, which normalises key order and whitespace. That is the
	// right trade — it makes the column queryable and the comparison semantic
	// rather than textual — but it means these are the same JSON and not the
	// same bytes, and anything that ever needs the bytes (a signature over the
	// payload, say) cannot use them.
	Instruction []byte `json:"instruction"`
	Result      []byte `json:"result,omitempty"`

	// Outcome is "completed" or "failed", empty while nothing has reported.
	Outcome string `json:"outcome,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	ReportedAt *time.Time `json:"reported_at,omitempty"`
}

// Reported says whether anything came back at all.
func (a *AdapterInvocation) Reported() bool { return a != nil && a.ReportedAt != nil }

// Answered says whether what came back was a conclusion.
//
// Distinct from Reported, and the distinction is the point: an adapter that ran
// and could not reach an answer has told Mendel something, and it is not the
// answer. Both are distinct again from a run nobody has heard from.
func (a *AdapterInvocation) Answered() bool {
	return a.Reported() && a.Outcome == AdapterOutcomeCompleted
}

const (
	AdapterOutcomeCompleted = "completed"
	AdapterOutcomeFailed    = "failed"
)

// Expired reports whether this invocation's token is no longer good.
//
// A separate question from whether it reported. A job that is still running past
// its window cannot report any more, which is a deliberate bound rather than a
// failure of the job — and telling the two apart is what stops a page saying
// "still working" forever.
func (a *AdapterInvocation) Expired(now time.Time) bool {
	return a != nil && !a.Reported() && now.After(a.ExpiresAt)
}

// AdapterInvocationState is where one run stands, for a reader.
type AdapterInvocationState string

const (
	AdapterRunning   AdapterInvocationState = "running"   // Asked; nothing back yet.
	AdapterAnswered  AdapterInvocationState = "answered"  // Came back with a conclusion.
	AdapterFailed    AdapterInvocationState = "failed"    // Came back unable to conclude.
	AdapterAbandoned AdapterInvocationState = "abandoned" // Never came back inside its window.
)

// State collapses the three facts into the one thing a reader needs.
func (a *AdapterInvocation) State(now time.Time) AdapterInvocationState {
	switch {
	case a == nil:
		return AdapterAbandoned
	case a.Answered():
		return AdapterAnswered
	case a.Reported():
		return AdapterFailed
	case a.Expired(now):
		return AdapterAbandoned
	default:
		return AdapterRunning
	}
}
