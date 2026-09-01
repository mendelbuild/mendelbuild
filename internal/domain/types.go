package domain

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Project is the top-level container for a MendelBuild project.
type Project struct {
	ID     uuid.UUID       `json:"id"`
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config,omitempty"` // Project-wide credentials (anthropic_api_key, etc.)

	// Brief is what the user said they wanted built, in their own words. Nil
	// for projects loaded from a strategy JSON file, which never asked.
	Brief *string `json:"brief,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProjectConfig holds project-wide credentials and settings.
type ProjectConfig struct {
	AnthropicAPIKey string `json:"anthropic_api_key,omitempty"`
}

// ProjectReadiness describes whether a project has the required settings configured.
type ProjectReadiness struct {
	HasRepoURL   bool
	HasAuthToken bool
	HasAPIKey    bool
}

// IsReady returns true if all required settings are configured.
func (pr ProjectReadiness) IsReady() bool {
	return pr.HasRepoURL && pr.HasAuthToken
}

// MissingSettings returns a human-readable list of missing settings.
func (pr ProjectReadiness) MissingSettings() []string {
	var missing []string
	if !pr.HasRepoURL {
		missing = append(missing, "Repository URL")
	}
	if !pr.HasAuthToken {
		missing = append(missing, "GitHub Auth Token")
	}
	return missing
}

// User represents an authenticated user.
type User struct {
	ID         uuid.UUID `json:"id"`
	Email      string    `json:"email"`
	Name       string    `json:"name,omitempty"`
	PictureURL string    `json:"picture_url,omitempty"`
	GoogleID   string    `json:"google_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ProjectMemberRole defines the role a user has in a project.
type ProjectMemberRole string

const (
	ProjectMemberRoleOwner  ProjectMemberRole = "owner"
	ProjectMemberRoleMember ProjectMemberRole = "member"
)

// ProjectMember links a user to a project with a role.
type ProjectMember struct {
	ID        uuid.UUID         `json:"id"`
	ProjectID uuid.UUID         `json:"project_id"`
	UserID    uuid.UUID         `json:"user_id"`
	Role      ProjectMemberRole `json:"role"`
	CreatedAt time.Time         `json:"created_at"`
}

// Session represents an authenticated session.
type Session struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	TokenHash []byte    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// Strategy captures OKRs, funding sources, and the roadmap (DAG of Hops).
type Strategy struct {
	ID        uuid.UUID  `json:"id"`
	ProjectID uuid.UUID  `json:"project_id"`
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	Name      string     `json:"name"`

	// OKRsApprovedAt is when a human validated these OKRs. Nil means the
	// objectives are still an unreviewed draft, and no roadmap should be built
	// against them yet.
	OKRsApprovedAt *time.Time `json:"okrs_approved_at,omitempty"`

	// DraftNotes is what the drafting agent said about its own draft. Nil for
	// strategies that were never drafted, such as those loaded from JSON.
	DraftNotes json.RawMessage `json:"draft_notes,omitempty"`

	// Where the background draft is up to. Drafting takes 30-45 seconds, longer
	// than an HTTP request may safely block, so it runs detached and the review
	// screen polls these.
	DraftStatus    StrategyDraftStatus `json:"draft_status"`
	DraftError     *string             `json:"draft_error,omitempty"`
	DraftStartedAt *time.Time          `json:"draft_started_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StrategyDraftStatus is where a strategy's background draft stands.
type StrategyDraftStatus string

const (
	StrategyDraftDrafting StrategyDraftStatus = "drafting"
	StrategyDraftReady    StrategyDraftStatus = "ready"
	StrategyDraftFailed   StrategyDraftStatus = "failed"
)

// DraftStaleAfter is how long a draft may sit in 'drafting' before it is
// presumed dead. Comfortably longer than the drafting call's own timeout, so
// only a draft whose process actually went away trips it -- a deploy or a crash
// mid-draft leaves the row claiming work that no goroutine is doing any more.
//
// Whether a draft has passed this point is decided in SQL, against NOW().
//
// It has to be a single clock, and the database's is the one both the web
// process and any future worker share. Comparing a stored timestamp against the
// app process's own clock reintroduces skew between them -- which is a smaller
// version of the bug migration 035 fixed, where the two sides did not even
// agree on a time zone.
const DraftStaleAfter = 6 * time.Minute

// OKRsApproved reports whether a human has signed off on this Strategy's OKRs.
func (s *Strategy) OKRsApproved() bool { return s.OKRsApprovedAt != nil }

// DraftErrorText is what to show the user about a failed draft.
//
// A draft that was lost with its process has no recorded error, so it needs its
// own explanation rather than a blank one. The caller passes whether the
// database judged it stale; see DraftStaleAfter for why that judgement is not
// made here.
func (s *Strategy) DraftErrorText(stale bool) string {
	if s.DraftError != nil && *s.DraftError != "" {
		return *s.DraftError
	}
	if stale {
		return "The draft stopped without finishing — most likely Mendel restarted while it was running."
	}
	return "Mendel could not draft objectives from your brief."
}

// StrategyDraftNotes is the drafting agent's commentary on a drafted strategy.
//
// It is shown next to the draft so the user validating it can see what was
// assumed on their behalf, and kept afterwards as the record of what the plan
// was built on.
type StrategyDraftNotes struct {
	Summary       string   `json:"summary"`
	Assumptions   []string `json:"assumptions"`
	OpenQuestions []string `json:"open_questions"`
	BudgetNote    string   `json:"budget_note"`
}

// Notes decodes DraftNotes, or returns nil when there are none to show.
func (s *Strategy) Notes() *StrategyDraftNotes {
	if len(s.DraftNotes) == 0 {
		return nil
	}
	var n StrategyDraftNotes
	if err := json.Unmarshal(s.DraftNotes, &n); err != nil {
		return nil
	}
	return &n
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
	ID          uuid.UUID `json:"id"`
	StrategyID  uuid.UUID `json:"strategy_id"`
	Description string    `json:"description"`

	// The target, structured [037], judged one of three ways [038]. Value is
	// what a measurement is compared against; unit is for display only, and so
	// carries any qualifier ("ms p99", "signups per week") that does not affect
	// the arithmetic.
	TargetComparator string  `json:"target_comparator"` // TargetAtLeast, TargetAtMost, TargetDone
	TargetValue      float64 `json:"target_value"`
	TargetUnit       string  `json:"target_unit"`

	TargetDate   *time.Time `json:"target_date,omitempty"`
	TuneScore    *float64   `json:"tune_score,omitempty"`
	TuneFeedback *string    `json:"tune_feedback,omitempty"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// How a Key Result is judged [038].
const (
	// TargetAtLeast: the number should reach the target or pass it.
	TargetAtLeast = "at_least"
	// TargetAtMost: the number should stay at the target or below it.
	TargetAtMost = "at_most"
	// TargetDone: it happened, or it has not.
	//
	// Available, and weaker on purpose. A number says on the Tuesday of week
	// three whether the work is on course; a checkbox says nothing at all until
	// it flips, so a `done` Key Result can never be reported as on track --
	// only as met or not yet. See ProgressSignal.
	TargetDone = "done"
)

// IsBoolean reports whether this Key Result is judged as done-or-not.
func (k KeyResult) IsBoolean() bool { return k.TargetComparator == TargetDone }

// Target renders the target as a phrase: "at least 1000 users", "at most 200 ms
// p99", "Done".
//
// Derived rather than stored, so the words and the number cannot disagree.
func (k KeyResult) Target() string {
	if k.IsBoolean() {
		return "Done"
	}
	value := strconv.FormatFloat(k.TargetValue, 'f', -1, 64)
	lead := "at least"
	if k.TargetComparator == TargetAtMost {
		lead = "at most"
	}
	return strings.TrimSpace(lead + " " + value + " " + k.TargetUnit)
}

// Met reports whether a measured value satisfies this target. A boolean Key
// Result stores a target of 1, so anything at or above it counts as done.
func (k KeyResult) Met(measured float64) bool {
	switch k.TargetComparator {
	case TargetAtLeast, TargetDone:
		return measured >= k.TargetValue
	case TargetAtMost:
		return measured <= k.TargetValue
	default:
		// A mode nobody defined cannot be satisfied. Of the two ways to be
		// wrong, reporting a Key Result met is the worse one.
		return false
	}
}

// ProgressSignal reports whether this Key Result can say anything before it is
// met. A numeric target can be compared against the pace needed to reach it; a
// boolean one cannot, and pretending otherwise would invent a reading.
func (k KeyResult) ProgressSignal() bool { return !k.IsBoolean() }

// KeyResultHistory is a single measurement for a KeyResult.
type KeyResultHistory struct {
	ID          uuid.UUID `json:"id"`
	KeyResultID uuid.UUID `json:"key_result_id"`
	MeasuredValue float64   `json:"measured_value"`
	MeasuredAt  time.Time `json:"measured_at"`
	Source      *string   `json:"source,omitempty"`
}

// FundingSource is a pool of money allocated to a Strategy.
//
// USD is the unit of account. Tokens are not a unit of value -- prices differ
// ~10x across models, cache reads are a tenth of an input token and cache
// writes a premium over one -- so a token-denominated budget floats in worth.
// Token counts are still recorded in full on CostEntry.
type FundingSource struct {
	ID         uuid.UUID  `json:"id"`
	StrategyID uuid.UUID  `json:"strategy_id"`
	Name       string     `json:"name"`
	AmountUSD  float64    `json:"amount_usd"`

	// Optional window this budget is meant to cover. Together with the Key
	// Results reached through FundingSuccessCriteria (which carry target
	// dates), this is what ties a budget to dates and to OKR milestones.
	PeriodStart *time.Time `json:"period_start,omitempty"`
	PeriodEnd   *time.Time `json:"period_end,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Elapsed reports how far through its period a budget is, in [0,1], and whether
// the period is known. Used to compare burn rate against time elapsed.
func (f *FundingSource) Elapsed(now time.Time) (float64, bool) {
	if f.PeriodStart == nil || f.PeriodEnd == nil {
		return 0, false
	}
	total := f.PeriodEnd.Sub(*f.PeriodStart)
	if total <= 0 {
		return 0, false
	}
	frac := now.Sub(*f.PeriodStart).Seconds() / total.Seconds()
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return frac, true
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

	// BudgetPausedUSD is set when a generation run stopped at its spend
	// ceiling rather than failing. The work directory is intact and the run
	// can be continued once a human decides it is worth more money. Its
	// presence is what distinguishes a spend pause from being blocked on
	// credentials, since both use the "blocked" status.
	BudgetPausedUSD  *float64 `json:"budget_paused_usd,omitempty"`
	BudgetCeilingUSD *float64 `json:"budget_ceiling_usd,omitempty"`

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

// VariationRevisionStatus represents the state of a revision request.
type VariationRevisionStatus string

const (
	VariationRevisionStatusPending    VariationRevisionStatus = "pending"
	VariationRevisionStatusInProgress VariationRevisionStatus = "in_progress"
	VariationRevisionStatusCompleted  VariationRevisionStatus = "completed"
	VariationRevisionStatusFailed     VariationRevisionStatus = "failed"
)

// VariationRevision tracks a user's request to improve a variation.
type VariationRevision struct {
	ID           uuid.UUID               `json:"id"`
	VariationID  uuid.UUID               `json:"variation_id"`
	Feedback     string                  `json:"feedback"`
	Status       VariationRevisionStatus `json:"status"`
	ErrorMessage *string                 `json:"error_message,omitempty"`
	CreatedAt    time.Time               `json:"created_at"`
	StartedAt    *time.Time              `json:"started_at,omitempty"`
	CompletedAt  *time.Time              `json:"completed_at,omitempty"`
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

// BudgetAllocation is the spend ceiling a Hop is granted from a FundingSource.
type BudgetAllocation struct {
	ID              uuid.UUID `json:"id"`
	HopID           uuid.UUID `json:"hop_id"`
	FundingSourceID uuid.UUID `json:"funding_source_id"`
	LimitUSD        float64   `json:"limit_usd"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Estimator identifies who produced a HopCostEstimate.
type Estimator string

const (
	EstimatorProposer    Estimator = "proposer"    // The roadmap proposer's first guess
	EstimatorAuditor     Estimator = "auditor"     // The cost auditor's fact-checked revision
	EstimatorHuman       Estimator = "human"       // A person overrode it in review
	EstimatorCalibration Estimator = "calibration" // Derived purely from observed history
)

// HopCostEstimate is one dated estimate of what a Hop will cost, with the
// provenance needed to judge it. Estimates are append-only: keeping the whole
// history is what lets Mendel measure whether its own estimator is any good.
type HopCostEstimate struct {
	ID                 uuid.UUID `json:"id"`
	HopID              uuid.UUID `json:"hop_id"`
	AmountUSD          float64   `json:"amount_usd"`
	Estimator          Estimator `json:"estimator"`
	Confidence         *float64  `json:"confidence,omitempty"`
	Basis              *string   `json:"basis,omitempty"`
	CalibratedFromHops int       `json:"calibrated_from_hops"`
	CreatedAt          time.Time `json:"created_at"`
}

// CostKind distinguishes what a ledger entry paid for.
type CostKind string

const (
	CostKindModel   CostKind = "model"
	CostKindHosting CostKind = "hosting"
)

// TokenCounts holds usage exactly as the Messages API reports it.
//
// InputTokens is the uncached remainder only: the full prompt is
// InputTokens + CacheReadTokens + CacheWriteTokens. Counting only InputTokens
// on a cache-heavy agentic run undercounts the prompt badly.
type TokenCounts struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// Add accumulates another set of counts.
func (t *TokenCounts) Add(o TokenCounts) {
	t.InputTokens += o.InputTokens
	t.OutputTokens += o.OutputTokens
	t.CacheReadTokens += o.CacheReadTokens
	t.CacheWriteTokens += o.CacheWriteTokens
}

// PromptTokens is the true size of the prompt, cached portions included.
func (t TokenCounts) PromptTokens() int {
	return t.InputTokens + t.CacheReadTokens + t.CacheWriteTokens
}

// Total is every token billed, in either direction.
func (t TokenCounts) Total() int { return t.PromptTokens() + t.OutputTokens }

// IsZero reports whether nothing was used.
func (t TokenCounts) IsZero() bool { return t.Total() == 0 }

// CostEntry is one line of the actuals ledger. Each row carries both the raw
// telemetry the provider reported and the USD it converts to, plus the rate
// card used, so any figure in the UI traces back to counts x a dated price.
type CostEntry struct {
	ID        uuid.UUID  `json:"id"`
	ProjectID uuid.UUID  `json:"project_id"`

	StrategyID  *uuid.UUID `json:"strategy_id,omitempty"`
	HopID       *uuid.UUID `json:"hop_id,omitempty"`
	VariationID *uuid.UUID `json:"variation_id,omitempty"`

	Kind CostKind `json:"kind"`

	// Which part of Mendel spent this: "codegen", "proposer", "deploy", etc.
	Component string `json:"component"`

	Model  *string     `json:"model,omitempty"`
	Tokens TokenCounts `json:"tokens"`

	DeploymentID    *uuid.UUID `json:"deployment_id,omitempty"`
	MachineShape    *string    `json:"machine_shape,omitempty"`
	DurationSeconds *float64   `json:"duration_seconds,omitempty"`

	AmountUSD           float64    `json:"amount_usd"`
	ModelRateCardID     *uuid.UUID `json:"model_rate_card_id,omitempty"`
	HostingRateCardID   *uuid.UUID `json:"hosting_rate_card_id,omitempty"`
	ReconciledAmountUSD *float64   `json:"reconciled_amount_usd,omitempty"`

	OccurredAt time.Time `json:"occurred_at"`
}

// EffectiveUSD is the reconciled figure when a provider invoice has corrected
// the estimate, and the estimate otherwise.
func (c *CostEntry) EffectiveUSD() float64 {
	if c.ReconciledAmountUSD != nil {
		return *c.ReconciledAmountUSD
	}
	return c.AmountUSD
}

// ModelRateCard prices one model's tokens, effective from a given date.
// Multipliers apply to the input price.
type ModelRateCard struct {
	ID                   uuid.UUID `json:"id"`
	Model                string    `json:"model"`
	InputUSDPerMTok      float64   `json:"input_usd_per_mtok"`
	OutputUSDPerMTok     float64   `json:"output_usd_per_mtok"`
	CacheReadMultiplier  float64   `json:"cache_read_multiplier"`
	CacheWriteMultiplier float64   `json:"cache_write_multiplier"`
	BatchMultiplier      float64   `json:"batch_multiplier"`
	EffectiveFrom        time.Time `json:"effective_from"`
	Source               string    `json:"source"`
	CreatedAt            time.Time `json:"created_at"`
}

// HostingRateCard prices one machine shape on one platform, per hour.
// These are list-price approximations, never a claim about what was invoiced.
type HostingRateCard struct {
	ID            uuid.UUID `json:"id"`
	PlatformSlug  string    `json:"platform_slug"`
	MachineShape  string    `json:"machine_shape"`
	USDPerHour    float64   `json:"usd_per_hour"`
	BillsWhenIdle bool      `json:"bills_when_idle"`
	EffectiveFrom time.Time `json:"effective_from"`
	Source        string    `json:"source"`
	CreatedAt     time.Time `json:"created_at"`
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
	InputRequestKindHostingPlatform    InputRequestKind = "hosting_platform" // Select demo hosting platform
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

// HostingDeploymentStatus represents the lifecycle state of a hosting deployment.
type HostingDeploymentStatus string

const (
	HostingDeploymentStatusDeploying  HostingDeploymentStatus = "deploying"
	HostingDeploymentStatusRunning    HostingDeploymentStatus = "running"
	HostingDeploymentStatusFailed     HostingDeploymentStatus = "failed"
	HostingDeploymentStatusTerminated HostingDeploymentStatus = "terminated"
)

// HostingDeploymentKind distinguishes what a deployment is for.
type HostingDeploymentKind string

const (
	HostingDeploymentKindDemo HostingDeploymentKind = "demo"
	HostingDeploymentKindProd HostingDeploymentKind = "prod"
)

// HostingDeployment records a deployment made through a project's deployment
// channel. Demo deployments carry a VariationID; production deployments track
// the main branch and have none.
type HostingDeployment struct {
	ID        uuid.UUID             `json:"id"`
	ProjectID uuid.UUID             `json:"project_id"`
	ChannelID uuid.UUID             `json:"channel_id"`
	Kind      HostingDeploymentKind `json:"kind"`

	VariationID *uuid.UUID `json:"variation_id,omitempty"`

	CommitSHA            *string `json:"commit_sha,omitempty"`
	AppName              string  `json:"app_name"`
	URL                  *string `json:"url,omitempty"`
	TeardownInstructions *string `json:"teardown_instructions,omitempty"`

	Status       HostingDeploymentStatus `json:"status"`
	ErrorMessage *string                 `json:"error_message,omitempty"`

	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// InFlight reports whether this deployment is still running.
//
// Nil-safe and single-return so a template can ask without a status comparison
// of its own; a project that has never deployed is not deploying.
func (d *HostingDeployment) InFlight() bool {
	return d != nil && d.Status == HostingDeploymentStatusDeploying
}

// ShortCommit returns the abbreviated commit SHA, or "" if unknown.
func (d *HostingDeployment) ShortCommit() string {
	if d.CommitSHA == nil || len(*d.CommitSHA) < 8 {
		return ""
	}
	return (*d.CommitSHA)[:8]
}

// HostingDeploymentLog is a single log line emitted while deploying.
type HostingDeploymentLog struct {
	ID           uuid.UUID `json:"id"`
	DeploymentID uuid.UUID `json:"deployment_id"`
	LoggedAt     time.Time `json:"logged_at"`
	Level        LogLevel  `json:"level"`
	Message      string    `json:"message"`
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

// HostingPlatform defines a cloud platform available for demo deployments.
type HostingPlatform struct {
	ID            uuid.UUID `json:"id"`
	Slug          string    `json:"slug"`           // "fly-io", "cloud-run"
	Name          string    `json:"name"`           // "Fly.io", "Google Cloud Run"
	DeployerImage string    `json:"deployer_image"` // Docker image with /bin/sh
	Instructions  string    `json:"instructions"`   // Prose: what is needed, and where each value goes
	SetupScript   string    `json:"setup_script"`   // Commands to paste into a terminal, offered with a copy button
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DeployArtifactKind describes what a project produces for deployment.
type DeployArtifactKind string

const (
	DeployArtifactContainer    DeployArtifactKind = "container"     // Single Dockerfile
	DeployArtifactKubernetes   DeployArtifactKind = "kubernetes"    // k8s manifests
	DeployArtifactStatic       DeployArtifactKind = "static"        // Static files
	DeployArtifactSourceDeploy DeployArtifactKind = "source_deploy" // Platform builds from source
)

// SupportedDeploymentCombo represents a validated (artifact_kind, hosting_platform) pair.
type SupportedDeploymentCombo struct {
	ID                uuid.UUID          `json:"id"`
	ArtifactKind      DeployArtifactKind `json:"artifact_kind"`
	HostingPlatformID uuid.UUID          `json:"hosting_platform_id"`
	Notes             *string            `json:"notes,omitempty"`
	Guidance          json.RawMessage    `json:"guidance,omitempty"`
	CreatedAt         time.Time          `json:"created_at"`

	// Joined fields
	HostingPlatform *HostingPlatform `json:"hosting_platform,omitempty"`
}

// ProjectDeploymentChannel tracks a project's deployment configuration.
type ProjectDeploymentChannel struct {
	ID                uuid.UUID          `json:"id"`
	ProjectID         uuid.UUID          `json:"project_id"`
	ArtifactKind      DeployArtifactKind `json:"artifact_kind"`
	HostingPlatformID uuid.UUID          `json:"hosting_platform_id"`

	// Validation state
	DemoValidatedAt      *time.Time `json:"demo_validated_at,omitempty"`
	DemoValidatingAt     *time.Time `json:"demo_validating_at,omitempty"`
	DemoValidationError  *string    `json:"demo_validation_error,omitempty"`
	ProdValidatedAt      *time.Time `json:"prod_validated_at,omitempty"`
	ProdValidatingAt     *time.Time `json:"prod_validating_at,omitempty"`
	ProdValidationError  *string    `json:"prod_validation_error,omitempty"`

	// Production state lives in hosting_deployments (kind = "prod").

	// History: nil = current active channel
	DisabledAt *time.Time `json:"disabled_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Joined fields
	HostingPlatform *HostingPlatform `json:"hosting_platform,omitempty"`
}

// IsActive returns true if this channel is the current active one.
func (c *ProjectDeploymentChannel) IsActive() bool {
	return c.DisabledAt == nil
}

// IsDemoValidated returns true if demo deployment has been validated.
func (c *ProjectDeploymentChannel) IsDemoValidated() bool {
	return c.DemoValidatedAt != nil
}

// IsProdValidated returns true if production deployment has been validated.
func (c *ProjectDeploymentChannel) IsProdValidated() bool {
	return c.ProdValidatedAt != nil
}

// IsDemoValidating returns true if demo validation is in progress.
func (c *ProjectDeploymentChannel) IsDemoValidating() bool {
	return c.DemoValidatingAt != nil
}

// IsProdValidating returns true if prod validation is in progress.
func (c *ProjectDeploymentChannel) IsProdValidating() bool {
	return c.ProdValidatingAt != nil
}

// PausedForBudget reports whether this Variation stopped at its spend ceiling
// and is waiting on a decision, as opposed to being blocked on something else.
func (v *Variation) PausedForBudget() bool {
	return v != nil && v.BudgetPausedUSD != nil
}

// BudgetSpentUSD is what the paused run had cost, or zero if not paused.
// Single-return so html/template can call it; guard with PausedForBudget.
func (v *Variation) BudgetSpentUSD() float64 {
	if !v.PausedForBudget() {
		return 0
	}
	return *v.BudgetPausedUSD
}

// BudgetLimitUSD is the ceiling that was in force, or zero if not paused.
func (v *Variation) BudgetLimitUSD() float64 {
	if !v.PausedForBudget() || v.BudgetCeilingUSD == nil {
		return 0
	}
	return *v.BudgetCeilingUSD
}

// BaseModelID strips a dated snapshot suffix from a model identifier.
//
// The API reports the snapshot that actually served a request --
// "claude-haiku-4-5-20251001" -- while rate cards are keyed on the model line,
// "claude-haiku-4-5". Matching them literally means every dated response finds
// no card and is priced at zero, so the project's cost silently understates
// itself. That is exactly what happened to the OKR tuner: it asked for
// claude-haiku-4-5, the API answered as claude-haiku-4-5-20251001, and every
// one of its calls was billed at nothing.
//
// Adding a card per snapshot is the wrong fix: every future snapshot would need
// one, and the same model's spend would fragment across aliases.
//
// The rule is mirrored in SQL wherever a query has to do this join; the
// identical regexp is spelled out at each site and
// TestBaseModelIDMatchesSQL keeps the two from drifting.
func BaseModelID(model string) string {
	return datedSnapshotSuffix.ReplaceAllString(model, "")
}

// datedSnapshotSuffix matches a trailing -YYYYMMDD. Deliberately simple so the
// SQL form ('-[0-9]{8}$') can be identical rather than merely similar.
var datedSnapshotSuffix = regexp.MustCompile(`-[0-9]{8}$`)
