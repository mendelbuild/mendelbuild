-- Revert 001_initial: Drop all tables in reverse dependency order

DROP INDEX IF EXISTS idx_variations_status;
DROP INDEX IF EXISTS idx_variations_hop;
DROP INDEX IF EXISTS idx_hops_status;
DROP INDEX IF EXISTS idx_hops_strategy;
DROP INDEX IF EXISTS idx_key_results_objective;
DROP INDEX IF EXISTS idx_objectives_strategy;
DROP INDEX IF EXISTS idx_strategies_project;
DROP INDEX IF EXISTS idx_decisions_subject;
DROP INDEX IF EXISTS idx_decisions_status;
DROP INDEX IF EXISTS idx_var_migrations;
DROP INDEX IF EXISTS idx_variation_history;
DROP INDEX IF EXISTS idx_spend_log_allocation;
DROP INDEX IF EXISTS idx_kr_history_kr_id;

DROP TABLE IF EXISTS traffic_allocation_slices;
DROP TABLE IF EXISTS traffic_allocations;
DROP TABLE IF EXISTS variation_migrations;
DROP TABLE IF EXISTS variation_state_history;
DROP TABLE IF EXISTS variations;
DROP TABLE IF EXISTS ecosystems;
DROP TABLE IF EXISTS repositories;
DROP TABLE IF EXISTS decisions;
DROP TABLE IF EXISTS budget_spend_log;
DROP TABLE IF EXISTS budget_allocations;
DROP TABLE IF EXISTS hop_dependencies;
DROP TABLE IF EXISTS hops;
DROP TABLE IF EXISTS funding_success_criteria;
DROP TABLE IF EXISTS funding_sources;
DROP TABLE IF EXISTS key_result_history;
DROP TABLE IF EXISTS key_results;
DROP TABLE IF EXISTS objectives;
DROP TABLE IF EXISTS strategies;
DROP TABLE IF EXISTS projects;
