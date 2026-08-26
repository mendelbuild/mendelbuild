ALTER TABLE project_deployment_channels
DROP COLUMN demo_validating_at,
DROP COLUMN demo_validation_error,
DROP COLUMN prod_validating_at,
DROP COLUMN prod_validation_error;
