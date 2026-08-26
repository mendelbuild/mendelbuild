-- Variation revisions track user feedback requests for improving a variation.
-- When a user requests a change, a revision is created and the variation
-- goes back to "creating" status for Claude Code to apply the feedback.

CREATE TABLE variation_revisions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,
    feedback TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'completed', 'failed')),
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_variation_revisions_variation_id ON variation_revisions(variation_id);
CREATE INDEX idx_variation_revisions_status ON variation_revisions(status);
