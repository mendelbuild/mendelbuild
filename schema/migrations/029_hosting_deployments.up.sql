-- Unified record of deployments made through a project's deployment channel.
-- Covers production deploys today; shaped so demo deploys can move onto it
-- (kind = 'demo' with variation_id set) and retire demo_instances.

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

-- Production state now lives in hosting_deployments; drop the denormalized copies.
ALTER TABLE project_deployment_channels
    DROP COLUMN prod_url,
    DROP COLUMN prod_deployed_at;

-- deployed_instances was only written and read by the retired script-based
-- deploy/envoy packages. Nothing references it.
DROP TABLE deployed_instances;
