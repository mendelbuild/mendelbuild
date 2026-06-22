-- Variation logs for tracking code generation progress
CREATE TABLE variation_logs (
    id UUID PRIMARY KEY,
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,
    logged_at TIMESTAMP NOT NULL DEFAULT NOW(),
    level TEXT NOT NULL CHECK (level IN ('info', 'milestone', 'error', 'heartbeat')),
    message TEXT NOT NULL
);

CREATE INDEX idx_variation_logs_variation_id ON variation_logs(variation_id);
CREATE INDEX idx_variation_logs_logged_at ON variation_logs(variation_id, logged_at DESC);
