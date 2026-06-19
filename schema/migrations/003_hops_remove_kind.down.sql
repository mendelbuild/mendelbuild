-- Revert: add kind column back, rename params to kind_params, make commentary nullable

ALTER TABLE hops ALTER COLUMN commentary DROP NOT NULL;
ALTER TABLE hops RENAME COLUMN params TO kind_params;
ALTER TABLE hops ADD COLUMN kind TEXT NOT NULL DEFAULT 'feature';
