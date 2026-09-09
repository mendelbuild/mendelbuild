-- Recreates demo_instances as it stood after 035. Demo rows written into
-- hosting_deployments are not moved back: they are the same deployments, and
-- copying them would double-count every one of them in the hosting meter.

CREATE TABLE demo_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id),
    url TEXT NOT NULL,
    teardown_instructions TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    stopped_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'starting',
    process_info JSONB,
    error_message TEXT,
    suggested_fix TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_demo_instances_variation ON demo_instances(variation_id);
CREATE INDEX idx_demo_instances_status ON demo_instances(status) WHERE status = 'running';

DROP INDEX idx_hosting_deployments_variation_status;

ALTER TABLE hosting_deployments DROP COLUMN suggested_fix;
