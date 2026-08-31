# Phase 10 — Cost Model: Credible, Verifiable Budgets

## Overview

Mendel had a budget model on paper and almost nothing behind it. This phase
replaced it with a USD ledger that records what was actually spent, prices it
against dated rate cards kept in the database, and feeds observed history back
into the estimates so they can be checked rather than trusted.

The end goal this serves: someone starts a project in Mendel, gets a cost
estimate for a *strategy*, and that estimate turns out to be roughly right — and
can be audited when it isn't.

## What was actually there before

Worth recording, because most of it looked functional and wasn't:

- `budget_spend_log` and `LogBudgetSpend` existed and were **never called**.
  There was no actuals ledger at all.
- `funding_success_criteria` — the budget-to-Key-Result link — had existed since
  migration 001 and was **never read or written**.
- `budget_allocations` was written once from the proposer's guess and then never
  compared to anything.
- Token counting dropped half its data. The executor measured input, output,
  cache-read and cache-write ([executor.go](../../internal/codegen/executor/executor.go)),
  but persistence took only input and output. Since the API reports
  `input_tokens` as the *uncached remainder*, and a long agentic run is mostly
  cache reads, Mendel was counting a fraction of the tokens it bought.
- Pricing was hardcoded in two places with different formulas, both Sonnet-only,
  one omitting cache writes entirely — in violation of the project's own rule
  against hardcoding platform data.
- Every agent call outside codegen (proposer, evaluator, OKR tuner, criteria
  generator, conflict auditor) returned usage that was discarded.
- Nothing connected a budget to a date or to an OKR milestone.

Measured effect of the token loss: a realistic cache-heavy generation run
(20k input, 40k output, 4M cache read, 200k cache write on Sonnet 4.6) costs
**$2.61**. The old accounting reported **$0.66** — a 4x undercount.

## Key design decisions

### USD is the unit of account; tokens are evidence

`claude_tokens` is gone as a resource type. Tokens are not a unit of value:
prices differ ~10x across models, a cache read is 0.1x an input token, a cache
write 1.25x, and batch is half price. A hop could come in "under its token
budget" having cost triple what was planned.

Token counts are still recorded in full, per model, exactly as the API reports
them — they are what makes a dollar figure auditable. Every ledger row carries
both the counts and the rate card that priced them, so any number in the UI
traces back to `counts x a dated price`.

### Rate cards live in the database

`model_rate_cards` and `hosting_rate_cards`, seeded on startup and refreshed via
`mendel rates refresh`. Versioned by `effective_from`: a refresh writes new cards
and leaves old ones alone, so figures already in the ledger keep the price that
produced them and stay verifiable. Each card records a `source` string so a
human reviewing a cost can check it against a citable list price.

### Estimates are append-only, with provenance

`hop_cost_estimates` keeps every estimate rather than overwriting, with its
author (`proposer` / `auditor` / `human` / `calibration`), confidence, and stated
basis. This is what lets Mendel score its own estimator against actuals — an
estimate that is overwritten can never be graded.

Estimates are kept separate from `budget_allocations`: an estimate is a
prediction, a ceiling is a governance decision a human can change without
rewriting the prediction history.

### Credibility comes from two mechanisms, not one

1. **Calibration** ([calibration.go](../../internal/cost/calibration.go)):
   completed hops' estimate-vs-actual is summarised into medians and an
   `estimate_bias_ratio`, and passed into the proposer's context. Medians rather
   than means, so one runaway hop doesn't redefine normal. Hops with zero
   recorded spend are excluded from the medians — they predate the ledger and
   would drag every future estimate down.

2. **A cost auditor** ([cost_auditor.go](../../internal/agent/cost_auditor.go)):
   a separate call with its own adversarial prompt that fact-checks the
   proposer's figures against the same calibration data. Deliberately not the
   proposer grading itself, which produces agreement rather than scrutiny. Its
   verdict is stored on the input request and shown in the roadmap review beside
   the estimates it is challenging.

Both are instructed to say "we cannot know this yet" when there is no history,
and to keep confidence low — a fabricated figure someone then plans against is
worse than an admitted unknown.

### Budgets tie to dates and to OKR milestones

- **Dates**: `funding_sources` gained `period_start` / `period_end`. The strategy
  page compares money burned against time elapsed, which is the comparison that
  makes a spend figure mean something — 60% of a budget is fine two-thirds
  through a quarter and alarming a tenth of the way in.
- **OKRs**: `funding_success_criteria` was finally wired up. Key Results carry
  `target_date`, so joining through it gives every budget a set of dated
  milestones. Strategy input files declare this with `key_result_ids`.

### Hosting is estimated, and labelled as estimated

Priced from machine shape x wall-clock against list-price rate cards, metered on
a 10-minute cadence so a still-running deployment's cost is visible while it
runs rather than only after teardown. Never presented as a provider invoice.
Scale-to-zero platforms (Cloud Run) are seeded with `bills_when_idle = false`
and report zero: Mendel can see how long a deployment existed but not how long
it served traffic, so wall-clock there would be confidently wrong. The ledger
carries a `reconciled_amount_usd` column so a future provider-billing importer
can correct entries without reshaping anything.

Mendel does not yet learn what machine a deploy script provisioned, so
deployments are metered against a per-platform `default` shape. That assumption
is recorded on each ledger row rather than buried in a total.

## New files

| File | Purpose |
|---|---|
| `internal/cost/pricing.go` | Turns token counts and durations into USD |
| `internal/cost/rates.go` | Default rate cards, seeding, refresh |
| `internal/cost/recorder.go` | The single place spend is written to the ledger |
| `internal/cost/calibration.go` | Summarises observed history for the agents |
| `internal/cost/context.go` | Shared strategy context builder (was duplicated 3x) |
| `internal/cost/hosting.go` | Periodic hosting spend settlement |
| `internal/agent/cost_auditor.go` | Adversarial fact-check of roadmap estimates |
| `internal/db/cost_queries.go` | Rate cards, ledger, estimates, calibration, OKR links |
| `internal/web/cost.go` | Recording helpers and the cost view models |
| `schema/migrations/030_cost_model.{up,down}.sql` | The schema change |

## Schema changes (migration 030)

**New tables**: `model_rate_cards`, `hosting_rate_cards`, `hop_cost_estimates`,
`cost_entries`.

**Changed**:
- `funding_sources`: dropped `resource_type` / `amount`; added `name`,
  `amount_usd`, `period_start`, `period_end`.
- `budget_allocations`: `limit_amount` → `limit_usd`.
- `funding_success_criteria`: added a uniqueness constraint (it is now written).
- `input_requests`: added `cost_audit JSONB`.

**Dropped**: `budget_spend_log` (never written, never read);
`variations.input_tokens` / `output_tokens` (undercounted; the ledger supersedes
them and also counts cache tokens and non-codegen agent calls).

Per the prototype-stage rule, existing funding rows are deleted rather than
migrated: a token-denominated pool cannot be converted to USD without knowing
which model was assumed.

## Incidental cleanup

The same anonymous variation-proposal struct had been hand-copied **seven
times** across the web and codegen layers. All now use `agent.VariationProposal`
or a JSON-compatible shape. The strategy-context builder had three copies, none
of which passed calibration data; there is now one.

## Verification

```bash
MENDEL_TEST_DB_URL="postgres://bhs:@localhost:5432/mendel_test?sslmode=disable" go test ./...
```

- `internal/cost/pricing_test.go` — pricing math per token kind, the cache-token
  regression, batch discount, missing rate card, scale-to-zero hosting.
- `internal/web/cost_test.go` — pace/variance/remaining logic and template
  rendering for the strategy budget card, hop cost card, and roadmap cost
  review, each in both the populated and absent states.

Smoke-tested end to end against a scratch database: migrate → setup → seed 18
rate cards → `load-strategy` (funding and 4 KR links created) → record ledger
entries → summaries and component breakdown. Hand-checked arithmetic matched.

## Known gaps

- Machine shape is assumed, not observed (see above).
- No provider-billing reconciliation yet; `reconciled_amount_usd` is the hook.
- Nothing sets `hosting_deployments.status = 'terminated'` yet, so a torn-down
  deployment keeps metering until that path exists.
- Budget ceilings are recorded and displayed but not enforced — exceeding one
  does not yet pause a Hop or raise a Decision, which is what
  DESIGN.md section 2.5 describes.
