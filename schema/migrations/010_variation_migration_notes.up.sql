-- Add migration_notes field to variations for debugging/informational purposes
-- Records where to find migrations in the user's repo and/or datastore

ALTER TABLE variations ADD COLUMN migration_notes TEXT;
