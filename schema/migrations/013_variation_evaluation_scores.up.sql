-- Cache for variation evaluation scores against criteria
-- Scores are computed by LLM and cached here to avoid repeated API calls
CREATE TABLE variation_evaluation_scores (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,
    criterion_name TEXT NOT NULL,
    score DECIMAL(3,2) NOT NULL CHECK (score >= 0 AND score <= 1),
    rationale TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Only one score per variation per criterion
    UNIQUE (variation_id, criterion_name)
);

CREATE INDEX idx_variation_eval_scores_variation ON variation_evaluation_scores(variation_id);
