-- Where a project's deployments are reachable is a property of the project, not
-- of whichever channel happens to be selected: the domain outlives the channel,
-- and the same names are wanted whether demos run on Kubernetes today or
-- somewhere else later.
--
-- It was briefly a credential, which was wrong twice over. A domain is not a
-- secret, and a credential has nowhere to say what records the user must create
-- for it to work -- which is the part they actually need help with.
CREATE TABLE project_domains (
    project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,

    -- The domain the user controls, e.g. example.com.
    base_domain TEXT NOT NULL,

    -- One label under it for demos, so a single wildcard record covers them all:
    -- 'mendel-demos' gives *.mendel-demos.example.com.
    demo_subdomain TEXT NOT NULL DEFAULT 'mendel-demos',

    -- One label for production, empty when production has no name yet.
    prod_subdomain TEXT NOT NULL DEFAULT '',

    -- The address the records point at, once Mendel has reserved one. Known
    -- before any deployment exists, which is what lets Mendel state the record
    -- rather than asking the user to go and find an IP.
    static_ip TEXT NOT NULL DEFAULT '',
    static_ip_name TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
