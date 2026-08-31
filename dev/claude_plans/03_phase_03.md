# Phase 3: Variation Generation

## Overview

For each active Hop, an AI proposes differentiated Variations, human reviews/approves via Decision, then Claude CLI subprocesses generate code as git branches.

**Two-phase flow:**
1. **Variation Proposal** (API-based agent) → proposes N approaches with budget estimates and differentiation rationale
2. **Code Generation** (CLI subprocess) → implements each approved Variation as a branch

---

## Key Design Decisions

1. **Variation approval before generation**: New Decision kind `variation_review` for human to review proposed Variations, ensure differentiation, adjust budget split, add ideas
2. **Git isolation**: Separate clones per Variation (not worktrees) for simplicity; Docker containers later when process coexistence needed
3. **Triggering**: Manual via webapp button or CLI command
4. **Concurrency**: N=2 parallel generators to start (prove concurrency, lower risk)
5. **Work directory**: `/tmp/mendel/<variation-id>/` for clones
6. **Package split**: `internal/agent/` for API-based chat agents, `internal/codegen/` for CLI-wrapper code generation

---

## New Files

```
internal/git/
└── git.go              # Git operations via os/exec

internal/agent/
└── variation_proposer.go  # Proposes differentiated Variations (API-based)

internal/codegen/
├── cli.go              # Claude CLI subprocess wrapper
├── generator.go        # Single Variation code generator
└── orchestrator.go     # Spawns/manages multiple generators
```

## Files to Modify

- `internal/domain/types.go` — add VariationStateHistory, BudgetAllocation, ProjectCredentials types
- `internal/domain/strategy_input.go` — add credentials section to input format
- `internal/db/queries.go` — add Variation CRUD, state history, budget spend, credential retrieval
- `internal/web/server.go` — add hop routes
- `internal/web/templates/strategy.html` — make hop names clickable
- `cmd/mendel/main.go` — add `generate` command

## New Migration

`004_project_credentials.up.sql`:
```sql
ALTER TABLE projects ADD COLUMN config JSONB;
-- Store anthropic_api_key and other project-wide credentials
```

---

## New Routes

```go
r.Route("/p/{projectID}", func(r chi.Router) {
    // Hop routes
    r.Get("/hops/{hopID}", s.handleHopDetail)
    r.Post("/hops/{hopID}/propose-variations", s.handleProposeVariations)  // Creates Decision

    // Variation review Decision routes (reuse pattern from roadmap_review)
    r.Get("/decisions/{decisionID}", s.handleDecisionDetail)  // existing, handles variation_review
    r.Post("/decisions/{decisionID}/approve", s.handleApprove)  // existing, triggers generation
})
```

---

## New Decision Kind: `variation_review`

Similar to `roadmap_review`, stores proposed Variations in `Decision.Details` as JSON:

```go
type ProposedVariation struct {
    Name           string  `json:"name"`           // e.g., "cache-layer-approach"
    Approach       string  `json:"approach"`       // Detailed implementation approach
    Differentiation string `json:"differentiation"` // Why this differs from others
    EstimatedTokens int    `json:"estimated_tokens"`
}

type VariationProposal struct {
    HopID      string              `json:"hop_id"`
    Variations []ProposedVariation `json:"variations"`
    TotalBudget int                `json:"total_budget"`
    Rationale   string             `json:"rationale"`  // Why these approaches
}
```

Human can:
- Edit proposed Variations (add/remove/modify approaches)
- Adjust budget split between Variations
- Add their own implementation ideas
- Approve to trigger code generation

---

## Database Queries to Add

```go
// Variation CRUD
func (db *DB) CreateVariation(ctx, v *domain.Variation) error
func (db *DB) GetVariation(ctx, id uuid.UUID) (*domain.Variation, error)
func (db *DB) UpdateVariation(ctx, v *domain.Variation) error
func (db *DB) GetVariationsByHop(ctx, hopID uuid.UUID) ([]domain.Variation, error)

// State history
func (db *DB) CreateVariationStateTransition(ctx, variationID uuid.UUID, from, to string, reason string) error

// Hop queries
func (db *DB) GetHop(ctx, id uuid.UUID) (*domain.Hop, error)
func (db *DB) UpdateHopStatus(ctx, hopID uuid.UUID, status domain.HopStatus) error

// Repository
func (db *DB) GetRepositoryByProject(ctx, projectID uuid.UUID) (*domain.Repository, error)

// Budget
func (db *DB) LogBudgetSpend(ctx, allocationID uuid.UUID, amount float64, description string) error
func (db *DB) GetBudgetAllocationsByHop(ctx, hopID uuid.UUID) ([]BudgetAllocation, error)
```

---

## Workflow

### Phase A: Variation Proposal (API-based)

1. User clicks "Propose Variations" on Hop detail page
2. Variation Proposer agent analyzes Hop context + budget
3. Agent proposes N differentiated approaches with rationale
4. Creates Decision (kind=`variation_review`) with proposal in Details
5. Human reviews, edits, approves via Decision UI

### Phase B: Code Generation (CLI subprocess)

On Decision approval:
1. Create Variation rows from approved proposal (status=`creating`)
2. For each Variation (N=2 concurrent):
   - Clone repository to `/tmp/mendel/<variation-id>/`
   - Create branch `mendel/<hop-name>/<variation-name>`
   - Invoke Claude CLI with approach-specific prompt
   - Parse output for token usage, log to `budget_spend_log`
   - Run test command from repository config
   - If tests pass: commit, push, status → `pending`
   - If tests fail: status → `terminated` with reason

---

## Implementation Steps

1. **Git client** (`internal/git/git.go`)
   - Clone, CreateBranch, Checkout, CommitAll, Push, GetCurrentCommit
   - Use auth_token from repository config (project-scoped)

2. **Variation Proposer** (`internal/agent/variation_proposer.go`)
   - API-based agent (like Roadmap Proposer)
   - Input: Hop context, budget, repository summary
   - Output: ProposedVariations with differentiation rationale

3. **CLI wrapper** (`internal/codegen/cli.go`)
   - Run Claude CLI subprocess with timeout
   - Parse JSON output for token usage

4. **Generator** (`internal/codegen/generator.go`)
   - Full workflow: clone → branch → claude → test → push
   - Record state transitions and budget spend

5. **Orchestrator** (`internal/codegen/orchestrator.go`)
   - Spawn N=2 generators concurrently
   - Aggregate results, handle failures

6. **DB queries** (`internal/db/queries.go`)
   - Variation CRUD, state history, budget logging

7. **Web handlers** (`internal/web/handlers_hop.go`)
   - Hop detail page with "Propose Variations" button
   - Variation review Decision UI (similar to roadmap_review)

8. **Templates**
   - `hop_detail.html` - Shows Variations with status
   - `decision_variation.html` - Edit/approve proposed Variations
   - Update `strategy.html` - link hop names to detail

---

## Project & Credential Management

**User provides existing repo + credentials** during project setup. Keep design general (not GitHub-specific).

**Updated strategy JSON input:**
```json
{
  "project": "my-saas",
  "repository": {
    "url": "https://github.com/user/repo",
    "main_branch": "main",
    "config": {
      "test_command": "go test ./...",
      "auth_token": "ghp_..."       // Git auth (works for GitHub, GitLab, etc.)
    }
  },
  "credentials": {
    "anthropic_api_key": "sk-ant-..."  // For AI agents
  },
  "strategy": { ... }
}
```

**Storage:**
- `repositories.config` JSONB: repo-specific config including auth
- New `project_credentials` table (or `projects.config` JSONB): AI API keys, other project-wide secrets

**Security note for v0.1:** Store credentials in plaintext. Future: encrypt at rest or use external secrets manager.

**Global env var (optional fallback):**
```bash
MENDEL_WORK_DIR=/tmp/mendel       # Working directory for clones
```

---

## Verification

1. Approve a roadmap to create Hops (existing flow)
2. Click into a Hop from strategy page
3. Click "Propose Variations" → creates Decision with proposed approaches
4. Review proposed Variations in Decision UI (edit approaches, adjust budget)
5. Click "Approve" → triggers code generation
6. Watch Variations progress: `creating` → `pending` (tests pass) or `terminated`
7. Check GitHub for branches `mendel/<hop-name>/<variation-name>`
8. Verify budget spend logged in database
