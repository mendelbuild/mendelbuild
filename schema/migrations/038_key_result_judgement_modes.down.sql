-- `done` has no operator to go back to, so those rows become ">= 1", which is
-- what they were already storing underneath.

ALTER TABLE key_results DROP CONSTRAINT key_results_target_comparator_check;

UPDATE key_results SET target_comparator = '>=' WHERE target_comparator IN ('at_least', 'done');
UPDATE key_results SET target_comparator = '<=' WHERE target_comparator = 'at_most';

ALTER TABLE key_results ADD CONSTRAINT key_results_target_comparator_check
    CHECK (target_comparator IN ('>=', '<=', '>', '<', '='));
