-- MendelBuild Core Schema
-- This file represents the complete schema after all migrations (001-010).
-- It should be kept in sync with migrations for reference.
--
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
    config JSONB,  -- Project-wide credentials (anthropic_api_key, etc.) [added in 004]
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
-- Objectives can be hierarchical via parent_id for organizational alignment.
-- Key Results are linked via the objective_key_result_pairs junction table.

CREATE TABLE objectives (
    id UUID PRIMARY KEY,
    strategy_id UUID NOT NULL REFERENCES strategies(id),
    parent_id UUID REFERENCES objectives(id),  -- NULL for top-level objectives [added in 007]

    description TEXT NOT NULL,  -- Plain-English objective

    -- OKR quality tuning feedback from AI [added in 007]
    tune_score REAL,      -- Quality score 0.0-1.0
    tune_feedback TEXT,   -- Brief feedback on clarity, specificity

    deleted_at TIMESTAMP,  -- Soft delete timestamp [added in 007]

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- KEY RESULTS
--------------------------------------------------------------------------------
-- Key Results are quantitative targets that can be linked to multiple Objectives
-- via the objective_key_result_pairs junction table [changed in 007].
-- Each KR has a target value expressed with units that MendelBuild parses.

CREATE TABLE key_results (
    id UUID PRIMARY KEY,
    strategy_id UUID NOT NULL REFERENCES strategies(id),  -- Changed from objective_id in 007

    description TEXT NOT NULL,

    -- Target expressed with units, e.g., "1000 users", "99.9%", "< 200ms p99"
    -- MendelBuild Core parses this to extract:
    --   - numeric target
    --   - unit type (count, percentage, duration, currency, etc.)
    --   - comparison operator (=, <, >, >=, <=)
    --   - measurement horizon if applicable (e.g., "per week", "7-day rolling")
    target_units TEXT NOT NULL,

    target_date TIMESTAMP,  -- When we expect to hit target

    -- OKR quality tuning feedback from AI [added in 007]
    tune_score REAL,      -- Quality score 0.0-1.0
    tune_feedback TEXT,   -- Brief feedback on measurability, clarity

    deleted_at TIMESTAMP,  -- Soft delete timestamp [added in 007]

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- OBJECTIVE KEY RESULT PAIRS
--------------------------------------------------------------------------------
-- Junction table for many-to-many relationship between Objectives and Key Results.
-- A Key Result can contribute to multiple Objectives [added in 007].

CREATE TABLE objective_key_result_pairs (
    objective_id UUID NOT NULL REFERENCES objectives(id),
    key_result_id UUID NOT NULL REFERENCES key_results(id),
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (objective_id, key_result_id)
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
-- It defines WHAT we want (via commentary) but not HOW.
-- Each Hop can spawn multiple Variations that compete.
--
-- Hops form a DAG via hop_dependencies. They attach directly to Strategies
-- (no separate "roadmaps" table needed).
--
-- Hop lifecycle states (see DESIGN.md Section 5 for Variation states):
--   'pending'   - Not yet started (blocked on dependencies or not scheduled)
--   'active'    - Currently running, Variations being generated/evaluated
--   'selecting' - All Variations done, awaiting human selection [added in 006]
--   'completed' - A Variation was selected and merged
--   'rejected'  - Human rejected all Variations [added in 006]
--   'abandoned' - Hop was cancelled without selecting a winner

CREATE TABLE hops (
    id UUID PRIMARY KEY,
    strategy_id UUID NOT NULL REFERENCES strategies(id),

    name TEXT NOT NULL,

    -- Context about the Hop: what it achieves, why it matters, expected impact
    commentary TEXT NOT NULL,  -- Made NOT NULL in 003

    -- JSON blob with hop metadata (e.g., objective_ids linking to OKRs)
    params JSONB,  -- Renamed from kind_params in 003

    -- AI-generated structured criteria for comparing Variations [added in 006]
    -- JSONB structure: { "criteria": [...], "rationale": "...", "tradeoffs": "..." }
    evaluation_criteria JSONB,

    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'active', 'selecting', 'completed', 'rejected', 'abandoned')),

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
--   'draining', 'error', 'terminated', 'pruned', 'selected',
--   'merged', 'rejected' [merged/rejected added in 006]

CREATE TABLE variations (
    id UUID PRIMARY KEY,
    hop_id UUID NOT NULL REFERENCES hops(id),

    -- Variation identity for code generation [added in 004]
    name TEXT,           -- e.g., "cache-layer-approach"
    approach TEXT,       -- Detailed implementation approach

    -- Repository location
    repository_id UUID,  -- FK added below after repositories table
    commit_ref TEXT,     -- Opaque reference; for git repos this is a SHA

    -- Ecosystem deployment (nullable if not yet deployed)
    ecosystem_id UUID,   -- FK added below after ecosystems table
    deployment_ref TEXT, -- e.g., pod name, URL, etc.

    status TEXT NOT NULL DEFAULT 'creating'
        CHECK (status IN ('creating', 'pending', 'migrating', 'active', 'draining',
                          'error', 'terminated', 'pruned', 'selected', 'merged', 'rejected')),

    -- Notes on where to find migrations in user's repo/datastore [added in 010]
    migration_notes TEXT,

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
-- VARIATION LOGS
--------------------------------------------------------------------------------
-- Log entries for variation code generation process [added in 005]

CREATE TABLE variation_logs (
    id UUID PRIMARY KEY,
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,
    logged_at TIMESTAMP NOT NULL DEFAULT NOW(),
    level TEXT NOT NULL CHECK (level IN ('info', 'milestone', 'error', 'heartbeat')),
    message TEXT NOT NULL
);

CREATE INDEX idx_variation_logs_variation_id ON variation_logs(variation_id);
CREATE INDEX idx_variation_logs_logged_at ON variation_logs(variation_id, logged_at DESC);

--------------------------------------------------------------------------------
-- DEMO INSTANCES
--------------------------------------------------------------------------------
-- Demo instances track running demos of variations [added in 008]
-- Designed to be stateless: Mendel can crash and recover by reading teardown instructions

CREATE TABLE demo_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id),
    url TEXT NOT NULL,
    teardown_instructions TEXT NOT NULL,  -- shell commands to stop the demo
    started_at TIMESTAMP NOT NULL DEFAULT NOW(),
    stopped_at TIMESTAMP,
    status TEXT NOT NULL DEFAULT 'running',  -- running, stopped, error
    process_info JSONB,  -- pid, port, container_id, etc - whatever is needed for teardown
    error_message TEXT,  -- populated if status = 'error'
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_demo_instances_variation ON demo_instances(variation_id);
CREATE INDEX idx_demo_instances_status ON demo_instances(status) WHERE status = 'running';

--------------------------------------------------------------------------------
-- VARIATION MIGRATIONS
--------------------------------------------------------------------------------
-- Schema/storage changes that are specific to a Variation.
-- These are TEMPORARY migrations applied during variation testing/demo.
--
-- Lifecycle:
--   - up_instructions executed when variation demo starts
--   - down_instructions executed when variation is rejected OR accepted
--   - When accepted, the "real" migration lives in the merged code
--
-- Instructions are freeform text for Claude Code to interpret.
-- They can reference MENDEL.md or specify commands directly.
-- [Simplified in 009 from structured kind/params approach]

CREATE TABLE variation_migrations (
    id UUID PRIMARY KEY,
    variation_id UUID NOT NULL REFERENCES variations(id),

    up_instructions TEXT NOT NULL,    -- Instructions for Claude Code to apply migration
    down_instructions TEXT NOT NULL,  -- Instructions for Claude Code to revert migration

    -- Execution state
    applied_at TIMESTAMP,
    reverted_at TIMESTAMP,

    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- DECISIONS
--------------------------------------------------------------------------------
-- A Decision is a choice point in the system. Every Decision has objectivity
-- and importance scores that can be used, in conjunction with "details", to
-- determine which human or agent should review.

CREATE TABLE decisions (
    id UUID PRIMARY KEY,

    -- What kind of decision?
    --   'pass_fail'           - Binary yes/no decision
    --   'choose_one'          - Select exactly one option (e.g., pick winning Variation)
    --   'choose_many'         - Select zero or more options
    --   'roadmap_review'      - Conversational edit/approve cycle for Roadmap proposals
    --   'variation_review'    - Review/approve proposed Variations before code generation [added in 004]
    --   'variation_selection' - Pick winning Variation for a Hop [added in 006]
    kind TEXT NOT NULL CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review',
                                        'variation_review', 'variation_selection')),

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
-- DECISION MESSAGES
--------------------------------------------------------------------------------
-- Conversation history for Decision review cycles [added in 002]

CREATE TABLE decision_messages (
    id UUID PRIMARY KEY,
    decision_id UUID NOT NULL REFERENCES decisions(id) ON DELETE CASCADE,

    -- Who sent this message?
    --   'user'  - Human reviewer
    --   'agent' - AI agent (proposer, reviser, etc.)
    --   'system' - System-generated messages (status changes, etc.)
    role TEXT NOT NULL CHECK (role IN ('user', 'agent', 'system')),

    content TEXT NOT NULL,

    -- Token usage for agent messages (for budget tracking)
    tokens_used INTEGER,

    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_decision_messages_decision ON decision_messages(decision_id, created_at);

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
CREATE INDEX idx_objectives_parent ON objectives(parent_id) WHERE deleted_at IS NULL;  -- [added in 007]
CREATE INDEX idx_objectives_deleted ON objectives(deleted_at);  -- [added in 007]
CREATE INDEX idx_key_results_strategy ON key_results(strategy_id) WHERE deleted_at IS NULL;  -- [added in 007]
CREATE INDEX idx_key_results_deleted ON key_results(deleted_at);  -- [added in 007]
CREATE INDEX idx_okr_junction_kr ON objective_key_result_pairs(key_result_id);  -- [added in 007]
CREATE INDEX idx_hops_strategy ON hops(strategy_id);
CREATE INDEX idx_hops_status ON hops(status);
CREATE INDEX idx_variations_hop ON variations(hop_id);
CREATE INDEX idx_variations_status ON variations(status);
