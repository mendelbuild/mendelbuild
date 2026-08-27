CREATE TABLE deployed_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,
    cloud_ecosystem TEXT NOT NULL,
    url TEXT NOT NULL,
    public_url TEXT,
    instance_info JSONB,
    deployed_at TIMESTAMP NOT NULL DEFAULT NOW(),
    status TEXT NOT NULL DEFAULT 'deploying'
        CHECK (status IN ('deploying', 'running', 'failed', 'terminated')),
    error_message TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_deployed_instances_variation ON deployed_instances(variation_id);
CREATE INDEX idx_deployed_instances_status ON deployed_instances(status);

ALTER TABLE project_deployment_channels
    ADD COLUMN prod_url TEXT,
    ADD COLUMN prod_deployed_at TIMESTAMPTZ;

DROP TABLE hosting_deployment_logs;
DROP TABLE hosting_deployments;
