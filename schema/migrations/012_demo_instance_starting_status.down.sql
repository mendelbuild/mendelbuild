-- Remove suggested_fix column

ALTER TABLE demo_instances DROP COLUMN IF EXISTS suggested_fix;
