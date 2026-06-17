# MendelBuild v0.1 Implementation Plan

> **Goal**: Demonstrate the core evolutionary loop — from OKRs to proposed Roadmap to competing Variations to selected winner merged to main.

---

## Scope

**In scope:**
- Strategy/OKRs loaded from JSON file
- Agent proposes Roadmap (DAG of Hops) based on Strategy
- Human approves Roadmap via webapp
- Agent generates 3+ Variations per Hop (branches)
- Evaluation via tests + static analysis (no live traffic)
- Human selects winner via webapp
- Winner merged to main, others cleaned up
- Budget tracking (tokens + cloud $)
- Realtime dashboard showing progress + OKR status

**Out of scope for v0.1:**
- Live traffic routing / SDK
- Non-git repositories (Figma, GDrive)
- Non-GCP ecosystems
- Deploying Variations to k8s
- Complex OKR hierarchies (one Strategy, flat Objectives)
- Automated Decision resolution (all human for now)

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        MendelBuild Core                         │
│                          (Go binary)                            │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐  │
│  │   HTTP API  │  │   Webapp    │  │   Agent Orchestrator    │  │
│  │  (for CLI)  │  │  (htmx)     │  │  (spawns Claude CLI)    │  │
│  └──────┬──────┘  └──────┬──────┘  └───────────┬─────────────┘  │
│         │                │                     │                │
│         └────────────────┼─────────────────────┘                │
│                          │                                      │
│                   ┌──────▼──────┐                               │
│                   │  Postgres   │                               │
│                   │  (schema)   │                               │
│                   └─────────────┘                               │
└─────────────────────────────────────────────────────────────────┘
          │                                       │
          ▼                                       ▼
   ┌─────────────┐                        ┌─────────────┐
   │   GitHub    │                        │ Claude CLI  │
   │   (repos)   │                        │ (subprocess)│
   └─────────────┘                        └─────────────┘
```

**Tech choices:**
- **Language**: Go
- **Database**: Postgres
- **Webapp**: Go templates + htmx (minimal JS, server-rendered, no auth for v0.1)
- **Agent runtime**: Hybrid approach
  - *Roadmap Proposer*: Direct Anthropic API (structured JSON output, exact token tracking)
  - *Variation Generator*: Claude CLI subprocess (needs full coding capability)
- **Git**: GitHub API (or shell out to `git`/`gh`)

**Decision Queue as the single control plane:**
All human review happens via the Decision Queue — no separate approval flows. This includes:
- Approving/editing proposed Roadmap
- Selecting winning Variation
- Handling budget overruns
- Any other choice point

---

## Directory Structure

```
mendelbuild/
├── cmd/
│   └── mendel/
│       └── main.go           # Entry point (server + CLI commands)
├── internal/
│   ├── db/
│   │   ├── db.go             # Connection, migrations
│   │   └── queries.go        # SQL queries (or use sqlc)
│   ├── domain/
│   │   └── types.go          # Go structs matching schema
│   ├── agent/
│   │   ├── orchestrator.go   # Manages agent lifecycle
│   │   ├── proposer.go       # Roadmap proposal agent
│   │   └── generator.go      # Variation generation agent
│   ├── git/
│   │   └── github.go         # Branch creation, PR, merge
│   ├── eval/
│   │   └── evaluator.go      # Run tests, collect metrics
│   └── web/
│       ├── server.go         # HTTP server setup
│       ├── handlers.go       # Route handlers
│       └── templates/
│           ├── layout.html
│           ├── dashboard.html
│           ├── decisions.html
│           └── ...
├── schema/
│   └── 001_initial.sql
├── DESIGN.md
├── PLAN_v0.1.md              # This file
└── go.mod
```

---

## Phases

### Phase 1: Foundation

**Deliverable**: Can load Strategy from JSON, store in Postgres, view in webapp.

- [ ] Set up Go module, basic project structure
- [ ] Postgres connection + migration system:
  - `schema/full.sql` — complete current schema (for reference and fresh installs)
  - `schema/migrations/NNN_*.up.sql` / `NNN_*.down.sql` — incremental changes
  - Tooling to validate `full.sql` matches applied migrations
- [ ] Define Go types matching schema (`internal/domain/types.go`)
- [ ] CLI command: `mendel load-strategy <file.json>` — parses JSON, upserts entities:
  - Each Objective and KeyResult requires a user-specified stable `id` (string)
  - On reload: match by `id`, update existing or insert new
  - Warn if an `id` disappears (don't auto-delete, require explicit removal)
- [ ] Basic webapp skeleton (htmx)
- [ ] Webapp page: View Strategy/OKRs (read-only)

**JSON input format** (example):
```json
{
  "project": "my-saas",
  "strategy": {
    "name": "Q3 Growth",
    "objectives": [
      {
        "id": "retention",
        "description": "Improve user retention",
        "key_results": [
          {"id": "churn-rate", "description": "Reduce churn", "target_units": "< 5% monthly"},
          {"id": "dau", "description": "Increase DAU", "target_units": "1000 users"}
        ]
      }
    ],
    "funding": [
      {"resource_type": "dollars", "amount": 100},
      {"resource_type": "claude_tokens", "amount": 10000000}
    ]
  },
  "repository": {
    "url": "https://github.com/user/repo",
    "main_branch": "main",
    "config": {
      "test_command": "go test ./..."
    }
  }
}
```

---

### Phase 2: Roadmap Proposal

**Deliverable**: Agent proposes Hops, human approves/edits via Decision Queue.

- [ ] Anthropic API client (for Roadmap Proposer)
- [ ] Roadmap Proposer agent (direct API, not subprocess):
  - Input: Strategy context (Objectives, KRs, budget)
  - Output: Proposed Roadmap (JSON document with Hops, estimates, dependencies)
  - Roadmap stored as draft document, NOT as Hop rows yet
- [ ] Create Decision (kind='roadmap_review') — a conversational edit/approve cycle:
  - User can view proposed Roadmap
  - User can directly edit the JSON (add/remove/modify Hops)
  - User can send feedback to agent ("split this Hop", "add more detail", "this is too expensive")
  - Agent revises and resubmits
  - Cycle continues until user approves
- [ ] On approval: Hops created in DB with status='pending'
- [ ] Webapp page: Decisions list (all pending decisions)
- [ ] Webapp page: Decision detail for `roadmap_review`:
  - View/edit proposed Roadmap
  - Chat interface for feedback to agent
  - Approve button to finalize

**Agent prompt sketch** (Roadmap Proposer):
```
You are a technical product manager planning work for an AI coding agent.

Given the Strategy and budget below, propose a Roadmap of Hops that:
1. Advances the OKRs as much as possible WITHIN the budget
2. Prioritizes high-impact work if budget is limited
3. If the budget is clearly insufficient to make meaningful progress,
   say so and explain what WOULD be needed

Budget constraints are hard. Do not propose work that exceeds the budget.
Include a ~20% buffer for estimation error.

For each Hop, provide:
- name: short identifier
- kind: one of [code_feature, code_refactor, code_test, code_perf]
- commentary: what this Hop should accomplish, success criteria
- estimated_tokens: conservative estimate (we'll run 3+ Variations)
- depends_on: list of Hop names this depends on (or empty)

At the end, include:
- total_estimated_tokens: sum of all Hops
- budget_utilization: percentage of token budget used
- feasibility_notes: any concerns about achieving OKRs within budget

Strategy: ...
OKRs: ...
Budget: ... tokens, $...
Codebase summary: ... (if available)

Output as JSON.
```

---

### Phase 3: Variation Generation

**Deliverable**: For each active Hop, agent generates 3+ Variations as git branches.

- [ ] Claude CLI subprocess management (spawn, monitor, capture output)
- [ ] Variation Generator agent (Claude CLI subprocess — needs full coding capability):
  - Input: Hop context (kind, commentary), repo access
  - Output: Code changes committed to a new branch
- [ ] For each active Hop, spawn N agents (configurable, default 3)
- [ ] Each agent creates branch `mendel/<hop-id>/<variation-id>`
- [ ] Track Variation status: creating → pending
- [ ] Webapp: Dashboard shows Hops with their Variations and status
- [ ] Budget tracking: parse Claude CLI output for token usage, log to `budget_spend_log`

**Agent invocation sketch**:
```bash
claude --print "Given this Hop specification, implement it in the codebase.
Create your changes on branch mendel/<hop>/<var>. Run tests before finishing.

Hop: <name>
Kind: <kind>
Commentary: <commentary>
Repository: <path>
"
```

---

### Phase 4: Evaluation & Selection

**Deliverable**: All Variations pass tests; human picks winner.

Note: In v0.1, agents are expected to keep working until tests pass (no "failing" Variations). Pruning based on live traffic metrics comes later. For now, all Variations that complete are viable candidates.

- [ ] Variation Generator must pass tests before marking complete:
  - Agent runs `test_command` from repo config
  - If tests fail, agent continues iterating
  - Variation only reaches status='pending' once tests pass
- [ ] Collect metrics for comparison (not for pruning):
  - Test count, coverage %
  - Lint warnings/errors
  - Lines of code changed
- [ ] Create Decision (kind='choose_one') for winner selection
- [ ] Webapp: Decision detail — show Variations with:
  - Metrics summary
  - Code diff (vs main)
  - Link to branch
  - Human picks winner
- [ ] On selection: update Variation status to 'selected'

---

### Phase 5: Merge & Completion

**Deliverable**: Selected Variation merged to main, Hop marked complete.

- [ ] Git integration: merge selected branch to main (PR or direct)
- [ ] Clean up non-selected branches
- [ ] Update Hop status: 'active' → 'completed'
- [ ] Update non-selected Variations: 'pending' → 'terminated'
- [ ] Webapp: Dashboard reflects completion

---

### Phase 6: Dashboard & Polish

**Deliverable**: Realtime visibility into progress.

- [ ] Dashboard page showing:
  - Strategy/OKR summary
  - Roadmap DAG with Hop statuses
  - Active Hop detail: Variations, their statuses, scores
  - Budget: spent vs remaining (tokens + $)
- [ ] Auto-refresh or SSE for realtime updates
- [ ] Budget enforcement: pause agent work when budget exceeded, create Decision
- [ ] Error handling: surface agent failures in UI
- [ ] Multi-Hop: when Hop completes, start dependent Hops

---

## Open Items (to resolve during implementation)

| Item | Notes |
|------|-------|
| Claude CLI output parsing | Need to extract token usage for Variation Generator; check if `--output-format json` helps |
| Anthropic API setup | Need API key management; which model for Roadmap Proposer (Sonnet for cost, Opus for quality)? |
| Test runner | Specified in repository `config.test_command` (required field) |
| Concurrent Variation generation | Run 3 agents in parallel? Or sequential? (parallel preferred if stable) |
| Git auth | GitHub token in env var? SSH keys? |
| KR measurement | How do we populate `key_result_history`? Manual for v0.1? |

---

## Success Criteria for v0.1

1. Load a Strategy JSON → see OKRs in webapp
2. Agent proposes Roadmap → approve via webapp
3. Agent generates 3+ Variations per Hop → see branches in GitHub
4. Evaluator runs tests → see pass/fail in webapp
5. Human picks winner → merged to main
6. Budget tracked throughout → visible in dashboard
7. Entire flow completes for a simple, greenfield repo
