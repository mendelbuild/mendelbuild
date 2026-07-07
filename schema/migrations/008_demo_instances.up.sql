-- Demo instances track running demos of variations
-- Designed to be stateless: Mendel can crash and recover by reading teardown instructions

CREATE TABLE demo_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id),
    url TEXT NOT NULL,
    teardown_instructions TEXT NOT NULL,  -- shell commands to stop the demo
    started_at TIMESTAMP NOT NULL DEFAULT NOW(),
    stopped_at TIMESTAMP,
    status TEXT NOT NULL DEFAULT 'running',  -- running, stopped, error
    process_info JSONB,  -- pid, port, container_id, etc - whatever is needed for teardown
    error_message TEXT,  -- populated if status = 'error'
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_demo_instances_variation ON demo_instances(variation_id);
CREATE INDEX idx_demo_instances_status ON demo_instances(status) WHERE status = 'running';
