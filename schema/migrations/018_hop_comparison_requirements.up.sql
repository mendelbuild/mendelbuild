-- Add comparison requirement flags to hops
-- These control whether variations need demos and/or production traffic for comparison

ALTER TABLE hops ADD COLUMN requires_demo BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE hops ADD COLUMN requires_production BOOLEAN NOT NULL DEFAULT false;
