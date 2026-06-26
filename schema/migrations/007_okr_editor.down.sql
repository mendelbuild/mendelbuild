-- 007_okr_editor.down.sql
-- Reverts the OKR editor schema changes.

-- Drop indexes first
DROP INDEX IF EXISTS idx_okr_junction_kr;
DROP INDEX IF EXISTS idx_key_results_deleted;
DROP INDEX IF EXISTS idx_key_results_strategy;
DROP INDEX IF EXISTS idx_objectives_deleted;
DROP INDEX IF EXISTS idx_objectives_parent;

-- Re-add objective_id column to key_results
ALTER TABLE key_results ADD COLUMN objective_id UUID REFERENCES objectives(id);

-- Migrate data back from junction table (use first objective if linked to multiple)
UPDATE key_results kr SET objective_id = (
    SELECT objective_id FROM objective_key_result_pairs
    WHERE key_result_id = kr.id
    ORDER BY created_at ASC
    LIMIT 1
);

-- Drop junction table
DROP TABLE objective_key_result_pairs;

-- Remove tuning and soft-delete columns from key_results
ALTER TABLE key_results DROP COLUMN deleted_at;
ALTER TABLE key_results DROP COLUMN tune_feedback;
ALTER TABLE key_results DROP COLUMN tune_score;
ALTER TABLE key_results DROP COLUMN strategy_id;

-- Remove hierarchical and tuning columns from objectives
ALTER TABLE objectives DROP COLUMN deleted_at;
ALTER TABLE objectives DROP COLUMN tune_feedback;
ALTER TABLE objectives DROP COLUMN tune_score;
ALTER TABLE objectives DROP COLUMN parent_id;
