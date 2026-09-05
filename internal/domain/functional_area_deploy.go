package domain

import (
	"fmt"
	"strings"
)

// The conditions behind running a demo and deploying to production.
//
// Most of them were, until step 4 of the design, an inline `if` in a handler or
// a failure inside a background job. Neither can be listed, shown in advance, or
// linked to, and the second costs the user a deploy to discover --
// `runChannelDemoDeployment` got as far as decrypting credentials before finding
// out one was absent. Those call sites now assess the area and render the
// sentences below, so a refusal and the checklist a reader is sent to say the
// same thing.

// Areas.
const (
	AreaDemo AreaID = "demo"
	AreaProd AreaID = "production"
)

// Conditions shared across areas. The sharing is the reason this is a table
// rather than one checklist per area: six of these are wanted by three areas
// each and are today checked in three unrelated places, in different words.
const (
	CondRepoURL           ConditionID = "project.repository-url-set"
	CondPushToken         ConditionID = "project.push-token-stored"
	CondEncryptionKey     ConditionID = "install.encryption-key-configured"
	CondChannel           ConditionID = "channel.configured"
	CondChannelCombo      ConditionID = "channel.combination-supported"
	CondChannelCreds      ConditionID = "channel.credentials-stored"
	CondDemoValidated     ConditionID = "channel.demo-path-validated"
	CondProdValidated     ConditionID = "channel.production-path-validated"
	CondSecrets           ConditionID = "requirements.secrets-supplied"
	CondAcknowledgements  ConditionID = "requirements.acknowledgements-confirmed"
	CondURLRegistrable    ConditionID = "deployment.url-registrable"
)

func deployConditions() []Condition {
	return []Condition{{
		ID:          CondRepoURL,
		Name:        "Give Mendel a repository to write into",
		Evidence:    EvidenceAsked,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeProject,
		SatisfiedAt: ScopeProject,
		Evaluate: func(o Observations) Finding {
			if o.Readiness.HasRepoURL {
				return Finding{State: CondSatisfied, Detail: "A repository is configured."}
			}
			return Finding{
				State:  CondUnsatisfied,
				Detail: "Set the repository URL in project settings.",
				Missing: "No repository URL. Mendel writes code by pushing branches, and has " +
					"nowhere to push until it has been told where.",
			}
		},
	}, {
		ID:          CondPushToken,
		Name:        "Store a token Mendel can push with",
		Evidence:    EvidenceAsked,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeProject,
		SatisfiedAt: ScopeProject,
		DependsOn:   []ConditionID{CondRepoURL},
		Evaluate: func(o Observations) Finding {
			if o.Readiness.HasAuthToken {
				return Finding{State: CondSatisfied, Detail: "A token is stored."}
			}
			return Finding{
				State:  CondUnsatisfied,
				Detail: "Store a token in project settings.",
				Missing: "No push token. Mendel can read the repository and cannot write to it, " +
					"so nothing it generates would reach anywhere.",
			}
		},
	}, {
		ID:          CondEncryptionKey,
		Name:        "Configure the encryption key",
		Evidence:    EvidenceObserved,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeInstallation,
		SatisfiedAt: ScopeInstallation,
		Evaluate: func(o Observations) Finding {
			if o.EncryptionKeyConfigured {
				return Finding{State: CondSatisfied, Detail: "Credentials can be stored and read back."}
			}
			return Finding{
				State:  CondUnsatisfied,
				Detail: "Set MENDEL_CREDENTIAL_KEY on the Mendel installation.",
				Missing: "No encryption key on this installation, so no credential can be decrypted " +
					"and every deployment would fail partway through with a key error.",
			}
		},
	}, {
		ID:          CondChannel,
		Name:        "Choose how this project deploys",
		Evidence:    EvidenceAsked,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeProject,
		SatisfiedAt: ScopeProject,
		Evaluate: func(o Observations) Finding {
			if o.Channel != nil {
				return Finding{State: CondSatisfied, Detail: channelName(o.Channel)}
			}
			return Finding{
				State:   CondUnsatisfied,
				Detail:  "Pick an artifact kind and a hosting platform in Deployment settings.",
				Missing: "No deployment channel, so Mendel has nowhere to put anything it builds.",
			}
		},
	}, {
		ID:          CondChannelCombo,
		Name:        "That combination is one Mendel can deploy",
		Evidence:    EvidenceDerived,
		Remedy:      RemedyUnavailable,
		DeclaredAt:  ScopeProject,
		SatisfiedAt: ScopeChannel,
		DependsOn:   []ConditionID{CondChannel},
		Evaluate: func(o Observations) Finding {
			if o.Channel == nil {
				return Finding{
					State:   CondUnsatisfied,
					Detail:  "No channel to check.",
					Missing: "There is no deployment channel, so there is no combination to support.",
				}
			}
			if o.ChannelCombinationSupported {
				return Finding{State: CondSatisfied, Detail: channelName(o.Channel) + " is supported."}
			}
			return Finding{
				State:  CondUnsatisfied,
				Detail: "Choose a different artifact kind or platform.",
				Missing: fmt.Sprintf("Mendel has no deployment path for %s. The remedy is to change "+
					"the channel rather than to supply anything.", channelName(o.Channel)),
			}
		},
	}, {
		ID:   CondChannelCreds,
		Name: "Store the credentials that channel needs",
		// Declared by the channel -- which platform it is decides which
		// credentials exist -- and satisfied once for the project, because a
		// service-account key serves every deployment made through it.
		Evidence:    EvidenceAsked,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeChannel,
		SatisfiedAt: ScopeProject,
		DependsOn:   []ConditionID{CondChannel},
		Evaluate: func(o Observations) Finding {
			if len(o.MissingChannelCredentials) == 0 {
				return Finding{State: CondSatisfied, Detail: "Every credential this channel needs is stored."}
			}
			names := strings.Join(o.MissingChannelCredentials, ", ")
			return Finding{
				State:       CondUnsatisfied,
				Detail:      "Missing: " + names + ".",
				Missing:     "This channel cannot authenticate without " + names + ".",
				Outstanding: len(o.MissingChannelCredentials),
			}
		},
	}, {
		ID:          CondDemoValidated,
		Name:        "Prove the demo path works",
		Evidence:    EvidenceProbed,
		Remedy:      RemedyMendel,
		DeclaredAt:  ScopeChannel,
		SatisfiedAt: ScopeChannel,
		DependsOn:   []ConditionID{CondChannelCreds},
		Evaluate: func(o Observations) Finding { return validationFinding(o.Channel, "demo") },
	}, {
		ID:          CondProdValidated,
		Name:        "Prove the production path works",
		Evidence:    EvidenceProbed,
		Remedy:      RemedyMendel,
		DeclaredAt:  ScopeChannel,
		SatisfiedAt: ScopeChannel,
		DependsOn:   []ConditionID{CondChannelCreds},
		Evaluate: func(o Observations) Finding { return validationFinding(o.Channel, "production") },
	}, {
		ID:   CondSecrets,
		Name: "Supply the values this code needs",
		// The two-scope case, and the reason the pair exists. What the code
		// needs is a property of the code, so it is declared per Variation. An
		// OAuth client secret is the same value for every Variation and for
		// production, so it is satisfied once for the project.
		Evidence:    EvidenceDeclared,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeVariation,
		SatisfiedAt: ScopeProject,
		Evaluate:    func(o Observations) Finding { return requirementFinding(o, RequirementKindSecret) },
	}, {
		ID:   CondAcknowledgements,
		Name: "Confirm the setup steps done elsewhere",
		// Declared the same way and satisfied per *deployment*: a redirect URI
		// registered for the demo is not the one production needs, and both have
		// to be confirmed separately.
		Evidence:    EvidenceDeclared,
		Remedy:      RemedyUser,
		DeclaredAt:  ScopeVariation,
		SatisfiedAt: ScopeDeployment,
		Evaluate:    func(o Observations) Finding { return requirementFinding(o, RequirementKindAcknowledgement) },
	}, {
		ID:   CondURLRegistrable,
		Name: "The deployment has a URL a provider will accept",
		// Unavailable rather than user: nothing the reader supplies fixes a bare
		// IP over http. The remedy is to revisit the platform or set a domain,
		// and a checklist row that says "impossible" without naming that is a
		// dead end.
		Evidence:    EvidenceDerived,
		Remedy:      RemedyUnavailable,
		DeclaredAt:  ScopeDeployment,
		SatisfiedAt: ScopeDeployment,
		Evaluate: func(o Observations) Finding {
			limitation := ""
			for _, st := range o.Requirements {
				if st.Limitation != "" {
					limitation = st.Limitation
					break
				}
			}
			if limitation == "" {
				return Finding{State: CondSatisfied, Detail: "Nothing about this URL will be refused."}
			}
			return Finding{State: CondUnsatisfied, Detail: limitation, Missing: limitation}
		},
	}}
}

// requirementFinding aggregates one kind of requirement into a single answer.
//
// Counted rather than listed as a step each, for the same reason the certificate
// records are: they are supplied in one sitting, and a checklist that grows a
// rung per requirement tells the reader the shape of the task changed when it
// did not.
//
// A requirement that cannot be judged yet is neither met nor outstanding.
// Production's redirect URI is not knowable until production exists on the
// platforms that assign a hostname at deploy time, and reporting it as
// outstanding would ask someone to register a string nobody can produce.
func requirementFinding(o Observations, kind RequirementKind) Finding {
	var total, met, deferred int
	var outstanding []string

	for _, st := range o.Requirements {
		if st.Requirement.Kind != kind {
			continue
		}
		total++
		switch {
		case st.Met:
			met++
		case st.Deferred:
			deferred++
		default:
			outstanding = append(outstanding, st.Requirement.Name)
		}
	}

	switch {
	case total == 0:
		// Total predicate: code that needs nothing has everything it needs.
		return Finding{State: CondSatisfied, Detail: "This code needs none."}
	case len(outstanding) == 0 && deferred > 0:
		return Finding{
			State: CondSatisfied, Total: total,
			Detail: fmt.Sprintf("%d of %d supplied; %d cannot be judged until the deployment exists.",
				met, total, deferred),
		}
	case len(outstanding) == 0:
		return Finding{State: CondSatisfied, Total: total,
			Detail: fmt.Sprintf("All %d supplied.", total)}
	}

	names := strings.Join(outstanding, ", ")
	noun := "value"
	if kind == RequirementKindAcknowledgement {
		noun = "setup step"
	}
	if len(outstanding) > 1 {
		noun += "s"
	}
	return Finding{
		State:       CondUnsatisfied,
		Detail:      fmt.Sprintf("%d of %d supplied. Still needed: %s.", met, total, names),
		Missing:     fmt.Sprintf("This code cannot run without %d %s: %s.", len(outstanding), noun, names),
		Outstanding: len(outstanding),
		Total:       total,
	}
}

// validationFinding reports a channel's hello-world deploy, health check and
// teardown, keeping "running now" apart from "failed" and from "never tried".
//
// The nil channel is answered here rather than by the caller, and it is the case
// this got wrong first: reading the validation state off a channel that does not
// exist is a panic, and every condition has to answer whatever it is asked
// about. TestEveryConditionIsATotalPredicate found it.
func validationFinding(ch *ProjectDeploymentChannel, which string) Finding {
	if ch == nil {
		return Finding{
			State:   CondUnsatisfied,
			Detail:  "No channel to validate.",
			Missing: "There is no deployment channel, so its " + which + " path cannot be proved.",
		}
	}

	done, running, failure := ch.IsDemoValidated(), ch.IsDemoValidating(), ch.DemoValidationError
	if which == "production" {
		done, running, failure = ch.IsProdValidated(), ch.IsProdValidating(), ch.ProdValidationError
	}

	switch {
	case done:
		return Finding{State: CondSatisfied, Detail: "Validated against the real platform."}
	case running:
		return Finding{State: CondUnsatisfied, Detail: "Validation is running."}
	case failure != nil && *failure != "":
		return Finding{
			State:   CondUnsatisfied,
			Detail:  "Last attempt failed: " + *failure,
			Missing: "The " + which + " path failed validation: " + *failure,
		}
	}
	return Finding{
		State:  CondUnsatisfied,
		Detail: "Not validated yet.",
		Missing: "The " + which + " path has never been proved end to end. Mendel validates it with " +
			"a hello-world deploy, a health check and a teardown, which is what catches a wrong " +
			"credential before it costs a real deployment.",
	}
}

func channelName(ch *ProjectDeploymentChannel) string {
	if ch == nil {
		return "no channel"
	}
	platform := "an unknown platform"
	if ch.HostingPlatform != nil {
		platform = ch.HostingPlatform.Name
	}
	return string(ch.ArtifactKind) + " on " + platform
}

// deployAreas are the two rows these conditions serve.
//
// They differ by one condition, which is the sort of thing a table makes obvious
// and six separate checklists never would.
func deployAreas() []FunctionalArea {
	shared := []ConditionID{
		CondRepoURL, CondPushToken, CondEncryptionKey,
		CondChannel, CondChannelCombo, CondChannelCreds,
		CondSecrets, CondAcknowledgements, CondURLRegistrable,
	}
	demo := append(append([]ConditionID{}, shared...), CondDemoValidated)
	prod := append(append([]ConditionID{}, shared...), CondProdValidated)

	return []FunctionalArea{
		{ID: AreaDemo, Name: "Run a demo of a Variation", Requires: demo},
		{ID: AreaProd, Name: "Deploy to production", Requires: prod},
	}
}
