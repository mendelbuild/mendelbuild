package web

import (
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
)

// Deciding whether to run an adapter, apart from running one.
//
// A pure function over what is already known, for a reason beyond testability:
// starting a probe costs a Cloud Build, a Job and a pod in someone's cluster, so
// the decision to spend that is worth being able to read on its own rather than
// finding it distributed through the code that does the spending.

// ProbeStaleAfter is how long a probe's answer stands before it is asked again.
//
// Long, because the question is what engine this is and whether Mendel may make
// a database on it -- facts that change when someone changes them, not on a
// timer. The domain ladder refreshes DNS every ten seconds because a user is
// sitting there having just created a record; nobody is sitting there waiting
// for their datastore to become a different datastore.
//
// The cost of being wrong is also asymmetric. Too short spends a build and a pod
// for an answer that has not changed; too long shows a stale answer, and the one
// way it goes stale that matters -- a grant being added so provisioning starts
// working -- is something a person just did and can ask to re-check.
const ProbeStaleAfter = 12 * time.Hour

// ProbeDecision is whether to start a probe, and why not when not.
//
// The reason is not decoration. This decides to spend a build and a pod in
// someone else's cluster, and "nothing happened when I asked" is the hardest
// thing to diagnose from the outside -- so the answer says which of the several
// good reasons for doing nothing applied.
type ProbeDecision struct {
	Start bool

	// Because is what to say when Start is false, phrased for whoever is
	// looking at a page wondering why it has not refreshed.
	Because string
}

// ShouldProbe decides whether a project's datastore is worth asking about.
//
// Takes the latest invocation rather than fetching it, so the decision is a
// function of its inputs and the thing that costs a database round trip belongs
// to the caller.
func ShouldProbe(latest *domain.AdapterInvocation, now time.Time, forced bool) ProbeDecision {
	// A running probe is the one case where starting another is actively
	// harmful rather than merely wasteful: two jobs against one datastore, two
	// reports, and the second refused as a duplicate for an invocation it does
	// not belong to. Forcing does not override it -- what a person asking for a
	// refresh wants is a fresh answer, and one is already on its way.
	if latest.State(now) == domain.AdapterRunning {
		return ProbeDecision{Because: "a probe is already running; its answer is what will refresh this"}
	}

	if forced {
		return ProbeDecision{Start: true}
	}

	switch latest.State(now) {
	case domain.AdapterAbandoned:
		// Covers both never-asked and asked-and-never-heard-from. Both mean
		// Mendel does not know, and not knowing is the thing a probe fixes.
		return ProbeDecision{Start: true}

	case domain.AdapterFailed:
		// A failed run says something went wrong, not that the datastore is
		// unsuitable, so it is worth asking again -- but on the same schedule
		// as any other answer rather than immediately. Retrying a failure at
		// once turns a datastore that is down into a build every few seconds.
		if now.Sub(*latest.ReportedAt) < ProbeStaleAfter {
			return ProbeDecision{Because: "the last attempt failed recently; Mendel will try again " +
				"rather than retry immediately, which would turn an unreachable datastore into a " +
				"job every few seconds"}
		}
		return ProbeDecision{Start: true}

	default: // answered
		if now.Sub(*latest.ReportedAt) < ProbeStaleAfter {
			return ProbeDecision{Because: "this project's datastore was looked at recently"}
		}
		return ProbeDecision{Start: true}
	}
}
