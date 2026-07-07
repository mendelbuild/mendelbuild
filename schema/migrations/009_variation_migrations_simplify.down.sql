-- Restore original variation_migrations structure

ALTER TABLE variation_migrations DROP COLUMN up_instructions;
ALTER TABLE variation_migrations DROP COLUMN down_instructions;

ALTER TABLE variation_migrations ADD COLUMN kind TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE variation_migrations ADD COLUMN params JSONB NOT NULL DEFAULT '{}';
ALTER TABLE variation_migrations ADD COLUMN sequence_num INTEGER NOT NULL DEFAULT 0;

ALTER TABLE variation_migrations ALTER COLUMN kind DROP DEFAULT;
ALTER TABLE variation_migrations ALTER COLUMN params DROP DEFAULT;
ALTER TABLE variation_migrations ALTER COLUMN sequence_num DROP DEFAULT;

CREATE INDEX idx_var_migrations ON variation_migrations(variation_id, sequence_num);
