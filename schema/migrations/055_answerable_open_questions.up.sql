-- An open question is something whose answer would change a project's
-- objectives, asked by the drafting agent because a brief is almost never
-- complete.
--
-- They lived in the strategy's draft_notes JSONB, as a list of strings under a
-- heading reading "Worth answering", and there was nothing to answer them with.
-- They were good questions -- is this one office or many, what benchmark data
-- exists, who may read the audit log -- and every one of them was a fork in the
-- plan that Mendel had spotted and then dropped.
--
-- A row rather than a string because an answer has to attach to something. The
-- suggested answers come from the same pass that asked the question, so a
-- person who does not know what a reasonable answer looks like can recognise
-- one instead of composing it, with a free-text box for when none of them fit.
CREATE TABLE strategy_open_questions (
    id UUID PRIMARY KEY,
    strategy_id UUID NOT NULL REFERENCES strategies(id) ON DELETE CASCADE,

    question TEXT NOT NULL,

    -- What the drafting agent offered as plausible answers, in its order. An
    -- array of strings; empty when it offered none, which is allowed and means
    -- the free-text box is the only way in.
    suggested_answers JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- The user's answer, whether chosen from the suggestions or typed. Null
    -- means unanswered, which is why this is not defaulted to the empty string:
    -- "they have not said" and "they said nothing" would otherwise be the same
    -- value, and only the first should keep the question on screen.
    answer TEXT,
    answered_at TIMESTAMPTZ,

    position INT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_strategy_open_questions_strategy
    ON strategy_open_questions(strategy_id, position);
