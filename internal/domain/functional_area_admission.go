package domain

import (
	"fmt"
	"strings"
)

// Whether one particular experiment may start.
//
// A separate area from `live-experiments`, and the separation is the point.
// That one asks whether a *project* is equipped to run experiments at all --
// a cluster that can route, production answering at a name, a datastore that
// can be verified against. This one asks whether *this* experiment, with these
// Arms and this migration, may be admitted. Folding them together was what made
// §17 O25 read as one blocked unit: nine cheap conditions and four that need a
// live datastore, sharing a subject that fits neither.
//
// The subject is one experiment, which is the first thing in the catalogue
// finer than a project (ScopeExperiment). Every condition here still answers
// about a project with no experiment in hand, because D51 admits no exceptions:
// the answer is "there is no experiment to admit", which is a fact rather than
// an evasion.
//
// **This area does not gate anything yet, and that is deliberate.** Six of its
// thirteen conditions have no evaluator, so an assessment of it is unavailable
// by construction. `experiment.Applier.Admit` continues to do the real refusing
// against the live datastore, as it always has -- and must, since a catalogue
// row is a forecast and only the check at the moment of action is safe. What
// this area buys today is that the six unbuilt ones are *visible*: they render
// as `unimplemented` in the generated matrix and on the page, which is a hole
// nobody has to remember.

const AreaExperimentAdmission AreaID = "experiment-admission"

const (
	CondAssignmentDeclared  ConditionID = "admission.assignment-unit-declared"
	CondKeyEdgeExtractable  ConditionID = "admission.assignment-key-readable-at-the-edge"
	CondStatsPreregistered  ConditionID = "admission.statistics-preregistered"
	CondDissonanceAck       ConditionID = "admission.withdrawal-dissonance-acknowledged"
	CondAllocationValid     ConditionID = "admission.allocation-totals-one-hundred"
	CondMigrationReversible ConditionID = "admission.migration-can-be-withdrawn"
	CondMigrationNamespaced ConditionID = "admission.migration-stays-in-its-namespace"

	// The six with no evaluator. Declared here rather than left to a document,
	// because a condition that exists as a nil evaluator reports as unbuilt and
	// a condition that exists only in prose reports as nothing at all.
	CondPlatformRoutesByUnit ConditionID = "admission.platform-routes-by-assignment-unit"
	CondOneDeployableUnit    ConditionID = "admission.changes-one-deployable-unit"
	CondPurelyAdditive       ConditionID = "admission.changes-are-purely-additive"
	CondTouchedHaveIdentity  ConditionID = "admission.touched-collections-have-an-identity"
	CondVerifyMatchesProd    ConditionID = "admission.verification-datastore-matches-production"
	CondArchiveUnderCeiling  ConditionID = "admission.projected-archive-fits"
)

// noExperiment is the answer every condition here owes to a project with none.
//
// Satisfied rather than unchecked, and it is worth being explicit about why: an
// area asked about nothing has nothing to refuse. The alternative -- reporting
// unchecked -- would put thirteen grey rungs in front of a reader who has not
// declared an experiment yet, describing work that does not exist.
func noExperiment(detail string) Finding {
	return Finding{State: CondSatisfied, Detail: detail}
}

// admissionConditions is the per-experiment half of §17 §5.
func admissionConditions() []Condition {
	return []Condition{{
		ID:          CondAssignmentDeclared,
		Name:        "The experiment says what one participant is",
		Evidence:    EvidenceDeclared,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeExperiment,
		SatisfiedAt: ScopeExperiment,
		Evaluate: func(o Observations) Finding {
			e := o.Admission.Experiment
			if e == nil {
				return noExperiment("No experiment declared, so there is no unit to name.")
			}
			// The three travel together on purpose. A unit without a key names
			// a participant nothing can identify, and a key source without a
			// name names a place to look and nothing to look for.
			var missing []string
			if e.AssignmentUnit == "" {
				missing = append(missing, "an assignment unit")
			}
			if e.AssignmentKeySource == "" {
				missing = append(missing, "where the key is read from")
			}
			if strings.TrimSpace(e.AssignmentKeyName) == "" {
				missing = append(missing, "the key's name")
			}
			if len(missing) > 0 {
				return Finding{
					State:  CondUnsatisfied,
					Detail: "The declaration does not say what one participant is.",
					Missing: fmt.Sprintf("`.mendel/experiment.json` is missing %s. What the edge hashes, "+
						"what durable writes are keyed by, and the denominator of the success metric "+
						"must be the same thing, so it is declared rather than assumed.",
						strings.Join(missing, ", ")),
				}
			}
			return Finding{State: CondSatisfied, Detail: fmt.Sprintf(
				"One participant is one %s, read from the %s %q.",
				e.AssignmentUnit, e.AssignmentKeySource, e.AssignmentKeyName)}
		},
	}, {
		ID:          CondKeyEdgeExtractable,
		Name:        "That key can be read at the edge",
		Evidence:    EvidenceDeclared,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeExperiment,
		SatisfiedAt: ScopeExperiment,
		DependsOn:   []ConditionID{CondAssignmentDeclared},
		Evaluate: func(o Observations) Finding {
			e := o.Admission.Experiment
			if e == nil {
				return noExperiment("No experiment declared, so there is no key to read.")
			}
			if e.AssignmentKeySource == "" {
				return Finding{
					State:   CondUnsatisfied,
					Detail:  "Checked once the declaration says where the key comes from.",
					Missing: "The declaration does not say where the assignment key is read from.",
				}
			}
			// §16 D30: the router assigns before the application sees the
			// request, so the key has to be in what arrives. A value the
			// application computes -- a row it looks up, a session it decodes
			// server-side -- is not available to whatever is doing the routing.
			switch e.AssignmentKeySource {
			case AssignmentKeyCookie, AssignmentKeyHeader, AssignmentKeyJWTClaim, AssignmentKeySubdomain:
				return Finding{State: CondSatisfied, Detail: fmt.Sprintf(
					"A %s arrives with the request, so the router can read it before the application runs.",
					e.AssignmentKeySource)}
			default:
				return Finding{
					State:  CondUnsatisfied,
					Detail: fmt.Sprintf("%q is not something the router can read.", e.AssignmentKeySource),
					Missing: fmt.Sprintf("The assignment key comes from %q, which the edge cannot see. "+
						"Assignment happens before the application runs, so the key has to be in the "+
						"request itself -- a cookie, a header, a JWT claim or a subdomain.",
						e.AssignmentKeySource),
				}
			}
		},
	}, {
		ID:          CondStatsPreregistered,
		Name:        "Effect size, duration and stopping rule are set",
		Evidence:    EvidenceAsked,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeExperiment,
		SatisfiedAt: ScopeExperiment,
		Evaluate: func(o Observations) Finding {
			e := o.Admission.Experiment
			if e == nil {
				return noExperiment("No experiment declared, so there is nothing to pre-register.")
			}
			// The same sentences NotReadyToStart returns, because a reader who
			// sees one of them in a refusal and a different one on the checklist
			// has to work out whether they are the same problem (D39).
			switch {
			case e.MinimumDetectableEffect == nil:
				return Finding{State: CondUnsatisfied,
					Detail:  "No minimum detectable effect.",
					Missing: e.NotReadyToStart()}
			case e.PlannedDurationHours == nil:
				return Finding{State: CondUnsatisfied,
					Detail:  "No duration estimate.",
					Missing: e.NotReadyToStart()}
			case e.StoppingRule == "":
				return Finding{State: CondUnsatisfied,
					Detail:  "No pre-registered stopping rule.",
					Missing: e.NotReadyToStart()}
			}
			return Finding{State: CondSatisfied,
				Detail: "Effect size, duration and stopping rule were all set before any data arrived."}
		},
	}, {
		ID:          CondDissonanceAck,
		Name:        "The withdrawal dissonance is acknowledged",
		Evidence:    EvidenceAsked,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeExperiment,
		SatisfiedAt: ScopeExperiment,
		Evaluate: func(o Observations) Finding {
			e := o.Admission.Experiment
			if e == nil {
				return noExperiment("No experiment declared, so nothing can be withdrawn.")
			}
			// Total, the way the datastore conditions are: an experiment whose
			// withdrawal nobody will notice has no dissonance to acknowledge,
			// and is therefore already past this.
			if e.DissonanceDescription == "" {
				return Finding{State: CondSatisfied,
					Detail: "Not needed: withdrawing this experiment has no described user-visible effect."}
			}
			if e.AcknowledgedAt == nil {
				return Finding{State: CondUnsatisfied,
					Detail: "Described, and not acknowledged.",
					Missing: "This experiment has a described user-visible effect on withdrawal that " +
						"nobody has acknowledged yet.",
				}
			}
			return Finding{State: CondSatisfied, Detail: "The effect of withdrawing it was acknowledged."}
		},
	}, {
		ID:          CondAllocationValid,
		Name:        "Traffic adds up, with exactly one mainline",
		Evidence:    EvidenceDerived,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeExperiment,
		SatisfiedAt: ScopeExperiment,
		Evaluate: func(o Observations) Finding {
			if o.Admission.Experiment == nil {
				return noExperiment("No experiment declared, so there is no traffic to allocate.")
			}
			if why := ValidateAllocation(o.Admission.Arms); why != "" {
				return Finding{State: CondUnsatisfied, Detail: why, Missing: why}
			}
			return Finding{State: CondSatisfied, Detail: fmt.Sprintf(
				"%d Arms, one of them mainline, sharing 100%% of traffic.", len(o.Admission.Arms))}
		},
	}, {
		ID:          CondMigrationReversible,
		Name:        "Any schema change it declares can be undone",
		Evidence:    EvidenceDeclared,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeVariation,
		SatisfiedAt: ScopeExperiment,
		Evaluate: func(o Observations) Finding {
			if o.Admission.Experiment == nil {
				return noExperiment("No experiment declared, so there is no schema change to undo.")
			}
			// Total: an experiment that changes no schema has nothing to
			// reverse, which is the same shape as the datastore conditions in
			// the project-level area (D51).
			var irreversible []string
			declared := 0
			for _, a := range o.Admission.Arms {
				if strings.TrimSpace(a.DeclaredMigrationUp) == "" {
					continue
				}
				declared++
				if strings.TrimSpace(a.DeclaredMigrationDown) == "" {
					irreversible = append(irreversible, a.Slug)
				}
			}
			if declared == 0 {
				return Finding{State: CondSatisfied, Detail: "Not needed: no Arm changes the schema."}
			}
			if len(irreversible) > 0 {
				return Finding{
					State:  CondUnsatisfied,
					Detail: fmt.Sprintf("%d of %d declared migrations have no down.", len(irreversible), declared),
					Missing: fmt.Sprintf("These Arms declare a migration with no down: %s. An Arm that "+
						"cannot be withdrawn cannot be run, because withdrawal is how an experiment ends "+
						"whether it succeeds or fails.", strings.Join(irreversible, ", ")),
					Outstanding: len(irreversible),
					Total:       declared,
				}
			}
			return Finding{State: CondSatisfied, Total: declared, Detail: fmt.Sprintf(
				"All %d declared migrations have a down.", declared)}
		},
	}, {
		ID:          CondMigrationNamespaced,
		Name:        "Any schema change it declares stays in its own namespace",
		Evidence:    EvidenceDeclared,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeVariation,
		SatisfiedAt: ScopeExperiment,
		DependsOn:   []ConditionID{CondMigrationReversible},
		Evaluate: func(o Observations) Finding {
			if o.Admission.Experiment == nil {
				return noExperiment("No experiment declared, so there is nothing to namespace.")
			}
			// The weak half of a two-part check, and it says so. All that can
			// be read from declared text without parsing a dialect Mendel does
			// not know is whether the prefix appears at all; whether *every*
			// object created carries it is settled by applying the migration
			// and reading the delta, which is CondPurelyAdditive's neighbour in
			// `Applier.Admit`. Both are worth having: this one catches the
			// common mistake before anything is provisioned.
			var unmarked []string
			declared := 0
			for _, a := range o.Admission.Arms {
				up := strings.TrimSpace(a.DeclaredMigrationUp)
				if up == "" {
					continue
				}
				declared++
				if !strings.Contains(up, experimentNamespacePrefix) {
					unmarked = append(unmarked, a.Slug)
				}
			}
			if declared == 0 {
				return Finding{State: CondSatisfied, Detail: "Not needed: no Arm changes the schema."}
			}
			if len(unmarked) > 0 {
				return Finding{
					State:  CondUnsatisfied,
					Detail: fmt.Sprintf("%d of %d declared migrations never mention the prefix.", len(unmarked), declared),
					Missing: fmt.Sprintf("These Arms declare a migration that does not mention %q: %s. "+
						"Everything an experiment creates is prefixed so that withdrawing it is a matter "+
						"of dropping what it made, and so that two experiments cannot collide.",
						experimentNamespacePrefix, strings.Join(unmarked, ", ")),
					Outstanding: len(unmarked),
					Total:       declared,
				}
			}
			return Finding{State: CondSatisfied, Total: declared, Detail: fmt.Sprintf(
				"All %d declared migrations name the %s namespace. Whether every object they create "+
					"stays inside it is settled at admission, by applying and reading the difference.",
				declared, experimentNamespacePrefix)}
		},
	}}
}

// experimentNamespacePrefix mirrors experiment.NamespacePrefix.
//
// Copied rather than imported, and not because of a cycle -- internal/experiment
// depends on nothing in this repository, which is what lets it be checked by a
// conformance suite with no Mendel around it. Importing it here would be the
// first edge into that isolation, spent on one string, and would make any later
// need for a domain type inside internal/experiment a cycle to untangle rather
// than a line to add.
//
// So: a copy, held to the original by TestTheNamespacePrefixesAgree, which
// imports experiment from the test binary where a dependency costs nothing.
const experimentNamespacePrefix = "mendel_exp_"

// unbuiltAdmissionConditions are the six §17 §5 named and nobody has written.
//
// Declared with no evaluator on purpose. A nil Evaluate reports as
// `unimplemented`, which is neither satisfied nor failed -- so these appear in
// the generated matrix and on the page as visible holes rather than as silent
// passes. That is the whole reason they are here: a condition deferred in a
// document is a condition someone has to remember, and a condition deferred as
// a nil evaluator is one a test renders every time.
//
// Four of them are `probed`, which is why they are not merely unwritten but
// were architecturally impossible until recently: settling whether a migration
// is purely additive means running it against a real datastore, and an
// evaluator is a pure function over a struct. §20's adapter is what dissolves
// that -- an `admit` phase runs as a Job, records its result, and the condition
// becomes a pure read of a stored fact, exactly as probeFacts reads a probe.
func unbuiltAdmissionConditions() []Condition {
	return []Condition{{
		ID:          CondPlatformRoutesByUnit,
		Name:        "The platform can route by assignment unit",
		Evidence:    EvidenceDeclared,
		Remedy:      RemedyUnavailable,
		DeclaredAt:  ScopeChannel,
		SatisfiedAt: ScopeChannel,
		// §13 §6.3: Cloud Run splits by percentage-of-requests across revisions
		// and pins by instance, best-effort, broken by autoscaling. It gives
		// "10% of requests", never "these assignment units". Unwritten because
		// no platform record carries this yet, and hardcoding a list of
		// platforms in Go is the thing CLAUDE.md forbids outright.
	}, {
		ID:          CondOneDeployableUnit,
		Name:        "The Variation changes one deployable unit",
		Evidence:    EvidenceProbed,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeVariation,
		SatisfiedAt: ScopeExperiment,
	}, {
		ID:          CondPurelyAdditive,
		Name:        "Whatever it changes is purely additive",
		Evidence:    EvidenceProbed,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeVariation,
		SatisfiedAt: ScopeExperiment,
	}, {
		ID:          CondTouchedHaveIdentity,
		Name:        "Whatever it touches has an identity",
		Evidence:    EvidenceProbed,
		Remedy:      RemedyUnavailable,
		DeclaredAt:  ScopeVariation,
		SatisfiedAt: ScopeExperiment,
	}, {
		ID:          CondVerifyMatchesProd,
		Name:        "The verification datastore agrees with production",
		Evidence:    EvidenceProbed,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeProject,
		SatisfiedAt: ScopeExperiment,
	}, {
		ID:          CondArchiveUnderCeiling,
		Name:        "The projected archive fits",
		Evidence:    EvidenceDerived,
		Remedy:      RemedyUnavailable,
		DeclaredAt:  ScopeExperiment,
		SatisfiedAt: ScopeExperiment,
	}}
}

// admissionArea is the row. The order of Requires is the order the ladder
// renders in, which is roughly cheapest-to-settle first: everything readable
// from the declaration before anything that needs a datastore.
func admissionArea() FunctionalArea {
	return FunctionalArea{
		ID:   AreaExperimentAdmission,
		Name: "Admit this experiment",
		Requires: []ConditionID{
			CondAssignmentDeclared, CondKeyEdgeExtractable, CondPlatformRoutesByUnit,
			CondStatsPreregistered, CondDissonanceAck, CondAllocationValid,
			CondOneDeployableUnit,
			CondMigrationReversible, CondMigrationNamespaced,
			CondPurelyAdditive, CondTouchedHaveIdentity, CondVerifyMatchesProd,
			CondArchiveUnderCeiling,
		},
	}
}
