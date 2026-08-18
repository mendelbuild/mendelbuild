-- Add 'blocked' status to variations
-- Used when a variation is waiting for an InputRequest (e.g., credentials)

ALTER TABLE variations DROP CONSTRAINT IF EXISTS variations_status_check;
ALTER TABLE variations ADD CONSTRAINT variations_status_check
    CHECK (status IN ('creating', 'pending', 'blocked', 'migrating', 'active', 'draining',
                      'error', 'terminated', 'pruned', 'selected', 'merged', 'rejected'));
