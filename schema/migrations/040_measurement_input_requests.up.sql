-- A recurring ask for Key Result measurements.
--
-- key_result_history has existed since the schema was written and nothing ever
-- wrote to it, so no Key Result could say where it stands. The values arrive
-- through the Input Needed queue rather than a form somebody has to remember to
-- visit: one request per project covering every Key Result, at most one open at
-- a time, rows skippable and backdatable.
--
-- See dev/claude_plans/15_key_result_measurement.md.

ALTER TABLE input_requests DROP CONSTRAINT input_requests_kind_check;

ALTER TABLE input_requests ADD CONSTRAINT input_requests_kind_check
    CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review',
                    'variation_review', 'variation_selection',
                    'credential_request', 'manual_setup', 'confirmation',
                    'hosting_platform', 'measurement'));

-- When the strategy was last asked for measurements. The cadence is judged from
-- this rather than from the open request's created_at, so that resolving a
-- request and filing the next one are separate facts: a request updated in
-- place must not look like a fresh one.
ALTER TABLE strategies ADD COLUMN measurements_asked_at TIMESTAMPTZ;

-- One measurement may not be recorded twice for the same Key Result at the same
-- instant. Answering the same form twice is a double submit, not two readings.
CREATE UNIQUE INDEX idx_kr_history_unique ON key_result_history(key_result_id, measured_at);
