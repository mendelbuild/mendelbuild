-- Guided project creation [032]
--
-- Two facts the old JSON-upload path never had to record:
--
-- projects.brief is the user's own description of what they want built. It is
-- the input the drafting agent works from, and it stays useful afterwards as
-- context for the roadmap proposer and as a reminder on the review screen of
-- what was actually asked for.
--
-- strategies.okrs_approved_at separates a drafted strategy the user has not
-- looked at yet from one they have validated. This is not derivable: an
-- objective written by an agent and an objective a human has signed off on look
-- identical in the objectives table, and only the second should let a roadmap
-- be built against it.

ALTER TABLE projects ADD COLUMN brief TEXT;
ALTER TABLE strategies ADD COLUMN okrs_approved_at TIMESTAMP;

-- strategies.draft_notes is what the drafting agent said about its own draft:
-- how it read the brief, what it filled in, what it could not tell, and whether
-- the scope looks like it fits the budget. It is kept after approval rather
-- than discarded -- when a hop later overruns, the assumptions the plan was
-- built on are the first thing worth re-reading.
ALTER TABLE strategies ADD COLUMN draft_notes JSONB;
