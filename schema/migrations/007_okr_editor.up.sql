-- 007_okr_editor.up.sql
-- Adds hierarchical objectives, many-to-many KR relationships, tuning feedback, and soft-delete.

-- Add hierarchical structure and tuning feedback to objectives
ALTER TABLE objectives ADD COLUMN parent_id UUID REFERENCES objectives(id);
ALTER TABLE objectives ADD COLUMN tune_score REAL;
ALTER TABLE objectives ADD COLUMN tune_feedback TEXT;
ALTER TABLE objectives ADD COLUMN deleted_at TIMESTAMP;

-- Add strategy_id to key_results (replacing objective_id)
-- First, populate strategy_id from the objective's strategy
ALTER TABLE key_results ADD COLUMN strategy_id UUID REFERENCES strategies(id);
UPDATE key_results kr SET strategy_id = (
    SELECT o.strategy_id FROM objectives o WHERE o.id = kr.objective_id
);
ALTER TABLE key_results ALTER COLUMN strategy_id SET NOT NULL;

-- Add tuning feedback and soft delete to key_results
ALTER TABLE key_results ADD COLUMN tune_score REAL;
ALTER TABLE key_results ADD COLUMN tune_feedback TEXT;
ALTER TABLE key_results ADD COLUMN deleted_at TIMESTAMP;

-- Junction table for many-to-many objective-KR relationship
CREATE TABLE objective_key_result_pairs (
    objective_id UUID NOT NULL REFERENCES objectives(id),
    key_result_id UUID NOT NULL REFERENCES key_results(id),
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (objective_id, key_result_id)
);

-- Migrate existing KRs to junction table
INSERT INTO objective_key_result_pairs (objective_id, key_result_id)
SELECT objective_id, id FROM key_results WHERE objective_id IS NOT NULL;

-- Drop old objective_id column (replaced by junction table)
ALTER TABLE key_results DROP COLUMN objective_id;

-- Indexes
CREATE INDEX idx_objectives_parent ON objectives(parent_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_objectives_deleted ON objectives(deleted_at);
CREATE INDEX idx_key_results_strategy ON key_results(strategy_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_key_results_deleted ON key_results(deleted_at);
CREATE INDEX idx_okr_junction_kr ON objective_key_result_pairs(key_result_id);
