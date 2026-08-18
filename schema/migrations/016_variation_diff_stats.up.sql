-- Add diff stats to variations
-- Populated during code generation when we have the local clone

ALTER TABLE variations ADD COLUMN diff_files_changed INTEGER;
ALTER TABLE variations ADD COLUMN diff_additions INTEGER;
ALTER TABLE variations ADD COLUMN diff_deletions INTEGER;
