-- Revert: rename input_requests back to decisions

-- Remove new columns
ALTER TABLE input_requests DROP COLUMN IF EXISTS instructions;
ALTER TABLE input_requests DROP COLUMN IF EXISTS link;
ALTER TABLE input_requests DROP COLUMN IF EXISTS required_capabilities;

-- Restore original kind CHECK constraint
ALTER TABLE input_requests DROP CONSTRAINT IF EXISTS input_requests_kind_check;
ALTER TABLE input_requests ADD CONSTRAINT decisions_kind_check
    CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review',
                    'variation_review', 'variation_selection'));

-- Rename check constraints back
ALTER TABLE input_requests RENAME CONSTRAINT input_requests_objectivity_score_check TO decisions_objectivity_score_check;
ALTER TABLE input_requests RENAME CONSTRAINT input_requests_importance_score_check TO decisions_importance_score_check;
ALTER TABLE input_requests RENAME CONSTRAINT input_requests_status_check TO decisions_status_check;

-- Rename check constraint on messages table back
ALTER TABLE input_request_messages RENAME CONSTRAINT input_request_messages_role_check TO decision_messages_role_check;

-- Rename foreign key constraint back
ALTER TABLE input_request_messages RENAME CONSTRAINT input_request_messages_input_request_id_fkey TO decision_messages_decision_id_fkey;

-- Rename primary key constraints back
ALTER INDEX input_requests_pkey RENAME TO decisions_pkey;
ALTER INDEX input_request_messages_pkey RENAME TO decision_messages_pkey;

-- Rename indexes back
ALTER INDEX idx_input_requests_status RENAME TO idx_decisions_status;
ALTER INDEX idx_input_requests_subject RENAME TO idx_decisions_subject;
ALTER INDEX idx_input_request_messages_input_request RENAME TO idx_decision_messages_decision;

-- Rename foreign key column back
ALTER TABLE input_request_messages RENAME COLUMN input_request_id TO decision_id;

-- Rename tables back
ALTER TABLE input_request_messages RENAME TO decision_messages;
ALTER TABLE input_requests RENAME TO decisions;
