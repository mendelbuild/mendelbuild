DROP INDEX idx_kr_history_unique;

ALTER TABLE strategies DROP COLUMN measurements_asked_at;

DELETE FROM input_requests WHERE kind = 'measurement';

ALTER TABLE input_requests DROP CONSTRAINT input_requests_kind_check;

ALTER TABLE input_requests ADD CONSTRAINT input_requests_kind_check
    CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review',
                    'variation_review', 'variation_selection',
                    'credential_request', 'manual_setup', 'confirmation',
                    'hosting_platform'));
