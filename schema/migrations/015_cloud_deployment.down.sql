-- Revert cloud deployment infrastructure

-- Remove unique constraints from traffic_allocation_slices
ALTER TABLE traffic_allocation_slices DROP CONSTRAINT IF EXISTS traffic_allocation_slices_allocation_order_key;
ALTER TABLE traffic_allocation_slices DROP CONSTRAINT IF EXISTS traffic_allocation_slices_allocation_variation_key;

-- Remove created_at from traffic_allocation_slices
ALTER TABLE traffic_allocation_slices DROP COLUMN IF EXISTS created_at;

-- Revert foreign key constraints (remove ON DELETE CASCADE)
ALTER TABLE traffic_allocation_slices DROP CONSTRAINT IF EXISTS traffic_allocation_slices_variation_id_fkey;
ALTER TABLE traffic_allocation_slices ADD CONSTRAINT traffic_allocation_slices_variation_id_fkey
    FOREIGN KEY (variation_id) REFERENCES variations(id);

ALTER TABLE traffic_allocation_slices DROP CONSTRAINT IF EXISTS traffic_allocation_slices_traffic_allocation_id_fkey;
ALTER TABLE traffic_allocation_slices ADD CONSTRAINT traffic_allocation_slices_traffic_allocation_id_fkey
    FOREIGN KEY (traffic_allocation_id) REFERENCES traffic_allocations(id);

-- Remove unique constraint from traffic_allocations
ALTER TABLE traffic_allocations DROP CONSTRAINT IF EXISTS traffic_allocations_hop_id_key;

-- Drop new tables
DROP TABLE IF EXISTS traffic_allocation_envoy_configs;
DROP TABLE IF EXISTS deployed_instances;
DROP TABLE IF EXISTS project_credentials;
