-- A certificate covers more than one zone, so a project has more than one
-- ownership challenge.
--
-- Production lives at app.example.com and demos at *.mendel-demos.example.com.
-- A wildcard covers exactly one label, so no single wildcard covers both, and
-- Certificate Manager authorizes one domain name per authorization. Two zones
-- means two authorizations and two _acme-challenge records, which the single
-- acme_record_name/value pair could not represent.
CREATE TABLE project_domain_challenges (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

    -- The domain the authorization is for: the base domain, or the demo zone.
    domain TEXT NOT NULL,

    -- The record the user creates. Minted by Certificate Manager, so the value
    -- cannot be worked out in advance.
    record_name TEXT NOT NULL,
    record_value TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (project_id, domain)
);

-- The gateway references a certificate *map*, not a certificate. Passing it a
-- certificate name annotated a map that did not exist, so the gateway served no
-- HTTPS at all and said nothing about why. The map is stable across certificate
-- replacements; the certificate is not, since its domain list is immutable.
ALTER TABLE project_domains ADD COLUMN certificate_map_name TEXT NOT NULL DEFAULT '';

-- Superseded by project_domain_challenges.
ALTER TABLE project_domains DROP COLUMN acme_record_name;
ALTER TABLE project_domains DROP COLUMN acme_record_value;
