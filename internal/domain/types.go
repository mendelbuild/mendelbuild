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
	ID            uuid.UUID       `json:"id"`
	HopID         uuid.UUID       `json:"hop_id"`
	Name          string          `json:"name"`                    // e.g., "cache-layer-approach"
	Approach      string          `json:"approach"`                // Detailed implementation approach
	RepositoryID  *uuid.UUID      `json:"repository_id,omitempty"`
	CommitRef     *string         `json:"commit_ref,omitempty"`
	EcosystemID   *uuid.UUID      `json:"ecosystem_id,omitempty"`
	DeploymentRef *string         `json:"deployment_ref,omitempty"`
	Status        VariationStatus `json:"status"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
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

// VariationLog is a log entry for a variation's code generation process.
type VariationLog struct {
	ID          uuid.UUID `json:"id"`
	VariationID uuid.UUID `json:"variation_id"`
	LoggedAt    time.Time `json:"logged_at"`
	Level       LogLevel  `json:"level"`
	Message     string    `json:"message"`
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

// DecisionKind represents the type of decision.
type DecisionKind string

const (
	DecisionKindPassFail           DecisionKind = "pass_fail"
	DecisionKindChooseOne          DecisionKind = "choose_one"
	DecisionKindChooseMany         DecisionKind = "choose_many"
	DecisionKindRoadmapReview      DecisionKind = "roadmap_review"
	DecisionKindVariationReview    DecisionKind = "variation_review"
	DecisionKindVariationSelection DecisionKind = "variation_selection" // Pick winning Variation for a Hop
)

// DecisionStatus represents the lifecycle state of a Decision.
type DecisionStatus string

const (
	DecisionStatusNeedsAssignment DecisionStatus = "needs_assignment"
	DecisionStatusAssigned        DecisionStatus = "assigned"
	DecisionStatusAccepted        DecisionStatus = "accepted"
	DecisionStatusResolved        DecisionStatus = "resolved"
)

// Decision is a choice point in the system.
type Decision struct {
	ID               uuid.UUID      `json:"id"`
	Kind             DecisionKind   `json:"kind"`
	Title            string         `json:"title"`
	Details          *string        `json:"details,omitempty"`
	ObjectivityScore float64        `json:"objectivity_score"`
	ImportanceScore  float64        `json:"importance_score"`
	Status           DecisionStatus `json:"status"`
	AssignedTo       *string        `json:"assigned_to,omitempty"`
	AssignedAt       *time.Time     `json:"assigned_at,omitempty"`
	AcceptedBy       *string        `json:"accepted_by,omitempty"`
	AcceptedAt       *time.Time     `json:"accepted_at,omitempty"`
	ResolvedBy       *string        `json:"resolved_by,omitempty"`
	ResolvedAt       *time.Time     `json:"resolved_at,omitempty"`
	Resolution       *string        `json:"resolution,omitempty"`
	Rationale        *string        `json:"rationale,omitempty"`
	SubjectType      *string        `json:"subject_type,omitempty"`
	SubjectID        *uuid.UUID     `json:"subject_id,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// DecisionMessage is a message in a decision review conversation.
type DecisionMessage struct {
	ID         uuid.UUID  `json:"id"`
	DecisionID uuid.UUID  `json:"decision_id"`
	Role       string     `json:"role"` // "user", "agent", "system"
	Content    string     `json:"content"`
	TokensUsed *int       `json:"tokens_used,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

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
