-- Things a variation's code needs in order to run anywhere.
--
-- These belong to the variation, not to demos. A variation that wired up
-- Google sign-in needs client credentials and a registered redirect URI to
-- function at all; demos are merely where that first bites, because a demo is
-- the first time the code is pushed through a deployment channel. The same
-- requirements gate a production deploy of that variation once it is merged.
--
-- Two kinds, which differ in what Mendel does about them:
--
--   secret          A value Mendel needs from the user (GOOGLE_CLIENT_SECRET).
--                   Stored encrypted, project-scoped, injected at deploy time.
--
--   acknowledgement An action the user must take somewhere else, where Mendel
--                   already knows the string involved (the deployment's OAuth
--                   redirect URI) and only needs confirmation it was done.
--                   Mendel stores the confirmation, never a secret.
--
-- Declared per variation by code generation, because what is needed depends on
-- the code that was written.

CREATE TABLE variation_requirements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,

    kind TEXT NOT NULL CHECK (kind IN ('secret', 'acknowledgement')),

    -- Stable identifier within a variation: the env var name for a secret,
    -- a slug like 'google-redirect-uri' for an acknowledgement.
    name TEXT NOT NULL,
    description TEXT,

    -- Acknowledgements only. instructions may contain {{deploy_url}}, which
    -- resolves to the URL of whichever deployment is being gated, so the same
    -- requirement yields one string for a demo and another for production.
    -- console_url links to the page where the action is performed.
    instructions TEXT,
    console_url TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (variation_id, kind, name),

    -- An acknowledgement without instructions cannot be acted on.
    CONSTRAINT variation_requirements_ack_has_instructions CHECK (
        kind <> 'acknowledgement' OR instructions IS NOT NULL
    )
);

CREATE INDEX idx_variation_requirements_variation
    ON variation_requirements(variation_id);

-- Values for 'secret' requirements. Project-scoped: an OAuth client ID is the
-- same for every variation and for production, so it is entered once per
-- project. Kept apart from project_credentials, which holds the platform
-- credentials Mendel needs in order to deploy at all, rather than values
-- belonging to the user's own application.
CREATE TABLE project_env_vars (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    encrypted_value BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE INDEX idx_project_env_vars_project ON project_env_vars(project_id);

-- Confirmations that an acknowledgement was carried out.
--
-- Keyed by the exact string confirmed rather than by deployment, because one
-- requirement legitimately has several: the demo URL and the production URL
-- are different redirect URIs and both must be registered. A changed URL
-- leaves no matching row, so the requirement is unmet again rather than
-- silently vouching for a string nobody registered.
CREATE TABLE requirement_acknowledgements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    requirement_id UUID NOT NULL REFERENCES variation_requirements(id) ON DELETE CASCADE,
    resolved_value TEXT NOT NULL,
    acknowledged_by UUID REFERENCES users(id),
    acknowledged_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (requirement_id, resolved_value)
);

CREATE INDEX idx_requirement_acknowledgements_requirement
    ON requirement_acknowledgements(requirement_id);
