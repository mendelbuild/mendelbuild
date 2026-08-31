-- Timestamps record instants, not wall-clock readings [035]
--
-- Every timestamp column was `timestamp without time zone`, which stores the
-- digits of a clock face and forgets which clock. pgx writes a Go time.Time as
-- its *local* wall clock and reads one back labelled *UTC*, so a value written
-- and read on a host seven hours off UTC comes back seven hours wrong. Nothing
-- warns you; the number simply differs from the one you stored.
--
-- Two live consequences before this migration:
--
--   * sessions.expires_at is compared against time.Now() in auth.go, so
--     sessions outlived their expiry by the host's UTC offset.
--   * strategies.draft_started_at was compared the same way, and a draft
--     thirty seconds old read as hours stale -- the review screen said the
--     draft had failed while it was still running.
--
-- Both are the same bug, and neither is visible on a UTC host, which is why
-- staging never showed them and a laptop did.
--
-- The fix is to store instants as `timestamptz`, which records a point in time
-- rather than a reading. Postgres normalises on the way in and renders in the
-- reader's zone on the way out, so Go comparisons are simply correct.
--
-- The existing values were written as local wall clock by both NOW() and pgx,
-- so they are interpreted in the database's own TimeZone -- which is what the
-- default conversion does, spelled out here so the assumption is on the record.

ALTER TABLE budget_allocations ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE budget_allocations ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE demo_instances ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE demo_instances ALTER COLUMN started_at TYPE TIMESTAMPTZ
    USING started_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE demo_instances ALTER COLUMN stopped_at TYPE TIMESTAMPTZ
    USING stopped_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE ecosystems ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE ecosystems ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE funding_sources ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE funding_sources ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE funding_success_criteria ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE hops ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE hops ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE hosting_platforms ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE hosting_platforms ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_request_messages ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_requests ALTER COLUMN accepted_at TYPE TIMESTAMPTZ
    USING accepted_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_requests ALTER COLUMN assigned_at TYPE TIMESTAMPTZ
    USING assigned_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_requests ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_requests ALTER COLUMN resolved_at TYPE TIMESTAMPTZ
    USING resolved_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE input_requests ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE key_result_history ALTER COLUMN measured_at TYPE TIMESTAMPTZ
    USING measured_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE key_results ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE key_results ALTER COLUMN deleted_at TYPE TIMESTAMPTZ
    USING deleted_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE key_results ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE objective_key_result_pairs ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE objectives ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE objectives ALTER COLUMN deleted_at TYPE TIMESTAMPTZ
    USING deleted_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE objectives ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE project_credentials ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE project_credentials ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE project_members ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE projects ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE projects ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE repositories ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE repositories ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE sessions ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE sessions ALTER COLUMN expires_at TYPE TIMESTAMPTZ
    USING expires_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE strategies ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE strategies ALTER COLUMN draft_started_at TYPE TIMESTAMPTZ
    USING draft_started_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE strategies ALTER COLUMN okrs_approved_at TYPE TIMESTAMPTZ
    USING okrs_approved_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE strategies ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocation_envoy_configs ALTER COLUMN applied_at TYPE TIMESTAMPTZ
    USING applied_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocation_envoy_configs ALTER COLUMN generated_at TYPE TIMESTAMPTZ
    USING generated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocation_envoy_configs ALTER COLUMN superseded_at TYPE TIMESTAMPTZ
    USING superseded_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocation_slices ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocations ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE traffic_allocations ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE users ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE users ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variation_logs ALTER COLUMN logged_at TYPE TIMESTAMPTZ
    USING logged_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variation_migrations ALTER COLUMN applied_at TYPE TIMESTAMPTZ
    USING applied_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variation_migrations ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variation_migrations ALTER COLUMN reverted_at TYPE TIMESTAMPTZ
    USING reverted_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variation_state_history ALTER COLUMN transitioned_at TYPE TIMESTAMPTZ
    USING transitioned_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variations ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at AT TIME ZONE current_setting('TimeZone');
ALTER TABLE variations ALTER COLUMN updated_at TYPE TIMESTAMPTZ
    USING updated_at AT TIME ZONE current_setting('TimeZone');

-- key_results.target_date is the exception: it is a calendar date, not an
-- instant. "Hit 100 signups by 1 November" names a day, with no time of day and
-- no zone. Stored as timestamptz it would render as 31 October to any reader
-- west of UTC -- an off-by-one on the date the whole key result is about. DATE
-- says exactly what is meant, and matches funding_sources.period_start/end,
-- which were already right.
ALTER TABLE key_results ALTER COLUMN target_date TYPE DATE
    USING target_date::date;

-- The migration runner's own bookkeeping, for the same reason. Guarded because
-- the runner creates this table, so it is absent when the schema test applies
-- these files to a bare schema.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables
               WHERE table_schema = current_schema() AND table_name = '_migrations') THEN
        ALTER TABLE _migrations ALTER COLUMN applied_at TYPE TIMESTAMPTZ
            USING applied_at AT TIME ZONE current_setting('TimeZone');
    END IF;
END $$;
