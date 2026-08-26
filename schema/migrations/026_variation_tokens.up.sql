-- Track token usage per variation for cost visibility.
-- Tokens accumulate across all code generation runs (initial + revisions).

ALTER TABLE variations
    ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0;
