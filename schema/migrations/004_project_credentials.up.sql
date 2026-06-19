-- Add project-wide credentials storage
-- Used for API keys (anthropic_api_key) and other project-level secrets
-- Note: v0.1 stores in plaintext; future versions should encrypt at rest

ALTER TABLE projects ADD COLUMN config JSONB;

-- Add variation_review to decisions kind constraint
ALTER TABLE decisions DROP CONSTRAINT decisions_kind_check;
ALTER TABLE decisions ADD CONSTRAINT decisions_kind_check
    CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review', 'variation_review'));

-- Add name and approach columns to variations for code generation
ALTER TABLE variations ADD COLUMN name TEXT;
ALTER TABLE variations ADD COLUMN approach TEXT;
