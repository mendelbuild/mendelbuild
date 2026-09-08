-- deployment_name was never written.
--
-- It was added in 047 to record "what was deployed for an Arm", with a setter
-- that no caller ever called, so every row has held '' since the table existed.
-- What an Arm is actually running is now recorded properly beside it -- 049's
-- image and source_commit, written after the build succeeds -- which leaves this
-- column as a second, emptier answer to a question already answered.
--
-- The name it would have held is derived rather than stored in any case:
-- experimentArmResource(experiment, arm) computes it, and the teardown and
-- rollout paths both call that instead of reading a column.

ALTER TABLE experiment_arms DROP COLUMN deployment_name;
