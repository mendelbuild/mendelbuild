-- What an Arm was built from.
--
-- Without this, "this arm is running code from before your last change" is
-- undetectable. The first live experiment hit exactly that: the code on both
-- experiment branches changed while the experiment ran, and whether the running
-- arms included it was unknowable -- they did, but only because the experiment
-- had been restarted, which was luck rather than knowledge.
--
-- Recording the commit at build time turns staleness into a comparison against
-- the branch head, which is a question Mendel can answer rather than guess at.

ALTER TABLE experiment_arms
    -- The commit the image was built from. Empty means never built, which is
    -- different from built and unknown -- and the difference is the whole point,
    -- so it is not conflated with NULL.
    ADD COLUMN source_commit TEXT NOT NULL DEFAULT '',

    -- The image reference that commit produced. Uniquely tagged per build, so
    -- two rows differing here ran different bytes even at the same commit.
    ADD COLUMN image TEXT NOT NULL DEFAULT '',

    -- NULL until the first build. A timestamp of zero would read as 1970 rather
    -- than as never.
    ADD COLUMN built_at TIMESTAMPTZ;
