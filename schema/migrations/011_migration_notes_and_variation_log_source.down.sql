-- Remove source tracking from variation_logs

DROP INDEX IF EXISTS idx_variation_logs_source;

ALTER TABLE variation_logs DROP COLUMN IF EXISTS source_id;
ALTER TABLE variation_logs DROP COLUMN IF EXISTS source_type;

-- Restore migration_notes to variations table
ALTER TABLE variations ADD COLUMN migration_notes TEXT;

-- Remove from variation_migrations
ALTER TABLE variation_migrations DROP COLUMN IF EXISTS notes;
