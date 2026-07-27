-- Add project_id directly to input_requests for simpler queries
-- Denormalized but makes project-scoped queries much cleaner

ALTER TABLE input_requests ADD COLUMN project_id UUID REFERENCES projects(id);

-- Backfill from existing data
UPDATE input_requests ir SET project_id = (
    CASE
        WHEN ir.subject_type = 'strategy' THEN (
            SELECT s.project_id FROM strategies s WHERE s.id = ir.subject_id
        )
        WHEN ir.subject_type = 'hop' THEN (
            SELECT s.project_id FROM hops h
            JOIN strategies s ON h.strategy_id = s.id
            WHERE h.id = ir.subject_id
        )
        WHEN ir.subject_type = 'variation' THEN (
            SELECT s.project_id FROM variations v
            JOIN hops h ON v.hop_id = h.id
            JOIN strategies s ON h.strategy_id = s.id
            WHERE v.id = ir.subject_id
        )
    END
);

-- Make it NOT NULL now that it's backfilled
ALTER TABLE input_requests ALTER COLUMN project_id SET NOT NULL;

-- Index for project-scoped queries
CREATE INDEX idx_input_requests_project ON input_requests(project_id);
CREATE INDEX idx_input_requests_project_status ON input_requests(project_id, status);
