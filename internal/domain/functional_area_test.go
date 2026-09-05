package domain

import (
	"strings"
	"testing"
	"time"
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

// --- The two-scope model, which step 3 exists to test ---

// A coarse answer cannot rest on a finer one without a rule for combining them,
// and there is no such rule. A project-scoped condition depending on a
// per-deployment one has as many answers as there are deployments, and silently
// picking one is how a project gets reported ready on the strength of its
// healthiest deployment.
func TestACoarseConditionCannotDependOnAFinerOne(t *testing.T) {
	coarse := unsatisfied("coarse", "Coarse")
	coarse.SatisfiedAt = ScopeProject
	coarse.DependsOn = []ConditionID{"fine"}

	fine := unsatisfied("fine", "Fine")
	fine.SatisfiedAt = ScopeDeployment

	_, err := BuildCatalogue([]Condition{coarse, fine},
		[]FunctionalArea{{ID: "x", Name: "X", Requires: []ConditionID{"coarse"}}})
	if err == nil {
		t.Fatal("a project-scoped condition depending on a per-deployment one must be refused")
	}
	if !strings.Contains(err.Error(), "coarser") {
		t.Errorf("the error should say what is incoherent about it, got %q", err)
	}
}

// The case the pair of scopes exists for. Both requirements are declared by the
// same Variation; one is answered once for the project and the other separately
// for every deployment. A single scope field would have to pick, and either
// choice asks the wrong question of somebody.
func TestRequirementsAreDeclaredAndSatisfiedAtDifferentScopes(t *testing.T) {
	cat := FunctionalAreas()

	secret, ok := cat.Condition(CondSecrets)
	if !ok {
		t.Fatal("the secrets condition should exist")
	}
	if secret.DeclaredAt != ScopeVariation || secret.SatisfiedAt != ScopeProject {
		t.Errorf("a secret is declared per variation and satisfied per project, got %s/%s",
			secret.DeclaredAt, secret.SatisfiedAt)
	}

	ack, _ := cat.Condition(CondAcknowledgements)
	if ack.DeclaredAt != ScopeVariation || ack.SatisfiedAt != ScopeDeployment {
		t.Errorf("an acknowledgement is declared per variation and satisfied per deployment, got %s/%s",
			ack.DeclaredAt, ack.SatisfiedAt)
	}
	if secret.DeclaredAt != ack.DeclaredAt || secret.SatisfiedAt == ack.SatisfiedAt {
		t.Error("the two differ in where they are answered and agree on where they are asked; " +
			"that difference is the whole reason for two fields")
	}
}

// Fan-out is counted, not listed as a step each. Requirements are supplied in
// one sitting, and a checklist that grows a rung per requirement tells the
// reader the shape of the task changed when it did not.
func TestRequirementsAreCountedRatherThanListedAsSteps(t *testing.T) {
	name := func(s string) VariationRequirement {
		return VariationRequirement{Kind: RequirementKindSecret, Name: s}
	}
	obs := deployObservations()
	obs.Requirements = []RequirementStatus{
		{Requirement: name("GOOGLE_CLIENT_ID"), Met: true},
		{Requirement: name("GOOGLE_CLIENT_SECRET")},
		{Requirement: name("STRIPE_KEY")},
	}

	a := FunctionalAreas().Assess(AreaDemo, obs)
	step := stepFor(t, a, CondSecrets)

	if step.Outstanding != 2 || step.Total != 3 {
		t.Errorf("counts = %d of %d outstanding, want 2 of 3", step.Outstanding, step.Total)
	}
	if !strings.Contains(step.Missing, "GOOGLE_CLIENT_SECRET") || !strings.Contains(step.Missing, "STRIPE_KEY") {
		t.Errorf("the sentence has to name what is missing, got %q", step.Missing)
	}
	if strings.Contains(step.Missing, "GOOGLE_CLIENT_ID") {
		t.Errorf("a supplied value should not be listed as missing, got %q", step.Missing)
	}
}

// A requirement that cannot be judged yet is neither met nor outstanding.
// Production's redirect URI is unknowable until production exists on the
// platforms that assign a hostname at deploy time, and reporting it as
// outstanding asks someone to register a string nobody can produce.
func TestADeferredRequirementDoesNotBlock(t *testing.T) {
	obs := deployObservations()
	obs.Requirements = []RequirementStatus{{
		Requirement: VariationRequirement{Kind: RequirementKindAcknowledgement, Name: "google-redirect-uri"},
		Deferred:    true,
	}}

	step := stepFor(t, FunctionalAreas().Assess(AreaDemo, obs), CondAcknowledgements)
	if step.State != CondSatisfied {
		t.Errorf("state = %q, want satisfied: a deferred acknowledgement is not outstanding", step.State)
	}
	if !strings.Contains(step.Detail, "cannot be judged") {
		t.Errorf("the detail should say why it is not being asked for, got %q", step.Detail)
	}
}

// Code that needs nothing has everything it needs. The total-predicate rule
// applied to the commonest case there is.
func TestNoRequirementsIsSatisfiedRatherThanEmpty(t *testing.T) {
	step := stepFor(t, FunctionalAreas().Assess(AreaDemo, deployObservations()), CondSecrets)
	if step.State != CondSatisfied {
		t.Errorf("state = %q, want satisfied", step.State)
	}
}

// A bare IP over http is refused by every sign-in provider, and nothing the
// reader supplies changes that. The remedy names the choice to revisit rather
// than asking for something.
func TestAnUnregistrableURLIsUnavailableRatherThanTheUsersMove(t *testing.T) {
	obs := deployObservations()
	obs.Requirements = []RequirementStatus{{
		Requirement: VariationRequirement{Kind: RequirementKindAcknowledgement, Name: "google-redirect-uri"},
		Limitation:  DeployURLLimitation("http://34.56.24.112"),
	}}

	cat := FunctionalAreas()
	step := stepFor(t, cat.Assess(AreaDemo, obs), CondURLRegistrable)
	if step.State != CondUnsatisfied {
		t.Fatalf("state = %q, want unsatisfied", step.State)
	}
	if cond, _ := cat.Condition(CondURLRegistrable); cond.Remedy != RemedyUnavailable {
		t.Errorf("remedy = %q, want unavailable: no value the reader supplies fixes a bare IP", cond.Remedy)
	}
	if !strings.Contains(step.Missing, "domain") {
		t.Errorf("the sentence should name the choice that would lift it, got %q", step.Missing)
	}
}

// Demo and production differ by exactly one condition. That is the sort of thing
// a table makes obvious and separate checklists never would.
func TestDemoAndProductionDifferByOneCondition(t *testing.T) {
	cat := FunctionalAreas()
	only := func(a, b AreaID) []ConditionID {
		inB := map[ConditionID]bool{}
		for _, area := range cat.Areas() {
			if area.ID == b {
				for _, id := range area.Requires {
					inB[id] = true
				}
			}
		}
		var out []ConditionID
		for _, area := range cat.Areas() {
			if area.ID != a {
				continue
			}
			for _, id := range area.Requires {
				if !inB[id] {
					out = append(out, id)
				}
			}
		}
		return out
	}

	if got := only(AreaDemo, AreaProd); len(got) != 1 || got[0] != CondDemoValidated {
		t.Errorf("demo alone requires %v, want just the demo path validation", got)
	}
	if got := only(AreaProd, AreaDemo); len(got) != 1 || got[0] != CondProdValidated {
		t.Errorf("production alone requires %v, want just the production path validation", got)
	}
}

// deployObservations is a project with everything in place, so that a test can
// break one thing and see only that.
func deployObservations() Observations {
	return Observations{
		ProjectDomain:               &ProjectDomain{},
		Readiness:                   ProjectReadiness{HasRepoURL: true, HasAuthToken: true},
		EncryptionKeyConfigured:     true,
		Channel:                     validatedChannel(),
		ChannelCombinationSupported: true,
	}
}

func validatedChannel() *ProjectDeploymentChannel {
	now := timeNowForTest()
	return &ProjectDeploymentChannel{
		ArtifactKind:    DeployArtifactContainer,
		HostingPlatform: &HostingPlatform{Name: "Fly.io", Slug: "fly-io"},
		DemoValidatedAt: &now,
		ProdValidatedAt: &now,
	}
}

func stepFor(t *testing.T, a Assessment, id ConditionID) Step {
	t.Helper()
	for _, s := range a.Steps {
		if s.Condition == id {
			return s
		}
	}
	t.Fatalf("%s has no step for %s", a.Area, id)
	return Step{}
}

func timeNowForTest() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }
