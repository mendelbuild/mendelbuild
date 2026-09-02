-- Setup guidance had structure the database could not hold: an ordered list of
-- prerequisites, and the one value the user must supply before the script will
-- run. Both were being expressed as indentation inside a block of prose, which
-- a proportional font renders as ragged columns rather than as a list.
--
-- Holding them as data lets the page render a real list, and lets the script be
-- completed in the browser so what is copied needs no editing.
ALTER TABLE hosting_platforms
    ADD COLUMN setup_prerequisites JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN setup_input_label TEXT NOT NULL DEFAULT '',
    ADD COLUMN setup_input_credential TEXT NOT NULL DEFAULT '';
