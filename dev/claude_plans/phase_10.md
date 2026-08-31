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

### Nothing slow happens inside a request

Both agent calls in this flow — the OKR draft and the roadmap proposal — run in
goroutines on their own `context.Background()`, because the request context dies
with the redirect. The browser is redirected immediately and the destination
page polls.

**This was not the original design, and the first version shipped broken.** The
draft ran inline in `POST /new`. Local testing passed: the Go server sets no
write timeout, so a 35-second request completed fine. Staging did not, because
the app sits behind a GCE Ingress whose backend service defaults to a **30
second** timeout. A user creating a project got a gateway error at 30s while the
draft completed perfectly well behind it — the project existed, fully drafted,
with nothing in the UI to say so.

Two lessons worth keeping:

- "The server has no timeout" is not the same as "the request has no timeout".
  Every proxy between the browser and the process has an opinion, and the
  tightest one wins. Checking the Go server's own settings answered the wrong
  question.
- Raising the ingress timeout would have hidden this rather than fixed it. A
  request held open for 35 seconds is a dead page with a spinner the browser
  draws, no progress, and nothing to come back to if it is closed. The work
  belongs in the background whatever the proxy allows.

The review screen therefore has three states — drafting, failed, ready — and
polls itself every 3 seconds while a draft is running.

### Two pieces of onboarding state are stored; the rest is derived

`strategies.okrs_approved_at` is persisted because it is not derivable: an
objective written by an agent and one a human has signed off on are identical
rows, and only the second should let a roadmap be built against it.

`strategies.draft_status` is persisted for the same reason. Once drafting moved
to the background, "no objectives yet" stopped being one situation and became
three — a draft is running, a draft failed, or none was ever started — which the
rows cannot tell apart. The review screen must show a spinner, an error with a
retry, or the draft, and guessing wrong means either an immortal spinner or an
approve button over an empty strategy.

Everything else the ribbon needs — has a strategy, has objectives, has an open
roadmap review, has Hops, has Variations, has a repository — is derived in one
query (`GetOnboardingState`) from rows that already exist.

### Postscript: the timestamp bug was schema-wide

The staleness bug below turned out not to be local to this feature. An audit
found **57 naive `timestamp without time zone` columns** against 26 already
correct — and a second live instance of the same bug: `sessions.expires_at` was
compared against `time.Now()` in `auth.go`, so sessions outlived their expiry by
the host's UTC offset. The sweeper (`DELETE ... WHERE expires_at < NOW()`) was
correct, because both sides of that comparison were in the database's frame; the
per-request check was not. The two paths quietly disagreed.

Migration 035 converts the schema. The conversion is **not** blanket, which a
probe settled before writing it:

| Written | Naive column | `TIMESTAMPTZ` |
| --- | --- | --- |
| `time.Now()` | −7h drift | round-trips exactly |
| a parsed calendar date | renders `2026-11-01` | renders `2026-10-31` |

So instants become `TIMESTAMPTZ` (56 columns) and `key_results.target_date`
becomes `DATE`, matching `funding_sources.period_start/end`, which were already
right. Making that column `TIMESTAMPTZ` would have introduced an off-by-one on
the very date the key result is about.

`internal/db/timestamp_semantics_test.go` guards all of it, including a check
that no naive timestamp exists anywhere. The guards were verified to fail
against the old types before being kept — a test that cannot fail is not a
guard. They also cannot fail on a UTC host, which is how the bug reached
production in the first place, so the rule in CLAUDE.md says to run them under a
non-UTC zone.

### Staleness is judged in SQL, never in Go

A draft whose process dies — a deploy mid-draft — leaves a row saying
`drafting` that nothing is working on. `draft_started_at` lets that be
recognised, so the screen offers a retry instead of polling forever.

That comparison happens in SQL, and the reason is the bug described above: with
naive timestamp columns, `time.Now().Sub(*s.DraftStartedAt)` was wrong by the
machine's UTC offset, so a draft thirty seconds old read as seven hours stale and
the page said "the draft did not finish" while it was running.

Migration 035 makes the Go form correct too, but the SQL form is kept. It uses
one clock instead of two, so it does not drift if the app and database hosts
disagree — a smaller version of the same failure.

`internal/db/onboarding_queries_test.go` covers this against real SQL, and the
draft tests pass under UTC, US Pacific, Tokyo and Sydney. A unit test over Go
structs cannot catch it, which is exactly why the logic does not live there.

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

### A schema-valid draft is not necessarily a usable one

Structured output guarantees the shape of the answer, not that there is an
answer in it. Two failures came from assuming otherwise, and both reached a
user before being caught.

**Empty.** `json.Unmarshal` ignores unknown fields, so a response that does not
match the wrapper parses without error and leaves the struct zeroed. The model
returned a schema-valid object with every field blank, and the flow "succeeded"
into a project with no objectives and no explanation.

**Half-finished.** Later, in a batch of six drafts, one came back with a single
objective, no strategy name, no summary, and JSON fragments leaking into a
target value (`">= 1 working end-to-end poll creation flow,},"`). It passed a
check that only counted objectives. A user would have got a review screen
titled "Initial strategy" with one vague objective on it and nothing saying
anything had gone wrong.

So `draftDefect` now rejects a draft missing anything the prompt asks for
unconditionally: no objectives, no strategy name, an objective with no
description, or an objective with no key results. Each is evidence the
generation went wrong rather than that the brief was thin.

An unusable draft is retried **once**, with an added instruction naming what was
missing, and the spend from both attempts is summed — both were paid for. Only
this failure is retried; a transport error or a malformed response is reported
as-is, since retrying those just doubles the wait before the same failure.

### The prompt was inviting the stub

The failures had a tell: `"assumptions": ["placeholder"]`, `"summary":
"placeholder"`. The model was not failing to answer, it was filling in a form.

The instruction it was given began **"Fill in every field."** That is
form-filling language, and it was added by an earlier fix for the empty-draft
problem — so the remedy for one failure was plausibly causing the next. Rewording
it to describe the work instead of the shape ("Write 2 to 4 objectives... Write
the real thing: this goes straight to them for approval, and it is the plan their
money gets spent against") is the only change between these two measurements:

| Prompt | Result |
| --- | --- |
| "Fill in every field" | 5/8 concurrent, 1/3 sequential — **6 of 11 usable** |
| "Write the real thing" | **8 of 8 usable**, one recovered by the retry |

Both batches ran eight-at-a-time against the same brief, minutes apart, so
server-side conditions were comparable. Eight is not a rate, and this failure has
already fooled a small sample once in this project's history — an earlier "3/3"
here meant nothing, and the very next attempt after it failed. Treat the table as
directional.

All three mechanisms are kept, because each covers what the others do not:

- **the prompt**, so the common case works
- **the gate** (`draftDefect`), so a bad generation cannot reach a user quietly
- **the retry**, so the usual remedy happens without the user having to ask —
  in the eight-draft run it silently rescued one draft
- and, behind those, the **failure screen**, so the residual case is a clear
  message and a working retry button rather than a broken page

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

## Schema changes

Migration 032:

```sql
ALTER TABLE projects   ADD COLUMN brief TEXT;              -- the user's own words
ALTER TABLE strategies ADD COLUMN okrs_approved_at TIMESTAMP;  -- draft vs. approved
ALTER TABLE strategies ADD COLUMN draft_notes JSONB;       -- the agent's own commentary
```

Migration 034, when drafting moved to the background:

```sql
ALTER TABLE strategies ADD COLUMN draft_status TEXT NOT NULL DEFAULT 'ready';
ALTER TABLE strategies ADD COLUMN draft_error TEXT;
ALTER TABLE strategies ADD COLUMN draft_started_at TIMESTAMP;
```

`draft_status` defaults to `'ready'`, so strategies that predate this — and any
loaded from a JSON file — are never mistaken for a draft in flight.

`draft_notes` holds what the drafting agent said about its draft: how it read the
brief, what it filled in, what it could not tell, and whether the scope looks
like it fits the budget. It is kept after approval rather than discarded — when a
hop later overruns, the assumptions the plan was built on are the first thing
worth re-reading.

## Flow

```
GET  /new                          form
POST /new                          create project + strategy (draft_status='drafting')
                                   → goroutine: Strategist.DraftStrategy
                                   → redirect to setup/okrs immediately (~10ms)
GET  /p/{id}/setup/okrs            drafting → spinner, polls every 3s
                                   failed   → error + "Try again"
                                   ready    → draft, notes, tuner feedback, editable
POST /p/{id}/setup/okrs/revise     claim the draft, then redraft in a goroutine
POST /p/{id}/setup/okrs/redraft    retry a failed draft from the original brief
POST /p/{id}/setup/okrs/approve    persist inline edits
                                   → okrs_approved_at
                                   → "Connect a repository" request if needed
                                   → goroutine: proposeRoadmapForStrategy
                                   → redirect to /inputs

background draft                   ReplaceDraftOKRs, RenameStrategy, draft notes
                                   → funding source + links to every KR
                                   → OKRTuner scores the draft
                                   → FinishStrategyDraft: 'ready' or 'failed'
```

`BeginStrategyDraft` is a conditional UPDATE, so a double-submitted retry cannot
start a second agent call rewriting the same objectives as the first.

Ribbon stages: **Describe the project → Approve objectives → Approve roadmap →
Explore approaches**, retiring at the first Variation.

## Verification

- `go build ./... && go vet ./...`
- `go test ./...` — all packages pass, with no `MENDEL_TEST_DB_URL` set
- `go test ./schema/...` — migrations 032 and 034 apply and match `full.sql`
- The draft tests pass under `TZ=UTC`, `America/Los_Angeles` and `Asia/Tokyo`,
  which is the point: the bug they guard only appears off UTC.
- Walked the whole flow live against a local server with a real API key:
  - `POST /new` returns in ~10ms instead of ~35s, which was the bug that
    started this
  - the review screen shows the spinner, polls, and flips to the draft when it
    lands (~25s later)
  - a draft aged past the stale window renders the failure state with an
    explanation and a retry, and the retry restarts it
  - the approve form is absent in both non-ready states, so nothing can be
    approved that has not been drafted
  - inline edits persist, `okrs_approved_at` is set, the repository request is
    filed, and the roadmap review arrives from the background job

### On sample sizes

An earlier version of this document claimed the empty-draft prompt fix worked
"3/3". That was true and meant very little: the failure is intermittent, and it
recurred on the next attempt after that. The gate and the retry exist because
the prompt alone is not a guarantee — and a batch of eight is still not proof
of a rate, only enough to catch the shapes worth handling.
