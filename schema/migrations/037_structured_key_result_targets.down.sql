-- Reverting restores the prose column but not its contents: the rows that had
-- one were deleted going up, and a target reassembled from the structured
-- fields would be a different string from whatever was there before.

ALTER TABLE key_results DROP COLUMN target_unit;
ALTER TABLE key_results DROP COLUMN target_value;
ALTER TABLE key_results DROP COLUMN target_comparator;

ALTER TABLE key_results ADD COLUMN target_units TEXT NOT NULL DEFAULT '';
ALTER TABLE key_results ALTER COLUMN target_units DROP DEFAULT;
