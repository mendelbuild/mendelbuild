ALTER TABLE project_domains ADD COLUMN acme_record_value TEXT NOT NULL DEFAULT '';
ALTER TABLE project_domains ADD COLUMN acme_record_name TEXT NOT NULL DEFAULT '';
ALTER TABLE project_domains DROP COLUMN certificate_map_name;
DROP TABLE project_domain_challenges;
