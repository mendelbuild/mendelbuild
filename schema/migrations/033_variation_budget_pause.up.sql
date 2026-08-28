-- Pausing a code-generation run when it reaches its spend ceiling.
--
-- The run used to be bounded by a fixed 50 API rounds, which bounds the wrong
-- quantity: a round that reads a large file costs many times one that runs
-- `ls`. Worse, reaching the bound was a hard failure -- the variation went to
-- 'error', and retrying a fresh generation deletes the work directory and
-- re-clones, so the code written was thrown away and the next attempt paid full
-- price from zero.
--
-- Spend is now the bound, and reaching it pauses rather than fails: the work
-- directory is kept, the variation goes to 'blocked' (which already means
-- "waiting for an InputRequest"), and a human decides whether it is worth more
-- money after reading the log.
--
-- These columns are what make that state legible rather than inferred from the
-- transition history. budget_paused_usd being set is what marks a variation as
-- paused for spend, as opposed to blocked on credentials.

ALTER TABLE variations
    -- What this run had spent when it stopped. NULL means not paused for spend.
    ADD COLUMN budget_paused_usd REAL CHECK (budget_paused_usd >= 0),

    -- The ceiling that was in force, so the pause message can say what was
    -- exceeded and by how much rather than just naming a number.
    ADD COLUMN budget_ceiling_usd REAL CHECK (budget_ceiling_usd >= 0),

    -- A pause records both figures or neither.
    ADD CONSTRAINT variations_budget_pause_complete CHECK (
        (budget_paused_usd IS NULL) = (budget_ceiling_usd IS NULL)
    );

-- Paused variations are looked up to show what is awaiting a decision.
CREATE INDEX idx_variations_budget_paused ON variations(budget_paused_usd)
    WHERE budget_paused_usd IS NOT NULL;
