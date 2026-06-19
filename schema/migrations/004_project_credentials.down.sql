-- Revert project credentials and variation_review decision kind

ALTER TABLE variations DROP COLUMN approach;
ALTER TABLE variations DROP COLUMN name;

ALTER TABLE decisions DROP CONSTRAINT decisions_kind_check;
ALTER TABLE decisions ADD CONSTRAINT decisions_kind_check
    CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review'));

ALTER TABLE projects DROP COLUMN config;
