package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Project is the top-level container for a MendelBuild project.
type Project struct {
	ID        uuid.UUID       `json:"id"`
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config,omitempty"` // Project-wide credentials (anthropic_api_key, etc.)
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// ProjectConfig holds project-wide credentials and settings.
type ProjectConfig struct {
	AnthropicAPIKey string `json:"anthropic_api_key,omitempty"`
}

// Strategy captures OKRs, funding sources, and the roadmap (DAG of Hops).
type Strategy struct {
	ID        uuid.UUID  `json:"id"`
	ProjectID uuid.UUID  `json:"project_id"`
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Objective is the "O" in OKR. Objectives can be hierarchical via ParentID.
type Objective struct {
	ID           uuid.UUID  `json:"id"`
	StrategyID   uuid.UUID  `json:"strategy_id"`
	ParentID     *uuid.UUID `json:"parent_id,omitempty"`
	Description  string     `json:"description"`
	TuneScore    *float64   `json:"tune_score,omitempty"`
	TuneFeedback *string    `json:"tune_feedback,omitempty"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// KeyResult is a quantitative target that can be linked to multiple Objectives
// via the objective_key_result_pairs junction table.
type KeyResult struct {
	ID           uuid.UUID  `json:"id"`
	StrategyID   uuid.UUID  `json:"strategy_id"`
	Description  string     `json:"description"`
	TargetUnits  string     `json:"target_units"`
	TargetDate   *time.Time `json:"target_date,omitempty"`
	TuneScore    *float64   `json:"tune_score,omitempty"`
	TuneFeedback *string    `json:"tune_feedback,omitempty"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// KeyResultHistory is a single measurement for a KeyResult.
type KeyResultHistory struct {
	ID          uuid.UUID `json:"id"`
	KeyResultID uuid.UUID `json:"key_result_id"`
	MeasuredValue float64   `json:"measured_value"`
	MeasuredAt  time.Time `json:"measured_at"`
	Source      *string   `json:"source,omitempty"`
}

// ResourceType defines the type of resource in a FundingSource.
type ResourceType string

const (
	ResourceTypeDollars      ResourceType = "dollars"
	ResourceTypeClaudeTokens ResourceType = "claude_tokens"
)

// FundingSource is a pool of resources allocated to a Strategy.
type FundingSource struct {
	ID           uuid.UUID    `json:"id"`
	StrategyID   uuid.UUID    `json:"strategy_id"`
	ResourceType ResourceType `json:"resource_type"`
	Amount       float64      `json:"amount"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// FundingSuccessCriteria links FundingSources to KeyResults.
type FundingSuccessCriteria struct {
	ID              uuid.UUID `json:"id"`
	FundingSourceID uuid.UUID `json:"funding_source_id"`
	KeyResultID     uuid.UUID `json:"key_result_id"`
	Weight          float64   `json:"weight"`
	CreatedAt       time.Time `json:"created_at"`
}

// HopStatus represents the lifecycle state of a Hop.
type HopStatus string

const (
	HopStatusPending   HopStatus = "pending"   // Blocked on dependencies or not scheduled
	HopStatusActive    HopStatus = "active"    // Ready for work, can propose Variations
	HopStatusSelecting HopStatus = "selecting" // All Variations done, awaiting human selection
	HopStatusCompleted HopStatus = "completed" // Winner merged to main
	HopStatusRejected  HopStatus = "rejected"  // Human rejected the Hop entirely
	HopStatusAbandoned HopStatus = "abandoned" // Cancelled without selecting a winner
)

// Hop is the fundamental unit of evolutionary experimentation.
type Hop struct {
	ID                 uuid.UUID       `json:"id"`
	StrategyID         uuid.UUID       `json:"strategy_id"`
	Name               string          `json:"name"`
	Commentary         string          `json:"commentary"`
	Params             json.RawMessage `json:"params,omitempty"`              // Stores objective_ids and other hop metadata
	EvaluationCriteria json.RawMessage `json:"evaluation_criteria,omitempty"` // AI-generated structured criteria for comparing Variations (JSONB)
	RequiresDemo       bool            `json:"requires_demo"`                 // Variations need clickable demos
	RequiresProduction bool            `json:"requires_production"`           // Variations need production traffic
	Status             HopStatus       `json:"status"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// HopDependency represents a DAG edge between Hops.
type HopDependency struct {
	HopID         uuid.UUID `json:"hop_id"`
	DependsOnHopID uuid.UUID `json:"depends_on_hop_id"`
}

// VariationStatus represents the lifecycle state of a Variation.
type VariationStatus string

const (
	VariationStatusCreating   VariationStatus = "creating"   // Code being generated
	VariationStatusPending    VariationStatus = "pending"    // Code generated, awaiting selection
	VariationStatusBlocked    VariationStatus = "blocked"    // Waiting for InputRequest (e.g., credentials)
	VariationStatusMigrating  VariationStatus = "migrating"  // Data migrations in progress
	VariationStatusActive     VariationStatus = "active"     // Live and receiving traffic
	VariationStatusDraining   VariationStatus = "draining"   // Traffic being drained
	VariationStatusError      VariationStatus = "error"      // Mendel infrastructure failure (retryable)
	VariationStatusTerminated VariationStatus = "terminated" // Code/test failure (not retryable)
	VariationStatusPruned     VariationStatus = "pruned"     // Eliminated during evaluation
	VariationStatusSelected   VariationStatus = "selected"   // Legacy: use merged instead
	VariationStatusMerged     VariationStatus = "merged"     // Winner, code merged to main
	VariationStatusRejected   VariationStatus = "rejected"   // Loser, another Variation was selected
)

// Variation is a concrete implementation attempt within a Hop.
type Variation struct {
	ID               uuid.UUID       `json:"id"`
	HopID            uuid.UUID       `json:"hop_id"`
	Name             string          `json:"name"`                    // e.g., "cache-layer-approach"
	Approach         string          `json:"approach"`                // Detailed implementation approach
	RepositoryID     *uuid.UUID      `json:"repository_id,omitempty"`
	CommitRef        *string         `json:"commit_ref,omitempty"`
	EcosystemID      *uuid.UUID      `json:"ecosystem_id,omitempty"`
	DeploymentRef    *string         `json:"deployment_ref,omitempty"`
	DiffFilesChanged   *int            `json:"diff_files_changed,omitempty"`   // Files changed vs main
	DiffAdditions      *int            `json:"diff_additions,omitempty"`       // Lines added vs main
	DiffDeletions      *int            `json:"diff_deletions,omitempty"`       // Lines deleted vs main
	EvaluationScores   json.RawMessage `json:"evaluation_scores,omitempty"`    // Cached evaluation scores
	Status             VariationStatus `json:"status"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// VariationStateHistory records a state transition for a Variation.
type VariationStateHistory struct {
	ID             uuid.UUID `json:"id"`
	VariationID    uuid.UUID `json:"variation_id"`
	FromStatus     *string   `json:"from_status,omitempty"`
	ToStatus       string    `json:"to_status"`
	TransitionedAt time.Time `json:"transitioned_at"`
	Reason         *string   `json:"reason,omitempty"`
}

// LogLevel represents the severity/type of a variation log entry.
type LogLevel string

const (
	LogLevelInfo      LogLevel = "info"
	LogLevelMilestone LogLevel = "milestone"
	LogLevelError     LogLevel = "error"
	LogLevelHeartbeat LogLevel = "heartbeat"
)

// SourceType indicates what operation generated a log entry.
type SourceType string

const (
	SourceTypeCodegen SourceType = "codegen"
	SourceTypeDemo    SourceType = "demo"
	SourceTypeFix     SourceType = "fix"
)

// VariationLog is a log entry for a variation operation (codegen, demo, fix).
type VariationLog struct {
	ID          uuid.UUID  `json:"id"`
	VariationID uuid.UUID  `json:"variation_id"`
	LoggedAt    time.Time  `json:"logged_at"`
	Level       LogLevel   `json:"level"`
	Message     string     `json:"message"`
	SourceType  SourceType `json:"source_type"`
	SourceID    *uuid.UUID `json:"source_id,omitempty"`
}

// VariationMigration represents a temporary schema migration for a variation.
// These are applied during demo/testing and reverted when the variation ends
// (whether accepted or rejected). The "real" migration lives in merged code.
type VariationMigration struct {
	ID               uuid.UUID  `json:"id"`
	VariationID      uuid.UUID  `json:"variation_id"`
	UpInstructions   string     `json:"up_instructions"`   // Instructions for Claude Code to apply
	DownInstructions string     `json:"down_instructions"` // Instructions for Claude Code to revert
	Notes            *string    `json:"notes,omitempty"`   // Where to find migration files in user's CODE repo
	AppliedAt        *time.Time `json:"applied_at,omitempty"`
	RevertedAt       *time.Time `json:"reverted_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// DemoInstanceStatus represents the lifecycle state of a demo instance.
type DemoInstanceStatus string

const (
	DemoInstanceStatusStarting DemoInstanceStatus = "starting"
	DemoInstanceStatusRunning  DemoInstanceStatus = "running"
	DemoInstanceStatusStopped  DemoInstanceStatus = "stopped"
	DemoInstanceStatusError    DemoInstanceStatus = "error"
)

// DemoInstance tracks a running demo of a variation.
// Designed to be stateless: Mendel can crash and recover by reading teardown instructions.
type DemoInstance struct {
	ID                   uuid.UUID          `json:"id"`
	VariationID          uuid.UUID          `json:"variation_id"`
	URL                  string             `json:"url"`
	TeardownInstructions string             `json:"teardown_instructions"` // Shell commands to stop the demo
	StartedAt            time.Time          `json:"started_at"`
	StoppedAt            *time.Time         `json:"stopped_at,omitempty"`
	Status               DemoInstanceStatus `json:"status"`
	ProcessInfo          json.RawMessage    `json:"process_info,omitempty"` // pid, port, container_id, etc.
	ErrorMessage         *string            `json:"error_message,omitempty"`
	SuggestedFix         *string            `json:"suggested_fix,omitempty"` // LLM-suggested fix prompt when status = error
	CreatedAt            time.Time          `json:"created_at"`
}

// BudgetAllocation is a slice of a FundingSource assigned to a specific Hop.
type BudgetAllocation struct {
	ID              uuid.UUID `json:"id"`
	HopID           uuid.UUID `json:"hop_id"`
	FundingSourceID uuid.UUID `json:"funding_source_id"`
	LimitAmount     float64   `json:"limit_amount"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// BudgetSpendLog records consumption against a BudgetAllocation.
type BudgetSpendLog struct {
	ID                 uuid.UUID `json:"id"`
	BudgetAllocationID uuid.UUID `json:"budget_allocation_id"`
	Amount             float64   `json:"amount"`
	RecordedAt         time.Time `json:"recorded_at"`
	Description        *string   `json:"description,omitempty"`
}

// InputRequestKind represents the type of input needed.
type InputRequestKind string

const (
	InputRequestKindPassFail           InputRequestKind = "pass_fail"
	InputRequestKindChooseOne          InputRequestKind = "choose_one"
	InputRequestKindChooseMany         InputRequestKind = "choose_many"
	InputRequestKindRoadmapReview      InputRequestKind = "roadmap_review"
	InputRequestKindVariationReview    InputRequestKind = "variation_review"
	InputRequestKindVariationSelection InputRequestKind = "variation_selection"
	InputRequestKindCredentialRequest  InputRequestKind = "credential_request"
	InputRequestKindManualSetup        InputRequestKind = "manual_setup"
	InputRequestKindConfirmation       InputRequestKind = "confirmation"
)

// InputRequestStatus represents the lifecycle state of an InputRequest.
type InputRequestStatus string

const (
	InputRequestStatusNeedsAssignment InputRequestStatus = "needs_assignment"
	InputRequestStatusAssigned        InputRequestStatus = "assigned"
	InputRequestStatusAccepted        InputRequestStatus = "accepted"
	InputRequestStatusResolved        InputRequestStatus = "resolved"
)

// InputRequest is any input Mendel needs to proceed (decisions, credentials, confirmations, etc.).
type InputRequest struct {
	ID                   uuid.UUID          `json:"id"`
	ProjectID            uuid.UUID          `json:"project_id"`
	Kind                 InputRequestKind   `json:"kind"`
	Title                string             `json:"title"`
	Details              *string            `json:"details,omitempty"`
	Instructions         *string            `json:"instructions,omitempty"`          // How to provide the input
	Link                 *string            `json:"link,omitempty"`                  // URL to external service
	RequiredCapabilities []string           `json:"required_capabilities,omitempty"` // Permissions/scopes needed
	ObjectivityScore     float64            `json:"objectivity_score"`
	ImportanceScore      float64            `json:"importance_score"`
	Status               InputRequestStatus `json:"status"`
	AssignedTo           *string            `json:"assigned_to,omitempty"`
	AssignedAt           *time.Time         `json:"assigned_at,omitempty"`
	AcceptedBy           *string            `json:"accepted_by,omitempty"`
	AcceptedAt           *time.Time         `json:"accepted_at,omitempty"`
	ResolvedBy           *string            `json:"resolved_by,omitempty"`
	ResolvedAt           *time.Time         `json:"resolved_at,omitempty"`
	Resolution           *string            `json:"resolution,omitempty"`
	Rationale            *string            `json:"rationale,omitempty"`
	SubjectType          *string            `json:"subject_type,omitempty"`
	SubjectID            *uuid.UUID         `json:"subject_id,omitempty"`
	CreatedAt            time.Time          `json:"created_at"`
	UpdatedAt            time.Time          `json:"updated_at"`
}

// InputRequestMessage is a message in an input request conversation.
type InputRequestMessage struct {
	ID             uuid.UUID `json:"id"`
	InputRequestID uuid.UUID `json:"input_request_id"`
	Role           string    `json:"role"` // "user", "agent", "system"
	Content        string    `json:"content"`
	TokensUsed     *int      `json:"tokens_used,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Aliases for backwards compatibility during migration (can be removed later)
type DecisionKind = InputRequestKind
type DecisionStatus = InputRequestStatus
type Decision = InputRequest
type DecisionMessage = InputRequestMessage

const (
	DecisionKindPassFail           = InputRequestKindPassFail
	DecisionKindChooseOne          = InputRequestKindChooseOne
	DecisionKindChooseMany         = InputRequestKindChooseMany
	DecisionKindRoadmapReview      = InputRequestKindRoadmapReview
	DecisionKindVariationReview    = InputRequestKindVariationReview
	DecisionKindVariationSelection = InputRequestKindVariationSelection
	DecisionStatusNeedsAssignment  = InputRequestStatusNeedsAssignment
	DecisionStatusAssigned         = InputRequestStatusAssigned
	DecisionStatusAccepted         = InputRequestStatusAccepted
	DecisionStatusResolved         = InputRequestStatusResolved
)

// RepoType represents the type of repository.
type RepoType string

const (
	RepoTypeGit    RepoType = "git"
	RepoTypeFigma  RepoType = "figma"
	RepoTypeGDrive RepoType = "gdrive"
)

// Repository is a versioned store of artifacts.
type Repository struct {
	ID        uuid.UUID       `json:"id"`
	ProjectID uuid.UUID       `json:"project_id"`
	Name      string          `json:"name"`
	RepoType  RepoType        `json:"repo_type"`
	URL       *string         `json:"url,omitempty"`
	Config    json.RawMessage `json:"config,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Ecosystem is a runtime environment where Variations can be deployed.
type Ecosystem struct {
	ID            uuid.UUID       `json:"id"`
	ProjectID     uuid.UUID       `json:"project_id"`
	Name          string          `json:"name"`
	EcosystemType string          `json:"ecosystem_type"`
	Config        json.RawMessage `json:"config,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// ProjectCredential stores an encrypted credential for cloud deployments.
type ProjectCredential struct {
	ID             uuid.UUID `json:"id"`
	ProjectID      uuid.UUID `json:"project_id"`
	Name           string    `json:"name"`
	EncryptedValue []byte    `json:"-"` // Never serialize to JSON
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// DeployedInstanceStatus represents the lifecycle state of a deployed instance.
type DeployedInstanceStatus string

const (
	DeployedInstanceStatusDeploying  DeployedInstanceStatus = "deploying"
	DeployedInstanceStatusRunning    DeployedInstanceStatus = "running"
	DeployedInstanceStatusFailed     DeployedInstanceStatus = "failed"
	DeployedInstanceStatusTerminated DeployedInstanceStatus = "terminated"
)

// DeployedInstance tracks a variation deployed to a cloud environment.
type DeployedInstance struct {
	ID             uuid.UUID              `json:"id"`
	VariationID    uuid.UUID              `json:"variation_id"`
	CloudEcosystem string                 `json:"cloud_ecosystem"`
	URL            string                 `json:"url"`
	PublicURL      *string                `json:"public_url,omitempty"`
	InstanceInfo   json.RawMessage        `json:"instance_info,omitempty"`
	DeployedAt     time.Time              `json:"deployed_at"`
	Status         DeployedInstanceStatus `json:"status"`
	ErrorMessage   *string                `json:"error_message,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
}

// TrafficAllocation defines how traffic is split for a hop.
type TrafficAllocation struct {
	ID         uuid.UUID `json:"id"`
	HopID      uuid.UUID `json:"hop_id"`
	BucketSalt string    `json:"bucket_salt"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TrafficAllocationSlice defines a portion of traffic for a variation.
type TrafficAllocationSlice struct {
	ID                    uuid.UUID `json:"id"`
	TrafficAllocationID   uuid.UUID `json:"traffic_allocation_id"`
	VariationID           uuid.UUID `json:"variation_id"`
	Fraction              float64   `json:"fraction"`
	BucketOrder           int       `json:"bucket_order"`
	CreatedAt             time.Time `json:"created_at"`
}

// TrafficAllocationEnvoyConfig stores a generated Envoy configuration.
type TrafficAllocationEnvoyConfig struct {
	ID           uuid.UUID  `json:"id"`
	ProjectID    uuid.UUID  `json:"project_id"`
	ConfigYAML   string     `json:"config_yaml"`
	GeneratedAt  time.Time  `json:"generated_at"`
	AppliedAt    *time.Time `json:"applied_at,omitempty"`
	SupersededAt *time.Time `json:"superseded_at,omitempty"`
}
