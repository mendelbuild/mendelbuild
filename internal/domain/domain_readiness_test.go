package domain

import (
	"strings"
	"testing"
)

func ladder(d *ProjectDomain, obs DomainObservation) map[string]DomainStep {
	out := map[string]DomainStep{}
	for _, s := range d.DomainReadiness(obs) {
		out[s.Name] = s
	}
	return out
}

// TestLadderShowsOneNextThing keeps the ladder from presenting work that cannot
// be started. Three simultaneous "your move" steps, two of them impossible, is
// worse guidance than one.
func TestLadderShowsOneNextThing(t *testing.T) {
	empty := &ProjectDomain{}
	steps := empty.DomainReadiness(DomainObservation{Known: true})

	yourMove := 0
	for _, s := range steps {
		if s.State == StepYourMove {
			yourMove++
		}
	}
	if yourMove != 1 {
		t.Errorf("%d steps claim to be the user's move with nothing set up; want 1", yourMove)
	}
	if steps[0].State != StepYourMove {
		t.Error("the first thing to do should be the first step")
	}
	for _, s := range steps[2:] {
		if s.State != StepBlocked {
			t.Errorf("%q is not blocked but nothing before it is done", s.Name)
		}
	}
}

// TestWrongRecordIsNotProgress is the case an acknowledgement would get wrong:
// the user creates a record, believes they are done, and it points elsewhere.
func TestWrongRecordIsNotProgress(t *testing.T) {
	d := &ProjectDomain{BaseDomain: "example.com", DemoSubdomain: "mendel-demos", StaticIP: "34.1.2.3"}

	wrong := ladder(d, DomainObservation{Known: true, WildcardTarget: "203.0.113.9"})["Create the wildcard A record"]
	if wrong.State == StepDone {
		t.Fatal("a record pointing at the wrong address counted as done")
	}
	if !strings.Contains(wrong.Detail, "203.0.113.9") || !strings.Contains(wrong.Detail, "34.1.2.3") {
		t.Errorf("the detail should show both addresses so the mistake is visible: %s", wrong.Detail)
	}

	right := ladder(d, DomainObservation{Known: true, WildcardTarget: "34.1.2.3"})["Create the wildcard A record"]
	if right.State != StepDone {
		t.Error("a record pointing at the reserved address is done")
	}
}

// TestChallengeIgnoresTheTrailingDot covers the difference between what a
// resolver returns and what a person types into a provider's form.
func TestChallengeIgnoresTheTrailingDot(t *testing.T) {
	d := &ProjectDomain{
		BaseDomain: "example.com", DemoSubdomain: "mendel-demos", StaticIP: "34.1.2.3",
		ACMERecordName:  "_acme-challenge.mendel-demos.example.com",
		ACMERecordValue: "abc.4.authorize.certificatemanager.goog.",
	}
	obs := DomainObservation{Known: true,
		WildcardTarget:  "34.1.2.3",
		ChallengeTarget: "abc.4.authorize.certificatemanager.goog", // no trailing dot
	}
	if ladder(d, obs)["Create the certificate record"].State != StepDone {
		t.Error("a trailing dot is a resolver artefact, not a different name")
	}
}

// TestHeadlineNamesWhoIsHoldingItUp is what the ribbon says.
func TestHeadlineNamesWhoIsHoldingItUp(t *testing.T) {
	d := &ProjectDomain{
		BaseDomain: "example.com", DemoSubdomain: "mendel-demos", StaticIP: "34.1.2.3",
		ACMERecordName: "_acme-challenge.mendel-demos.example.com", ACMERecordValue: "abc.goog.",
	}

	// Waiting on the user to create a record.
	_, mine := DomainHeadline(d.DomainReadiness(DomainObservation{Known: true}))
	if !mine {
		t.Error("an outstanding DNS record is the user's move")
	}

	// Waiting on the authority, which is nobody's move.
	obs := DomainObservation{Known: true, WildcardTarget: "34.1.2.3", ChallengeTarget: "abc.goog",
		CertificateState: "PROVISIONING"}
	headline, mine := DomainHeadline(d.DomainReadiness(obs))
	if mine {
		t.Error("waiting on a certificate authority is not the user's move")
	}
	if !strings.Contains(headline, "Certificate") {
		t.Errorf("headline should name the step being waited on: %q", headline)
	}

	// Everything done.
	obs.CertificateState = "ACTIVE"
	headline, mine = DomainHeadline(d.DomainReadiness(obs))
	if mine || !strings.Contains(headline, "https") {
		t.Errorf("finished ladder should say so: %q", headline)
	}
}

// Not knowing is not the same as knowing the answer is no.
//
// The Domain page renders before Mendel has looked, because looking means
// running gcloud and the page should not wait for it. Rendering the zero
// observation as fact would tell someone to go and create records they created
// an hour ago -- and, worse, would file that in their queue as their move.
func TestUnobservedIsCheckingRatherThanOutstanding(t *testing.T) {
	d := &ProjectDomain{
		BaseDomain: "example.com", DemoSubdomain: "mendel-demos",
		StaticIP: "34.1.2.3", ACMERecordName: "_acme-challenge.example.com",
		ACMERecordValue: "abc.goog",
	}

	for name, step := range ladder(d, DomainObservation{}) {
		switch name {
		case "Give Mendel a domain you control", "Mendel reserves an address":
			if step.State == StepChecking {
				t.Errorf("%q needs no lookup and should not be checking", name)
			}
		default:
			if step.State != StepChecking {
				t.Errorf("%q claims %q without having looked", name, step.State)
			}
		}
	}

	headline, mine := DomainHeadline(d.DomainReadiness(DomainObservation{}))
	if mine {
		t.Error("an unobserved ladder must not be reported as the user's move")
	}
	if headline == "" {
		t.Error("an unobserved ladder should still say what is going on")
	}
}
