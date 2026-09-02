-- Live-traffic experiments: the persistence internal/experiment has never had.
--
-- The package could already admit a migration, prove it additive, apply it under
-- a lock, archive it and roll it back -- against a real database, with a
-- three-Arm concurrency test. What it could not do was be reached: no schema, no
-- caller, nothing that could create an experiment. These are the rows that make
-- it a feature rather than a library.

-- An experiment is a Hop taking live traffic.
CREATE TABLE experiments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    hop_id UUID NOT NULL REFERENCES hops(id) ON DELETE CASCADE,

    -- The Assignment Unit is a correctness constraint rather than a preference:
    -- what the edge hashes, what durable writes are keyed by, and the
    -- denominator of the success metric must be the same thing. The key source
    -- is app-specific, which is why it is declared rather than assumed.
    assignment_unit TEXT NOT NULL,
    assignment_key_source TEXT NOT NULL,
    assignment_key_name TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'draft',

    -- Pre-registered statistics. The hazard is peeking: an autonomous agent
    -- checking daily and stopping at p<0.05 has a badly inflated false-positive
    -- rate, and Mendel is autonomous by construction. Recorded before the
    -- experiment runs so the rule cannot be chosen once the data is in.
    minimum_detectable_effect DOUBLE PRECISION,
    stopping_rule TEXT NOT NULL DEFAULT '',
    planned_duration_hours INTEGER,

    -- What a person who experienced the Variation will feel when the Arm stops
    -- serving, and the phrase the Mendel user typed to acknowledge it. Same
    -- shape as requirement_acknowledgements: keyed by the exact string
    -- confirmed, recording who and when. The typed phrase is friction against a
    -- reflexive click, not a comprehension test.
    dissonance_description TEXT NOT NULL DEFAULT '',
    dissonance_phrase TEXT NOT NULL DEFAULT '',
    acknowledged_by UUID REFERENCES users(id),
    acknowledged_at TIMESTAMPTZ,

    started_at TIMESTAMPTZ,
    stopped_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT experiments_assignment_unit_known
        CHECK (assignment_unit IN ('user', 'session', 'request', 'tenant')),
    CONSTRAINT experiments_stopping_rule_known
        CHECK (stopping_rule IN ('', 'fixed_horizon', 'sequential')),

    -- "Required before an experiment starts, not after." A constraint rather
    -- than a convention, because the thing being guarded against is an
    -- autonomous agent deciding the rule once it has seen the data.
    CONSTRAINT experiments_declared_before_running CHECK (
        status IN ('draft', 'declined')
        OR (minimum_detectable_effect IS NOT NULL
            AND planned_duration_hours IS NOT NULL
            AND stopping_rule <> ''
            AND (dissonance_description = '' OR acknowledged_at IS NOT NULL))
    )
);

CREATE INDEX idx_experiments_project ON experiments(project_id);
CREATE INDEX idx_experiments_hop ON experiments(hop_id);

-- One Arm per Variation, plus mainline.
CREATE TABLE experiment_arms (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    experiment_id UUID NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,

    -- NULL is the mainline control: it has no Variation because it is the code
    -- that was already there.
    variation_id UUID REFERENCES variations(id) ON DELETE RESTRICT,

    -- What appears in the assignment cookie and in the HTTPRoute match.
    slug TEXT NOT NULL,

    allocation_weight INTEGER NOT NULL DEFAULT 0,

    -- Filled once the Arm is deployed; empty before that.
    deployment_name TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (experiment_id, slug),
    CONSTRAINT experiment_arms_weight_is_a_share
        CHECK (allocation_weight BETWEEN 0 AND 100)
);

-- Exactly one control per experiment. Two would make "mainline" ambiguous in
-- the one place the comparison depends on it being not.
CREATE UNIQUE INDEX idx_experiment_arms_one_control
    ON experiment_arms (experiment_id) WHERE variation_id IS NULL;

CREATE INDEX idx_experiment_arms_experiment ON experiment_arms(experiment_id);

-- The Admission, as internal/experiment produced it.
--
-- Append-only, for the same reason as hop_cost_estimates and rate cards: a
-- judgment already acted on stays inspectable exactly as it was made. A
-- re-admission after schema drift is a new row, not an edit.
CREATE TABLE arm_admissions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    arm_id UUID NOT NULL REFERENCES experiment_arms(id) ON DELETE CASCADE,

    migration_up TEXT NOT NULL,
    migration_down TEXT NOT NULL,

    -- The recorded Delta, and the shapes of the touched collections as they
    -- stood. Mendel is not the only writer of the user's datastore, so the
    -- shapes are re-read before applying and the difference is what decides
    -- whether the judgment still holds.
    delta JSONB NOT NULL,
    shapes JSONB NOT NULL,

    verdict TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    admitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT arm_admissions_verdict_known CHECK (verdict IN ('admitted', 'declined'))
);

CREATE INDEX idx_arm_admissions_arm ON arm_admissions(arm_id);

-- Where an Arm's data went when it was rolled back.
CREATE TABLE arm_archives (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    arm_id UUID NOT NULL REFERENCES experiment_arms(id) ON DELETE CASCADE,

    location TEXT NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,

    -- An archive is kept for a while and then is not. Recording the intent
    -- means a user can be told what will happen before it does.
    expires_at TIMESTAMPTZ,
    downloaded_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_arm_archives_arm ON arm_archives(arm_id);

-- What happened to the experiment while it ran.
--
-- Without this, "the control changed underneath the comparison" has nowhere to
-- be recorded: a mainline deploy landing mid-experiment is allowed to proceed,
-- and the annotation is the whole reason that is safe to allow.
CREATE TABLE experiment_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    experiment_id UUID NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,

    -- Null for events about the experiment rather than one Arm.
    arm_id UUID REFERENCES experiment_arms(id) ON DELETE SET NULL,

    kind TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    data JSONB,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_experiment_events_experiment ON experiment_events(experiment_id, occurred_at);
