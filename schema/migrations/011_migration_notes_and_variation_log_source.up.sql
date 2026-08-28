-- Consolidate migration notes into variation_migrations table
-- Notes describe where to find migration files in the user's CODE repo

-- Add notes column to variation_migrations
ALTER TABLE variation_migrations ADD COLUMN notes TEXT;

-- Remove from variations (was added in 010)
ALTER TABLE variations DROP COLUMN IF EXISTS migration_notes;

-- Add source tracking to variation_logs
-- source_type: what kind of operation generated this log
-- source_id: ID of the specific instance (e.g., demo_instance_id for demo logs)

ALTER TABLE variation_logs
    ADD COLUMN source_type TEXT NOT NULL DEFAULT 'codegen'
        CHECK (source_type IN ('codegen', 'demo', 'fix'));

ALTER TABLE variation_logs
    ADD COLUMN source_id UUID;

-- Index for filtering by source
CREATE INDEX idx_variation_logs_source ON variation_logs(source_type, source_id);
