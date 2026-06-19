# Phase 2: Roadmap Proposal Implementation Plan

## Overview

Implement the Roadmap Proposer workflow: an AI agent proposes a roadmap of Hops based on Strategy context, human reviews/edits via a conversational Decision, and on approval the Hops are persisted to the database.

---

## Key Design Decisions

### 1. Proposed roadmap storage
Store in `Decision.Details` as structured JSON. No schema change needed—the roadmap proposal is logically the "details" of a `roadmap_review` decision.

### 2. Chat history for edit cycle
New `decision_messages` table. Keeps conversation separate from the roadmap state.

### 3. Triggering the proposer
CLI command `mendel propose-roadmap -strategy <uuid>` creates the Decision. Webapp provides "Regenerate" button for revisions.

### 4. API key management
Environment variable `ANTHROPIC_API_KEY`.

### 5. Structured LLM API calls
All LLM API calls use structured JSON for both input and output (after system prompt). No free-form text exchanges. This applies to:
- Initial roadmap proposal
- Roadmap revisions (user feedback + current roadmap → revised roadmap)

Add this guidance to `CLAUDE.md` at repo root for consistency across all agents.

---

## New Files

```
internal/agent/
├── client.go      # Anthropic API client
├── proposer.go    # Roadmap Proposer logic
└── types.go       # ProposedRoadmap, ProposedHop structs

internal/web/
├── handlers_decision.go           # Decision handlers
└── templates/
    └── decision_roadmap.html      # roadmap_review detail page

schema/migrations/
├── 002_decision_messages.up.sql
└── 002_decision_messages.down.sql
```

## Files to Modify

- `internal/domain/types.go` — add DecisionMessage type
- `internal/db/queries.go` — add Decision and DecisionMessage queries
- `internal/web/server.go` — add decision routes
- `internal/web/templates/decisions.html` — show actual decisions
- `cmd/mendel/main.go` — add propose-roadmap command
- `CLAUDE.md` (create) — document structured API convention for all agents

---

## Database Migration

```sql
-- 002_decision_messages.up.sql
CREATE TABLE decision_messages (
    id UUID PRIMARY KEY,
    decision_id UUID NOT NULL REFERENCES decisions(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user', 'agent', 'system')),
    content TEXT NOT NULL,
    tokens_used INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_decision_messages_decision ON decision_messages(decision_id, created_at);
```

---

## New Types

```go
// internal/agent/types.go

// ResourceEstimate is an estimated cost for a single resource type.
type ResourceEstimate struct {
    ResourceType string  `json:"resource_type"` // "dollars", "claude_tokens", etc.
    Amount       float64 `json:"amount"`
}

type ProposedHop struct {
    Name           string             `json:"name"`
    Kind           string             `json:"kind"`
    Commentary     string             `json:"commentary"`
    ObjectiveIDs   []string           `json:"objective_ids"`   // Which objectives this advances
    EstimatedCosts []ResourceEstimate `json:"estimated_costs"` // Per-resource estimates
    DependsOn      []string           `json:"depends_on"`
}

type ProposedRoadmap struct {
    Hops             []ProposedHop `json:"hops"`
    FeasibilityNotes string        `json:"feasibility_notes"`
}

// TotalEstimatedCosts returns aggregated costs across all hops, per resource type.
func (r *ProposedRoadmap) TotalEstimatedCosts() []ResourceEstimate { ... }

// BudgetUtilization computes utilization per resource type given the strategy's funding_sources.
func (r *ProposedRoadmap) BudgetUtilization(funding []domain.FundingSource) map[string]float64 { ... }

// RevisionRequest is the structured input for roadmap revision (JSON in, JSON out).
type RevisionRequest struct {
    CurrentRoadmap ProposedRoadmap `json:"current_roadmap"`
    Feedback       string          `json:"feedback"`       // User's change request
    Strategy       StrategyContext `json:"strategy"`       // Full context for reference
}

// StrategyContext is the strategy info passed to the proposer.
type StrategyContext struct {
    Name       string            `json:"name"`
    Objectives []ObjectiveInfo   `json:"objectives"`
    Funding    []ResourceEstimate `json:"funding"`
}

// internal/domain/types.go
type DecisionMessage struct {
    ID         uuid.UUID
    DecisionID uuid.UUID
    Role       string    // "user", "agent", "system"
    Content    string
    TokensUsed *int
    CreatedAt  time.Time
}
```

---

## Future: Incremental Roadmap Updates (TODO for later)

For v0.1, we assume a fresh roadmap proposal with no existing Hops. Future work:
- **Adding to existing roadmap**: Proposer sees current Hops and proposes additions
- **Revising individual Hops**: Edit a single Hop without regenerating entire roadmap
- **Triggering roadmap revision**: Could be manual, or triggered by OKR changes, budget changes, or Hop completion

---

## Routes to Add

```go
r.Route("/p/{projectID}", func(r chi.Router) {
    r.Get("/decisions", s.handleDecisions)
    r.Get("/decisions/{decisionID}", s.handleDecisionDetail)
    r.Post("/decisions/{decisionID}/message", s.handleSendMessage)
    r.Post("/decisions/{decisionID}/regenerate", s.handleRegenerate)
    r.Put("/decisions/{decisionID}/roadmap", s.handleUpdateRoadmap)  // Direct JSON edit
    r.Post("/decisions/{decisionID}/approve", s.handleApprove)
})
```

**UI for roadmap_review**: Two-column layout
- **Left**: Roadmap JSON in a textarea (directly editable) + Save button
- **Right**: Chat history + feedback input

User can either:
1. Edit JSON directly and click Save
2. Send chat feedback to have agent revise

---

## Implementation Steps

1. **Migration** — Create `002_decision_messages` migration, add DecisionMessage type
2. **Anthropic client** — Create `internal/agent/client.go` with Message API wrapper
3. **Proposer agent** — Create `internal/agent/proposer.go` with ProposeRoadmap/ReviseRoadmap
4. **Decision queries** — Add CRUD for decisions and messages in queries.go
5. **CLI command** — Add `propose-roadmap` to main.go
6. **Decisions list** — Update handleDecisions to fetch real data
7. **Roadmap review page** — Create decision_roadmap.html with editable textarea + chat
8. **HTMX interactions** — Wire up Save (direct edit), message sending, regenerate, approve
9. **Approval flow** — CreateHopsFromRoadmap with dependency validation

---

## Approval Flow

When user clicks "Approve":
1. Parse `Decision.Details` → `ProposedRoadmap`
2. Validate (no cycles, valid hop kinds, objective IDs exist)
3. In transaction:
   - Create Hop rows with status='pending'
   - Store objective_ids in Hop.kind_params JSON (e.g., `{"objective_ids": ["retention", "growth"]}`)
   - Create HopDependency rows
   - Create hop_budget_limits rows from EstimatedCosts
   - Set Decision status='resolved', resolution='approved'
4. Redirect to strategy page showing new hops

---

## Verification

1. Set `ANTHROPIC_API_KEY` env var
2. Run `mendel propose-roadmap -strategy <uuid>`
3. Visit `/p/{projectID}/decisions` — see the new decision
4. Click into decision — see proposed roadmap JSON (editable textarea) and chat
5. Edit JSON directly, click Save — verify changes persist
6. Send feedback message — agent revises roadmap
7. Click Approve — verify Hops created in DB
8. View strategy page — see new Hops listed
