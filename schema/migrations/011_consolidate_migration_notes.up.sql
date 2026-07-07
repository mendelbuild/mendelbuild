-- Consolidate migration notes into variation_migrations table
-- Notes describe where to find migration files in the user's CODE repo

-- Add notes column to variation_migrations
ALTER TABLE variation_migrations ADD COLUMN notes TEXT;

-- Remove from variations (was added in 010)
ALTER TABLE variations DROP COLUMN IF EXISTS migration_notes;
