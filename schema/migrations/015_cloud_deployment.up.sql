-- Cloud deployment infrastructure: credentials, deployed instances, envoy configs
-- Note: traffic_allocations and traffic_allocation_slices already exist in 001_initial

-- Encrypted credentials for cloud deployments
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

-- Deployed variation instances in cloud environments
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

-- Generated Envoy configs (for audit/rollback)
CREATE TABLE traffic_allocation_envoy_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    config_yaml TEXT NOT NULL,
    generated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    applied_at TIMESTAMP,
    superseded_at TIMESTAMP
);

CREATE INDEX idx_traffic_allocation_envoy_configs_project ON traffic_allocation_envoy_configs(project_id);

-- Add UNIQUE constraint to traffic_allocations.hop_id if not present
-- (001_initial didn't have this constraint)
ALTER TABLE traffic_allocations ADD CONSTRAINT traffic_allocations_hop_id_key UNIQUE (hop_id);

-- Add ON DELETE CASCADE to traffic_allocation_slices
-- First drop and recreate the foreign key constraints
ALTER TABLE traffic_allocation_slices DROP CONSTRAINT IF EXISTS traffic_allocation_slices_traffic_allocation_id_fkey;
ALTER TABLE traffic_allocation_slices ADD CONSTRAINT traffic_allocation_slices_traffic_allocation_id_fkey
    FOREIGN KEY (traffic_allocation_id) REFERENCES traffic_allocations(id) ON DELETE CASCADE;

ALTER TABLE traffic_allocation_slices DROP CONSTRAINT IF EXISTS traffic_allocation_slices_variation_id_fkey;
ALTER TABLE traffic_allocation_slices ADD CONSTRAINT traffic_allocation_slices_variation_id_fkey
    FOREIGN KEY (variation_id) REFERENCES variations(id) ON DELETE CASCADE;

-- Add created_at to traffic_allocation_slices if not present
ALTER TABLE traffic_allocation_slices ADD COLUMN IF NOT EXISTS created_at TIMESTAMP NOT NULL DEFAULT NOW();

-- Add unique constraints for deterministic bucketing
ALTER TABLE traffic_allocation_slices ADD CONSTRAINT traffic_allocation_slices_allocation_variation_key
    UNIQUE (traffic_allocation_id, variation_id);
ALTER TABLE traffic_allocation_slices ADD CONSTRAINT traffic_allocation_slices_allocation_order_key
    UNIQUE (traffic_allocation_id, bucket_order);
