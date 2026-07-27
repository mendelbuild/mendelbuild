-- MendelBuild Core Schema
-- This file represents the complete schema after all migrations (001-016).
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
-- PROJECT CREDENTIALS
--------------------------------------------------------------------------------
-- Encrypted credentials for cloud deployments [added in 015]
-- Separate from project.config JSONB to support proper encryption and audit

CREATE TABLE project_credentials (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    encrypted_value BYTEA NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(project_id, name)
);

CREATE INDEX idx_project_credentials_project ON project_credentials(project_id);

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

    -- Comparison requirements [added in 018]
    requires_demo BOOLEAN NOT NULL DEFAULT false,
    requires_production BOOLEAN NOT NULL DEFAULT false,

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

    -- Diff stats vs main branch [added in 016]
    diff_files_changed INTEGER,
    diff_additions INTEGER,
    diff_deletions INTEGER,

    -- Cached evaluation scores [added in 017]
    evaluation_scores JSONB,

    status TEXT NOT NULL DEFAULT 'creating'
        CHECK (status IN ('creating', 'pending', 'migrating', 'active', 'draining',
                          'error', 'terminated', 'pruned', 'selected', 'merged', 'rejected')),

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
-- Log entries for variation operations (code generation, demos, fixes) [added in 005, extended in 011]
-- source_type: what kind of operation generated this log (codegen, demo, fix)
-- source_id: ID of the specific instance (e.g., demo_instance_id for demo logs)

CREATE TABLE variation_logs (
    id UUID PRIMARY KEY,
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,
    logged_at TIMESTAMP NOT NULL DEFAULT NOW(),
    level TEXT NOT NULL CHECK (level IN ('info', 'milestone', 'error', 'heartbeat')),
    message TEXT NOT NULL,
    source_type TEXT NOT NULL DEFAULT 'codegen' CHECK (source_type IN ('codegen', 'demo', 'fix')),
    source_id UUID
);

CREATE INDEX idx_variation_logs_variation_id ON variation_logs(variation_id);
CREATE INDEX idx_variation_logs_logged_at ON variation_logs(variation_id, logged_at DESC);
CREATE INDEX idx_variation_logs_source ON variation_logs(source_type, source_id);

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
    status TEXT NOT NULL DEFAULT 'starting',  -- starting, running, stopped, error
    process_info JSONB,  -- pid, port, container_id, etc - whatever is needed for teardown
    error_message TEXT,  -- populated if status = 'error'
    suggested_fix TEXT,  -- LLM-suggested fix prompt when status = 'error' [added in 012]
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_demo_instances_variation ON demo_instances(variation_id);
CREATE INDEX idx_demo_instances_status ON demo_instances(status) WHERE status = 'running';

--------------------------------------------------------------------------------
-- DEPLOYED INSTANCES
--------------------------------------------------------------------------------
-- Deployed variation instances in cloud environments [added in 015]
-- Tracks variations deployed to production/staging cloud environments

CREATE TABLE deployed_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,
    cloud_ecosystem TEXT NOT NULL,     -- 'gcp-cloudrun', 'aws-ecs', 'vercel', etc.
    url TEXT NOT NULL,                 -- internal service URL for Envoy routing
    public_url TEXT,                   -- optional external URL for direct access
    instance_info JSONB,               -- cloud-specific: project, region, service name, etc.
    deployed_at TIMESTAMP NOT NULL DEFAULT NOW(),
    status TEXT NOT NULL DEFAULT 'deploying'
        CHECK (status IN ('deploying', 'running', 'failed', 'terminated')),
    error_message TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_deployed_instances_variation ON deployed_instances(variation_id);
CREATE INDEX idx_deployed_instances_status ON deployed_instances(status);

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
    notes TEXT,                       -- Where to find migration files in user's CODE repo [added in 011]

    -- Execution state
    applied_at TIMESTAMP,
    reverted_at TIMESTAMP,

    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- DECISIONS
--------------------------------------------------------------------------------
-- An InputRequest is any input Mendel needs to proceed. This includes:
-- - Decisions (choosing between options)
-- - Credentials (API keys, tokens)
-- - Confirmations (proceed with action?)
-- - Manual setup tasks (create account, configure service)
-- Every InputRequest has objectivity and importance scores that can be used,
-- in conjunction with "details", to determine which human or agent should handle it.

CREATE TABLE input_requests (
    id UUID PRIMARY KEY,

    -- What kind of input is needed?
    --   'pass_fail'           - Binary yes/no decision
    --   'choose_one'          - Select exactly one option (e.g., pick winning Variation)
    --   'choose_many'         - Select zero or more options
    --   'roadmap_review'      - Conversational edit/approve cycle for Roadmap proposals
    --   'variation_review'    - Review/approve proposed Variations before code generation
    --   'variation_selection' - Pick winning Variation for a Hop
    --   'credential_request'  - Need an API key or credential [added in 019]
    --   'manual_setup'        - Human needs to do something external [added in 019]
    --   'confirmation'        - Simple proceed/cancel confirmation [added in 019]
    kind TEXT NOT NULL CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review',
                                        'variation_review', 'variation_selection',
                                        'credential_request', 'manual_setup', 'confirmation')),

    -- Human- and agent-readable summary
    title TEXT NOT NULL,
    details TEXT,  -- Markdown OK; can include links

    -- For credential_request/manual_setup: how to provide the input [added in 019]
    instructions TEXT,
    link TEXT,                      -- URL to external service (e.g., Render dashboard)
    required_capabilities TEXT[],   -- Permissions/scopes needed

    -- Scores that help determine routing to human vs agent
    objectivity_score REAL NOT NULL CHECK (objectivity_score >= 0 AND objectivity_score <= 1),
    -- Importance scores are meant to be comparable at the Project level. I.e.,
    -- even if an InputRequest is "important" to a Hop, if that Hop is not important
    -- in the Project, neither is the InputRequest.
    importance_score REAL NOT NULL CHECK (importance_score >= 0 AND importance_score <= 1),

    -- Resolution state
    --   'needs_assignment' - InputRequest created, awaiting routing to agent/human
    --   'assigned'         - Routed to a specific agent or human
    --   'accepted'         - Assignee has acknowledged and is working on it
    --   'resolved'         - Input provided
    status TEXT NOT NULL DEFAULT 'needs_assignment' CHECK (status IN ('needs_assignment', 'assigned', 'accepted', 'resolved')),

    assigned_to TEXT,      -- Identifier for agent or user; format TBD
    assigned_at TIMESTAMP,

    accepted_by TEXT,      -- Identifier for agent or user; format TBD
    accepted_at TIMESTAMP,

    resolved_by TEXT,      -- Identifier for agent or user; format TBD
    resolved_at TIMESTAMP,

    resolution TEXT,       -- The actual input/decision provided
    rationale TEXT,        -- Why this input was provided (for decisions)

    -- What entity does this input request relate to?
    subject_type TEXT,     -- 'hop', 'variation', 'strategy', 'project', etc.
    subject_id UUID,

    -- Cache for computed/ephemeral data (structure varies by kind)
    -- For variation_selection: stores LLM-computed evaluation scores
    cache JSONB,

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_input_requests_status ON input_requests(status);
CREATE INDEX idx_input_requests_subject ON input_requests(subject_type, subject_id);

--------------------------------------------------------------------------------
-- INPUT REQUEST MESSAGES
--------------------------------------------------------------------------------
-- Conversation history for InputRequest review cycles

CREATE TABLE input_request_messages (
    id UUID PRIMARY KEY,
    input_request_id UUID NOT NULL REFERENCES input_requests(id) ON DELETE CASCADE,

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

CREATE INDEX idx_input_request_messages_input_request ON input_request_messages(input_request_id, created_at);

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
-- Envoy proxy reads this (via generated config) for consistent bucketing.
-- Base tables from 001_initial, constraints added in 015.

CREATE TABLE traffic_allocations (
    id UUID PRIMARY KEY,
    hop_id UUID NOT NULL REFERENCES hops(id),
    bucket_salt TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Individual allocation slices (base from 001_initial)
CREATE TABLE traffic_allocation_slices (
    id UUID PRIMARY KEY,
    traffic_allocation_id UUID NOT NULL REFERENCES traffic_allocations(id),
    variation_id UUID NOT NULL REFERENCES variations(id),
    fraction REAL NOT NULL CHECK (fraction >= 0 AND fraction <= 1),
    bucket_order INTEGER NOT NULL
);

-- Modifications from 015_cloud_deployment:
-- Add UNIQUE constraint to hop_id
ALTER TABLE traffic_allocations ADD CONSTRAINT traffic_allocations_hop_id_key UNIQUE (hop_id);

-- Add ON DELETE CASCADE to FKs
ALTER TABLE traffic_allocation_slices DROP CONSTRAINT traffic_allocation_slices_traffic_allocation_id_fkey;
ALTER TABLE traffic_allocation_slices ADD CONSTRAINT traffic_allocation_slices_traffic_allocation_id_fkey
    FOREIGN KEY (traffic_allocation_id) REFERENCES traffic_allocations(id) ON DELETE CASCADE;

ALTER TABLE traffic_allocation_slices DROP CONSTRAINT traffic_allocation_slices_variation_id_fkey;
ALTER TABLE traffic_allocation_slices ADD CONSTRAINT traffic_allocation_slices_variation_id_fkey
    FOREIGN KEY (variation_id) REFERENCES variations(id) ON DELETE CASCADE;

-- Add created_at column
ALTER TABLE traffic_allocation_slices ADD COLUMN created_at TIMESTAMP NOT NULL DEFAULT NOW();

-- Add unique constraints for deterministic bucketing
ALTER TABLE traffic_allocation_slices ADD CONSTRAINT traffic_allocation_slices_allocation_variation_key
    UNIQUE (traffic_allocation_id, variation_id);
ALTER TABLE traffic_allocation_slices ADD CONSTRAINT traffic_allocation_slices_allocation_order_key
    UNIQUE (traffic_allocation_id, bucket_order);

--------------------------------------------------------------------------------
-- TRAFFIC ALLOCATION ENVOY CONFIGS
--------------------------------------------------------------------------------
-- Generated Envoy configs for audit/rollback [added in 015]

CREATE TABLE traffic_allocation_envoy_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    config_yaml TEXT NOT NULL,
    generated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    applied_at TIMESTAMP,             -- null until user confirms deployment
    superseded_at TIMESTAMP           -- set when newer config is applied
);

CREATE INDEX idx_traffic_allocation_envoy_configs_project ON traffic_allocation_envoy_configs(project_id);

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
