-- Remove diff stats from variations

ALTER TABLE variations DROP COLUMN IF EXISTS diff_files_changed;
ALTER TABLE variations DROP COLUMN IF EXISTS diff_additions;
ALTER TABLE variations DROP COLUMN IF EXISTS diff_deletions;
