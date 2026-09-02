-- MendelBuild Core Schema
-- This file represents the complete schema after all migrations (001-039).
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

    -- What the user said they wanted built, in their own words [added in 032].
    -- The drafting agent works from this, and it stays as the record of what
    -- was actually asked for.
    brief TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
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
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(project_id, name)
);

CREATE INDEX idx_project_credentials_project ON project_credentials(project_id);

--------------------------------------------------------------------------------
-- USERS AND AUTHENTICATION
--------------------------------------------------------------------------------
-- Users for multi-user support [added in 022]

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    name TEXT,
    picture_url TEXT,
    google_id TEXT UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_google_id ON users(google_id);

-- Project membership links users to projects with roles [added in 022]
CREATE TABLE project_members (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'member')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(project_id, user_id)
);

CREATE INDEX idx_project_members_user ON project_members(user_id);
CREATE INDEX idx_project_members_project ON project_members(project_id);

-- Sessions for authentication [added in 022]
CREATE TABLE sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sessions_token ON sessions(token_hash);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

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

    -- When a human validated these OKRs [added in 032]. NULL means the
    -- objectives below are still an unreviewed agent draft. An agent-written
    -- objective and a human-approved one are indistinguishable in the
    -- objectives table, so this cannot be derived.
    okrs_approved_at TIMESTAMPTZ,

    -- Where this strategy's draft is up to [added in 034]: 'drafting', 'ready',
    -- or 'failed'. Not derivable -- a strategy with no objectives looks the
    -- same whether a background draft is running, has failed, or never ran.
    -- draft_started_at lets a draft whose process died be recognised as stale
    -- rather than polled forever.
    draft_status TEXT NOT NULL DEFAULT 'ready',
    draft_error TEXT,
    draft_started_at TIMESTAMPTZ,

    -- When this strategy was last asked for Key Result measurements [040].
    -- The cadence is judged from this rather than from the open request's
    -- created_at, so that asking and resolving stay separate facts: a request
    -- updated in place must not read as a fresh one.
    measurements_asked_at TIMESTAMPTZ,

    -- What the drafting agent said about its own draft [added in 032]:
    -- how it read the brief, what it filled in, what it could not tell.
    -- Kept after approval -- when a hop overruns, the assumptions the plan
    -- was built on are the first thing worth re-reading.
    draft_notes JSONB,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT strategies_draft_status_valid
        CHECK (draft_status IN ('drafting', 'ready', 'failed'))
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

    deleted_at TIMESTAMPTZ,  -- Soft delete timestamp [added in 007]

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
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

    -- The target, structured [037]. It was one free-text column, with a comment
    -- claiming Core parsed it into these parts; no parser was ever written, so
    -- a Key Result could be shown but never compared against a measurement.
    --
    -- The drafting agent returns the three separately and the OKR editor takes
    -- them as three fields, so nothing parses a target at either end and the
    -- prose form ("1000 users") is derived for display rather than stored.
    -- How the target is judged [038]. Named rather than punctuated, because
    -- one of the three is not an operator:
    --   at_least  the number should reach the target or pass it
    --   at_most   the number should stay at the target or below
    --   done      it happened, or it has not (target_value 1, no unit)
    -- The strict operators were dropped: "more than 1000" and "at least 1000"
    -- differ only on the boundary and nobody writing a Key Result means it.
    target_comparator TEXT NOT NULL
        CHECK (target_comparator IN ('at_least', 'at_most', 'done')),
    target_value REAL NOT NULL,
    -- Display only -- comparison is value against value -- so this absorbs any
    -- qualifier ("ms p99", "signups per week") without a further column.
    target_unit TEXT NOT NULL,

    target_date DATE,  -- The day we expect to hit target (a date, not an instant)

    -- OKR quality tuning feedback from AI [added in 007]
    tune_score REAL,      -- Quality score 0.0-1.0
    tune_feedback TEXT,   -- Brief feedback on measurability, clarity

    deleted_at TIMESTAMPTZ,  -- Soft delete timestamp [added in 007]

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- OBJECTIVE KEY RESULT PAIRS
--------------------------------------------------------------------------------
-- Junction table for many-to-many relationship between Objectives and Key Results.
-- A Key Result can contribute to multiple Objectives [added in 007].

CREATE TABLE objective_key_result_pairs (
    objective_id UUID NOT NULL REFERENCES objectives(id),
    key_result_id UUID NOT NULL REFERENCES key_results(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
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
    measured_at TIMESTAMPTZ NOT NULL,

    -- Optional: source of measurement (for debugging/auditing)
    source TEXT
);

CREATE INDEX idx_kr_history_kr_id ON key_result_history(key_result_id, measured_at);

-- Answering the same measurement form twice is a double submit, not two
-- readings [040].
CREATE UNIQUE INDEX idx_kr_history_unique ON key_result_history(key_result_id, measured_at);

--------------------------------------------------------------------------------
-- FUNDING SOURCES
--------------------------------------------------------------------------------
-- A FundingSource is a pool of money allocated to a Strategy.
--
-- USD is the unit of account [030]. Tokens are not a unit of value: a Haiku
-- token and a Fable token differ 10x in price, cache reads are 0.1x an input
-- token and cache writes 1.25x-2x, and batch is half price. A token-denominated
-- budget therefore floats in worth. Token counts are still recorded in full, in
-- cost_entries, as the evidence behind each dollar figure.

CREATE TABLE funding_sources (
    id UUID PRIMARY KEY,
    strategy_id UUID NOT NULL REFERENCES strategies(id),

    name TEXT NOT NULL,             -- Human label, e.g. 'Seed round', 'Q3 build'
    amount_usd REAL NOT NULL CHECK (amount_usd >= 0),

    -- The date half of "budgets tied to dates and OKR milestones" [030].
    -- A budget that does not say when it must last is not a budget, it is a
    -- number. The OKR half is funding_success_criteria below.
    period_start DATE,
    period_end DATE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT funding_sources_period_ordered
        CHECK (period_start IS NULL OR period_end IS NULL OR period_start <= period_end)
);

--------------------------------------------------------------------------------
-- FUNDING SUCCESS CRITERIA
--------------------------------------------------------------------------------
-- Links FundingSources to KeyResults: "we're spending this budget to achieve these KRs"
--
-- This is the OKR half of tying budgets to milestones [030]. Key Results carry
-- target_date, so joining through here gives every budget a set of dated
-- milestones to be judged against.

CREATE TABLE funding_success_criteria (
    id UUID PRIMARY KEY,
    funding_source_id UUID NOT NULL REFERENCES funding_sources(id),
    key_result_id UUID NOT NULL REFERENCES key_results(id),

    -- Optional weight if some KRs matter more than others for this funding
    weight REAL DEFAULT 1.0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT funding_success_criteria_unique UNIQUE (funding_source_id, key_result_id)
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

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
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

    limit_usd REAL NOT NULL CHECK (limit_usd >= 0),  -- Spend ceiling for this Hop [030]

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Actual spend lives in cost_entries at the end of this file [030]. The old
-- budget_spend_log was defined, never written, and never read.

--------------------------------------------------------------------------------
-- HOP COST ESTIMATES
--------------------------------------------------------------------------------
-- Append-only estimate history [030]. Separate from budget_allocations because
-- an estimate ("what we think this costs") and an allocation ("what this Hop is
-- allowed to spend") are different claims by different authors. Keeping every
-- estimate rather than overwriting is what lets Mendel measure whether its own
-- estimator is any good, and feed that back into the next estimate.

CREATE TABLE hop_cost_estimates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hop_id UUID NOT NULL REFERENCES hops(id) ON DELETE CASCADE,

    amount_usd REAL NOT NULL CHECK (amount_usd >= 0),

    -- Who produced this estimate, and how much to trust it.
    estimator TEXT NOT NULL CHECK (estimator IN ('proposer', 'auditor', 'human', 'calibration')),
    confidence REAL CHECK (confidence >= 0 AND confidence <= 1),

    -- Free-text justification, shown in review so a human can fact-check it.
    basis TEXT,

    -- How many completed Hops of observed history this estimate was grounded in.
    calibrated_from_hops INTEGER NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_hop_cost_estimates_hop ON hop_cost_estimates(hop_id, created_at DESC);

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

    -- Token usage is recorded in cost_entries [030]. The columns that used to
    -- live here counted only codegen input/output, dropping cache tokens and
    -- every non-codegen agent call, so they undercounted real spend.

    -- Spend pause [033]. budget_paused_usd being set marks a run stopped at its
    -- cost ceiling -- work directory kept, awaiting a human decision -- as
    -- distinct from being blocked on credentials.
    budget_paused_usd REAL CHECK (budget_paused_usd >= 0),
    budget_ceiling_usd REAL CHECK (budget_ceiling_usd >= 0),
    CONSTRAINT variations_budget_pause_complete CHECK (
        (budget_paused_usd IS NULL) = (budget_ceiling_usd IS NULL)
    ),

    status TEXT NOT NULL DEFAULT 'creating'
        CHECK (status IN ('creating', 'pending', 'blocked', 'migrating', 'active', 'draining',
                          'error', 'terminated', 'pruned', 'selected', 'merged', 'rejected')),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Variation lifecycle history: timestamped state transitions
CREATE TABLE variation_state_history (
    id UUID PRIMARY KEY,
    variation_id UUID NOT NULL REFERENCES variations(id),

    from_status TEXT,
    to_status TEXT NOT NULL,
    transitioned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

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
    logged_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
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
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    stopped_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'starting',  -- starting, running, stopped, error
    process_info JSONB,  -- pid, port, container_id, etc - whatever is needed for teardown
    error_message TEXT,  -- populated if status = 'error'
    suggested_fix TEXT,  -- LLM-suggested fix prompt when status = 'error' [added in 012]
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_demo_instances_variation ON demo_instances(variation_id);
CREATE INDEX idx_demo_instances_status ON demo_instances(status) WHERE status = 'running';

--------------------------------------------------------------------------------
-- VARIATION REQUIREMENTS
--------------------------------------------------------------------------------
-- Things a variation's code needs in order to run anywhere [added in 031].
--
-- These belong to the variation, not to demos. A variation that wired up
-- Google sign-in needs client credentials and a registered redirect URI to
-- function at all; demos are merely where that first bites, because a demo is
-- the first time the code is pushed through a deployment channel. The same
-- requirements gate a production deploy of that variation once it is merged.
--
-- Two kinds, which differ in what Mendel does about them:
--
--   secret          A value Mendel needs from the user (GOOGLE_CLIENT_SECRET).
--                   Stored encrypted, project-scoped, injected at deploy time.
--
--   acknowledgement An action the user must take somewhere else, where Mendel
--                   already knows the string involved (the deployment's OAuth
--                   redirect URI) and only needs confirmation it was done.
--                   Mendel stores the confirmation, never a secret.
--
-- Declared per variation by code generation, because what is needed depends on
-- the code that was written.

CREATE TABLE variation_requirements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,

    kind TEXT NOT NULL CHECK (kind IN ('secret', 'acknowledgement')),

    -- Stable identifier within a variation: the env var name for a secret,
    -- a slug like 'google-redirect-uri' for an acknowledgement.
    name TEXT NOT NULL,
    description TEXT,

    -- Acknowledgements only. instructions may contain {{deploy_url}}, which
    -- resolves to the URL of whichever deployment is being gated, so the same
    -- requirement yields one string for a demo and another for production.
    -- console_url links to the page where the action is performed.
    instructions TEXT,
    console_url TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (variation_id, kind, name),

    -- An acknowledgement without instructions cannot be acted on.
    CONSTRAINT variation_requirements_ack_has_instructions CHECK (
        kind <> 'acknowledgement' OR instructions IS NOT NULL
    )
);

CREATE INDEX idx_variation_requirements_variation
    ON variation_requirements(variation_id);

-- Values for 'secret' requirements. Project-scoped: an OAuth client ID is the
-- same for every variation and for production, so it is entered once per
-- project. Kept apart from project_credentials, which holds the platform
-- credentials Mendel needs in order to deploy at all, rather than values
-- belonging to the user's own application.
CREATE TABLE project_env_vars (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    encrypted_value BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE INDEX idx_project_env_vars_project ON project_env_vars(project_id);

-- Confirmations that an acknowledgement was carried out.
--
-- Keyed by the exact string confirmed rather than by deployment, because one
-- requirement legitimately has several: the demo URL and the production URL
-- are different redirect URIs and both must be registered. A changed URL
-- leaves no matching row, so the requirement is unmet again rather than
-- silently vouching for a string nobody registered.
CREATE TABLE requirement_acknowledgements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    requirement_id UUID NOT NULL REFERENCES variation_requirements(id) ON DELETE CASCADE,
    resolved_value TEXT NOT NULL,
    acknowledged_by UUID REFERENCES users(id),
    acknowledged_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (requirement_id, resolved_value)
);

CREATE INDEX idx_requirement_acknowledgements_requirement
    ON requirement_acknowledgements(requirement_id);


--------------------------------------------------------------------------------
-- HOSTING PLATFORMS
--------------------------------------------------------------------------------
-- Available cloud platforms for demo deployment [added in 024]
-- Seeded on startup, refreshable via CLI

CREATE TABLE hosting_platforms (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug TEXT UNIQUE NOT NULL,           -- "fly-io", "cloud-run", "vercel"
    name TEXT NOT NULL,                  -- "Fly.io", "Google Cloud Run", "Vercel"
    deployer_image TEXT NOT NULL,        -- Docker image with /bin/sh (e.g., "alpine:latest")
    instructions TEXT NOT NULL,          -- Prose a person reads: what is needed and where it goes
    setup_script TEXT NOT NULL DEFAULT '', -- Commands they paste into a terminal, offered with a copy button
    setup_prerequisites JSONB NOT NULL DEFAULT '[]'::jsonb, -- Ordered list rendered as a real list, not indented prose
    setup_input_label TEXT NOT NULL DEFAULT '',   -- Prompt for the one value the script needs before it will run
    setup_input_credential TEXT NOT NULL DEFAULT '', -- The credential that value also supplies, if any
    -- 'platform' when the platform hands out a hostname (*.fly.dev), 'user'
    -- when the deployment is only reachable at an address until the user
    -- brings a domain of their own.
    hostname_source TEXT NOT NULL DEFAULT 'platform'
        CHECK (hostname_source IN ('platform', 'user')),
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_hosting_platforms_slug ON hosting_platforms(slug);

--------------------------------------------------------------------------------
-- DEPLOYMENT CHANNELS
--------------------------------------------------------------------------------
-- Deploy artifact kinds and project deployment configuration [added in 027]
-- Supports a sparse matrix of (artifact_kind, hosting_platform) combinations

CREATE TYPE deploy_artifact_kind AS ENUM (
    'container',       -- Single Dockerfile -> Fly.io, Cloud Run, Render
    'kubernetes',      -- k8s manifests (Helm, raw YAML, kustomize) -> GKE, EKS, AKS
    'static',          -- Static files -> Vercel, Netlify, S3+CloudFront
    'source_deploy'    -- No container, platform builds -> Vercel, Render, Railway
);

-- Supported (artifact_kind, hosting_platform) combinations
-- The sparse matrix - only these combos are validated to work
CREATE TABLE supported_deployment_combos (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    artifact_kind deploy_artifact_kind NOT NULL,
    hosting_platform_id UUID NOT NULL REFERENCES hosting_platforms(id) ON DELETE CASCADE,
    notes TEXT,
    guidance JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(artifact_kind, hosting_platform_id)
);

-- Project's deployment configuration (keeps history)
CREATE TABLE project_deployment_channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    artifact_kind deploy_artifact_kind NOT NULL,
    hosting_platform_id UUID NOT NULL REFERENCES hosting_platforms(id) ON DELETE CASCADE,

    -- Validation state
    demo_validated_at TIMESTAMPTZ,
    demo_validating_at TIMESTAMPTZ,
    demo_validation_error TEXT,
    prod_validated_at TIMESTAMPTZ,
    prod_validating_at TIMESTAMPTZ,
    prod_validation_error TEXT,

    -- Production state lives in hosting_deployments (kind = 'prod') [029]

    -- History: null = current active channel
    disabled_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Only one active channel per project
CREATE UNIQUE INDEX project_deployment_channels_active_idx
    ON project_deployment_channels(project_id)
    WHERE disabled_at IS NULL;

CREATE INDEX idx_supported_deployment_combos_artifact ON supported_deployment_combos(artifact_kind);
CREATE INDEX idx_project_deployment_channels_project ON project_deployment_channels(project_id);

--------------------------------------------------------------------------------
-- HOSTING DEPLOYMENTS
--------------------------------------------------------------------------------
-- Deployments made through a project's deployment channel [added in 029]
-- Covers production deploys; shaped so demo deploys can move onto it
-- (kind = 'demo' with variation_id set) and retire demo_instances.
-- Replaced deployed_instances, which was only used by the retired
-- script-based deploy/envoy packages.

CREATE TABLE hosting_deployments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    channel_id UUID NOT NULL REFERENCES project_deployment_channels(id) ON DELETE CASCADE,

    -- What this deployment is for. Demo deploys carry a variation_id;
    -- prod deploys track the main branch and have none.
    kind TEXT NOT NULL CHECK (kind IN ('demo', 'prod')),
    variation_id UUID REFERENCES variations(id) ON DELETE CASCADE,

    -- What was deployed and where it landed
    commit_sha TEXT,
    app_name TEXT NOT NULL,           -- platform app/service name, needed for teardown
    url TEXT,                         -- populated once the deploy succeeds
    teardown_instructions TEXT,       -- shell command to tear this deployment down

    status TEXT NOT NULL DEFAULT 'deploying'
        CHECK (status IN ('deploying', 'running', 'failed', 'terminated')),
    error_message TEXT,

    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A demo deploy must name its variation; a prod deploy must not.
    CONSTRAINT hosting_deployments_variation_matches_kind CHECK (
        (kind = 'demo' AND variation_id IS NOT NULL) OR
        (kind = 'prod' AND variation_id IS NULL)
    )
);

CREATE INDEX idx_hosting_deployments_project ON hosting_deployments(project_id, kind, started_at DESC);
CREATE INDEX idx_hosting_deployments_variation ON hosting_deployments(variation_id);
CREATE INDEX idx_hosting_deployments_status ON hosting_deployments(status);

-- Log lines produced while deploying. Mendel reads these even when the UI does not.
CREATE TABLE hosting_deployment_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id UUID NOT NULL REFERENCES hosting_deployments(id) ON DELETE CASCADE,
    logged_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    level TEXT NOT NULL CHECK (level IN ('info', 'milestone', 'error')),
    message TEXT NOT NULL
);

CREATE INDEX idx_hosting_deployment_logs_deployment
    ON hosting_deployment_logs(deployment_id, logged_at);

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
    applied_at TIMESTAMPTZ,
    reverted_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

--------------------------------------------------------------------------------
-- VARIATION REVISIONS
--------------------------------------------------------------------------------
-- Track user feedback requests for improving a variation [added in 025]
-- When a user requests a change, a revision is created and the variation
-- goes back to "creating" status for Claude Code to apply the feedback.

CREATE TABLE variation_revisions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,
    feedback TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'completed', 'failed')),
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_variation_revisions_variation_id ON variation_revisions(variation_id);
CREATE INDEX idx_variation_revisions_status ON variation_revisions(status);

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

    -- Which project does this belong to? Denormalized for query simplicity.
    project_id UUID NOT NULL REFERENCES projects(id),

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
    --   'hosting_platform'    - Select demo hosting platform [added in 023]
    kind TEXT NOT NULL CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review',
                                        'variation_review', 'variation_selection',
                                        'credential_request', 'manual_setup', 'confirmation',
                                        'hosting_platform', 'measurement')),

    -- Human- and agent-readable summary
    title TEXT NOT NULL,
    details TEXT,  -- Markdown OK; can include links

    -- The cost auditor's verdict on a proposed roadmap [030]. Stored so the
    -- review page shows it beside the estimates it is checking, rather than
    -- re-running an LLM call on every page view.
    cost_audit JSONB,

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
    assigned_at TIMESTAMPTZ,

    accepted_by TEXT,      -- Identifier for agent or user; format TBD
    accepted_at TIMESTAMPTZ,

    resolved_by TEXT,      -- Identifier for agent or user; format TBD
    resolved_at TIMESTAMPTZ,

    resolution TEXT,       -- The actual input/decision provided
    rationale TEXT,        -- Why this input was provided (for decisions)

    -- What entity does this input request relate to?
    subject_type TEXT,     -- 'hop', 'variation', 'strategy', 'project', etc.
    subject_id UUID,

    -- Cache for computed/ephemeral data (structure varies by kind)
    -- For variation_selection: stores LLM-computed evaluation scores
    cache JSONB,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_input_requests_status ON input_requests(status);
CREATE INDEX idx_input_requests_subject ON input_requests(subject_type, subject_id);
CREATE INDEX idx_input_requests_project ON input_requests(project_id);
CREATE INDEX idx_input_requests_project_status ON input_requests(project_id, status);

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

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
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

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
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

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE variations
    ADD CONSTRAINT fk_variations_ecosystem
    FOREIGN KEY (ecosystem_id) REFERENCES ecosystems(id);

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
CREATE INDEX idx_variations_budget_paused ON variations(budget_paused_usd)
    WHERE budget_paused_usd IS NOT NULL;  -- [033]

--------------------------------------------------------------------------------
-- RATE CARDS [030]
--------------------------------------------------------------------------------
-- Pricing lives in the database, seeded on startup and refreshable via CLI,
-- following the same rule as hosting_platforms: no hardcoded platform data in
-- Go. Rate cards are versioned by effective_from, so a historical ledger entry
-- keeps the price that was actually in force when the spend happened.

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
-- COST LEDGER [030]
--------------------------------------------------------------------------------
-- The actuals. Append-only; every row carries both the raw telemetry the
-- provider reported and the USD it converts to, plus the rate card used, so any
-- figure in the UI can be traced back to counts x a dated price.

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

-------------------------------------------------------------------------------
-- Project domains
-------------------------------------------------------------------------------

-- Where a project's deployments are reachable is a property of the project, not
-- of whichever channel happens to be selected: the domain outlives the channel,
-- and the same names are wanted whether demos run on Kubernetes today or
-- somewhere else later.
--
-- It was briefly a credential, which was wrong twice over. A domain is not a
-- secret, and a credential has nowhere to say what records the user must create
-- for it to work -- which is the part they actually need help with.
CREATE TABLE project_domains (
    project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,

    -- The domain the user controls, e.g. example.com.
    base_domain TEXT NOT NULL,

    -- One label under it for demos, so a single wildcard record covers them all:
    -- 'mendel-demos' gives *.mendel-demos.example.com.
    demo_subdomain TEXT NOT NULL DEFAULT 'mendel-demos',

    -- One label for production, empty when production has no name yet.
    prod_subdomain TEXT NOT NULL DEFAULT '',

    -- The address the records point at, once Mendel has reserved one. Known
    -- before any deployment exists, which is what lets Mendel state the record
    -- rather than asking the user to go and find an IP.
    static_ip TEXT NOT NULL DEFAULT '',
    static_ip_name TEXT NOT NULL DEFAULT '',

    -- The domain-ownership record for the wildcard certificate. Minted by
    -- Certificate Manager, so its value cannot be worked out in advance.
    acme_record_name TEXT NOT NULL DEFAULT '',
    acme_record_value TEXT NOT NULL DEFAULT '',
    certificate_name TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
