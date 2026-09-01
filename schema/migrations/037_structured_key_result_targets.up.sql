-- Key Result targets become structured.
--
-- target_units was free text ("1000 users", "< 200ms p99"). The comment on the
-- column claimed Core parsed it into a numeric target, unit and comparator; no
-- such parser was ever written, so a Key Result could be displayed but never
-- compared against a measurement. Nothing could say whether a KR was met.
--
-- The drafting agent was already being asked for a comparator, a value and a
-- unit -- and told to concatenate them into one string. It now returns them
-- separately, and the OKR editor takes them as three fields, so a target is
-- never parsed at either end and the prose form can be derived instead of
-- stored. See dev/claude_plans/15_key_result_measurement.md.
--
-- Existing rows cannot be given honest values. Per the prototype-stage guidance
-- in CLAUDE.md this deletes them rather than carrying a nullable "not yet
-- structured" state through the timeline, the tuner and the measurement ask
-- forever. Projects redraft their OKRs through the flow that already exists.

DELETE FROM funding_success_criteria;
DELETE FROM key_result_history;
DELETE FROM objective_key_result_pairs;
DELETE FROM key_results;

ALTER TABLE key_results DROP COLUMN target_units;

-- The comparison to make against a measurement.
ALTER TABLE key_results ADD COLUMN target_comparator TEXT NOT NULL
    CHECK (target_comparator IN ('>=', '<=', '>', '<', '='));

-- The number to compare against.
ALTER TABLE key_results ADD COLUMN target_value REAL NOT NULL;

-- What the number counts, for display only: comparison is value against value.
-- It therefore absorbs any qualifier -- "ms p99", "signups per week" -- without
-- a further column that nothing would compute on.
ALTER TABLE key_results ADD COLUMN target_unit TEXT NOT NULL;
