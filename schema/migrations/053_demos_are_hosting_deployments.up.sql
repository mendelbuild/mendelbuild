-- Demos become hosting deployments, and demo_instances is retired.
--
-- 029 created hosting_deployments "shaped so demo deploys can move onto it
-- (kind = 'demo' with variation_id set) and retire demo_instances". Until now
-- they did not, and the two records drifted in the way parallel records do:
-- stopping a demo closed the demo_instances row and left the hosting one open,
-- because there was no hosting one at all. Nothing in the tree ever moved a
-- hosting_deployments row off 'running', so the hosting meter had no idea when
-- anything stopped and project deletion could not gate on a status nobody
-- maintained.
--
-- demo_instances carried three things hosting_deployments did not:
--
--   suggested_fix   The LLM's proposed fix prompt when a demo fails to start.
--                   Added here; it is a property of a failed deployment, and
--                   production deploys can carry one just as sensibly.
--   process_info    Only ever held {"work_dir": ...}, was written and never
--                   read, and the value is recomputable from the project and
--                   variation IDs. Dropped rather than carried over.
--   stopped_at      hosting_deployments.finished_at already means this.

ALTER TABLE hosting_deployments
    ADD COLUMN suggested_fix TEXT;

-- Demo deploys and prod deploys stop for different reasons but leave the same
-- kind of trace, so the index that finds what is still up serves both.
CREATE INDEX idx_hosting_deployments_variation_status
    ON hosting_deployments(variation_id, status);

DROP TABLE demo_instances;
