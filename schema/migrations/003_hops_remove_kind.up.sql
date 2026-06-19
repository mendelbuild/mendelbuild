-- Remove kind column from hops (categorization handled by commentary + objectives)
-- Rename kind_params to params (generic metadata field)
-- Make commentary required

ALTER TABLE hops DROP COLUMN kind;
ALTER TABLE hops RENAME COLUMN kind_params TO params;
ALTER TABLE hops ALTER COLUMN commentary SET NOT NULL;
