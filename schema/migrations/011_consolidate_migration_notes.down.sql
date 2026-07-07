-- Restore migration_notes to variations table
ALTER TABLE variations ADD COLUMN migration_notes TEXT;

-- Remove from variation_migrations
ALTER TABLE variation_migrations DROP COLUMN IF EXISTS notes;
