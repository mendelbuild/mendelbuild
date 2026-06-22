-- Revert Phase 4: Variation Selection support

-- Remove variation_selection from decisions kind constraint
ALTER TABLE decisions DROP CONSTRAINT decisions_kind_check;
ALTER TABLE decisions ADD CONSTRAINT decisions_kind_check
    CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review', 'variation_review'));

-- Remove variations status constraint (was unconstrained before)
ALTER TABLE variations DROP CONSTRAINT variations_status_check;

-- Revert hops status constraint
ALTER TABLE hops DROP CONSTRAINT hops_status_check;
ALTER TABLE hops ADD CONSTRAINT hops_status_check
    CHECK (status IN ('pending', 'active', 'completed', 'abandoned'));

-- Remove evaluation_criteria column
ALTER TABLE hops DROP COLUMN evaluation_criteria;
