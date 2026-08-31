-- Drafting a strategy is a background job, not a request [034]
--
-- The first version drafted OKRs inline in POST /new. That call takes 30-45
-- seconds against the model, and the GCE ingress in front of the app closes a
-- backend connection at 30, so the user got a gateway error while the draft
-- completed fine behind it -- their project existed, fully drafted, with no way
-- to know it.
--
-- The draft now runs in the background and the browser is redirected to the
-- review screen immediately. That needs the strategy to say where its draft is
-- up to, because none of it is derivable: "no objectives yet" is the same row
-- state whether a draft is running, has failed, or was never started.
--
-- draft_started_at exists so a draft whose goroutine died with the process --
-- a deploy mid-draft -- can be recognised as stale instead of leaving the
-- review screen polling forever.

ALTER TABLE strategies ADD COLUMN draft_status TEXT NOT NULL DEFAULT 'ready';
ALTER TABLE strategies ADD COLUMN draft_error TEXT;
ALTER TABLE strategies ADD COLUMN draft_started_at TIMESTAMP;

ALTER TABLE strategies ADD CONSTRAINT strategies_draft_status_valid
    CHECK (draft_status IN ('drafting', 'ready', 'failed'));
