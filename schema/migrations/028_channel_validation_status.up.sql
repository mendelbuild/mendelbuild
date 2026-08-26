-- Add validation status tracking to deployment channels
ALTER TABLE project_deployment_channels
ADD COLUMN demo_validating_at TIMESTAMPTZ,
ADD COLUMN demo_validation_error TEXT,
ADD COLUMN prod_validating_at TIMESTAMPTZ,
ADD COLUMN prod_validation_error TEXT;
