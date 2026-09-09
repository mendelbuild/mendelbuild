-- Retiring a project marks it rather than removing the row.
--
-- A DELETE would cascade through the ledger, the roadmap, and the record of
-- what was deployed and what it cost -- all of it evidence about money that has
-- already been spent, which stays true whether or not anyone still wants the
-- project. Marking keeps that evidence and leaves an administrator able to
-- reverse the decision.
ALTER TABLE projects ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE projects ADD COLUMN deleted_by UUID REFERENCES users(id);

-- Every read path filters on this, so the interesting rows are the live ones.
CREATE INDEX idx_projects_live ON projects (name) WHERE deleted_at IS NULL;
