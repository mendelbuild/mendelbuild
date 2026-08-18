-- Store evaluation scores per variation for caching
-- Avoids re-evaluating variations when new ones complete

ALTER TABLE variations ADD COLUMN evaluation_scores JSONB;
