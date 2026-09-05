package domain

import (
	"fmt"
)

// Getting a deployment reachable by name is a chain of steps, and only some of
// them are Mendel's. The user creates two DNS records by hand, in a tool Mendel
// cannot see into, and then waits on a certificate authority. Without something
// tracking that, the honest answer to "where am I?" is a page of prose and a
// guess.
//
// Every step here is *observed* rather than asserted. A DNS record can be typed
// wrongly, and an acknowledgement would accept that cheerfully: the user says
// they created it, Mendel believes them, and the failure surfaces much later as
// a certificate that never issues. Resolving the name costs nothing and answers
// the question properly.

// DomainObservation is what Mendel found when it last looked, as opposed to what
// it was told.
type DomainObservation struct {
	// WildcardTarget is the address the demo wildcard resolves to, empty when it
	// does not resolve at all.
	WildcardTarget string

	// ChallengeTargets is what each ownership record resolves to, keyed by record
	// name. Absent means it does not resolve; a wrong value means it resolves
	// somewhere else, and those are different problems with different fixes.
	ChallengeTargets map[string]string

	// CertificateState is the authority's own word for it: PROVISIONING, ACTIVE,
	// FAILED, or empty when no certificate has been requested.
	CertificateState string

	// CertificateUnknown says Mendel asked and could not get an answer -- the
	// credentials would not load, gcloud would not run, the API said no.
	//
	// Distinct from an empty CertificateState, which means no certificate has
	// been requested. Collapsing the two reads as "not issued yet", which is a
	// claim about the certificate rather than about Mendel, and it is the wrong
	// one: a certificate that has been ACTIVE for a day is reported as an
	// outstanding step because one gcloud invocation failed.
	CertificateUnknown bool

	// Zone is the DNS zone the records live in -- the closest ancestor of the
	// base domain that is delegated. Observed rather than assumed, because it is
	// frequently not the base domain, and the difference decides what goes in a
	// provider's "Host" box.
	Zone string

	// Known says an observation actually happened. The zero value of this struct
	// is indistinguishable from "looked, and found nothing" -- which would tell a
	// user to create records they created an hour ago. Not knowing yet is its own
	// answer and gets its own state.
	Known bool
}

// DomainStepState is how far one step has got.
type DomainStepState string

const (
	StepDone     DomainStepState = "done"
	StepWaiting  DomainStepState = "waiting"  // Something else is working; nothing to do.
	StepYourMove DomainStepState = "yourmove" // Blocked on the user.
	StepBlocked  DomainStepState = "blocked"  // Cannot start until an earlier step finishes.
	StepChecking DomainStepState = "checking" // Mendel has not looked yet.
	StepUnknown  DomainStepState = "unknown"  // Mendel looked and could not tell.
)

// certificateComingDetail describes a certificate that has been asked for and
// has not issued. Named so the ladder's tests can assert that an undetermined
// certificate is not described this way, without pinning the wording.
const certificateComingDetail = "Issued once the record above resolves. This can take up to an hour."

// DomainStep is one rung of the ladder.
type DomainStep struct {
	Name   string
	State  DomainStepState
	Detail string

	// Advisory marks a step that is worth doing and does not have to be done.
	// Not every property is required-true: some are conditional on what is
	// actually being attempted, and some are real concerns that are a poor
	// reason to refuse to proceed. Without this the two are indistinguishable
	// from a state, and a caller ends up matching on step names to tell them
	// apart.
	Advisory bool
}

// DomainReadiness is the whole ladder, in the order it has to happen.
//
// Ordered because the order is the point: a user staring at a certificate that
// will not issue is usually two steps back, at a record that does not resolve.
//
// The ladder is now one functional area evaluated against the catalogue in
// functional_area_domain.go, rather than five steps assembled by hand. The
// output is unchanged and this file's tests are what say so.
func (d *ProjectDomain) DomainReadiness(obs DomainObservation) []DomainStep {
	if d == nil {
		return nil
	}

	a := FunctionalAreas().Assess(AreaNamedDemos, Observations{ProjectDomain: d, Domain: obs})

	steps := make([]DomainStep, 0, len(a.Steps))
	for _, s := range a.Steps {
		steps = append(steps, DomainStep{
			Name:   s.Name,
			State:  domainStepState(s),
			Detail: s.Detail,
		})
	}
	return steps
}

// DomainHeadline states where things stand in one line, and who is holding it up.
func DomainHeadline(steps []DomainStep) (headline string, waitingOnYou bool) {
	for _, s := range steps {
		switch s.State {
		case StepChecking:
			// Not "your move" -- nobody should be sent to their DNS provider on
			// the strength of an answer Mendel has not got yet.
			return "Checking where this stands", false
		case StepUnknown:
			// Also not "your move", and for the same reason: Mendel failing to
			// look is not work for the user. Phrased as what Mendel could not do
			// rather than after the step, so it is not read as a verdict on it.
			return "Could not check where this stands", false
		case StepYourMove:
			return s.Name, true
		case StepWaiting:
			return s.Name, false
		}
	}
	if len(steps) > 0 {
		return "Demos are reachable by name over https", false
	}
	return "", false
}

// observed keeps a step from claiming an answer Mendel has not gone and got.
// Only the steps whose truth comes from a lookup pass through here; a domain the
// user typed in is known without asking anyone.
func observed(obs DomainObservation, state DomainStepState) DomainStepState {
	if !obs.Known {
		return StepChecking
	}
	return state
}

func checkingOr(obs DomainObservation, detail string) string {
	if !obs.Known {
		return "Checking."
	}
	return detail
}

// challengeStepName counts, because a user who created one record and is looking
// at a step that still says "your move" needs to know a second one exists.
func challengeStepName(n int) string {
	if n == 1 {
		return "Create the certificate record"
	}
	return fmt.Sprintf("Create the %d certificate records", n)
}

func stateIf(done bool, otherwise DomainStepState) DomainStepState {
	if done {
		return StepDone
	}
	return otherwise
}

// gate keeps a step from claiming to be anyone's move before it can be started.
// A ladder that shows three things to do at once, two of which are impossible,
// is worse than one that shows the next one.
func gate(ready, done bool, active DomainStepState) DomainStepState {
	switch {
	case done:
		return StepDone
	case !ready:
		return StepBlocked
	default:
		return active
	}
}

func detailIf(done bool, whenDone, otherwise string) string {
	if done {
		return whenDone
	}
	return otherwise
}

// hostsEqual compares DNS names ignoring the trailing dot, which resolvers
// include and people typing into a provider's form do not.
func hostsEqual(a, b string) bool {
	return trimDot(a) == trimDot(b)
}

func trimDot(s string) string {
	for len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}
