-- A Strategic Consideration is something the drafting agent worked out about a
-- project before writing any objective, and then had to either cover or decline.
--
-- It exists because a brief describes a mechanism and objectives drafted
-- straight from one are that mechanism restated. The first real project drafted
-- this way produced three objectives that were one sentence of brief cut into
-- three pieces, and the broader thinking -- who else touches this, what makes it
-- fail even when the code works -- happened, but landed in open questions where
-- it could never become an objective.
--
-- Two kinds, deliberately. Both are enumerations the agent derives from this
-- project rather than a checklist Mendel carries:
--
--   failure_mode  a way this project fails with the software working exactly as
--                 described. The phrasing is load-bearing: stipulating that the
--                 code works excludes the mechanism, which is the thing the
--                 brief already said.
--   party         someone or something the system exchanges with, whose
--                 experience decides whether this succeeds -- a person, another
--                 program, an agent, a supplier whose outage is indistinguishable
--                 from your own. For a party that cannot complain, "a bad
--                 experience" is latency, error semantics and uptime, so
--                 software quality arrives through the same enumeration instead
--                 of needing a rule of its own.
CREATE TABLE strategic_considerations (
    id UUID PRIMARY KEY,
    strategy_id UUID NOT NULL REFERENCES strategies(id) ON DELETE CASCADE,

    kind TEXT NOT NULL,
    statement TEXT NOT NULL,

    -- Coverage, decided by the drafting pass that had this consideration in
    -- hand. Exactly one of these is set: an objective that covers it, or the
    -- reason nothing does. Both null means no draft has judged it yet, which is
    -- a third state and not the same as uncovered.
    --
    -- SET NULL rather than CASCADE because a redraft deletes every objective and
    -- writes new ones: the consideration survives that, its coverage does not,
    -- and the next pass decides again.
    covered_by_objective_id UUID REFERENCES objectives(id) ON DELETE SET NULL,
    uncovered_reason TEXT,

    -- The order the agent produced them in. created_at ties inside one batch,
    -- so it cannot carry this on its own.
    position INT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT strategic_considerations_kind_valid
        CHECK (kind IN ('failure_mode', 'party')),

    -- Covered and declined are alternatives, not both.
    CONSTRAINT strategic_considerations_coverage_exclusive
        CHECK (covered_by_objective_id IS NULL OR uncovered_reason IS NULL)
);

CREATE INDEX idx_strategic_considerations_strategy
    ON strategic_considerations(strategy_id, position);
