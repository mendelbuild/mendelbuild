DROP INDEX idx_variations_budget_paused;

ALTER TABLE variations
    DROP CONSTRAINT variations_budget_pause_complete,
    DROP COLUMN budget_ceiling_usd,
    DROP COLUMN budget_paused_usd;
