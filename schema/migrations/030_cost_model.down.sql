ALTER TABLE input_requests DROP COLUMN cost_audit;

-- Reverts the USD cost model back to the token-denominated budget tables.
-- Ledger data is dropped: it has no representation in the old schema.

ALTER TABLE variations
    ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0;

CREATE TABLE budget_spend_log (
    id UUID PRIMARY KEY,
    budget_allocation_id UUID NOT NULL REFERENCES budget_allocations(id),

    amount REAL NOT NULL,
    recorded_at TIMESTAMP NOT NULL DEFAULT NOW(),

    description TEXT
);

CREATE INDEX idx_spend_log_allocation ON budget_spend_log(budget_allocation_id, recorded_at);

DROP TABLE cost_entries;

ALTER TABLE budget_allocations
    DROP COLUMN limit_usd,
    ADD COLUMN limit_amount REAL NOT NULL DEFAULT 0;
ALTER TABLE budget_allocations ALTER COLUMN limit_amount DROP DEFAULT;

DROP TABLE hop_cost_estimates;

ALTER TABLE funding_success_criteria
    DROP CONSTRAINT funding_success_criteria_unique;

DELETE FROM funding_success_criteria;
DELETE FROM budget_allocations;
DELETE FROM funding_sources;

ALTER TABLE funding_sources
    DROP CONSTRAINT funding_sources_period_ordered,
    DROP COLUMN period_end,
    DROP COLUMN period_start,
    DROP COLUMN amount_usd,
    DROP COLUMN name,
    ADD COLUMN amount REAL NOT NULL,
    ADD COLUMN resource_type TEXT NOT NULL
        CHECK (resource_type IN ('dollars', 'claude_tokens'));

DROP TABLE hosting_rate_cards;
DROP TABLE model_rate_cards;
