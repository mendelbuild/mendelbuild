package domain

import (
	"fmt"
	"sort"
	"strings"
)

// A Functional Area is something Mendel can do for a project: run a demo, deploy
// to production, serve deployments by name over https, run a live experiment. A
// Functional Area Condition is something that must be true for one to be
// available.
//
// This file is the machinery; the conditions themselves live beside the subject
// they are about. The design is dev/claude_plans/17_functional_area_matrix.md.
//
// Two rules from that design are enforced here rather than left to reviewers,
// because both were arrived at the hard way:
//
//   - A condition is a *total predicate*. It is true or false in every
//     situation, never inapplicable. A condition that can go undefined names a
//     mechanism rather than an outcome and must be restated -- "a verification
//     datastore exists" is silent about an experiment that changes no schema,
//     where "schema changes can be proved additive without touching production"
//     is true of it. Validate rejects a condition that cannot say what it is.
//
//   - Absence of evidence is never satisfaction. Unchecked and Undetermined are
//     distinct from each other and from Unsatisfied, because reporting "looked
//     and found nothing" as "not done" tells a user to redo something they did
//     an hour ago.

// Evidence is how Mendel knows.
type Evidence string

const (
	EvidenceProbed   Evidence = "probed"   // Mendel runs something and looks.
	EvidenceObserved Evidence = "observed" // Mendel looks without asking anyone.
	EvidenceDeclared Evidence = "declared" // The repository says so.
	EvidenceAsked    Evidence = "asked"    // A person supplied it.
	EvidenceDerived  Evidence = "derived"  // Follows from another answer.
)

// Remedy is whose move it is when a condition is false.
//
// RemedyEither is the one that earns the vocabulary. Installing a Gateway API
// controller is Mendel's move where the cluster permits it and an
// administrator's where it does not, and which of those holds is a runtime
// answer rather than a property of the condition -- so an actor field would be
// wrong for it however it was filled in.
type Remedy string

const (
	RemedyMendel      Remedy = "mendel"
	RemedyUser        Remedy = "user"
	RemedyEither      Remedy = "either"
	RemedyElsewhere   Remedy = "elsewhere"
	RemedyUnavailable Remedy = "unavailable"
)

// ConditionState is where one condition stands.
type ConditionState string

const (
	CondSatisfied     ConditionState = "satisfied"
	CondUnsatisfied   ConditionState = "unsatisfied"
	CondBlocked       ConditionState = "blocked"       // A dependency is unmet; not startable.
	CondOffered       ConditionState = "offered"       // Mendel may do this and is asking first.
	CondUnchecked     ConditionState = "unchecked"     // Mendel has not looked yet.
	CondUndetermined  ConditionState = "undetermined"  // Mendel looked and could not tell.
	CondUnimplemented ConditionState = "unimplemented" // There is no evaluator.
)

// Finding is what evaluating one condition produced.
type Finding struct {
	State ConditionState

	// Detail is what was found, phrased for a reader.
	Detail string

	// Missing names what is absent, and is the same string the declining code
	// path returns. One sentence, hand-written per condition: what was found,
	// what it means, what would change it. Required whenever the state is one a
	// reader could act on.
	Missing string

	// Outstanding and Total carry fan-out, where one condition covers several
	// instances of the same task -- three DNS records created in one sitting.
	// Reported as a count rather than as a step each, because a ladder that
	// grows a rung per instance tells the reader the shape of the task changed
	// when it did not.
	Outstanding, Total int
}

// Satisfied reports whether this finding lets a dependent condition start.
func (f Finding) Satisfied() bool { return f.State == CondSatisfied }

// Condition is one column of the matrix.
type Condition struct {
	ID   ConditionID
	Name string // Reader-facing, imperative where it is someone's move.

	// NameFor overrides Name for a condition that covers a fan-out and has to
	// count in its own name. A reader who created one of three records and is
	// looking at a step that still says "your move" needs to know a second one
	// exists, and the count is the only thing that tells them.
	NameFor func(Observations) string

	Evidence Evidence
	Remedy   Remedy

	// DependsOn names conditions that must be satisfied before this one can be
	// anyone's move. Blocked is computed from this rather than authored, so two
	// areas sharing a condition cannot disagree about the order.
	DependsOn []ConditionID

	// Ready is an escape for a precondition that is a plain fact rather than a
	// condition in its own right. Prefer DependsOn: a precondition worth naming
	// is usually a condition worth listing, and this exists because one of the
	// five conditions in the domain ladder is gated on whether Mendel has
	// requested a certificate yet, which nothing else needs to know about.
	Ready func(Observations) bool

	// Evaluate answers the condition. Nil means unimplemented, which reports as
	// such rather than as satisfied or failed -- several conditions are designed
	// and not built, and a matrix that quietly passed them would claim a
	// functional area works when nobody has checked.
	Evaluate func(Observations) Finding
}

// ConditionID identifies a condition across areas.
type ConditionID string

// AreaID identifies a functional area.
type AreaID string

// FunctionalArea is one row: a name, and every condition that must hold.
//
// Requires is a plain conjunction. There is no operator in the matrix beyond
// and, and no third truth value in a cell -- see the total-predicate rule above.
type FunctionalArea struct {
	ID       AreaID
	Name     string
	Requires []ConditionID
}

// Observations is everything gathered for one evaluation.
//
// One struct rather than a typed input per area, because conditions are shared:
// a condition required by four areas cannot have four input types, and the
// sharing is the entire reason the matrix is a table rather than four
// checklists.
type Observations struct {
	// Domain is the project's domain, and what was found when Mendel last
	// looked for its records.
	ProjectDomain *ProjectDomain
	Domain        DomainObservation
}

// Catalogue holds the conditions and the areas built from them.
type Catalogue struct {
	conditions map[ConditionID]Condition
	areas      map[AreaID]FunctionalArea
}

// NewCatalogue builds and validates a catalogue, panicking on a malformed one.
//
// Panic rather than an error because the catalogue is a program constant: a
// cycle or a dangling reference is a bug in this package, not a runtime
// condition any caller could handle, and discovering it at startup is the point.
func NewCatalogue(conditions []Condition, areas []FunctionalArea) *Catalogue {
	c, err := BuildCatalogue(conditions, areas)
	if err != nil {
		panic("functional area catalogue: " + err.Error())
	}
	return c
}

// BuildCatalogue is NewCatalogue with the error returned, for tests that assert
// a malformed catalogue is caught.
func BuildCatalogue(conditions []Condition, areas []FunctionalArea) (*Catalogue, error) {
	c := &Catalogue{
		conditions: make(map[ConditionID]Condition, len(conditions)),
		areas:      make(map[AreaID]FunctionalArea, len(areas)),
	}
	for _, cond := range conditions {
		if cond.ID == "" {
			return nil, fmt.Errorf("a condition has no id")
		}
		if _, dup := c.conditions[cond.ID]; dup {
			return nil, fmt.Errorf("condition %q is defined twice", cond.ID)
		}
		if strings.TrimSpace(cond.Name) == "" {
			return nil, fmt.Errorf("condition %q has no name, and the name is what a reader sees", cond.ID)
		}
		c.conditions[cond.ID] = cond
	}
	for _, dep := range c.conditions {
		for _, on := range dep.DependsOn {
			if _, ok := c.conditions[on]; !ok {
				return nil, fmt.Errorf("condition %q depends on %q, which does not exist", dep.ID, on)
			}
		}
	}
	for _, a := range areas {
		if _, dup := c.areas[a.ID]; dup {
			return nil, fmt.Errorf("functional area %q is defined twice", a.ID)
		}
		if len(a.Requires) == 0 {
			return nil, fmt.Errorf("functional area %q requires nothing, so it is not gated by anything", a.ID)
		}
		for _, id := range a.Requires {
			if _, ok := c.conditions[id]; !ok {
				return nil, fmt.Errorf("functional area %q requires %q, which does not exist", a.ID, id)
			}
		}
		c.areas[a.ID] = a
	}
	if err := c.checkAcyclic(); err != nil {
		return nil, err
	}
	return c, nil
}

// checkAcyclic refuses a catalogue whose dependencies loop.
//
// A cycle would make every condition in it permanently blocked, which renders
// as a functional area nobody can ever unblock and no message explaining why.
func (c *Catalogue) checkAcyclic() error {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	mark := make(map[ConditionID]int, len(c.conditions))

	var walk func(ConditionID, []ConditionID) error
	walk = func(id ConditionID, path []ConditionID) error {
		switch mark[id] {
		case done:
			return nil
		case visiting:
			names := make([]string, 0, len(path)+1)
			for _, p := range append(path, id) {
				names = append(names, string(p))
			}
			return fmt.Errorf("conditions depend on each other in a cycle: %s", strings.Join(names, " -> "))
		}
		mark[id] = visiting
		for _, on := range c.conditions[id].DependsOn {
			if err := walk(on, append(path, id)); err != nil {
				return err
			}
		}
		mark[id] = done
		return nil
	}

	for _, id := range c.conditionIDs() {
		if err := walk(id, nil); err != nil {
			return err
		}
	}
	return nil
}

// conditionIDs returns every condition id in a stable order, so that an error
// message about a malformed catalogue does not change between runs.
func (c *Catalogue) conditionIDs() []ConditionID {
	ids := make([]ConditionID, 0, len(c.conditions))
	for id := range c.conditions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Condition returns one condition and whether it exists.
func (c *Catalogue) Condition(id ConditionID) (Condition, bool) {
	cond, ok := c.conditions[id]
	return cond, ok
}

// Areas returns every functional area, ordered by id.
func (c *Catalogue) Areas() []FunctionalArea {
	out := make([]FunctionalArea, 0, len(c.areas))
	for _, a := range c.areas {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AreasRequiring returns the areas that need a condition, ordered by id. This is
// the matrix read along a row, and it is what makes the sharing visible.
func (c *Catalogue) AreasRequiring(id ConditionID) []AreaID {
	var out []AreaID
	for _, a := range c.Areas() {
		for _, req := range a.Requires {
			if req == id {
				out = append(out, a.ID)
				break
			}
		}
	}
	return out
}

// Step is one condition as it stands for a particular evaluation.
type Step struct {
	Condition ConditionID
	Name      string
	Remedy    Remedy
	Finding
}

// Assessment is a functional area judged against one set of observations.
type Assessment struct {
	Area      AreaID
	Available bool

	// Steps are in the order they can be worked on, dependencies first.
	Steps []Step

	// Missing are the sentences naming what is absent, in that same order. The
	// declining code path renders these; so does the checklist. One source, so
	// the two cannot drift into telling a user different things.
	Missing []string

	// Headline states where things stand in one line, and WaitingOn says whose
	// move it is.
	Headline  string
	WaitingOn Actor
}

// Assess judges one functional area.
//
// Conditions are evaluated in dependency order, and one whose dependencies are
// unmet is Blocked without its evaluator running -- which is both the right
// answer and the reason a blocked ladder costs no DNS lookups.
func (c *Catalogue) Assess(id AreaID, obs Observations) Assessment {
	area, ok := c.areas[id]
	if !ok {
		return Assessment{Area: id, Headline: "No such functional area", WaitingOn: ActorNobody}
	}

	findings := make(map[ConditionID]Finding, len(area.Requires))
	a := Assessment{Area: id, Available: true}

	for _, cid := range c.evaluationOrder(area.Requires) {
		cond := c.conditions[cid]
		f := c.evaluate(cond, obs, findings)
		findings[cid] = f

		// Dependencies are evaluated to gate what follows; only what the area
		// actually asks for is reported.
		if !requires(area, cid) {
			continue
		}
		name := cond.Name
		if cond.NameFor != nil {
			name = cond.NameFor(obs)
		}
		a.Steps = append(a.Steps, Step{Condition: cid, Name: name, Remedy: cond.Remedy, Finding: f})
		if !f.Satisfied() {
			a.Available = false
			if f.Missing != "" {
				a.Missing = append(a.Missing, f.Missing)
			}
		}
	}

	a.Headline, a.WaitingOn = headline(a.Steps)
	return a
}

// evaluate answers one condition, then gates the answer.
//
// The order matters and is not the obvious one. A condition is asked first and
// only *demoted* to blocked when it comes back unsatisfied: something already
// true is true whether or not its prerequisite is, and reporting a finished step
// as blocked because an earlier one is outstanding would be a plain lie. Nor do
// the states that mean "Mendel does not know" get demoted -- an unchecked step
// is unchecked, and calling it blocked would claim knowledge in the other
// direction.
//
// Evaluators are pure functions over Observations, so running one that turns out
// to be blocked costs nothing. What costs is *gathering* the observations, and
// that decision belongs to the caller doing the gathering, not here.
func (c *Catalogue) evaluate(cond Condition, obs Observations, findings map[ConditionID]Finding) Finding {
	if cond.Evaluate == nil {
		return Finding{
			State:  CondUnimplemented,
			Detail: "Mendel cannot check this yet, so it will not claim it is true.",
			Missing: cond.Name + ": designed, and Mendel has no way to check it yet, " +
				"so this functional area is not offered.",
		}
	}

	f := cond.Evaluate(obs)
	if f.State != CondUnsatisfied {
		return f
	}
	for _, on := range cond.DependsOn {
		if dep, ok := findings[on]; ok && !dep.Satisfied() {
			return Finding{State: CondBlocked, Detail: f.Detail, Missing: f.Missing}
		}
	}
	if cond.Ready != nil && !cond.Ready(obs) {
		return Finding{State: CondBlocked, Detail: f.Detail, Missing: f.Missing}
	}
	return f
}

// evaluationOrder returns the requested conditions and everything they depend
// on, dependencies first, in a stable order.
func (c *Catalogue) evaluationOrder(want []ConditionID) []ConditionID {
	var out []ConditionID
	seen := make(map[ConditionID]bool, len(want))

	var visit func(ConditionID)
	visit = func(id ConditionID) {
		if seen[id] {
			return
		}
		seen[id] = true
		for _, on := range c.conditions[id].DependsOn {
			visit(on)
		}
		out = append(out, id)
	}
	for _, id := range want {
		visit(id)
	}
	return out
}

func requires(a FunctionalArea, id ConditionID) bool {
	for _, r := range a.Requires {
		if r == id {
			return true
		}
	}
	return false
}

// headline states where an assessment stands, and who is holding it up.
//
// The order of the cases is the whole content: nobody should be sent to their
// DNS provider on the strength of an answer Mendel has not got yet, so an
// unchecked step outranks an actionable one.
func headline(steps []Step) (string, Actor) {
	for _, s := range steps {
		if s.State == CondUnchecked {
			return "Checking where this stands", ActorMendel
		}
	}
	for _, s := range steps {
		switch s.State {
		case CondUnsatisfied, CondOffered:
			if s.Remedy == RemedyUser || s.Remedy == RemedyEither {
				return s.Name, ActorYou
			}
			return s.Name, ActorMendel
		case CondUndetermined, CondUnimplemented:
			return s.Name, ActorMendel
		case CondBlocked:
			return s.Name, ActorMendel
		}
	}
	if len(steps) > 0 {
		return "Everything this needs is in place", ActorNobody
	}
	return "", ActorNobody
}
