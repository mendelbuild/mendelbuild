package web

import (
	"encoding/json"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/experiment"
)

// Reading a probe back into what the readiness ladder asks about.
//
// The whole value of the three states an invocation can be in is spent here. A
// probe that never ran, one still running, one that could not conclude and one
// that concluded "no" are four different things, and three of them are not "no".
// Collapsing them is how a page tells someone their datastore is unsuitable
// because a job was slow.

// datastoreFacts is what a probe established about a project's datastore.
type datastoreFacts struct {
	// Supported is whether an adapter answered at all and reported something
	// Mendel can work with.
	Supported domain.Fact

	// CanProvision is whether a verification sandbox can be created, which is
	// what a migration needs and a presentation-only experiment does not.
	CanProvision domain.Fact

	// Kind and Version identify the engine, for a sentence a person reads and
	// for re-gating a cached adapter when the datastore changes underneath it.
	Kind    string
	Version string

	// Why is what to say when something is not true, written for a reader.
	Why string
}

// probeFacts reads a project's latest probe.
//
// The nil invocation is the case worth naming: a project nobody has probed has
// not been found unsuitable, and returning FactFalse here would put a decline on
// a page about a question nobody has asked yet.
func probeFacts(inv *domain.AdapterInvocation, now time.Time) datastoreFacts {
	unknown := datastoreFacts{Supported: domain.FactUnknown, CanProvision: domain.FactUnknown}

	switch inv.State(now) {
	case domain.AdapterAbandoned:
		if inv == nil {
			unknown.Why = "Mendel has not looked at this project's datastore yet."
		} else {
			unknown.Why = "The last look at this project's datastore never reported back. " +
				"That is a fact about the attempt rather than about the datastore, and the " +
				"next one will try again."
		}
		return unknown

	case domain.AdapterRunning:
		unknown.Why = "Mendel is looking at this project's datastore now."
		return unknown

	case domain.AdapterFailed:
		unknown.Why = "Mendel could not find out what this project's datastore is. " +
			failureOf(inv.Result)
		return unknown
	}

	var result experiment.Result
	if err := json.Unmarshal(inv.Result, &result); err != nil || result.Probe == nil {
		// A report that validated on arrival and will not read back now is
		// Mendel's problem, and saying so beats reporting it as the datastore's.
		unknown.Why = "Mendel recorded an answer about this datastore that it can no longer read."
		return unknown
	}

	facts := datastoreFacts{
		Supported: domain.FactTrue,
		Kind:      result.Probe.Kind,
		Version:   result.Probe.Version,
	}
	if result.Probe.CanProvision {
		facts.CanProvision = domain.FactTrue
	} else {
		facts.CanProvision = domain.FactFalse
		facts.Why = result.Probe.CannotProvisionBecause
	}
	return facts
}

// failureOf pulls the sentence a failed run wrote for a person.
func failureOf(raw []byte) string {
	var result experiment.Result
	if err := json.Unmarshal(raw, &result); err != nil || result.Failure == "" {
		return ""
	}
	return result.Failure
}
