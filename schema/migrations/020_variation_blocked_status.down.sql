-- Remove 'blocked' status from variations
-- Note: This will fail if any variations are in 'blocked' status

ALTER TABLE variations DROP CONSTRAINT IF EXISTS variations_status_check;
ALTER TABLE variations ADD CONSTRAINT variations_status_check
    CHECK (status IN ('creating', 'pending', 'migrating', 'active', 'draining',
                      'error', 'terminated', 'pruned', 'selected', 'merged', 'rejected'));
