-- Deploy artifact kinds: what the project produces for deployment
CREATE TYPE deploy_artifact_kind AS ENUM (
    'container',       -- Single Dockerfile -> Fly.io, Cloud Run, Render
    'kubernetes',      -- k8s manifests (Helm, raw YAML, kustomize) -> GKE, EKS, AKS
    'static',          -- Static files -> Vercel, Netlify, S3+CloudFront
    'source_deploy'    -- No container, platform builds -> Vercel, Render, Railway
);

-- Supported (artifact_kind, hosting_platform) combinations
-- The sparse matrix - only these combos are validated to work
CREATE TABLE supported_deployment_combos (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    artifact_kind deploy_artifact_kind NOT NULL,
    hosting_platform_id UUID NOT NULL REFERENCES hosting_platforms(id) ON DELETE CASCADE,
    notes TEXT,
    guidance JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(artifact_kind, hosting_platform_id)
);

-- Project's deployment configuration (keeps history)
CREATE TABLE project_deployment_channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    artifact_kind deploy_artifact_kind NOT NULL,
    hosting_platform_id UUID NOT NULL REFERENCES hosting_platforms(id) ON DELETE CASCADE,

    -- Validation state
    demo_validated_at TIMESTAMPTZ,
    prod_validated_at TIMESTAMPTZ,

    -- Production state
    prod_url TEXT,
    prod_deployed_at TIMESTAMPTZ,

    -- History: null = current active channel
    disabled_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Only one active channel per project
CREATE UNIQUE INDEX project_deployment_channels_active_idx
    ON project_deployment_channels(project_id)
    WHERE disabled_at IS NULL;

CREATE INDEX idx_supported_deployment_combos_artifact ON supported_deployment_combos(artifact_kind);
CREATE INDEX idx_project_deployment_channels_project ON project_deployment_channels(project_id);
