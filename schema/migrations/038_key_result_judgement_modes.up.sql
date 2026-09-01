-- How a Key Result is judged, rather than which operator to apply.
--
-- 037 stored a comparison operator and offered five: >=, <=, >, <, =. Three of
-- them earn nothing. "More than 1000" and "at least 1000" differ only on the
-- boundary, and nobody writing a Key Result means the distinction; "exactly"
-- is nearly always "at least" in disguise, and where it genuinely is not -- one
-- release, one launch -- what is meant is "did this happen", which is now its
-- own mode.
--
-- So the column becomes a judgement mode with three values, named rather than
-- punctuated because one of them is not an operator at all:
--
--   at_least  the number should reach the target or pass it
--   at_most   the number should stay at the target or below
--   done      it happened, or it has not
--
-- A `done` Key Result is deliberately available and deliberately weaker. It
-- carries no early signal: a number tells you on the Tuesday of week three
-- whether you are on course, and a checkbox tells you nothing at all until it
-- flips. The OKR tuner is told to say so.

ALTER TABLE key_results DROP CONSTRAINT key_results_target_comparator_check;

-- Fold the strict operators into their inclusive forms, which is the collapse
-- being made rather than a loss: a target of "> 1000" meant "1000 or better".
UPDATE key_results SET target_comparator = 'at_least' WHERE target_comparator IN ('>=', '>', '=');
UPDATE key_results SET target_comparator = 'at_most'  WHERE target_comparator IN ('<=', '<');

ALTER TABLE key_results ADD CONSTRAINT key_results_target_comparator_check
    CHECK (target_comparator IN ('at_least', 'at_most', 'done'));

-- A `done` Key Result stores a target of 1 and no unit, so every row keeps a
-- value and the comparison stays one function rather than two. Measurements
-- record 0 or 1.
