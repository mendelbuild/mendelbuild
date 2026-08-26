DROP INDEX IF EXISTS idx_project_deployment_channels_project;
DROP INDEX IF EXISTS idx_supported_deployment_combos_artifact;
DROP INDEX IF EXISTS project_deployment_channels_active_idx;
DROP TABLE IF EXISTS project_deployment_channels;
DROP TABLE IF EXISTS supported_deployment_combos;
DROP TYPE IF EXISTS deploy_artifact_kind;
