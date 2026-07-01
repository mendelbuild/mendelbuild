-- Simplify variation_migrations to use plaintext instructions for Claude Code
-- Remove structured kind/params/sequence_num in favor of freeform up/down instructions

-- Drop old columns
ALTER TABLE variation_migrations DROP COLUMN kind;
ALTER TABLE variation_migrations DROP COLUMN params;
ALTER TABLE variation_migrations DROP COLUMN sequence_num;

-- Add new instruction columns
ALTER TABLE variation_migrations ADD COLUMN up_instructions TEXT NOT NULL DEFAULT '';
ALTER TABLE variation_migrations ADD COLUMN down_instructions TEXT NOT NULL DEFAULT '';

-- Remove the default after adding (it was just to handle existing rows)
ALTER TABLE variation_migrations ALTER COLUMN up_instructions DROP DEFAULT;
ALTER TABLE variation_migrations ALTER COLUMN down_instructions DROP DEFAULT;

-- Drop old index
DROP INDEX IF EXISTS idx_var_migrations;

-- Note: datastore targeting (dev vs prod, etc.) is an open question.
-- For now, instructions can reference MENDEL.md or specify connection details inline.
