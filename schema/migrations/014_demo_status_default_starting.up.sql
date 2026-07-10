-- Change default status for demo_instances from 'running' to 'starting'
-- Demos now start in 'starting' status and transition to 'running' after health check
ALTER TABLE demo_instances ALTER COLUMN status SET DEFAULT 'starting';
