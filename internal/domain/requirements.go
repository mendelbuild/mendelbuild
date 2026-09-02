package domain

import (
	"net"
	"net/url"
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

// DeployURLLimitation explains why a deployment's URL will not be accepted by
// the service an acknowledgement points at, or "" when nothing is wrong with it.
//
// Providers that take a callback URL do not take just anything. Google's OAuth
// rules refuse a raw IP address and refuse plain http outside localhost, and it
// is a common shape of rule rather than a Google quirk. A Kubernetes demo is
// reached at its LoadBalancer's address, so an acknowledgement telling the user
// to register that address is asking for something the console will reject --
// and they discover it in the console, several steps away from anything Mendel
// said, with no reason to think Mendel was wrong rather than themselves.
//
// Naming the limitation is the honest answer where Mendel cannot supply a
// hostname: the deployment still works, and only this requirement cannot be met
// on this channel.
//
// The obvious escape does not work, and is worth recording so it is not tried
// twice. Every GCP external address has a reverse-DNS name that also resolves
// forward -- 34.56.24.112 is 112.24.56.34.bc.googleusercontent.com, and it does
// serve the deployment over http. It cannot be used here for two independent
// reasons: Google's redirect-URI rules say host domains cannot be
// googleusercontent.com, and no certificate is obtainable for a domain nobody
// here owns, so https is unreachable on that name. A hostname with a
// certificate needs a domain the user controls.
func DeployURLLimitation(deployURL string) string {
	if deployURL == "" {
		return ""
	}
	parsed, err := url.Parse(deployURL)
	if err != nil || parsed.Host == "" {
		return ""
	}

	host := parsed.Hostname()
	isIP := net.ParseIP(host) != nil
	isLocal := host == "localhost" || (isIP && net.ParseIP(host).IsLoopback())
	if isLocal {
		return "" // Providers exempt loopback from both rules.
	}

	switch {
	case isIP && parsed.Scheme != "https":
		return "This deployment is reached at " + deployURL + ", a bare IP address over plain " +
			"http. Sign-in providers accept neither: a redirect URI has to name a host rather " +
			"than an address, and has to be https. Registering this one will be refused. " +
			"This platform does not issue hostnames, so setting a domain you control on " +
			"the Domain tab is what lets a deployment have a name."
	case isIP:
		return "This deployment is reached at " + deployURL + ", a bare IP address. Sign-in " +
			"providers require a host name rather than an address, so registering this one " +
			"will be refused."
	case parsed.Scheme != "https":
		return "This deployment is reached over plain http. Sign-in providers require https " +
			"for anything that is not localhost, so registering this URL will be refused."
	}
	return ""
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

	// Limitation explains why the deployment's URL will not be accepted where
	// this requirement says to register it. Empty when there is no such problem.
	Limitation string `json:"limitation,omitempty"`

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
			// Only where the instruction actually names the deployment: a
			// requirement that says something else entirely is not affected by
			// what the URL happens to look like.
			if req.NeedsDeployURL() && !st.Met {
				st.Limitation = DeployURLLimitation(deployURL)
			}
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

// DeploymentHostname is the name a deployment answers to under a base domain.
//
// One label, because a wildcard DNS record covers exactly one: *.demos.example.com
// answers for pong-abc123.demos.example.com and not for anything deeper. That is
// what lets the user create a single record and Mendel invent names under it
// afterwards without touching DNS again.
func DeploymentHostname(appName, baseDomain string) string {
	baseDomain = strings.TrimSpace(strings.Trim(strings.TrimSpace(baseDomain), "."))
	if baseDomain == "" || appName == "" {
		return ""
	}
	// A name Mendel generated may not be a legal label on its own.
	label := strings.Trim(strings.ToLower(appName), "-")
	if label == "" {
		return ""
	}
	return label + "." + baseDomain
}
