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

	// Starting and Stopping are real states, not decoration.
	//
	// Both take minutes -- one builds an image per Arm, the other waits on a
	// load balancer -- and without a state to be in, a page could only show what
	// the experiment was before the work began. Pressing Stop and being returned
	// to a page still offering Stop is indistinguishable from the button not
	// working, which is how it was read.
	ExperimentStarting ExperimentStatus = "starting"
	ExperimentStopping ExperimentStatus = "stopping"
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

	AllocationWeight int `json:"allocation_weight"`

	// What an Arm's objects are named is derived, not stored:
	// experimentArmResource computes it from the experiment and the slug, and
	// the teardown and rollout paths both call that. A deployment_name column
	// existed alongside it until migration 050, was never written by anything,
	// and was a second and emptier answer to a question already answered.

	// What this Arm proposes, before anything has judged it. Admission needs the
	// user's datastore to reach a verdict, so the migration has to survive the
	// gap between code generation writing it and admission ruling on it.
	DeclaredMigrationUp   string `json:"declared_migration_up,omitempty"`
	DeclaredMigrationDown string `json:"declared_migration_down,omitempty"`

	// What this Arm was built from, recorded at build time.
	//
	// Without it, "this arm is running code from before your last change" is
	// undetectable. With it, staleness is a comparison against the branch head
	// rather than a memory of whether somebody restarted the experiment.
	//
	// SourceCommit empty means never built. That is not the same as built from
	// an unknown commit, and BuiltAt is a pointer for the same reason: a zero
	// time reads as 1970 rather than as never.
	SourceCommit string     `json:"source_commit,omitempty"`
	Image        string     `json:"image,omitempty"`
	BuiltAt      *time.Time `json:"built_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ArmFreshness is how an Arm's build stands against its branch.
//
// Three values and not a bool, for the reason Fact is three: an Arm Mendel could
// not check is not an Arm that is up to date, and showing the first as the second
// is how somebody concludes their change is live when it is not.
type ArmFreshness string

const (
	// ArmNeverBuilt is an Arm with no image yet -- a draft experiment, or one
	// whose start failed before it got here.
	ArmNeverBuilt ArmFreshness = "never-built"

	// ArmCurrent is an Arm built from the commit its branch head names.
	ArmCurrent ArmFreshness = "current"

	// ArmStale is an Arm built from something else. Mendel does not act on this
	// by itself: rebuilding an Arm that is serving traffic changes what
	// participants see, which is a decision rather than a tidy-up.
	ArmStale ArmFreshness = "stale"

	// ArmBuildUnrecorded is an Arm that is serving traffic with no record of
	// what it was built from.
	//
	// Not the same as never built, and the difference is the whole reason this
	// type has four states rather than a bool. An empty SourceCommit means
	// Mendel has no record; it does not mean no image exists, and the Arm
	// answering requests is proof that one does. Reporting such an Arm as "not
	// built yet -- starting the experiment builds it" describes the opposite of
	// what is happening.
	//
	// Reachable in normal running, not only on rows that predate the columns:
	// recording the build is deliberately not allowed to fail the build, since
	// the image exists whether or not Mendel managed to write it down.
	ArmBuildUnrecorded ArmFreshness = "unrecorded"

	// ArmFreshnessUnknown is an Arm whose branch Mendel could not read. Reported
	// as its own state rather than folded into stale, because "go rebuild this"
	// is the wrong instruction when the truth is that Mendel could not look.
	ArmFreshnessUnknown ArmFreshness = "unknown"
)

// ArmBuild is what Mendel knows about one Arm's build and how it stands.
//
// Assembled for a reader rather than stored: the columns record what happened,
// and the branch head is looked up when somebody asks.
type ArmBuild struct {
	Freshness ArmFreshness `json:"freshness"`

	// SourceCommit is what the running image was built from, HeadCommit what the
	// branch names now. Either may be empty, and which one is says something
	// different: no source means never built, no head means Mendel could not read
	// the branch.
	SourceCommit string     `json:"source_commit,omitempty"`
	HeadCommit   string     `json:"head_commit,omitempty"`
	Image        string     `json:"image,omitempty"`
	BuiltAt      *time.Time `json:"built_at,omitempty"`

	// Detail is the sentence a reader is shown, written once here so the page and
	// any refusal say the same thing.
	Detail string `json:"detail"`
}

// Short renders a commit the length a person reads.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// DescribeArmBuild judges a build against a branch head.
//
// head is empty when Mendel could not read the branch, which is the case that
// must not be reported as staleness: telling someone to rebuild an arm that is
// already current wastes a build and, mid-experiment, changes what participants
// see for no reason.
//
// serving says whether this Arm is answering requests right now. It is what
// separates an Arm nobody has built from one whose build went unrecorded, and
// the caller knows it from the experiment's status -- which is why it is passed
// rather than inferred here from an empty commit, the very thing in question.
func DescribeArmBuild(arm ExperimentArm, head string, serving bool) ArmBuild {
	b := ArmBuild{
		SourceCommit: arm.SourceCommit,
		HeadCommit:   head,
		Image:        arm.Image,
		BuiltAt:      arm.BuiltAt,
	}
	switch {
	case arm.IsMainline():
		// Mainline is not built by the experiment -- it keeps the Deployment the
		// ordinary production deploy made. Asking whether it is stale is asking
		// about production, which is a different page's question.
		b.Freshness = ArmCurrent
		b.Detail = "Mainline runs whatever production runs; the experiment does not build it."
	case arm.SourceCommit == "" && serving:
		b.Freshness = ArmBuildUnrecorded
		b.Detail = "Serving traffic, but Mendel has no record of what it was built from, so " +
			"whether it includes your latest change cannot be said either way. Rebasing and " +
			"rebuilding it will record what it is running."
	case arm.SourceCommit == "":
		b.Freshness = ArmNeverBuilt
		b.Detail = "Not built yet. Starting the experiment builds it from its branch."
	case head == "":
		b.Freshness = ArmFreshnessUnknown
		b.Detail = "Built from " + short(arm.SourceCommit) + ". Mendel could not read the branch, " +
			"so whether that is still its head is unknown -- not out of date, unchecked."
	case head == arm.SourceCommit:
		b.Freshness = ArmCurrent
		b.Detail = "Built from " + short(head) + ", which is the head of its branch."
	default:
		b.Freshness = ArmStale
		b.Detail = "Serving " + short(arm.SourceCommit) + ", but its branch is now at " +
			short(head) + ". Visitors in this arm are not seeing the later change."
	}
	return b
}

// Stale reports whether this build is behind, for a caller that needs to decide
// rather than to render. Unknown is not stale.
func (b ArmBuild) Stale() bool { return b.Freshness == ArmStale }

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

	// EventFailed records that Mendel could not do what was asked. Its Detail is
	// written for the person who pressed the button; the technical cause goes in
	// Data, where it is available without being the headline.
	EventFailed ExperimentEventKind = "failed"
)

// FailureReport is what a person needs to know when an action did not work.
//
// Deliberately not the error. "the namespace from the provided object
// envoy-gateway-system does not match" is exactly right for whoever maintains
// Mendel and useless to whoever is running an experiment on their own
// application: it tells them nothing about what happened to their production
// traffic, and nothing they could act on.
type FailureReport struct {
	// Summary says what did not happen, in the terms the button was pressed in.
	Summary string

	// Effect says what this means for traffic, which is the question anybody
	// asks first and which the error never answers.
	Effect string

	// Yours says whether the person can do anything about it. Most of these are
	// Mendel's fault, and saying so is more useful than implying otherwise.
	Yours bool

	// Detail is the technical cause, kept for whoever maintains Mendel.
	Detail string
}

// ReportStartFailure describes a failed start in those terms.
//
// The effect is the same whatever went wrong, and it is the reassuring half:
// production is repointed last, after everything else has succeeded, so a start
// that fails at any earlier step has changed nothing a visitor can see.
func ReportStartFailure(detail string) FailureReport {
	return FailureReport{
		Summary: "Mendel could not start this experiment.",
		Effect:  "Nothing changed for your visitors: production is still serving mainline on its own.",
		Detail:  detail,
	}
}

// ReportStartFailurePtr is the same for a caller holding a pointer, which the
// page is, since a nil report means nothing went wrong.
func ReportStartFailurePtr(detail string) *FailureReport {
	r := ReportStartFailure(detail)
	return &r
}

// ReportStopFailure describes a failed stop.
//
// The effect here is not reassuring and must not be written as though it were.
// Stopping returns traffic first and tidies afterwards, so a failure may leave
// an experiment still serving -- which is the thing somebody stopping it wanted
// to end.
func ReportStopFailure(detail string, trafficReturned bool) FailureReport {
	effect := "Traffic may still be split across arms. Try again, or stop it at the cluster."
	if trafficReturned {
		effect = "Visitors are back on mainline; some of the experiment's resources may remain."
	}
	return FailureReport{
		Summary: "Mendel could not finish stopping this experiment.",
		Effect:  effect,
		Detail:  detail,
	}
}

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

// InProgress reports whether Mendel is part-way through changing this
// experiment, and so whether anything on screen is about to be out of date.
func (e *Experiment) InProgress() bool {
	return e != nil && (e.Status == ExperimentStarting || e.Status == ExperimentStopping)
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
