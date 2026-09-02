-- The record that proves the domain is yours, so a wildcard certificate can be
-- issued for it. Its value is not derivable: Certificate Manager mints a unique
-- target when the authorization is created, and only then can Mendel say what to
-- put in the record. Storing it is what lets the Domain page list it beside the
-- others rather than leaving the user to find it somewhere else.
ALTER TABLE project_domains
    ADD COLUMN acme_record_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN acme_record_value TEXT NOT NULL DEFAULT '',
    ADD COLUMN certificate_name TEXT NOT NULL DEFAULT '';
