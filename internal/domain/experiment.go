package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// An Experiment is a Hop taking live traffic: mainline and one Arm per
// Variation, served side by side to real visitors.

// AssignmentUnit is what a single participant is.
//
// Not a preference. Three things have to agree -- what the edge hashes, what
// durable writes are keyed by, and the denominator of the success metric -- and
// a mismatch is refusable mechanically rather than arguable.
type AssignmentUnit string

const (
	AssignmentUnitUser    AssignmentUnit = "user"
	AssignmentUnitSession AssignmentUnit = "session"
	AssignmentUnitRequest AssignmentUnit = "request"
	AssignmentUnitTenant  AssignmentUnit = "tenant"
)

// AssignmentKeySource is where the edge finds the key. App-specific, so it is
// declared by the application rather than assumed by Mendel.
type AssignmentKeySource string

const (
	AssignmentKeyCookie    AssignmentKeySource = "cookie"
	AssignmentKeyHeader    AssignmentKeySource = "header"
	AssignmentKeyJWTClaim  AssignmentKeySource = "jwt_claim"
	AssignmentKeySubdomain AssignmentKeySource = "subdomain"
)

type ExperimentStatus string

const (
	ExperimentDraft    ExperimentStatus = "draft"
	ExperimentDeclined ExperimentStatus = "declined"
	ExperimentRunning  ExperimentStatus = "running"
	ExperimentStopped  ExperimentStatus = "stopped"
	ExperimentPromoted ExperimentStatus = "promoted"
)

// StoppingRule is how the experiment decides it is over.
//
// Pre-registered, because the hazard is peeking: an agent checking daily and
// stopping at p<0.05 has a badly inflated false-positive rate, and Mendel is
// autonomous by construction. "Look each morning" is not one of these.
type StoppingRule string

const (
	StoppingFixedHorizon StoppingRule = "fixed_horizon"
	StoppingSequential   StoppingRule = "sequential"
)

type Experiment struct {
	ID        uuid.UUID `json:"id"`
	ProjectID uuid.UUID `json:"project_id"`
	HopID     uuid.UUID `json:"hop_id"`

	AssignmentUnit      AssignmentUnit      `json:"assignment_unit"`
	AssignmentKeySource AssignmentKeySource `json:"assignment_key_source"`
	AssignmentKeyName   string              `json:"assignment_key_name"`

	Status ExperimentStatus `json:"status"`

	MinimumDetectableEffect *float64     `json:"minimum_detectable_effect,omitempty"`
	StoppingRule            StoppingRule `json:"stopping_rule"`
	PlannedDurationHours    *int         `json:"planned_duration_hours,omitempty"`

	// DissonanceDescription is what a person who experienced the Variation will
	// feel when the Arm stops serving -- by rollback, by the kill switch, or by
	// an allocation change that withdraws it. DissonancePhrase is what the
	// Mendel user typed to acknowledge it, stored verbatim because the record is
	// keyed by the exact string confirmed.
	DissonanceDescription string     `json:"dissonance_description"`
	DissonancePhrase      string     `json:"dissonance_phrase"`
	AcknowledgedBy        *uuid.UUID `json:"acknowledged_by,omitempty"`
	AcknowledgedAt        *time.Time `json:"acknowledged_at,omitempty"`

	StartedAt *time.Time `json:"started_at,omitempty"`
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`

	// Arms is populated by the loader; it is not a column.
	Arms []ExperimentArm `json:"arms,omitempty"`
}

// ExperimentArm is one side of the comparison.
type ExperimentArm struct {
	ID           uuid.UUID  `json:"id"`
	ExperimentID uuid.UUID  `json:"experiment_id"`
	VariationID  *uuid.UUID `json:"variation_id,omitempty"` // nil is mainline.
	Slug         string     `json:"slug"`

	AllocationWeight int    `json:"allocation_weight"`
	DeploymentName   string `json:"deployment_name"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsMainline reports whether this Arm is the control.
func (a ExperimentArm) IsMainline() bool { return a.VariationID == nil }

// MainlineSlug is what the cookie carries for the control.
const MainlineSlug = "0"

// ArmAdmission is the verdict internal/experiment reached about an Arm's
// migration, recorded as it was reached.
type ArmAdmission struct {
	ID    uuid.UUID `json:"id"`
	ArmID uuid.UUID `json:"arm_id"`

	MigrationUp   string `json:"migration_up"`
	MigrationDown string `json:"migration_down"`

	// Delta and Shapes are stored as the JSON internal/experiment produced, so
	// the record does not drift as those types gain fields.
	Delta  []byte `json:"delta"`
	Shapes []byte `json:"shapes"`

	Verdict    string    `json:"verdict"`
	Reason     string    `json:"reason"`
	AdmittedAt time.Time `json:"admitted_at"`
}

const (
	AdmissionAdmitted = "admitted"
	AdmissionDeclined = "declined"
)

// ArmArchive says where an Arm's data went when it was rolled back.
type ArmArchive struct {
	ID           uuid.UUID  `json:"id"`
	ArmID        uuid.UUID  `json:"arm_id"`
	Location     string     `json:"location"`
	SizeBytes    int64      `json:"size_bytes"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	DownloadedAt *time.Time `json:"downloaded_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// ExperimentEventKind names what happened.
type ExperimentEventKind string

const (
	EventAllocationChanged ExperimentEventKind = "allocation_changed"
	EventGuardrailFired    ExperimentEventKind = "guardrail_fired"
	EventMainlineDeployed  ExperimentEventKind = "mainline_deployed"
	EventArmWithdrawn      ExperimentEventKind = "arm_withdrawn"
	EventKillSwitchPulled  ExperimentEventKind = "kill_switch_pulled"
)

// ExperimentEvent is something that happened while the experiment ran.
//
// A mainline deploy landing mid-experiment is allowed to proceed rather than
// blocked, and the annotation is the whole reason that is safe: without a record
// of it, "the control changed underneath the comparison" has nowhere to live.
type ExperimentEvent struct {
	ID           uuid.UUID           `json:"id"`
	ExperimentID uuid.UUID           `json:"experiment_id"`
	ArmID        *uuid.UUID          `json:"arm_id,omitempty"`
	Kind         ExperimentEventKind `json:"kind"`
	Detail       string              `json:"detail"`
	Data         []byte              `json:"data,omitempty"`
	OccurredAt   time.Time           `json:"occurred_at"`
}

// PermitsDurableWrites reports whether Arms may write per-Assignment-Unit state.
//
// A derivation, not a separate rule. Per-request assignment means one person
// meets both Arms, so the same row gets written by two Arms' logic and any
// user-scoped metric counts a participant twice. Whether that is allowed follows
// from the Assignment Unit rather than being decided beside it.
func (e *Experiment) PermitsDurableWrites() bool {
	return e != nil && e.AssignmentUnit != AssignmentUnitRequest
}

// NotReadyToStart says why this experiment may not take traffic, or "" when it
// may.
//
// The database enforces the same rule; this exists to say which part is missing
// rather than failing a constraint whose name the user should never have to see.
func (e *Experiment) NotReadyToStart() string {
	if e == nil {
		return "there is no experiment"
	}
	switch {
	case e.MinimumDetectableEffect == nil:
		return "No minimum detectable effect. Without one there is no way to say how " +
			"long this needs to run, and no way to tell a null result from an underpowered one."
	case e.PlannedDurationHours == nil:
		return "No duration estimate. It sets the expected hosting spend as well as the " +
			"stopping point, so an experiment without one has an unknown cost."
	case e.StoppingRule == "":
		return "No pre-registered stopping rule. Choosing when to stop after seeing the " +
			"data is what inflates the false-positive rate, and Mendel checks continuously."
	case e.DissonanceDescription != "" && e.AcknowledgedAt == nil:
		return "This experiment has a described user-visible effect on withdrawal that " +
			"nobody has acknowledged yet."
	}
	return ""
}

// Acknowledged reports whether a typed phrase matches what was asked for.
//
// Character-for-character, because the point is friction: a phrase that can be
// approximated can be clicked past. Surrounding whitespace is forgiven since it
// is an artefact of copying rather than of intent.
func (e *Experiment) Acknowledged(typed string) bool {
	if e == nil || e.DissonancePhrase == "" {
		return false
	}
	return strings.TrimSpace(typed) == strings.TrimSpace(e.DissonancePhrase)
}

// DefaultDissonancePhrase is what Mendel asks for when nothing more specific is
// worth asking. The phrase is friction, not a comprehension test.
const DefaultDissonancePhrase = "I understand"

// ValidateAllocation reports why a set of Arms cannot take traffic, or "".
func ValidateAllocation(arms []ExperimentArm) string {
	if len(arms) < 2 {
		return "An experiment needs mainline and at least one Arm to compare against it."
	}
	total, mainline := 0, 0
	for _, a := range arms {
		total += a.AllocationWeight
		if a.IsMainline() {
			mainline++
		}
	}
	if mainline != 1 {
		return fmt.Sprintf("Exactly one Arm is the mainline control; this set has %d.", mainline)
	}
	if total != 100 {
		return fmt.Sprintf("Allocation weights are a share of traffic and must total 100; these total %d.", total)
	}
	return ""
}
