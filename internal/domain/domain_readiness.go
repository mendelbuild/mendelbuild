package domain

import (
	"fmt"
	"strings"
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
func (d *ProjectDomain) DomainReadiness(obs DomainObservation) []DomainStep {
	if d == nil {
		return nil
	}

	steps := make([]DomainStep, 0, 5)

	// 1. The domain itself.
	domainSet := d.BaseDomain != ""
	steps = append(steps, DomainStep{
		Name:   "Give Mendel a domain you control",
		State:  stateIf(domainSet, StepYourMove),
		Detail: detailIf(domainSet, d.BaseDomain, "Mendel puts names under this and never touches your DNS."),
	})

	// 2. The address its records point at. Mendel's own job.
	haveIP := d.StaticIP != ""
	steps = append(steps, DomainStep{
		Name:   "Mendel reserves an address",
		State:  gate(domainSet, haveIP, StepWaiting),
		Detail: detailIf(haveIP, d.StaticIP, "Reserved on the next deploy or validation of this channel."),
	})

	// 3. The wildcard record. Verified, not taken on trust.
	wildcardRight := haveIP && obs.WildcardTarget == d.StaticIP
	wildcardDetail := "Create the A record listed below."
	switch {
	case wildcardRight:
		wildcardDetail = d.DemoWildcard() + " resolves to " + d.StaticIP
	case obs.WildcardTarget != "":
		wildcardDetail = fmt.Sprintf("%s resolves to %s, but the deployments are at %s. "+
			"The record points somewhere else.", d.DemoWildcard(), obs.WildcardTarget, d.StaticIP)
	}
	steps = append(steps, DomainStep{
		Name:   "Create the wildcard A record",
		State:  observed(obs, gate(haveIP, wildcardRight, StepYourMove)),
		Detail: checkingOr(obs, wildcardDetail),
	})

	// 4. The ownership records for the certificate, one per zone it covers.
	//
	// Reported together rather than as a step each: they are created in the same
	// sitting, in the same tool, and a ladder that grows a rung per zone tells
	// the reader the shape of the task changed when it did not.
	challengeAsked := len(d.Challenges) > 0
	var outstanding, wrong []string
	for _, c := range d.Challenges {
		target, found := obs.ChallengeTargets[c.RecordName]
		switch {
		case !found:
			outstanding = append(outstanding, c.RecordName)
		case !hostsEqual(target, c.RecordValue):
			wrong = append(wrong, fmt.Sprintf("%s resolves to %s", c.RecordName, target))
		}
	}
	challengeRight := challengeAsked && len(outstanding) == 0 && len(wrong) == 0

	challengeDetail := ""
	switch {
	case !challengeAsked:
		challengeDetail = "Mendel requests the certificate first; the records appear once it has."
	case challengeRight:
		challengeDetail = fmt.Sprintf("All %d records resolve correctly.", len(d.Challenges))
	case len(wrong) > 0:
		challengeDetail = strings.Join(wrong, "; ") + " rather than the value below."
	default:
		challengeDetail = fmt.Sprintf("%d of %d created. Still to create: %s.",
			len(d.Challenges)-len(outstanding), len(d.Challenges), strings.Join(outstanding, ", "))
	}

	steps = append(steps, DomainStep{
		Name:   challengeStepName(len(d.Challenges)),
		State:  observed(obs, gate(challengeAsked, challengeRight, StepYourMove)),
		Detail: checkingOr(obs, challengeDetail),
	})

	// 5. The authority's part, which nobody can hurry.
	//
	// Three outcomes, not two. Mendel either has the authority's answer, has not
	// asked yet, or asked and could not find out -- and the third is not a fact
	// about the certificate. Reporting it as one turns a transient gcloud failure
	// into a step the user is told is outstanding, which is how a certificate
	// that has been ACTIVE for a day comes and goes from the page.
	certDetail := certificateComingDetail
	certDone := obs.CertificateState == "ACTIVE"
	certState := observed(obs, gate(challengeRight, certDone, StepWaiting))
	switch {
	case obs.CertificateUnknown:
		certState = StepUnknown
		certDetail = "Mendel could not reach the certificate authority to check. " +
			"This says nothing about the certificate; the next check will try again."
	case certDone:
		certDetail = "Deployments answer over https, and their URLs can be registered."
	case obs.CertificateState != "":
		certDetail = "Certificate state: " + obs.CertificateState
	}
	steps = append(steps, DomainStep{
		Name:   "Certificate issued",
		State:  certState,
		Detail: checkingOr(obs, certDetail),
	})

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
