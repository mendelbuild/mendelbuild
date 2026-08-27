--------------------------------------------------------------------------------
-- COST MODEL
--------------------------------------------------------------------------------
-- Replaces the vestigial token-denominated budget model with a USD ledger.
--
-- Rationale: tokens are not a unit of value. A Haiku token and a Fable token
-- differ 10x in price, a cache read is 0.1x an input token and a cache write is
-- 1.25x-2x, and batch requests are half price. Budgeting in tokens means
-- budgeting in a unit whose worth floats, so a Hop can land "under budget" in
-- tokens and still cost triple what was planned.
--
-- USD is therefore the unit of account. Token counts are still recorded
-- explicitly, per model, exactly as the Messages API reports them -- they are
-- the evidence behind every dollar figure, and they are what makes an estimate
-- auditable after the fact.

--------------------------------------------------------------------------------
-- RATE CARDS
--------------------------------------------------------------------------------
-- Pricing lives in the database, seeded on startup and refreshable via CLI,
-- following the same rule as hosting_platforms: no hardcoded platform data in
-- Go. Rate cards are versioned by effective_from so historical ledger entries
-- keep the price that was actually in force when the spend happened.

CREATE TABLE model_rate_cards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    model TEXT NOT NULL,                        -- exact API model id, e.g. 'claude-opus-5'

    -- Base prices, USD per million tokens.
    input_usd_per_mtok REAL NOT NULL CHECK (input_usd_per_mtok >= 0),
    output_usd_per_mtok REAL NOT NULL CHECK (output_usd_per_mtok >= 0),

    -- Multipliers applied to the input price. Cache reads are billed at a
    -- fraction of input; cache writes carry a premium over it.
    cache_read_multiplier REAL NOT NULL DEFAULT 0.1 CHECK (cache_read_multiplier >= 0),
    cache_write_multiplier REAL NOT NULL DEFAULT 1.25 CHECK (cache_write_multiplier >= 0),

    -- Batch API discount, applied to the whole entry.
    batch_multiplier REAL NOT NULL DEFAULT 0.5 CHECK (batch_multiplier >= 0),

    effective_from TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Where this rate came from, so a human reviewing a cost figure can check it.
    source TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (model, effective_from)
);

CREATE INDEX idx_model_rate_cards_lookup ON model_rate_cards(model, effective_from DESC);

-- Hosting is priced from machine shape x wall-clock. These are list-price
-- approximations refreshed via CLI, never a claim about what was invoiced;
-- everything derived from them is labeled "estimated" in the UI.
CREATE TABLE hosting_rate_cards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    platform_slug TEXT NOT NULL,       -- matches hosting_platforms.slug
    machine_shape TEXT NOT NULL,       -- e.g. 'shared-cpu-1x-256mb'

    usd_per_hour REAL NOT NULL CHECK (usd_per_hour >= 0),

    -- Charged even while idle? Scale-to-zero platforms only bill on request.
    bills_when_idle BOOLEAN NOT NULL DEFAULT true,

    effective_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    source TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (platform_slug, machine_shape, effective_from)
);

CREATE INDEX idx_hosting_rate_cards_lookup
    ON hosting_rate_cards(platform_slug, machine_shape, effective_from DESC);

--------------------------------------------------------------------------------
-- FUNDING SOURCES -> USD BUDGETS WITH A TIME WINDOW
--------------------------------------------------------------------------------
-- Prototype stage: clean break, no compatibility shim. Existing rows carry a
-- resource_type that no longer has meaning, so they are dropped rather than
-- guessed at -- a token-denominated pool cannot be converted to USD without
-- knowing which model was assumed.

DELETE FROM funding_success_criteria;
DELETE FROM budget_allocations;
DELETE FROM funding_sources;

ALTER TABLE funding_sources
    DROP COLUMN resource_type,
    DROP COLUMN amount,
    ADD COLUMN name TEXT NOT NULL DEFAULT 'Budget',
    ADD COLUMN amount_usd REAL NOT NULL CHECK (amount_usd >= 0),

    -- The date half of "tie budgets to dates and OKR milestones". A budget that
    -- does not say when it must last is not a budget, it is a number.
    ADD COLUMN period_start DATE,
    ADD COLUMN period_end DATE,

    ADD CONSTRAINT funding_sources_period_ordered
        CHECK (period_start IS NULL OR period_end IS NULL OR period_start <= period_end);

ALTER TABLE funding_sources ALTER COLUMN name DROP DEFAULT;

-- The OKR half. This table existed since the original schema and was never
-- read or written; it is the intended budget -> Key Result link, and Key
-- Results already carry target_date, which supplies the milestone dates.
ALTER TABLE funding_success_criteria
    ADD CONSTRAINT funding_success_criteria_unique UNIQUE (funding_source_id, key_result_id);

--------------------------------------------------------------------------------
-- HOP COST ESTIMATES
--------------------------------------------------------------------------------
-- Append-only estimate history. Kept separate from budget_allocations because
-- an estimate ("what we think this costs") and an allocation ("what this Hop is
-- allowed to spend") are different claims by different authors. Keeping every
-- estimate, rather than overwriting, is what lets Mendel measure whether its
-- own estimator is any good.

CREATE TABLE hop_cost_estimates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hop_id UUID NOT NULL REFERENCES hops(id) ON DELETE CASCADE,

    amount_usd REAL NOT NULL CHECK (amount_usd >= 0),

    -- Who produced this estimate and how much to trust it.
    estimator TEXT NOT NULL CHECK (estimator IN ('proposer', 'auditor', 'human', 'calibration')),
    confidence REAL CHECK (confidence >= 0 AND confidence <= 1),

    -- Free-text justification, shown in review so a human can fact-check it.
    basis TEXT,

    -- Whether this estimate was grounded in observed history, and how much.
    calibrated_from_hops INTEGER NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_hop_cost_estimates_hop ON hop_cost_estimates(hop_id, created_at DESC);

-- Allocations become USD ceilings.
ALTER TABLE budget_allocations
    DROP COLUMN limit_amount,
    ADD COLUMN limit_usd REAL NOT NULL CHECK (limit_usd >= 0);

--------------------------------------------------------------------------------
-- COST LEDGER
--------------------------------------------------------------------------------
-- The actuals. Append-only; every row carries both the raw telemetry the
-- provider reported and the USD it converts to, plus the rate card used, so any
-- figure in the UI can be traced back to counts x a dated price.
--
-- Replaces budget_spend_log, which was defined, never written, and never read.

CREATE TABLE cost_entries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

    -- Attribution, narrowing as it gets more specific. Hop-level spend rolls up
    -- to the strategy; some spend (e.g. OKR tuning) has no Hop at all.
    strategy_id UUID REFERENCES strategies(id) ON DELETE CASCADE,
    hop_id UUID REFERENCES hops(id) ON DELETE CASCADE,
    variation_id UUID REFERENCES variations(id) ON DELETE CASCADE,

    kind TEXT NOT NULL CHECK (kind IN ('model', 'hosting')),

    -- Which part of Mendel spent this, e.g. 'codegen', 'proposer',
    -- 'variation_evaluator', 'okr_tuner', 'cost_auditor', 'deploy'.
    component TEXT NOT NULL,

    -- Model telemetry, recorded exactly as the Messages API reports it.
    -- input_tokens is the uncached remainder only: full prompt size is
    -- input + cache_read + cache_write.
    model TEXT,
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cache_read_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0),
    cache_write_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0),

    -- Hosting telemetry.
    deployment_id UUID REFERENCES hosting_deployments(id) ON DELETE SET NULL,
    machine_shape TEXT,
    duration_seconds REAL CHECK (duration_seconds >= 0),

    -- The money, and the receipt for it.
    amount_usd REAL NOT NULL CHECK (amount_usd >= 0),
    model_rate_card_id UUID REFERENCES model_rate_cards(id),
    hosting_rate_card_id UUID REFERENCES hosting_rate_cards(id),

    -- Set only if a real provider invoice later corrects an estimate.
    reconciled_amount_usd REAL CHECK (reconciled_amount_usd >= 0),

    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A model entry names its model; a hosting entry names its deployment.
    CONSTRAINT cost_entries_kind_shape CHECK (
        (kind = 'model' AND model IS NOT NULL) OR
        (kind = 'hosting' AND deployment_id IS NOT NULL)
    )
);

CREATE INDEX idx_cost_entries_project ON cost_entries(project_id, occurred_at DESC);
CREATE INDEX idx_cost_entries_strategy ON cost_entries(strategy_id, occurred_at DESC);
CREATE INDEX idx_cost_entries_hop ON cost_entries(hop_id);
CREATE INDEX idx_cost_entries_variation ON cost_entries(variation_id);

DROP TABLE budget_spend_log;

-- Token totals now come from cost_entries, which counts cache tokens and every
-- agent call, not just codegen. These denormalized columns undercounted both.
ALTER TABLE variations
    DROP COLUMN input_tokens,
    DROP COLUMN output_tokens;

--------------------------------------------------------------------------------
-- COST AUDIT ON ROADMAP REVIEWS
--------------------------------------------------------------------------------
-- The cost auditor's verdict on a proposed roadmap, stored so the review page
-- shows it beside the estimates it is checking. An estimate nothing ever
-- challenges is indistinguishable from a guess, and this is the challenge.

ALTER TABLE input_requests ADD COLUMN cost_audit JSONB;
