-- Recreate the tables as they stood after 015, so the migration is reversible.
-- Reverting this does not restore the code that used them, which was deleted
-- earlier and separately.

CREATE TABLE traffic_allocations (
    id UUID PRIMARY KEY,
    hop_id UUID NOT NULL REFERENCES hops(id),
    bucket_salt TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT traffic_allocations_hop_id_key UNIQUE (hop_id)
);

CREATE TABLE traffic_allocation_slices (
    id UUID PRIMARY KEY,
    traffic_allocation_id UUID NOT NULL REFERENCES traffic_allocations(id) ON DELETE CASCADE,
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,
    fraction REAL NOT NULL CHECK (fraction >= 0 AND fraction <= 1),
    bucket_order INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT traffic_allocation_slices_allocation_variation_key UNIQUE (traffic_allocation_id, variation_id),
    CONSTRAINT traffic_allocation_slices_allocation_order_key UNIQUE (traffic_allocation_id, bucket_order)
);

CREATE TABLE traffic_allocation_envoy_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    config_yaml TEXT NOT NULL,
    generated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    applied_at TIMESTAMPTZ,
    superseded_at TIMESTAMPTZ
);

CREATE INDEX idx_traffic_allocation_envoy_configs_project ON traffic_allocation_envoy_configs(project_id);
