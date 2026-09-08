-- A datastore adapter runs as a job in the project's own deployment channel and
-- reports back over the network (dev/claude_plans/20_datastore_adapters.md D54,
-- D59). This is the record of one such invocation: what was asked, and what came
-- back.
--
-- It exists because the exchange is asynchronous. Mendel deploys a job and the
-- answer arrives later, from a process Mendel does not control, so there has to
-- be something for the report to be matched against -- and something that can
-- say "asked, nothing back yet", which is a different state from either answer.
CREATE TABLE adapter_invocations (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

    -- probe | admit | apply | withdraw | restore. Text rather than an enum for
    -- the same reason the rest of this schema uses text: a new phase should not
    -- need a migration to be reportable.
    phase TEXT NOT NULL,

    -- The token is minted per invocation and never stored. What is kept is its
    -- hash, as sessions already do: a report authenticates by presenting the
    -- token, and a database that cannot produce one cannot be used to forge a
    -- report even by someone reading it.
    token_hash BYTEA NOT NULL UNIQUE,

    -- Short-lived on purpose. A token outliving its job is a standing
    -- credential for an endpoint that accepts findings about a datastore.
    expires_at TIMESTAMPTZ NOT NULL,

    -- What was asked, so a late or malformed report is checked against the
    -- question as asked rather than a reconstruction of it. JSONB normalises
    -- key order and whitespace: this is the same JSON, not the same bytes.
    instruction JSONB NOT NULL,

    -- What came back. Null until something does, which is the third state:
    -- neither a yes nor a no but no answer yet.
    result JSONB,

    -- completed | failed, null while nothing has reported. Lifted out of the
    -- result so that "did this answer" is a query rather than a JSON traversal.
    outcome TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reported_at TIMESTAMPTZ
);

-- A report arrives knowing only its token.
CREATE INDEX idx_adapter_invocations_token ON adapter_invocations(token_hash);

-- The settings page asks for the newest probe of a project, which is the read
-- that happens on every render.
CREATE INDEX idx_adapter_invocations_recent
    ON adapter_invocations(project_id, phase, created_at DESC);
