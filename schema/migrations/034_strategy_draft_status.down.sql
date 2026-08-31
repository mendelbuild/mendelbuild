ALTER TABLE strategies DROP CONSTRAINT strategies_draft_status_valid;
ALTER TABLE strategies DROP COLUMN draft_started_at;
ALTER TABLE strategies DROP COLUMN draft_error;
ALTER TABLE strategies DROP COLUMN draft_status;
