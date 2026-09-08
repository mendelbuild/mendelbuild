package domain

// The conditions behind running a live-traffic experiment.
//
// These were a second ladder, with its own observation struct, its own state
// vocabulary aliased onto the domain ladder's, and its own blocker list -- built
// while the general form was being designed, and left aliased on purpose so the
// two could converge later. This is that convergence. `ExperimentReadiness` is
// now one assessment of this area, exactly as `DomainReadiness` is of its own.
//
// Two of the design's decisions get their first real test here, and both were
// written *because* of this ladder:
//
//   - The https step was `Advisory` unconditionally: a real concern about the
//     integrity of the assignment and a poor reason to refuse to run. It is a
//     warning now (D52), which is a thing an area carries and not a cell in it.
//
//   - The datastore steps were `Advisory` when no migration was declared, which
//     read as a severity and was really applicability. Restated as totals (D51)
//     they need no flag at all: an experiment that changes no schema has nothing
//     to prove, so the condition is simply true of it.
//
// With both folded in, nothing authors `Advisory` any more. It survives as the
// channel the ladder renders a warning through, derived from which list a
// condition came back in rather than set beside the condition itself -- which is
// the difference that matters, since a flag anyone can set is a flag two people
// will set for different reasons.

const AreaExperiment AreaID = "experiment"

const (
	CondGatewayAPI     ConditionID = "cluster.gateway-api-enabled"
	CondCookieMatching ConditionID = "cluster.cookie-matching-controller"
	CondProdHostname   ConditionID = "production.answers-at-a-name"
	CondProvableSchema ConditionID = "experiment.schema-changes-provable"
	CondVerifyReachable ConditionID = "experiment.verification-datastore-answers"
	CondProdHTTPS      ConditionID = "production.serves-https"
	CondArmEnvironment ConditionID = "experiment.arms-get-the-same-environment"
)

func experimentConditions() []Condition {
	return []Condition{{
		ID:          CondGatewayAPI,
		Name:        "Cluster can route per experiment arm",
		Evidence:    EvidenceProbed,
		Remedy:      RemedyEither,
		DeclaredAt:  ScopeChannel,
		SatisfiedAt: ScopeChannel,
		Evaluate: func(o Observations) Finding {
			return factFinding(o.Experiment.GatewayAPI,
				"Gateway API is enabled on the cluster.",
				"Gateway API is not enabled, so nothing can reconcile the routes an experiment needs. "+
					"Being on GKE does not imply it; it is off until someone turns it on.",
				"Mendel could not reach the cluster to check.")
		},
	}, {
		ID:   CondCookieMatching,
		Name: "A controller that can match an experiment cookie",
		// The remedy that earns the vocabulary: Mendel installs this where the
		// cluster permits it and hands over a command where it does not, and
		// which of those holds is a runtime answer rather than a property of the
		// condition.
		Evidence:    EvidenceProbed,
		Remedy:      RemedyEither,
		DeclaredAt:  ScopeChannel,
		SatisfiedAt: ScopeChannel,
		DependsOn:   []ConditionID{CondGatewayAPI},
		Evaluate: func(o Observations) Finding {
			return factFinding(o.Experiment.CookieMatching,
				"Envoy Gateway is installed and can route by cookie.",
				"Gateway API is on, but the only controller is GKE's, which matches headers exactly "+
					"and so cannot pick one cookie out of the several a visitor carries. Mendel keeps "+
					"GKE's gateway at the edge for TLS and the address, and puts one behind it that can "+
					"match.",
				"Mendel could not list the cluster's gateway controllers.")
		},
	}, {
		ID:          CondProdHostname,
		Name:        "Production answers at a name",
		Evidence:    EvidenceObserved,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeProject,
		SatisfiedAt: ScopeProject,
		Evaluate: func(o Observations) Finding {
			return factFinding(o.Experiment.ProdHostname,
				detailOr(o.Experiment.ProdHost, "Production has a hostname."),
				"Production has no hostname. Mendel runs one gateway for all its deployments and "+
					"tells their traffic apart by hostname, so without one there is no route to attach "+
					"arm matching to. Set a production subdomain on the Domain tab.",
				"Mendel could not read this project's domain settings.")
		},
	}, {
		ID:   CondProvableSchema,
		Name: "A non-production datastore to verify against",
		// The worked example of a total predicate. "A verification datastore
		// exists" is silent about an experiment that changes no schema; this is
		// true of it, because there is nothing to prove. That is what removed
		// the conditional Advisory flag rather than promoting it to a feature.
		Evidence:    EvidenceAsked,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeProject,
		SatisfiedAt: ScopeProject,
		Evaluate: func(o Observations) Finding {
			// The predicate is about *declared* schema changes, and "none
			// declared" is a definite state rather than an unknown one. An
			// experiment with no migration has nothing to prove, and so does a
			// settings page with no experiment in front of it; the difference
			// between those two is worth a sentence, not a different answer.
			if o.Experiment.SchemaChanges != FactTrue {
				detail := "Not needed: nothing in this experiment changes the schema."
				if o.Experiment.SchemaChanges == FactUnknown && o.Experiment.VerifyDatastore != FactTrue {
					detail = "Needed once a Variation changes the schema. Nothing has declared one yet."
				}
				return Finding{State: CondSatisfied, Detail: detail}
			}
			return factFinding(o.Experiment.VerifyDatastore,
				"Migrations are proved additive here rather than against production.",
				"No verification datastore. Whether a migration only adds is settled by running it and "+
					"diffing, and doing that against production takes real locks on live tables.",
				"Mendel could not read the stored connection.")
		},
	}, {
		ID:          CondVerifyReachable,
		Name:        "That datastore is reachable",
		Evidence:    EvidenceProbed,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeProject,
		SatisfiedAt: ScopeProject,
		DependsOn:   []ConditionID{CondProvableSchema},
		Evaluate: func(o Observations) Finding {
			// Total the same way, and for the same reason: an experiment with
			// nothing to prove needs nothing to prove it against.
			if o.Experiment.SchemaChanges != FactTrue {
				return Finding{State: CondSatisfied, Detail: "Not needed: nothing to verify against it."}
			}
			// A datastore nobody has given is the previous condition's business.
			// Saying so as unsatisfied lets DependsOn demote this to blocked,
			// which is what keeps it from being presented as a thing to do.
			if o.Experiment.VerifyDatastore != FactTrue {
				return Finding{
					State:   CondUnsatisfied,
					Detail:  "Checked once a connection has been given.",
					Missing: "There is no verification datastore to reach yet.",
				}
			}
			return factFinding(o.Experiment.VerifyReachable,
				"Mendel connected to it and can read its structure.",
				"Mendel could not connect, so nothing can be verified against it.",
				"Not checked yet.")
		},
	}, {
		ID:   CondArmEnvironment,
		Name: "Arms can be given the environment production runs with",
		// A condition and not a check buried in the start path, because it is
		// the third instance of a rule the other two deploy paths already
		// enforce. An Arm brought up without production's values differs from
		// the control in a way nobody chose, and the comparison then measures
		// the missing configuration rather than the change -- which is exactly
		// what happened on the first live experiment, where both Arms logged
		// "Google OAuth: NOT configured" and mainline beside them did not.
		Evidence:    EvidenceObserved,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeVariation,
		SatisfiedAt: ScopeProject,
		Evaluate: func(o Observations) Finding {
			return factFinding(o.Experiment.ArmEnvironment,
				"Every value the merged code needs is stored, so each arm starts with what "+
					"production has.",
				detailOr(o.Experiment.ArmEnvironmentMissing,
					"Production needs values that are not stored. Arms would run without them and "+
						"differ from the control in a way nobody chose."),
				"Mendel could not read what the code needs.")
		},
	}, {
		ID:   CondProdHTTPS,
		Name: "That name serves https",
		// The one warning. Not required by the routing mechanism -- an
		// assignment cookie works over plain http -- and it matters because such
		// a cookie cannot be Secure, so it can be rewritten in transit and a
		// participant who can choose their own Arm makes the comparison quietly
		// meaningless. Real, permanent, and a poor reason to refuse to run.
		Evidence:    EvidenceObserved,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeProject,
		SatisfiedAt: ScopeProject,
		Evaluate: func(o Observations) Finding {
			return factFinding(o.Experiment.ProdHTTPS,
				"Traffic to production is encrypted, so the assignment cookie can be Secure.",
				"Production answers over http only. An experiment can run, but its assignment cookie "+
					"cannot be marked Secure, so it can be rewritten in transit and a participant could "+
					"choose their own arm.",
				"Mendel could not determine the certificate state.")
		},
	}}
}

// experimentArea is the row. The order of Requires is the order the ladder
// renders in, and it is the order these are worth establishing.
func experimentArea() FunctionalArea {
	return FunctionalArea{
		ID:   AreaExperiment,
		Name: "Run a live-traffic experiment",
		Requires: []ConditionID{
			CondGatewayAPI, CondCookieMatching, CondProdHostname,
			CondArmEnvironment, CondProvableSchema, CondVerifyReachable,
		},
		Warns: []ConditionID{CondProdHTTPS},
	}
}

// factFinding renders a tri-state observation, keeping "could not tell" apart
// from "no".
//
// The distinction is the reason Fact is not a bool: a certificate whose state
// could not be read is not a certificate that has not been issued. The first is
// Mendel's problem and the second is the user's, and showing the first as the
// second sends someone to fix something that is not broken.
func factFinding(f Fact, whenTrue, whenFalse, whenUnknown string) Finding {
	switch f {
	case FactTrue:
		return Finding{State: CondSatisfied, Detail: whenTrue}
	case FactFalse:
		return Finding{State: CondUnsatisfied, Detail: whenFalse, Missing: whenFalse}
	default:
		return Finding{State: CondUnchecked, Detail: whenUnknown}
	}
}
