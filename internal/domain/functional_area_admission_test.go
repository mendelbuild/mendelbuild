package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/experiment"
)

func armVariation() *uuid.UUID {
	id := uuid.New()
	return &id
}

// wholeExperiment is one that should pass every condition that is built.
func wholeExperiment() AdmissionObservation {
	effect, hours, ack := 0.05, 72, time.Now()
	return AdmissionObservation{
		Experiment: &Experiment{
			AssignmentUnit:          AssignmentUnitUser,
			AssignmentKeySource:     AssignmentKeyCookie,
			AssignmentKeyName:       "session_id",
			MinimumDetectableEffect: &effect,
			PlannedDurationHours:    &hours,
			StoppingRule:            "stop at the planned duration, whatever the data says",
			DissonanceDescription:   "Scores against past orders disappear.",
			AcknowledgedAt:          &ack,
		},
		Arms: []ExperimentArm{
			{Slug: MainlineSlug, AllocationWeight: 50},
			{Slug: "b", VariationID: armVariation(), AllocationWeight: 50,
				DeclaredMigrationUp:   "ALTER TABLE orders ADD COLUMN mendel_exp_b_score INT;",
				DeclaredMigrationDown: "ALTER TABLE orders DROP COLUMN mendel_exp_b_score;"},
		},
	}
}

// The point of the whole area: every built condition holds of a well-formed
// experiment, so an unsatisfied one means something is actually wrong rather
// than that the fixture is thin.
func TestAWellFormedExperimentSatisfiesEveryBuiltCondition(t *testing.T) {
	a := FunctionalAreas().Assess(AreaExperimentAdmission, Observations{Admission: wholeExperiment()})

	for _, s := range a.Steps {
		switch s.State {
		case CondSatisfied, CondUnimplemented:
		default:
			t.Errorf("%s is %s for a well-formed experiment: %s", s.Condition, s.State, s.Detail)
		}
	}
}

// D51 over the new subject. The commonest state of a project is having no
// experiment at all, and a condition that cannot answer about that is not a
// total predicate -- it is a crash, or a grey rung describing work nobody has.
func TestEveryAdmissionConditionAnswersAboutNoExperiment(t *testing.T) {
	shapes := map[string]AdmissionObservation{
		"nothing at all":     {},
		"experiment, no arms": {Experiment: &Experiment{}},
		"arms, no experiment": {Arms: []ExperimentArm{{Slug: MainlineSlug, AllocationWeight: 100}}},
	}

	for name, adm := range shapes {
		t.Run(name, func(t *testing.T) {
			a := FunctionalAreas().Assess(AreaExperimentAdmission, Observations{Admission: adm})
			for _, s := range a.Steps {
				if s.State == "" {
					t.Errorf("%s has no state, so it is not a total predicate", s.Condition)
				}
				if s.State == CondUnsatisfied && s.Finding.Missing == "" {
					t.Errorf("%s is unsatisfied and says nothing about what would change it", s.Condition)
				}
			}
		})
	}
}

// The forget-proofing, asserted. Six conditions are declared with no evaluator
// on purpose; this fails if one of them quietly acquires a passing state, and
// equally if someone deletes them to make the area go green.
func TestTheUnbuiltAdmissionConditionsReportAsUnbuilt(t *testing.T) {
	deferred := []ConditionID{
		CondPlatformRoutesByUnit, CondOneDeployableUnit, CondPurelyAdditive,
		CondTouchedHaveIdentity, CondVerifyMatchesProd, CondArchiveUnderCeiling,
	}

	a := FunctionalAreas().Assess(AreaExperimentAdmission, Observations{Admission: wholeExperiment()})
	seen := map[ConditionID]ConditionState{}
	for _, s := range a.Steps {
		seen[s.Condition] = s.State
	}

	for _, id := range deferred {
		state, present := seen[id]
		if !present {
			t.Errorf("%s is no longer in the admission area. It is deferred, not finished -- "+
				"removing it hides the hole rather than filling it", id)
			continue
		}
		if state != CondUnimplemented {
			t.Errorf("%s is %s; if it now has an evaluator, take it out of this list and out of "+
				"unbuiltAdmissionConditions", id, state)
		}
	}

	// And the consequence that makes the deferral honest: an area with unbuilt
	// conditions cannot report itself available.
	if a.Available {
		t.Error("the admission area reports available while six of its conditions have no evaluator")
	}
}

// A `◌` in the plan is what a reader sees. It has to track the code, or the
// table claims coverage the catalogue does not have.
func TestTheMatrixMarksUnbuiltConditionsApart(t *testing.T) {
	built, unbuilt := 0, 0
	for _, row := range FunctionalAreas().Matrix() {
		if row.Built {
			built++
			continue
		}
		unbuilt++
		if !strings.Contains(FunctionalAreas().MatrixMarkdown(), "◌") {
			t.Fatal("a condition has no evaluator and the rendered matrix contains no ◌")
		}
	}
	if unbuilt == 0 {
		t.Skip("nothing is deferred any more, so there is nothing to mark")
	}
	if built == 0 {
		t.Error("no condition has an evaluator, which means Built is not being read")
	}
}

// The prefix is copied so that internal/experiment stays dependency-free. A
// copy that can drift is worse than an import that cannot, so the test binary
// takes the dependency the package refuses to.
func TestTheNamespacePrefixesAgree(t *testing.T) {
	if experimentNamespacePrefix != experiment.NamespacePrefix {
		t.Errorf("domain says %q and experiment says %q. Everything an experiment creates is "+
			"prefixed with one of them, and two spellings means the declaration check and the "+
			"delta check disagree about what counts",
			experimentNamespacePrefix, experiment.NamespacePrefix)
	}
}

// Each built condition, refused for its own reason. A condition that passes
// whatever it is given is not a gate.
func TestEachBuiltConditionRefusesItsOwnFault(t *testing.T) {
	cases := []struct {
		name  string
		cond  ConditionID
		break_ func(*AdmissionObservation)
	}{
		{"no assignment unit", CondAssignmentDeclared, func(a *AdmissionObservation) {
			a.Experiment.AssignmentUnit = ""
		}},
		{"key the edge cannot read", CondKeyEdgeExtractable, func(a *AdmissionObservation) {
			a.Experiment.AssignmentKeySource = AssignmentKeySource("database_row")
		}},
		{"no stopping rule", CondStatsPreregistered, func(a *AdmissionObservation) {
			a.Experiment.StoppingRule = ""
		}},
		{"dissonance unacknowledged", CondDissonanceAck, func(a *AdmissionObservation) {
			a.Experiment.AcknowledgedAt = nil
		}},
		{"weights do not total 100", CondAllocationValid, func(a *AdmissionObservation) {
			a.Arms[1].AllocationWeight = 40
		}},
		{"migration with no down", CondMigrationReversible, func(a *AdmissionObservation) {
			a.Arms[1].DeclaredMigrationDown = ""
		}},
		{"migration outside the namespace", CondMigrationNamespaced, func(a *AdmissionObservation) {
			a.Arms[1].DeclaredMigrationUp = "ALTER TABLE orders ADD COLUMN score INT;"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adm := wholeExperiment()
			tc.break_(&adm)

			a := FunctionalAreas().Assess(AreaExperimentAdmission, Observations{Admission: adm})
			for _, s := range a.Steps {
				if s.Condition != tc.cond {
					continue
				}
				if s.State == CondSatisfied {
					t.Fatalf("%s is satisfied despite %s", tc.cond, tc.name)
				}
				if s.Finding.Missing == "" {
					t.Fatalf("%s refused and said nothing about what would change it", tc.cond)
				}
				return
			}
			t.Fatalf("%s is not among the area's steps", tc.cond)
		})
	}
}
