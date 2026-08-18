-- Remove project_id from input_requests
DROP INDEX IF EXISTS idx_input_requests_project_status;
DROP INDEX IF EXISTS idx_input_requests_project;
ALTER TABLE input_requests DROP COLUMN project_id;
