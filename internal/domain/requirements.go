package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// A variation's requirements are the things its code needs in order to run
// anywhere. A variation that wired up Google sign-in needs client credentials
// and a registered redirect URI to function at all; demos are merely where that
// first bites, because a demo is the first time the code is pushed through a
// deployment channel. The same requirements gate a production deploy of that
// variation once it is merged.

// RequirementKind distinguishes the two things Mendel can do about a
// requirement: hold a value, or record that the user did something elsewhere.
type RequirementKind string

const (
	// RequirementKindSecret is a value Mendel needs from the user
	// (GOOGLE_CLIENT_SECRET). Stored encrypted, project-scoped, injected at
	// deploy time.
	RequirementKindSecret RequirementKind = "secret"

	// RequirementKindAcknowledgement is an action the user must take somewhere
	// else, where Mendel already knows the string involved (the deployment's
	// OAuth redirect URI) and only needs confirmation it was done. Mendel
	// stores the confirmation, never a secret.
	RequirementKindAcknowledgement RequirementKind = "acknowledgement"
)

// DeployURLPlaceholder appears in an acknowledgement's instructions and
// resolves to the URL of whichever deployment is being gated, so one
// requirement yields the demo's redirect URI and production's as separate
// acknowledgements.
const DeployURLPlaceholder = "{{deploy_url}}"

// VariationRequirement is one thing a variation needs before it can run.
type VariationRequirement struct {
	ID          uuid.UUID       `json:"id"`
	VariationID uuid.UUID       `json:"variation_id"`
	Kind        RequirementKind `json:"kind"`

	// Name is stable within a variation: the env var name for a secret, a slug
	// like 'google-redirect-uri' for an acknowledgement.
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`

	// Instructions and ConsoleURL are acknowledgements only. Instructions may
	// contain DeployURLPlaceholder; ConsoleURL links to the page where the
	// action is performed.
	Instructions *string `json:"instructions,omitempty"`
	ConsoleURL   *string `json:"console_url,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// ResolvedInstructions substitutes the deployment being gated into the
// requirement's instructions. An empty deployURL leaves the placeholder in
// place rather than rendering an instruction with a hole in it.
func (r *VariationRequirement) ResolvedInstructions(deployURL string) string {
	if r.Instructions == nil {
		return ""
	}
	if deployURL == "" {
		return *r.Instructions
	}
	return strings.ReplaceAll(*r.Instructions, DeployURLPlaceholder, deployURL)
}

// NeedsDeployURL reports whether this requirement's instructions depend on
// knowing where the code will be deployed. Such a requirement cannot be
// acknowledged before the URL is known.
func (r *VariationRequirement) NeedsDeployURL() bool {
	return r.Instructions != nil && strings.Contains(*r.Instructions, DeployURLPlaceholder)
}

// ProjectEnvVar holds the value of a 'secret' requirement. Project-scoped: an
// OAuth client ID is the same for every variation and for production, so it is
// entered once per project.
type ProjectEnvVar struct {
	ID             uuid.UUID `json:"id"`
	ProjectID      uuid.UUID `json:"project_id"`
	Name           string    `json:"name"`
	EncryptedValue []byte    `json:"-"` // Never serialize to JSON
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// RequirementAcknowledgement records that an acknowledgement was carried out
// for one particular resolved string. A requirement legitimately has several:
// the demo URL and the production URL are different redirect URIs and both
// must be registered.
type RequirementAcknowledgement struct {
	ID             uuid.UUID  `json:"id"`
	RequirementID  uuid.UUID  `json:"requirement_id"`
	ResolvedValue  string     `json:"resolved_value"`
	AcknowledgedBy *uuid.UUID `json:"acknowledged_by,omitempty"`
	AcknowledgedAt time.Time  `json:"acknowledged_at"`
}

// RequirementStatus is one requirement judged against a particular deployment:
// what it resolves to there, and whether it has been satisfied.
type RequirementStatus struct {
	Requirement VariationRequirement `json:"requirement"`

	// ResolvedValue is the string the user must act on — for an
	// acknowledgement, the instructions with the deploy URL substituted in.
	// Empty for secrets.
	ResolvedValue string `json:"resolved_value,omitempty"`

	// Met is true when the secret has a stored value, or when this exact
	// resolved value has been acknowledged.
	Met bool `json:"met"`

	// Deferred is true for an acknowledgement that cannot be judged yet
	// because the deployment's URL is not known until it exists. It is neither
	// met nor blocking; it becomes a normal unmet requirement once there is a
	// URL.
	Deferred bool `json:"deferred"`
}

// Blocking reports whether this requirement should stop a deployment.
func (s RequirementStatus) Blocking() bool {
	return !s.Met && !s.Deferred
}

// RequirementEvidence is what is known about a project's stored values and
// past acknowledgements, against which requirements are judged.
type RequirementEvidence struct {
	// EnvVarNames are the secret names the project has values for.
	EnvVarNames map[string]bool

	// Acknowledged holds, per requirement ID, the exact strings confirmed.
	// A changed URL leaves no matching entry, so the requirement is unmet
	// again rather than silently vouching for a string nobody registered.
	Acknowledged map[uuid.UUID]map[string]bool
}

// EvaluateRequirements judges each requirement against a deployment.
//
// deployURL is where the code is about to run, or "" when that is not knowable
// yet — Fly.io's hostname is deterministic from the app name, but Cloud Run
// assigns a hash at deploy time and GKE a LoadBalancer IP after provisioning.
// Acknowledgements that depend on the URL are deferred in that case rather
// than blocking a deployment on a string nobody can produce yet.
func EvaluateRequirements(reqs []VariationRequirement, ev RequirementEvidence, deployURL string) []RequirementStatus {
	statuses := make([]RequirementStatus, 0, len(reqs))
	for _, req := range reqs {
		st := RequirementStatus{Requirement: req}

		switch req.Kind {
		case RequirementKindSecret:
			st.Met = ev.EnvVarNames[req.Name]

		case RequirementKindAcknowledgement:
			if deployURL == "" && req.NeedsDeployURL() {
				st.Deferred = true
				break
			}
			st.ResolvedValue = req.ResolvedInstructions(deployURL)
			st.Met = ev.Acknowledged[req.ID][st.ResolvedValue]
		}

		statuses = append(statuses, st)
	}
	return statuses
}

// BlockingRequirements returns the statuses that should stop a deployment.
func BlockingRequirements(statuses []RequirementStatus) []RequirementStatus {
	var blocking []RequirementStatus
	for _, st := range statuses {
		if st.Blocking() {
			blocking = append(blocking, st)
		}
	}
	return blocking
}

// UnmetSummary describes what is missing, for an error message the user can
// act on without opening another page.
func UnmetSummary(statuses []RequirementStatus) string {
	var secrets, acks []string
	for _, st := range BlockingRequirements(statuses) {
		switch st.Requirement.Kind {
		case RequirementKindSecret:
			secrets = append(secrets, st.Requirement.Name)
		case RequirementKindAcknowledgement:
			acks = append(acks, st.Requirement.Name)
		}
	}

	var parts []string
	if len(secrets) > 0 {
		parts = append(parts, "missing values for "+strings.Join(secrets, ", "))
	}
	if len(acks) > 0 {
		parts = append(parts, "unconfirmed setup steps: "+strings.Join(acks, ", "))
	}
	return strings.Join(parts, "; ")
}
