package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Project is the top-level container for a MendelBuild project.
type Project struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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

// Objective is the "O" in OKR.
type Objective struct {
	ID          uuid.UUID `json:"id"`
	StrategyID  uuid.UUID `json:"strategy_id"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// KeyResult is a quantitative target attached to an Objective.
type KeyResult struct {
	ID          uuid.UUID  `json:"id"`
	ObjectiveID uuid.UUID  `json:"objective_id"`
	Description string     `json:"description"`
	TargetUnits string     `json:"target_units"`
	TargetDate  *time.Time `json:"target_date,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
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
	HopStatusPending   HopStatus = "pending"
	HopStatusActive    HopStatus = "active"
	HopStatusCompleted HopStatus = "completed"
	HopStatusAbandoned HopStatus = "abandoned"
)

// Hop is the fundamental unit of evolutionary experimentation.
type Hop struct {
	ID         uuid.UUID       `json:"id"`
	StrategyID uuid.UUID       `json:"strategy_id"`
	Name       string          `json:"name"`
	Commentary string          `json:"commentary"`
	Params     json.RawMessage `json:"params,omitempty"` // Stores objective_ids and other hop metadata
	Status     HopStatus       `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// HopDependency represents a DAG edge between Hops.
type HopDependency struct {
	HopID         uuid.UUID `json:"hop_id"`
	DependsOnHopID uuid.UUID `json:"depends_on_hop_id"`
}

// VariationStatus represents the lifecycle state of a Variation.
type VariationStatus string

const (
	VariationStatusCreating   VariationStatus = "creating"
	VariationStatusPending    VariationStatus = "pending"
	VariationStatusMigrating  VariationStatus = "migrating"
	VariationStatusActive     VariationStatus = "active"
	VariationStatusDraining   VariationStatus = "draining"
	VariationStatusTerminated VariationStatus = "terminated"
	VariationStatusPruned     VariationStatus = "pruned"
	VariationStatusSelected   VariationStatus = "selected"
)

// Variation is a concrete implementation attempt within a Hop.
type Variation struct {
	ID            uuid.UUID        `json:"id"`
	HopID         uuid.UUID        `json:"hop_id"`
	RepositoryID  *uuid.UUID       `json:"repository_id,omitempty"`
	CommitRef     *string          `json:"commit_ref,omitempty"`
	EcosystemID   *uuid.UUID       `json:"ecosystem_id,omitempty"`
	DeploymentRef *string          `json:"deployment_ref,omitempty"`
	Status        VariationStatus  `json:"status"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

// DecisionKind represents the type of decision.
type DecisionKind string

const (
	DecisionKindPassFail      DecisionKind = "pass_fail"
	DecisionKindChooseOne     DecisionKind = "choose_one"
	DecisionKindChooseMany    DecisionKind = "choose_many"
	DecisionKindRoadmapReview DecisionKind = "roadmap_review"
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
