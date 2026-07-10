-- Add suggested_fix column for LLM-generated fix suggestions when demos fail
-- Also documents that 'starting' is now a valid status (no CHECK constraint exists)

ALTER TABLE demo_instances ADD COLUMN suggested_fix TEXT;
