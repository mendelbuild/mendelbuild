-- Remove source tracking from variation_logs

DROP INDEX IF EXISTS idx_variation_logs_source;

ALTER TABLE variation_logs DROP COLUMN IF EXISTS source_id;
ALTER TABLE variation_logs DROP COLUMN IF EXISTS source_type;
