# Phase 10 — Guided Project Creation

## Overview

Creating a Mendel project meant writing a strategy JSON file by hand and running
`mendel load-strategy` against it. That asks a newcomer to know what an
Objective, a Key Result, a funding source, and a repository config are before
they have watched Mendel do anything — and the dashboard's empty state told them
to go do exactly that.

This phase replaced the front door with a three-screen flow that asks for the
three things a person actually knows on day one, drafts the rest, and shows the
draft back for correction:

```
/new                    describe the project (name, brief, deadline, budget)
/p/{id}/setup/okrs      validate the drafted objectives and key results
/p/{id}/inputs          the queue, with a roadmap review waiting in it
```

The lifecycle Ribbon — already used for Hops, Variations, and Decisions — carries
progress through all three, and retires once the first Variation exists.

`mendel load-strategy` is untouched. It is still the right tool for seeding an
example project from a file; it is no longer the only way in.

## Key design decisions

### The project row exists before the drafting agent runs

Tempting alternative: hold the draft in the browser and only write to the
database once the user approves, so a failed draft leaves nothing behind.

Rejected because of the cost rule. `cost.Recorder` files a charge against a
strategy; a drafting call that has no strategy to bill has nowhere to put its
spend, and an unrecorded agent call makes the project's cost understate itself
from its first minute. So `/new` creates the project and its strategy, then
drafts into them.

The failure path is handled instead of avoided: if drafting fails, the form
comes back with the user's own text still in it and a link to the setup screen
of the project that does now exist, rather than a silently orphaned row.

### Drafting is synchronous; roadmap proposal is not

The OKR draft runs inline in `POST /new`. There is nothing to show the user until
it finishes, it takes ~10-30s, and running it in the request means a failure
lands on their form rather than stranding them on a page polling for a draft that
will never arrive.

Roadmap proposal, after approval, is the opposite: it takes tens of seconds and
has nothing to say to the request that triggered it. It runs in a goroutine on
its own `context.Background()` — the request context dies with the redirect — and
the user lands on the Input Needed queue, which polls while the ribbon says
"Mendel is working".

### Only one piece of onboarding state is stored

`strategies.okrs_approved_at` is persisted because it is not derivable: an
objective written by an agent and one a human has signed off on are identical
rows, and only the second should let a roadmap be built against it.

Everything else the ribbon needs — has a strategy, has objectives, has an open
roadmap review, has Hops, has Variations, has a repository — is derived in one
query (`GetOnboardingState`) from rows that already exist. There is no
`onboarding_stage` column to drift out of sync with reality.

### The repository is asked for after approval, not before

Mendel cannot write code without somewhere to write it, but it does not need a
repository to draft objectives or a roadmap. Requiring a repo URL and a push
token on the first screen turns away anyone who has not created the repo yet.

So the ask is filed as a `manual_setup` input request when the OKRs are approved,
alongside the roadmap review, and resolved automatically when settings are saved
with a working repository. The ribbon's next-action text mentions it, so the
queue row is not the first the user hears of it.

### The budget is a funding source from the start

The user's dollar figure and target date become a `funding_sources` row during
the draft, spanning today to the deadline and linked to every drafted key result
via `funding_success_criteria`. That is the phase-08 model working as intended:
an amount, a period, and the Key Results the money is meant to buy.

It also means deadline and budget need no separate storage — a redraft
reconstructs the agent's brief from `projects.brief` plus that funding source.

### A silently empty draft is a bug, not an outcome

`json.Unmarshal` ignores unknown fields, so a response that does not match the
wrapper parses without error and leaves the struct zeroed. Observed in practice:
the model returned a schema-valid object with every field empty, and the flow
"succeeded" into a project with no objectives and no explanation.

`Strategist.send` now rejects a draft with no objectives outright, reporting the
stop reason and the raw content, and still returns the Spend so the call is
billed. It also accepts the bare strategy object as well as the wrapper, since
both shapes have been seen.

The prompt was the underlying cause: the original closing instruction produced an
empty object on every attempt (4/4). Naming the expected shape and stating that
an empty objectives list is not an answer fixed it (3/3 full drafts). Both
changes were kept — the prompt so it works, the guard so a regression is loud.

## New files

| File | What it does |
| --- | --- |
| `internal/domain/onboarding.go` | `OnboardingState` → `Ribbon` mapping for the setup flow |
| `internal/domain/onboarding_test.go` | Whose-move, stage advance, retirement, repo mention |
| `internal/agent/strategist.go` | Drafts and revises a strategy from a plain-English brief |
| `internal/db/onboarding_queries.go` | Project+strategy creation, draft replacement, state query |
| `internal/web/handlers_onboarding.go` | The three screens and what happens between them |
| `internal/web/templates/new_project.html` | Describe-your-project form |
| `internal/web/templates/setup_okrs.html` | Draft review and inline editing |
| `schema/migrations/032_project_onboarding.*.sql` | The three new columns |

## Modified files

- `internal/agent/types.go`, `schema.go` — `StrategyBrief`, `DraftedStrategy`,
  `StrategistResponse` and its schema function
- `internal/domain/types.go` — `Project.Brief`, `Strategy.OKRsApprovedAt`,
  `Strategy.DraftNotes` and `StrategyDraftNotes`
- `internal/db/queries.go` — the new columns in every project and strategy select
- `internal/web/handlers_input_request.go` — `handleProposeRoadmap` split into a
  handler and a reusable `proposeRoadmapForStrategy`, callable from a goroutine
- `internal/web/handlers_settings.go` — saving settings resolves the repo request
- `internal/web/handlers.go` — the setup ribbon on the strategy and inputs pages
- `internal/web/server.go` — `/new` and `/p/{id}/setup/okrs` routes
- `internal/web/templates/` — dashboard entry point, inputs queue ribbon and
  polling, strategy page ribbon, shared setup CSS in the layout

## Schema changes (032)

```sql
ALTER TABLE projects   ADD COLUMN brief TEXT;              -- the user's own words
ALTER TABLE strategies ADD COLUMN okrs_approved_at TIMESTAMP;  -- draft vs. approved
ALTER TABLE strategies ADD COLUMN draft_notes JSONB;       -- the agent's own commentary
```

`draft_notes` holds what the drafting agent said about its draft: how it read the
brief, what it filled in, what it could not tell, and whether the scope looks
like it fits the budget. It is kept after approval rather than discarded — when a
hop later overruns, the assumptions the plan was built on are the first thing
worth re-reading.

## Flow

```
GET  /new                          form
POST /new                          create project + strategy
                                   → Strategist.DraftStrategy
                                   → ReplaceDraftOKRs, RenameStrategy, draft notes
                                   → funding source + links to every KR
                                   → OKRTuner scores the draft
                                   → redirect to setup/okrs
GET  /p/{id}/setup/okrs            draft + notes + tuner feedback, inline editable
POST /p/{id}/setup/okrs/revise     Strategist.ReviseStrategy with the user's
                                   feedback, replacing the draft
POST /p/{id}/setup/okrs/approve    persist inline edits
                                   → okrs_approved_at
                                   → "Connect a repository" request if needed
                                   → goroutine: proposeRoadmapForStrategy
                                   → redirect to /inputs
```

Ribbon stages: **Describe the project → Approve objectives → Approve roadmap →
Explore approaches**, retiring at the first Variation.

## Verification

- `go build ./... && go vet ./...`
- `MENDEL_TEST_DB_URL=... go test ./...` — all packages pass
- `go test ./schema/...` — migration 032 applies and matches `full.sql`
- Walked live against a local server with a real API key: created a project from
  a brief, got three objectives with nine dated key results and tuner scores,
  edited an objective and a target inline, approved, and confirmed the edits
  persisted, `okrs_approved_at` was set, the repository request was filed, and
  the roadmap review arrived in the queue from the background job.
