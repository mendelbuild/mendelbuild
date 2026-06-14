-- MendelBuild Core Schema
-- Migration: 001_initial
--
-- This schema represents the core data model for MendelBuild.
-- See DESIGN.md Section 2 for conceptual overview.
--
-- Note on TEXT vs VARCHAR: In Postgres, TEXT and VARCHAR are functionally
-- equivalent in terms of performance. TEXT is preferred here for simplicity
-- since we rarely need length constraints.

--------------------------------------------------------------------------------
-- PROJECTS
--------------------------------------------------------------------------------
-- A Project is the top-level container. It groups together a Strategy,
-- one or more Repositories, and connections to Ecosystems.

CREATE TABLE projects (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- STRATEGIES
--------------------------------------------------------------------------------
-- A Strategy captures funding sources and owns the Roadmap (DAG of Hops).
-- Strategies can nest (sub-strategies) via parent_id for organizational alignment.
-- OKRs are modeled via the objectives and key_results tables below.

CREATE TABLE strategies (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id),
    parent_id UUID REFERENCES strategies(id),  -- NULL for top-level strategy

    name TEXT NOT NULL,

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- OBJECTIVES
--------------------------------------------------------------------------------
-- An Objective is the "O" in OKR. A Strategy can have multiple Objectives,
-- and each Objective can have multiple Key Results.

CREATE TABLE objectives (
    id UUID PRIMARY KEY,
    strategy_id UUID NOT NULL REFERENCES strategies(id),

    description TEXT NOT NULL,  -- Plain-English objective

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- KEY RESULTS
--------------------------------------------------------------------------------
-- Key Results are quantitative targets attached to an Objective.
-- Each KR has a target value expressed with units that MendelBuild parses.

CREATE TABLE key_results (
    id UUID PRIMARY KEY,
    objective_id UUID NOT NULL REFERENCES objectives(id),

    description TEXT NOT NULL,

    -- Target expressed with units, e.g., "1000 users", "99.9%", "< 200ms p99"
    -- MendelBuild Core parses this to extract:
    --   - numeric target
    --   - unit type (count, percentage, duration, currency, etc.)
    --   - comparison operator (=, <, >, >=, <=)
    --   - measurement horizon if applicable (e.g., "per week", "7-day rolling")
    target_units TEXT NOT NULL,

    target_date TIMESTAMP,  -- When we expect to hit target

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- KEY RESULT HISTORY
--------------------------------------------------------------------------------
-- Timeseries of actual KR measurements. Data volumes should be low enough
-- that storing everything in SQL keeps things simple and queryable.

CREATE TABLE key_result_history (
    id UUID PRIMARY KEY,
    key_result_id UUID NOT NULL REFERENCES key_results(id),

    measured_value REAL NOT NULL,
    measured_at TIMESTAMP NOT NULL,

    -- Optional: source of measurement (for debugging/auditing)
    source TEXT
);

CREATE INDEX idx_kr_history_kr_id ON key_result_history(key_result_id, measured_at);

--------------------------------------------------------------------------------
-- FUNDING SOURCES
--------------------------------------------------------------------------------
-- A FundingSource is a pool of resources allocated to a Strategy.
-- Resource types are constrained to a known set.

-- Allowed resource types (enforced via CHECK constraint):
--   'dollars'       - USD budget
--   'claude_tokens' - Anthropic Claude API tokens (note: different models have different token costs)

CREATE TABLE funding_sources (
    id UUID PRIMARY KEY,
    strategy_id UUID NOT NULL REFERENCES strategies(id),

    resource_type TEXT NOT NULL CHECK (resource_type IN ('dollars', 'claude_tokens')),
    amount REAL NOT NULL,  -- Total available in this pool

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- FUNDING SUCCESS CRITERIA
--------------------------------------------------------------------------------
-- Links FundingSources to KeyResults: "we're spending this budget to achieve these KRs"

CREATE TABLE funding_success_criteria (
    id UUID PRIMARY KEY,
    funding_source_id UUID NOT NULL REFERENCES funding_sources(id),
    key_result_id UUID NOT NULL REFERENCES key_results(id),

    -- Optional weight if some KRs matter more than others for this funding
    weight REAL DEFAULT 1.0,

    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- HOPS
--------------------------------------------------------------------------------
-- A Hop is the fundamental unit of evolutionary experimentation.
-- It defines WHAT we want (via commentary + kind-specific params) but not HOW.
-- Each Hop can spawn multiple Variations that compete.
--
-- Hops form a DAG via hop_dependencies. They attach directly to Strategies
-- (no separate "roadmaps" table needed).
--
-- Hop lifecycle states (see DESIGN.md Section 5 for Variation states):
--   'pending'   - Not yet started (blocked on dependencies or not scheduled)
--   'active'    - Currently running, Variations being generated/evaluated
--   'completed' - A Variation was selected and merged
--   'abandoned' - Hop was cancelled without selecting a winner

CREATE TABLE hops (
    id UUID PRIMARY KEY,
    strategy_id UUID NOT NULL REFERENCES strategies(id),

    name TEXT NOT NULL,

    -- Context about the Hop; helps with qualitative pruning/scoring
    commentary TEXT,

    -- Hop "kind" determines which Pruner/Scorer implementation is used.
    -- Implementations are hardcoded in Core; params customize behavior.
    -- Example kinds: 'code_quality', 'performance', 'user_engagement', 'cost_reduction'
    kind TEXT NOT NULL,

    -- JSON blob with parameters for this kind's Pruner and Scorer.
    -- Schema depends on kind. Example for 'code_quality':
    --   {"min_test_coverage": 0.8, "max_lint_errors": 0, "weight_performance": 0.3}
    kind_params JSONB,

    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'completed', 'abandoned')),

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- DAG edges: which Hops must complete before this one can start?
CREATE TABLE hop_dependencies (
    hop_id UUID NOT NULL REFERENCES hops(id),
    depends_on_hop_id UUID NOT NULL REFERENCES hops(id),
    PRIMARY KEY (hop_id, depends_on_hop_id),
    CHECK (hop_id != depends_on_hop_id)  -- No self-loops
);

--------------------------------------------------------------------------------
-- BUDGET ALLOCATIONS
--------------------------------------------------------------------------------
-- A BudgetAllocation is a slice of a FundingSource assigned to a specific Hop.

CREATE TABLE budget_allocations (
    id UUID PRIMARY KEY,
    hop_id UUID NOT NULL REFERENCES hops(id),
    funding_source_id UUID NOT NULL REFERENCES funding_sources(id),

    limit_amount REAL NOT NULL,  -- Ceiling for this Hop (in units of the funding source)

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Spend log: each entry records consumption against an allocation.
-- The resource type is inherited from the funding_source via budget_allocation.
CREATE TABLE budget_spend_log (
    id UUID PRIMARY KEY,
    budget_allocation_id UUID NOT NULL REFERENCES budget_allocations(id),

    amount REAL NOT NULL,
    recorded_at TIMESTAMP NOT NULL DEFAULT NOW(),

    -- Optional: what caused this spend (variation ID, agent run, etc.)
    description TEXT
);

CREATE INDEX idx_spend_log_allocation ON budget_spend_log(budget_allocation_id, recorded_at);

--------------------------------------------------------------------------------
-- VARIATIONS
--------------------------------------------------------------------------------
-- A Variation is a concrete implementation attempt within a Hop.
-- Variations compete; at most one is "selected" and merged to main.
--
-- Lifecycle states (see DESIGN.md Section 5):
--   'creating', 'pending', 'migrating', 'active',
--   'draining', 'terminated', 'pruned', 'selected'

CREATE TABLE variations (
    id UUID PRIMARY KEY,
    hop_id UUID NOT NULL REFERENCES hops(id),

    -- Repository location
    repository_id UUID,  -- FK added below after repositories table
    commit_ref TEXT,     -- Opaque reference; for git repos this is a SHA

    -- Ecosystem deployment (nullable if not yet deployed)
    ecosystem_id UUID,   -- FK added below after ecosystems table
    deployment_ref TEXT, -- e.g., pod name, URL, etc.

    status TEXT NOT NULL DEFAULT 'creating',

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Variation lifecycle history: timestamped state transitions
CREATE TABLE variation_state_history (
    id UUID PRIMARY KEY,
    variation_id UUID NOT NULL REFERENCES variations(id),

    from_status TEXT,
    to_status TEXT NOT NULL,
    transitioned_at TIMESTAMP NOT NULL DEFAULT NOW(),

    -- Optional context for the transition
    reason TEXT
);

CREATE INDEX idx_variation_history ON variation_state_history(variation_id, transitioned_at);

--------------------------------------------------------------------------------
-- VARIATION MIGRATIONS
--------------------------------------------------------------------------------
-- Schema/storage changes that are specific to a Variation.
-- When a Variation is terminated, these must be reverted.
--
-- Migrations are polymorphic: the `kind` field determines which driver
-- handles apply/revert, and `params` contains the driver-specific payload.
--
-- Example kinds:
--   'postgres' - params: {"up": "ALTER TABLE...", "down": "ALTER TABLE..."}
--   'redis'    - params: {"up": {"keys_to_create": [...]}, "down": {"keys_to_delete": [...]}}
--   'file'     - params: {"up": {"create": "/path/to/file"}, "down": {"delete": "/path/to/file"}}

CREATE TABLE variation_migrations (
    id UUID PRIMARY KEY,
    variation_id UUID NOT NULL REFERENCES variations(id),

    kind TEXT NOT NULL,    -- Driver type: 'postgres', 'redis', 'file', etc.
    params JSONB NOT NULL, -- Driver-specific up/down instructions

    -- Execution state
    applied_at TIMESTAMP,
    reverted_at TIMESTAMP,

    -- Ordering within this Variation's migrations
    sequence_num INTEGER NOT NULL,

    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_var_migrations ON variation_migrations(variation_id, sequence_num);

--------------------------------------------------------------------------------
-- DECISIONS
--------------------------------------------------------------------------------
-- A Decision is a choice point in the system. Every Decision has objectivity
-- and importance scores that can be used, in conjunction with "details", to
-- determine which human or agent should review.
CREATE TABLE decisions (
    id UUID PRIMARY KEY,

    -- What kind of decision?
    kind TEXT NOT NULL CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many')),

    -- Human- and agent-readable summary
    title TEXT NOT NULL,
    details TEXT,  -- Markdown OK; can include links

    -- Scores that help determine routing to human vs agent
    objectivity_score REAL NOT NULL CHECK (objectivity_score >= 0 AND objectivity_score <= 1),
    -- Importance scores are meant to be comparable at the Project level. I.e.,
    -- even if a Decision is "important" to a Hop, if that Hop is not important
    -- in the Project, neither is the Decision.
    importance_score REAL NOT NULL CHECK (importance_score >= 0 AND importance_score <= 1),

    -- Resolution state (see DESIGN.md Section 2.3)
    --   'needs_assignment' - Decision created, awaiting routing to agent/human
    --   'assigned'         - Routed to a specific agent or human
    --   'accepted'         - Assignee has acknowledged and is working on it
    --   'resolved'         - Decision made
    status TEXT NOT NULL DEFAULT 'needs_assignment' CHECK (status IN ('needs_assignment', 'assigned', 'accepted', 'resolved')),

    assigned_to TEXT,      -- Identifier for agent or user; format TBD
    assigned_at TIMESTAMP,

    accepted_by TEXT,      -- Identifier for agent or user; format TBD
    accepted_at TIMESTAMP,

    resolved_by TEXT,      -- Identifier for agent or user; format TBD
    resolved_at TIMESTAMP,

    resolution TEXT,       -- The actual decision made
    rationale TEXT,        -- Why this decision was made

    -- What entity does this decision relate to?
    subject_type TEXT,     -- 'hop', 'variation', 'strategy', etc.
    subject_id UUID,

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_decisions_status ON decisions(status);
CREATE INDEX idx_decisions_subject ON decisions(subject_type, subject_id);

--------------------------------------------------------------------------------
-- REPOSITORIES
--------------------------------------------------------------------------------
-- A Repository is a versioned store of artifacts.

CREATE TABLE repositories (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id),

    -- TODO: over time, support more repository types.
    name TEXT NOT NULL,
    repo_type TEXT NOT NULL CHECK (repo_type IN ('git', 'figma', 'gdrive')),

    -- Connection details
    url TEXT,  -- e.g., git URL

    -- Repository config (repo_type specific)
    config JSONB,

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

ALTER TABLE variations
    ADD CONSTRAINT fk_variations_repository
    FOREIGN KEY (repository_id) REFERENCES repositories(id);

--------------------------------------------------------------------------------
-- ECOSYSTEMS
--------------------------------------------------------------------------------
-- An Ecosystem is a runtime environment where Variations can be deployed.

CREATE TABLE ecosystems (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id),

    name TEXT NOT NULL,
    ecosystem_type TEXT NOT NULL,  -- 'kubernetes', 'vercel', 'squarespace', 'adwords', etc.

    -- Ecosystem configuration details (ecosystem_type specific)
    config JSONB,

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

ALTER TABLE variations
    ADD CONSTRAINT fk_variations_ecosystem
    FOREIGN KEY (ecosystem_id) REFERENCES ecosystems(id);

--------------------------------------------------------------------------------
-- TRAFFIC ALLOCATION
--------------------------------------------------------------------------------
-- How traffic is split across Variations within a Hop.
-- The SDK reads this to make consistent bucketing decisions.

CREATE TABLE traffic_allocations (
    id UUID PRIMARY KEY,
    hop_id UUID NOT NULL REFERENCES hops(id),

    -- Salt used for bucketing (combined with hopID and routingKey)
    bucket_salt TEXT NOT NULL,

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Individual allocation slices
CREATE TABLE traffic_allocation_slices (
    id UUID PRIMARY KEY,
    traffic_allocation_id UUID NOT NULL REFERENCES traffic_allocations(id),
    -- The variation_id must be associated with the traffic_allocation's hop_id
    variation_id UUID NOT NULL REFERENCES variations(id),

    -- Percentage of traffic (0.0 to 1.0); all slices for any given
    -- traffic_allocation should sum to 1.0.
    --
    -- If the numbers do not sum to 1.0, all fractions will be normalized such
    -- that the sum is indeed exactly 1.0.
    fraction REAL NOT NULL CHECK (fraction >= 0 AND fraction <= 1),

    -- Ordering matters for deterministic bucketing. The SDK walks slices in
    -- bucket_order, accumulating fractions until the user's bucket_pct is exceeded.
    -- Without consistent ordering, the same bucket_pct could map to different variations.
    bucket_order INTEGER NOT NULL
);

--------------------------------------------------------------------------------
-- ADDITIONAL INDEXES
--------------------------------------------------------------------------------

CREATE INDEX idx_strategies_project ON strategies(project_id);
CREATE INDEX idx_objectives_strategy ON objectives(strategy_id);
CREATE INDEX idx_key_results_objective ON key_results(objective_id);
CREATE INDEX idx_hops_strategy ON hops(strategy_id);
CREATE INDEX idx_hops_status ON hops(status);
CREATE INDEX idx_variations_hop ON variations(hop_id);
CREATE INDEX idx_variations_status ON variations(status);
