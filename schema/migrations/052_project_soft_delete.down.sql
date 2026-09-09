DROP INDEX IF EXISTS idx_projects_live;
ALTER TABLE projects DROP COLUMN deleted_by;
ALTER TABLE projects DROP COLUMN deleted_at;
