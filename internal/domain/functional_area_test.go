package domain

import (
	"strings"
	"testing"
)

// A catalogue is a program constant, so the interesting failures are the ones
// that make it malformed. Each of these would otherwise surface as a functional
// area nobody can unblock, with no message saying why.

func satisfied(id ConditionID, name string) Condition {
	return Condition{
		ID: id, Name: name, Evidence: EvidenceDerived, Remedy: RemedyMendel,
		Evaluate: func(Observations) Finding { return Finding{State: CondSatisfied, Detail: "yes"} },
	}
}

func unsatisfied(id ConditionID, name string) Condition {
	return Condition{
		ID: id, Name: name, Evidence: EvidenceAsked, Remedy: RemedyUser,
		Evaluate: func(Observations) Finding {
			return Finding{State: CondUnsatisfied, Detail: "no", Missing: name + " is missing"}
		},
	}
}

func TestCycleIsRefused(t *testing.T) {
	a := unsatisfied("a", "A")
	b := unsatisfied("b", "B")
	a.DependsOn = []ConditionID{"b"}
	b.DependsOn = []ConditionID{"a"}

	_, err := BuildCatalogue([]Condition{a, b}, []FunctionalArea{{ID: "x", Name: "X", Requires: []ConditionID{"a"}}})
	if err == nil {
		t.Fatal("a dependency cycle must be refused: every condition in it is permanently blocked")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("the error should say what is wrong, got %q", err)
	}
}

func TestDanglingReferencesAreRefused(t *testing.T) {
	dep := unsatisfied("a", "A")
	dep.DependsOn = []ConditionID{"nonexistent"}
	if _, err := BuildCatalogue([]Condition{dep},
		[]FunctionalArea{{ID: "x", Name: "X", Requires: []ConditionID{"a"}}}); err == nil {
		t.Error("a condition depending on one that does not exist must be refused")
	}

	if _, err := BuildCatalogue([]Condition{satisfied("a", "A")},
		[]FunctionalArea{{ID: "x", Name: "X", Requires: []ConditionID{"b"}}}); err == nil {
		t.Error("an area requiring a condition that does not exist must be refused")
	}

	if _, err := BuildCatalogue([]Condition{satisfied("a", "A")},
		[]FunctionalArea{{ID: "x", Name: "X"}}); err == nil {
		t.Error("an area that requires nothing is gated by nothing, and is a mistake rather than a feature")
	}
}

// A condition Mendel cannot check yet must not read as satisfied. Several are
// designed and not built, and a matrix that quietly passed them would claim a
// functional area works when nobody has looked.
func TestNoEvaluatorIsUnimplementedRatherThanSatisfied(t *testing.T) {
	c := NewCatalogue(
		[]Condition{{ID: "a", Name: "Install the thing", Evidence: EvidenceProbed, Remedy: RemedyEither}},
		[]FunctionalArea{{ID: "x", Name: "X", Requires: []ConditionID{"a"}}},
	)
	a := c.Assess("x", Observations{})

	if got := a.Steps[0].State; got != CondUnimplemented {
		t.Errorf("state = %q, want unimplemented", got)
	}
	if a.Available {
		t.Error("an area with an unimplemented condition is not available")
	}
	if len(a.Missing) == 0 {
		t.Error("an unimplemented condition still has to say why the area is unavailable")
	}
}

// Something already true is true whether or not its prerequisite is. Reporting a
// finished step as blocked because an earlier one is outstanding is a plain lie,
// and it is the mistake the obvious ordering makes.
func TestDoneOutranksBlocked(t *testing.T) {
	later := satisfied("later", "Later")
	later.DependsOn = []ConditionID{"earlier"}

	c := NewCatalogue([]Condition{unsatisfied("earlier", "Earlier"), later},
		[]FunctionalArea{{ID: "x", Name: "X", Requires: []ConditionID{"earlier", "later"}}})

	a := c.Assess("x", Observations{})
	if got := a.Steps[1].State; got != CondSatisfied {
		t.Errorf("a satisfied condition with an unmet prerequisite = %q, want satisfied", got)
	}
}

// Nor does "Mendel has not looked" become "blocked". That would claim knowledge
// in the other direction, and the whole point of the state is that there is none.
func TestUncheckedIsNotDemotedToBlocked(t *testing.T) {
	later := Condition{
		ID: "later", Name: "Later", Evidence: EvidenceObserved, Remedy: RemedyUser,
		DependsOn: []ConditionID{"earlier"},
		Evaluate:  func(Observations) Finding { return Finding{State: CondUnchecked, Detail: "Checking."} },
	}
	c := NewCatalogue([]Condition{unsatisfied("earlier", "Earlier"), later},
		[]FunctionalArea{{ID: "x", Name: "X", Requires: []ConditionID{"earlier", "later"}}})

	if got := c.Assess("x", Observations{}).Steps[1].State; got != CondUnchecked {
		t.Errorf("state = %q, want unchecked", got)
	}
}

// An unsatisfied condition whose prerequisite is unmet is blocked, which is the
// case the ladder exists to get right: a reader should be shown the next thing,
// not three things two of which are impossible.
func TestUnsatisfiedIsBlockedByAnUnmetPrerequisite(t *testing.T) {
	later := unsatisfied("later", "Later")
	later.DependsOn = []ConditionID{"earlier"}

	c := NewCatalogue([]Condition{unsatisfied("earlier", "Earlier"), later},
		[]FunctionalArea{{ID: "x", Name: "X", Requires: []ConditionID{"earlier", "later"}}})

	a := c.Assess("x", Observations{})
	if got := a.Steps[1].State; got != CondBlocked {
		t.Errorf("state = %q, want blocked", got)
	}
	if a.WaitingOn != ActorYou || a.Headline != "Earlier" {
		t.Errorf("the headline should name the startable step, got %q waiting on %q", a.Headline, a.WaitingOn)
	}
}

// The sharing is the reason this is a table rather than one checklist per area.
func TestAreasRequiringReadsTheMatrixAlongARow(t *testing.T) {
	c := NewCatalogue(
		[]Condition{satisfied("shared", "Shared"), satisfied("solo", "Solo")},
		[]FunctionalArea{
			{ID: "one", Name: "One", Requires: []ConditionID{"shared"}},
			{ID: "two", Name: "Two", Requires: []ConditionID{"shared", "solo"}},
		},
	)
	if got := c.AreasRequiring("shared"); len(got) != 2 {
		t.Errorf("shared condition wanted by %v, want both areas", got)
	}
	if got := c.AreasRequiring("solo"); len(got) != 1 || got[0] != "two" {
		t.Errorf("solo condition wanted by %v, want only two", got)
	}
}

// --- Rules the design states, asserted against the catalogue that ships ---

// Every condition answers, whatever it is asked about. A condition that can go
// undefined names a mechanism rather than an outcome and has to be restated; a
// test is the only thing that keeps that from being a sentence in a document
// nobody rereads.
func TestEveryConditionIsATotalPredicate(t *testing.T) {
	cat := FunctionalAreas()

	// A spread wide enough to include the shapes that have caused trouble: an
	// absent domain, an unobserved one, a wrongly-typed record, and an
	// authority that could not be reached.
	cases := []Observations{
		{ProjectDomain: &ProjectDomain{}},
		{ProjectDomain: &ProjectDomain{BaseDomain: "example.com"}},
		{ProjectDomain: &ProjectDomain{BaseDomain: "example.com", StaticIP: "1.2.3.4"}},
		{
			ProjectDomain: &ProjectDomain{BaseDomain: "example.com", StaticIP: "1.2.3.4"},
			Domain:        DomainObservation{Known: true},
		},
		{
			ProjectDomain: &ProjectDomain{BaseDomain: "example.com", StaticIP: "1.2.3.4"},
			Domain:        DomainObservation{Known: true, WildcardTarget: "9.9.9.9"},
		},
		{
			ProjectDomain: &ProjectDomain{
				BaseDomain: "example.com", StaticIP: "1.2.3.4",
				Challenges: []ACMEChallenge{{RecordName: "_acme.example.com", RecordValue: "abc"}},
			},
			Domain: DomainObservation{Known: true, WildcardTarget: "1.2.3.4"},
		},
		{
			ProjectDomain: &ProjectDomain{BaseDomain: "example.com", StaticIP: "1.2.3.4"},
			Domain:        DomainObservation{Known: true, CertificateUnknown: true},
		},
	}

	for _, area := range cat.Areas() {
		for i, obs := range cases {
			for _, s := range cat.Assess(area.ID, obs).Steps {
				if s.State == "" {
					t.Errorf("%s/%s case %d: no state. A condition is true or false in every "+
						"situation; one that goes undefined names a mechanism rather than an outcome",
						area.ID, s.Condition, i)
				}
				if s.Name == "" {
					t.Errorf("%s/%s case %d: no name, and the name is what a reader sees",
						area.ID, s.Condition, i)
				}
			}
		}
	}
}

// A condition a reader could act on has to say what would make it true, in the
// same string the declining code path returns. If the two can differ they will,
// and the page stops being the answer to "why can't I".
func TestUnsatisfiedConditionsSayWhatIsMissing(t *testing.T) {
	cat := FunctionalAreas()
	obs := Observations{
		ProjectDomain: &ProjectDomain{BaseDomain: "example.com", StaticIP: "1.2.3.4"},
		Domain:        DomainObservation{Known: true, WildcardTarget: "9.9.9.9"},
	}

	for _, area := range cat.Areas() {
		a := cat.Assess(area.ID, obs)
		for _, s := range a.Steps {
			if s.State != CondUnsatisfied && s.State != CondBlocked {
				continue
			}
			if strings.TrimSpace(s.Missing) == "" {
				t.Errorf("%s/%s is %s and says nothing about what would change it",
					area.ID, s.Condition, s.State)
			}
		}
		if !a.Available && len(a.Missing) == 0 {
			t.Errorf("%s is unavailable and gives no reason", area.ID)
		}
	}
}

// The ladder is the assessment, so the two must not be able to disagree about
// how many steps there are or what they are called.
func TestTheLadderIsTheAssessment(t *testing.T) {
	d := &ProjectDomain{
		BaseDomain: "example.com", StaticIP: "1.2.3.4",
		Challenges: []ACMEChallenge{
			{RecordName: "_acme.example.com", RecordValue: "a"},
			{RecordName: "_acme.demos.example.com", RecordValue: "b"},
		},
	}
	obs := DomainObservation{Known: true, WildcardTarget: "1.2.3.4"}

	ladder := d.DomainReadiness(obs)
	a := FunctionalAreas().Assess(AreaNamedDemos, Observations{ProjectDomain: d, Domain: obs})

	if len(ladder) != len(a.Steps) {
		t.Fatalf("ladder has %d steps, assessment has %d", len(ladder), len(a.Steps))
	}
	for i := range ladder {
		if ladder[i].Name != a.Steps[i].Name {
			t.Errorf("step %d: ladder %q, assessment %q", i, ladder[i].Name, a.Steps[i].Name)
		}
		if ladder[i].Detail != a.Steps[i].Detail {
			t.Errorf("step %d detail: ladder %q, assessment %q", i, ladder[i].Detail, a.Steps[i].Detail)
		}
	}
}
