-- Remove the traffic-splitting tables from the abandoned Envoy design.
--
-- These were created in 001 and reworked in 015 for an approach that generated
-- Envoy configuration from stored allocations. That approach was superseded:
-- the generator was deleted as dead code in "Remove the dead deploy and envoy
-- packages", the deployed_instances table it read from was dropped in 029, and
-- the live-traffic design that replaces it (dev/claude_plans/13) reaches
-- different conclusions -- Gateway API rather than Envoy configuration, Arms of
-- an Experiment rather than slices of a Hop.
--
-- Nothing has read or written them since: the query functions had no callers
-- outside internal/db, and they go with the tables.
--
-- They hold no data worth keeping. Anything routing-related in the new design
-- arrives under its own schema rather than by reviving these.

DROP TABLE IF EXISTS traffic_allocation_envoy_configs;
DROP TABLE IF EXISTS traffic_allocation_slices;
DROP TABLE IF EXISTS traffic_allocations;
