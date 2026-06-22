-- Phase 4: Variation Selection support
-- Adds new statuses for Hops and Variations, evaluation criteria, and selection Decision kind

-- Add evaluation_criteria column to hops (AI-generated structured criteria for comparing Variations)
-- JSONB structure: { "criteria": [...], "rationale": "...", "tradeoffs": "..." }
ALTER TABLE hops ADD COLUMN evaluation_criteria JSONB;

-- Update hops status constraint to include 'selecting' and 'rejected'
ALTER TABLE hops DROP CONSTRAINT hops_status_check;
ALTER TABLE hops ADD CONSTRAINT hops_status_check
    CHECK (status IN ('pending', 'active', 'selecting', 'completed', 'rejected', 'abandoned'));

-- Add constraint on variations status (was previously unconstrained)
-- Includes new 'merged' and 'rejected' statuses for selection outcomes
ALTER TABLE variations ADD CONSTRAINT variations_status_check
    CHECK (status IN ('creating', 'pending', 'migrating', 'active', 'draining',
                      'error', 'terminated', 'pruned', 'selected', 'merged', 'rejected'));

-- Add variation_selection to decisions kind constraint
ALTER TABLE decisions DROP CONSTRAINT decisions_kind_check;
ALTER TABLE decisions ADD CONSTRAINT decisions_kind_check
    CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review',
                    'variation_review', 'variation_selection'));
