-- What an Arm proposes, before anything has judged it.
--
-- arm_admissions records a verdict, and a verdict needs the user's datastore to
-- reach: whether a change is purely additive is established by applying it and
-- diffing, not by reading it. So there is a gap between code generation writing
-- a migration and admission ruling on it, and the migration has to live
-- somewhere across that gap.
--
-- On the Arm rather than on the Variation because it is the Arm's proposal:
-- variation_migrations is the ordinary migration for a normal deployment, which
-- has no additive-only obligation and no experiment namespace.
ALTER TABLE experiment_arms
    ADD COLUMN declared_migration_up TEXT NOT NULL DEFAULT '',
    ADD COLUMN declared_migration_down TEXT NOT NULL DEFAULT '';

-- Whether this Hop's Variations take live traffic beside the current code.
--
-- Something has to ask. Code generation only writes .mendel/experiment.json
-- when it is told to, because a Variation that declares an experiment nobody
-- asked for would put real traffic on a comparison nobody designed -- and the
-- ordinary case, by a wide margin, is a Variation that is simply deployed.
ALTER TABLE hops ADD COLUMN live_experiment BOOLEAN NOT NULL DEFAULT false;
