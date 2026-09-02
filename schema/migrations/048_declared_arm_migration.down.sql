ALTER TABLE hops DROP COLUMN live_experiment;
ALTER TABLE experiment_arms
    DROP COLUMN declared_migration_up,
    DROP COLUMN declared_migration_down;
