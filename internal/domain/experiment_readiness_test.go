package domain

import "testing"

func ladderByName(steps []ReadinessStep) map[string]ReadinessStep {
	out := make(map[string]ReadinessStep, len(steps))
	for _, s := range steps {
		out[s.Name] = s
	}
	return out
}

func allTrue() ExperimentObservation {
	return ExperimentObservation{
		GatewayAPI: FactTrue, ProdHostname: FactTrue, ProdHost: "app.example.com",
		ProdHTTPS: FactTrue, VerifyDatastore: FactTrue, VerifyReachable: FactTrue,
	}
}

func TestEverythingTrueIsReady(t *testing.T) {
	steps := ExperimentReadiness(allTrue())
	for _, s := range steps {
		if s.State != StepDone {
			t.Errorf("%q is %q when everything holds", s.Name, s.State)
		}
	}
	headline, blocked := ExperimentHeadline(steps)
	if blocked {
		t.Error("a project with every property satisfied is not blocked")
	}
	if headline == "" {
		t.Error("a ready project should still say so")
	}
}

// The bug this type exists to prevent: a check Mendel could not perform must not
// be presented as a property the user has failed to set up. Those are different
// problems belonging to different people, and showing the first as the second
// sends someone to fix something that is not broken.
func TestUnknownIsNotTheSameAsMissing(t *testing.T) {
	obs := allTrue()
	obs.GatewayAPI = FactUnknown
	steps := ladderByName(ExperimentReadiness(obs))

	got := steps["Cluster can route per experiment arm"]
	if got.State == StepYourMove {
		t.Error("a check Mendel could not perform was presented as the user's move")
	}
	if got.State != StepChecking {
		t.Errorf("an undetermined property should read as checking, got %q", got.State)
	}

	// And it must not be reported as ready either, which would be the opposite
	// error: claiming a property holds because nobody could tell that it does not.
	if _, blocked := ExperimentHeadline(ExperimentReadiness(obs)); blocked {
		t.Error("checking is nobody's move, so it must not read as blocked")
	}
	if headline, _ := ExperimentHeadline(ExperimentReadiness(obs)); headline == "Ready to run live-traffic experiments" {
		t.Error("a project with an undetermined property was declared ready")
	}
}

// https is a real concern about the integrity of the assignment and a poor
// reason to refuse to run, so it warns rather than blocks. Every other property
// blocks.
func TestOnlyHTTPSIsAWarning(t *testing.T) {
	obs := allTrue()
	obs.ProdHTTPS = FactFalse

	steps := ExperimentReadiness(obs)
	headline, blocked := ExperimentHeadline(steps)
	if blocked {
		t.Error("http-only production should warn, not block")
	}
	if headline != "Ready, with one warning" {
		t.Errorf("the warning should be visible in the headline, got %q", headline)
	}
	for _, b := range ExperimentBlockers(steps) {
		if b == "" {
			continue
		}
		t.Errorf("https must not appear as a blocker: %s", b)
	}

	// Everything else does block.
	for name, mutate := range map[string]func(*ExperimentObservation){
		"gateway":   func(o *ExperimentObservation) { o.GatewayAPI = FactFalse },
		"hostname":  func(o *ExperimentObservation) { o.ProdHostname = FactFalse },
		"datastore": func(o *ExperimentObservation) { o.VerifyDatastore = FactFalse },
	} {
		t.Run(name, func(t *testing.T) {
			o := allTrue()
			mutate(&o)
			steps := ExperimentReadiness(o)
			if _, blocked := ExperimentHeadline(steps); !blocked {
				t.Errorf("a missing %s should block", name)
			}
			if len(ExperimentBlockers(steps)) == 0 {
				t.Errorf("a missing %s should be listed as a blocker", name)
			}
		})
	}
}

// A step nobody can start yet must not be presented as something to do. The
// datastore has to exist before reaching it is a meaningful question.
func TestReachabilityIsNotAskedBeforeThereIsSomethingToReach(t *testing.T) {
	obs := allTrue()
	obs.VerifyDatastore = FactFalse
	obs.VerifyReachable = FactFalse

	steps := ladderByName(ExperimentReadiness(obs))
	if got := steps["That datastore is reachable"]; got.State != StepBlocked {
		t.Errorf("reachability should wait for a connection, got %q", got.State)
	}
}
