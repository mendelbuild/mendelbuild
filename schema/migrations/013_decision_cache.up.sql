-- Add cache column to decisions for storing computed/ephemeral data
-- For variation_selection decisions, this caches LLM-computed evaluation scores
-- Structure varies by decision kind; NULL when no cache exists
ALTER TABLE decisions ADD COLUMN cache JSONB;
