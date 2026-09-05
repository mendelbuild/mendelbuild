package domain

import (
	"fmt"
	"strings"
)

// The conditions behind "serve deployments by name over https", and the
// catalogue they sit in.
//
// This is the first functional area expressed as one, and it was chosen because
// DomainReadiness already had every part the general shape needs -- ordering,
// gating, observation, fan-out by count, and a state for "not looked yet". The
// requirement on this file is that it reproduce that ladder exactly:
// domain_readiness_test.go is the acceptance criterion and is unchanged.

// Condition and area identifiers.
const (
	AreaNamedDemos AreaID = "named-demos"

	CondBaseDomain       ConditionID = "domain.base-domain-set"
	CondStaticIP         ConditionID = "domain.static-ip-reserved"
	CondWildcardRecord   ConditionID = "domain.wildcard-record-resolves"
	CondChallengeRecords ConditionID = "domain.challenge-records-resolve"
	CondCertificate      ConditionID = "domain.certificate-issued"
)

// domainConditions are the five rungs, in the order they have to happen.
//
// Four of the five are gated on the condition before them, which is what
// DependsOn is for. The fifth is not: the certificate records cannot be created
// until Mendel has asked the authority for a certificate, and "Mendel has asked"
// is a fact about the request rather than a condition anybody needs listed. That
// one uses Ready, and it is the only use of Ready in the catalogue -- worth
// noticing, because the general shape was designed on the assumption that
// ordering is always condition-to-condition and this is the first evidence
// about how often that holds.
func domainConditions() []Condition {
	return []Condition{{
		ID:       CondBaseDomain,
		Name:     "Give Mendel a domain you control",
		Evidence: EvidenceAsked,
		Remedy:   RemedyUser,
		Evaluate: func(o Observations) Finding {
			if o.ProjectDomain.BaseDomain != "" {
				return Finding{State: CondSatisfied, Detail: o.ProjectDomain.BaseDomain}
			}
			return Finding{
				State:  CondUnsatisfied,
				Detail: "Mendel puts names under this and never touches your DNS.",
				Missing: "No base domain. Mendel invents names for deployments underneath one " +
					"you control, and cannot do that until it has been told which.",
			}
		},
	}, {
		ID:        CondStaticIP,
		Name:      "Mendel reserves an address",
		Evidence:  EvidenceDerived,
		Remedy:    RemedyMendel,
		DependsOn: []ConditionID{CondBaseDomain},
		Evaluate: func(o Observations) Finding {
			if o.ProjectDomain.StaticIP != "" {
				return Finding{State: CondSatisfied, Detail: o.ProjectDomain.StaticIP}
			}
			return Finding{
				State:   CondUnsatisfied,
				Detail:  "Reserved on the next deploy or validation of this channel.",
				Missing: "No address reserved yet, so there is nothing for a DNS record to point at.",
			}
		},
	}, {
		ID:        CondWildcardRecord,
		Name:      "Create the wildcard A record",
		Evidence:  EvidenceObserved,
		Remedy:    RemedyUser,
		DependsOn: []ConditionID{CondStaticIP},
		Evaluate:  evaluateWildcard,
	}, {
		ID:       CondChallengeRecords,
		Name:     "Create the certificate record",
		NameFor:  func(o Observations) string { return challengeStepName(len(o.ProjectDomain.Challenges)) },
		Evidence: EvidenceObserved,
		Remedy:   RemedyUser,
		Ready:    func(o Observations) bool { return len(o.ProjectDomain.Challenges) > 0 },
		Evaluate: evaluateChallenges,
	}, {
		ID:        CondCertificate,
		Name:      "Certificate issued",
		Evidence:  EvidenceObserved,
		Remedy:    RemedyElsewhere,
		DependsOn: []ConditionID{CondChallengeRecords},
		Evaluate:  evaluateCertificate,
	}}
}

// domainCatalogue is the catalogue as it stands: one area, five conditions.
//
// Built once, because a malformed catalogue is a bug in this package and the
// right time to find out is the first time anything touches it.
var domainCatalogue = NewCatalogue(domainConditions(), []FunctionalArea{{
	ID:       AreaNamedDemos,
	Name:     "Serve deployments by name over https",
	Requires: []ConditionID{CondBaseDomain, CondStaticIP, CondWildcardRecord, CondChallengeRecords, CondCertificate},
}})

// Catalogue returns the functional areas Mendel knows about.
func FunctionalAreas() *Catalogue { return domainCatalogue }

// evaluateWildcard checks the record against the address rather than trusting
// that it was created. A record can be typed wrongly, and an acknowledgement
// would accept that cheerfully.
func evaluateWildcard(o Observations) Finding {
	d, obs := o.ProjectDomain, o.Domain
	if !obs.Known {
		return Finding{State: CondUnchecked, Detail: "Checking."}
	}
	if d.StaticIP != "" && obs.WildcardTarget == d.StaticIP {
		return Finding{State: CondSatisfied, Detail: d.DemoWildcard() + " resolves to " + d.StaticIP}
	}
	if obs.WildcardTarget != "" {
		detail := fmt.Sprintf("%s resolves to %s, but the deployments are at %s. "+
			"The record points somewhere else.", d.DemoWildcard(), obs.WildcardTarget, d.StaticIP)
		return Finding{State: CondUnsatisfied, Detail: detail, Missing: detail}
	}
	return Finding{
		State:  CondUnsatisfied,
		Detail: "Create the A record listed below.",
		Missing: d.DemoWildcard() + " does not resolve, so no deployment can be reached by name. " +
			"Create the A record pointing it at " + d.StaticIP + ".",
	}
}

// evaluateChallenges reports the ownership records together rather than as a
// step each: they are created in the same sitting, in the same tool, and a
// ladder that grows a rung per zone tells the reader the shape of the task
// changed when it did not.
func evaluateChallenges(o Observations) Finding {
	d, obs := o.ProjectDomain, o.Domain
	if !obs.Known {
		return Finding{State: CondUnchecked, Detail: "Checking."}
	}

	if len(d.Challenges) == 0 {
		return Finding{
			State:   CondUnsatisfied,
			Detail:  "Mendel requests the certificate first; the records appear once it has.",
			Missing: "Mendel has not requested a certificate yet, so the records to create are not known.",
		}
	}

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

	switch {
	case len(wrong) > 0:
		detail := strings.Join(wrong, "; ") + " rather than the value below."
		return Finding{
			State: CondUnsatisfied, Detail: detail, Missing: detail,
			Outstanding: len(outstanding) + len(wrong), Total: len(d.Challenges),
		}
	case len(outstanding) > 0:
		detail := fmt.Sprintf("%d of %d created. Still to create: %s.",
			len(d.Challenges)-len(outstanding), len(d.Challenges), strings.Join(outstanding, ", "))
		return Finding{
			State: CondUnsatisfied, Detail: detail, Missing: detail,
			Outstanding: len(outstanding), Total: len(d.Challenges),
		}
	}
	return Finding{
		State:  CondSatisfied,
		Detail: fmt.Sprintf("All %d records resolve correctly.", len(d.Challenges)),
		Total:  len(d.Challenges),
	}
}

// evaluateCertificate reports the authority's part, which nobody can hurry.
//
// Three outcomes, not two. Mendel either has the authority's answer, has not
// asked yet, or asked and could not find out -- and the third is not a fact
// about the certificate. Reporting it as one turns a transient gcloud failure
// into a step the user is told is outstanding, which is how a certificate that
// has been ACTIVE for a day comes and goes from the page.
func evaluateCertificate(o Observations) Finding {
	obs := o.Domain

	if obs.CertificateUnknown {
		detail := "Mendel could not reach the certificate authority to check. " +
			"This says nothing about the certificate; the next check will try again."
		if !obs.Known {
			detail = "Checking."
		}
		return Finding{State: CondUndetermined, Detail: detail}
	}
	if !obs.Known {
		return Finding{State: CondUnchecked, Detail: "Checking."}
	}
	if obs.CertificateState == "ACTIVE" {
		return Finding{
			State:  CondSatisfied,
			Detail: "Deployments answer over https, and their URLs can be registered.",
		}
	}
	detail := certificateComingDetail
	if obs.CertificateState != "" {
		detail = "Certificate state: " + obs.CertificateState
	}
	return Finding{
		State:   CondUnsatisfied,
		Detail:  detail,
		Missing: "No certificate yet, so deployments cannot answer over https. " + detail,
	}
}

// domainStepState renders a condition's state as the ladder's own vocabulary.
//
// The two states meaning "Mendel does not know" stay distinct here as they do
// everywhere else: unchecked is Mendel not having looked, undetermined is Mendel
// having looked and been unable to tell, and collapsing them would report a
// failed lookup as an outstanding task.
func domainStepState(s Step) DomainStepState {
	switch s.State {
	case CondSatisfied:
		return StepDone
	case CondBlocked:
		return StepBlocked
	case CondUnchecked:
		return StepChecking
	case CondUndetermined:
		return StepUnknown
	default:
		if s.Remedy == RemedyUser || s.Remedy == RemedyEither {
			return StepYourMove
		}
		return StepWaiting
	}
}
