-- Rename decisions to input_requests
-- This reflects that these are not just "decisions" but any input Mendel needs
-- (credentials, confirmations, manual setup tasks, etc.)

-- Rename tables
ALTER TABLE decisions RENAME TO input_requests;
ALTER TABLE decision_messages RENAME TO input_request_messages;

-- Rename foreign key column
ALTER TABLE input_request_messages RENAME COLUMN decision_id TO input_request_id;

-- Rename indexes
ALTER INDEX idx_decisions_status RENAME TO idx_input_requests_status;
ALTER INDEX idx_decisions_subject RENAME TO idx_input_requests_subject;
ALTER INDEX idx_decision_messages_decision RENAME TO idx_input_request_messages_input_request;

-- Rename primary key constraints
ALTER INDEX decisions_pkey RENAME TO input_requests_pkey;
ALTER INDEX decision_messages_pkey RENAME TO input_request_messages_pkey;

-- Rename check constraints on input_requests
ALTER TABLE input_requests RENAME CONSTRAINT decisions_objectivity_score_check TO input_requests_objectivity_score_check;
ALTER TABLE input_requests RENAME CONSTRAINT decisions_importance_score_check TO input_requests_importance_score_check;
ALTER TABLE input_requests RENAME CONSTRAINT decisions_status_check TO input_requests_status_check;

-- Rename check constraint on input_request_messages
ALTER TABLE input_request_messages RENAME CONSTRAINT decision_messages_role_check TO input_request_messages_role_check;

-- Rename foreign key constraint
ALTER TABLE input_request_messages RENAME CONSTRAINT decision_messages_decision_id_fkey TO input_request_messages_input_request_id_fkey;

-- Add new columns for credential/setup requests
ALTER TABLE input_requests ADD COLUMN instructions TEXT;
ALTER TABLE input_requests ADD COLUMN link TEXT;
ALTER TABLE input_requests ADD COLUMN required_capabilities TEXT[];

-- Update kind CHECK constraint to include new types
ALTER TABLE input_requests DROP CONSTRAINT IF EXISTS decisions_kind_check;
ALTER TABLE input_requests ADD CONSTRAINT input_requests_kind_check
    CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review',
                    'variation_review', 'variation_selection',
                    'credential_request', 'manual_setup', 'confirmation'));
