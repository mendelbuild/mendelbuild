-- Reverts 035. The instants are rendered in the database's own TimeZone,
-- which is the wall clock they were stored as before.

ALTER TABLE budget_allocations ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE budget_allocations ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE demo_instances ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE demo_instances ALTER COLUMN started_at TYPE TIMESTAMP
    USING started_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE demo_instances ALTER COLUMN stopped_at TYPE TIMESTAMP
    USING stopped_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE ecosystems ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE ecosystems ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE funding_sources ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE funding_sources ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE funding_success_criteria ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE hops ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE hops ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE hosting_platforms ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE hosting_platforms ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_request_messages ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_requests ALTER COLUMN accepted_at TYPE TIMESTAMP
    USING accepted_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_requests ALTER COLUMN assigned_at TYPE TIMESTAMP
    USING assigned_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_requests ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_requests ALTER COLUMN resolved_at TYPE TIMESTAMP
    USING resolved_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_requests ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE key_result_history ALTER COLUMN measured_at TYPE TIMESTAMP
    USING measured_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE key_results ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE key_results ALTER COLUMN deleted_at TYPE TIMESTAMP
    USING deleted_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE key_results ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE objective_key_result_pairs ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE objectives ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE objectives ALTER COLUMN deleted_at TYPE TIMESTAMP
    USING deleted_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE objectives ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE project_credentials ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE project_credentials ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE project_members ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE projects ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE projects ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE repositories ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE repositories ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE sessions ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE sessions ALTER COLUMN expires_at TYPE TIMESTAMP
    USING expires_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE strategies ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE strategies ALTER COLUMN draft_started_at TYPE TIMESTAMP
    USING draft_started_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE strategies ALTER COLUMN okrs_approved_at TYPE TIMESTAMP
    USING okrs_approved_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE strategies ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocation_envoy_configs ALTER COLUMN applied_at TYPE TIMESTAMP
    USING applied_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocation_envoy_configs ALTER COLUMN generated_at TYPE TIMESTAMP
    USING generated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocation_envoy_configs ALTER COLUMN superseded_at TYPE TIMESTAMP
    USING superseded_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocation_slices ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocations ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocations ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE users ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE users ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variation_logs ALTER COLUMN logged_at TYPE TIMESTAMP
    USING logged_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variation_migrations ALTER COLUMN applied_at TYPE TIMESTAMP
    USING applied_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variation_migrations ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variation_migrations ALTER COLUMN reverted_at TYPE TIMESTAMP
    USING reverted_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variation_state_history ALTER COLUMN transitioned_at TYPE TIMESTAMP
    USING transitioned_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variations ALTER COLUMN created_at TYPE TIMESTAMP
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variations ALTER COLUMN updated_at TYPE TIMESTAMP
    USING updated_at AT TIME ZONE current_setting('TimeZone');

ALTER TABLE key_results ALTER COLUMN target_date TYPE TIMESTAMP
    USING target_date::timestamp;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables
               WHERE table_schema = current_schema() AND table_name = '_migrations') THEN
        ALTER TABLE _migrations ALTER COLUMN applied_at TYPE TIMESTAMP
            USING applied_at AT TIME ZONE current_setting('TimeZone');
    END IF;
END $$;
