package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const strategistSystemPrompt = `You turn a plain-English project brief into a strategy Mendel can plan against: a small set of Objectives, each measured by Key Results with dates.

The person reading your output wrote the brief and is about to approve or correct what you produce. They are not an OKR practitioner. Write for them.

Objectives -- the outcome, never the mechanism:
- 2 to 4 of them. Fewer is better than padding. Every objective becomes work someone pays for.
- An objective names who ends up better off and how. It does not describe how the software gets them there. The steps, features and flows belong in key results.
- The tell you have written the mechanism: your objective lists actions. If it reads "the user can do X, then Y, then Z", you have written a feature list with a subject in front of it. Cut it back to what X, Y and Z were *for*.

  Too tactical: "An elected official can create a poll, send it to a sample drawn from their constituent list, and collect responses without needing technical help."
  Right: "Elected officials can be successful with this on their own, without technical help or a manual."

  Too tactical: "A user can paste a recipe URL, pick recipes for the week, and get a merged shopping list grouped by aisle."
  Right: "Someone who cooks a few times a week can plan the week and shop for it without losing track."

- A good objective survives a change of implementation. If a different design would satisfy the same brief, the objective should still read true; only the key results would change.
- Plain language. No "leverage", "delight", "world-class", "seamless".
- Cover what the brief actually asked for. Do not widen the scope: an objective for a feature the user never mentioned is work they did not ask to pay for, and belongs in open_questions.
- A success condition on the scope they *did* ask for is not extra scope. Whether anyone adopts the thing, whether it is fast enough to be used, whether the people it asks work of will do that work -- these are not additions to the brief, they are what makes the brief worth building. The considerations below are exactly this, and they were worked out from this project rather than from a list Mendel carries.

Considerations:
You are given what a first pass worked out about this project: the ways it fails with the software working exactly as described, and the parties whose experience decides whether it succeeds. They are the standard your objectives are checked against.
- Every consideration must end up in exactly one place: in some objective's covers list, or in uncovered with a reason. Never both, never neither.
- Do not pad to cover them all. Four objectives is the cap and it binds: choosing to leave something uncovered, and saying why, is a better answer than a fifth objective or a bloated third one.
- Weigh them on merit against what the brief asked for. A consideration that would sink the project beats a restatement of the brief, even though the brief is what the user wrote down.
- The reason on an uncovered consideration is read by the user. Write it for them: what would have to be true for this to be worth an objective.

Key Results:
- 2 to 3 per objective. Each must be checkable: a reader must be able to say yes or no.
- target_units carries the number, the unit, and the comparison: ">= 100 completed signups", "< 200ms p99", ">= 80%".
- Prefer things this project can actually measure. A brand-new product has no users yet, so "50000 monthly actives" is not a key result, it is a wish. Say so in open_questions if the brief implies traction that does not exist.
- Do not give key results dates. Every one of them is due at the end of the planning cycle, and Mendel sets that date itself.
  This is a planning cycle, not a sequence of deadlines. What tells you mid-cycle whether the work is going well is that each key result can be measured every week -- which is required above -- not that some of them fall due earlier. A staggered due date is a milestone, and a milestone is a Hop on the roadmap rather than a key result.

Budget and deadline:
- Take the user's figures as given. Do not talk them up or down.
- In budget_note, say whether the scope fits the money and the time, and what you would cut first if not. If you cannot tell, say that.
- Do not invent per-piece dollar figures. You have no cost history for this project, and a confident number someone then plans against is worse than an admitted unknown. Mendel estimates the cost of the work later, against real data.

Assumptions and open questions:
A brief is almost never complete. You will have to fill in specifics -- platform, audience, scale, stack -- to write anything concrete.
- Every specific you supplied that the brief did not state goes in assumptions, one short sentence each.
- Anything whose answer would change these objectives goes in open_questions, phrased for the user to answer.
- Do not present invented specifics as though the user had given them. The user can only correct what they can see, and they are approving this next.`

const strategistReviseSystemPrompt = `You are revising a drafted strategy based on the user's feedback.

The same rules apply as when drafting: 2 to 4 plain-English objectives that name an outcome rather than a mechanism (an objective listing actions -- "the user can do X, then Y" -- is a feature list; cut it back to what those actions were for), 2 to 3 checkable key results each with target_units carrying number-unit-comparison, target dates between today and the deadline, and no invented dollar figures.

Beyond that:
- The feedback is the point. Act on it directly rather than producing a differently-worded version of the same draft.
- Keep the parts the user did not object to. Rewriting untouched objectives wastes their re-reading.
- If the feedback conflicts with something you believe matters, do what they asked and record your concern in open_questions rather than quietly ignoring them.
- If the feedback is too vague to act on, say what you would need to know in open_questions instead of guessing at length.
- The considerations travel with the draft and the coverage rule still holds: every one of them lands in an objective's covers list or in uncovered with a reason. If the feedback asks for something you had declined, cover it now and say in budget_note what gave way to make room.`

// errUnusableDraft is a response that satisfied the schema but not the point.
// Two shapes have been seen in practice: every field present and blank, and a
// half-finished generation -- one objective, no strategy name, and JSON
// fragments leaking into a string value. Both are retryable; a transport or
// parsing failure is not.
var errUnusableDraft = errors.New("the model returned an unusable draft")

// emptyRetryNudge is appended on the second attempt. Kept separate from the
// first prompt so an ordinary draft is not shaped by an instruction written for
// a failure that usually does not happen.
const emptyRetryNudge = `Your previous attempt came back as a stub -- blank fields, or the literal word "placeholder" where the content should have been. That is not a draft, and there is a person waiting on it to start their project.

Write the actual objectives this time, in real words about this specific project. If the brief genuinely leaves something open, say so in assumptions or open_questions; that is never a reason to return a stub.`

// KeyedConsideration is a Strategic Consideration as the drafting pass sees it:
// the statement, plus a short reference the model points back at.
//
// The reference exists because coverage has to survive the round trip. Objectives
// do not have identities until they are written, so the model cannot answer
// "which objective covers this"; it can answer "which of these does this
// objective cover", and a short opaque key is far harder to garble than an index
// or a restatement of the sentence.
type KeyedConsideration struct {
	Ref       string `json:"ref" desc:"Short reference for this consideration. Copy it exactly when pointing back at it."`
	Kind      string `json:"kind" desc:"'failure_mode' -- a way this project fails with the software working as described -- or 'party', someone or something whose experience of the exchange decides whether it succeeds."`
	Statement string `json:"statement" desc:"What was worked out about this project."`
}

// ConsiderationRef is the reference key for the nth consideration.
func ConsiderationRef(n int) string { return fmt.Sprintf("C%d", n+1) }

// Strategist drafts a strategy -- objectives, key results, and a budget label --
// from the plain-English brief a user writes when creating a project.
type Strategist struct {
	client *Client
}

// NewStrategist creates a new Strategist.
func NewStrategist(client *Client) *Strategist {
	return &Strategist{client: client}
}

// DraftStrategy produces a first-pass strategy from a brief. The result is a
// draft: nothing is built from it until a human approves it.
func (s *Strategist) DraftStrategy(ctx context.Context, brief StrategyBrief, considerations []KeyedConsideration) (*DraftedStrategy, Spend, error) {
	payload := struct {
		Brief          StrategyBrief        `json:"brief"`
		Considerations []KeyedConsideration `json:"considerations"`
	}{brief, considerations}

	briefJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, Spend{}, fmt.Errorf("marshal brief: %w", err)
	}

	userMessage := fmt.Sprintf(`Draft a strategy for this project:

%s

Write 2 to 4 objectives for this project, each with 2 to 3 key results, that the
person who wrote the brief would recognize as what they asked for. Write the real
thing: this goes straight to them for approval, and it is the plan their money
gets spent against. Where the brief left something open, say what you assumed --
that belongs in assumptions and open_questions.`, string(briefJSON))

	return s.sendWithRetry(ctx, strategistSystemPrompt, userMessage)
}

// ReviseStrategy redrafts a strategy from the user's feedback on the previous
// draft. The brief travels with it: the feedback is a correction to the reading
// of the brief, not a replacement for it.
func (s *Strategist) ReviseStrategy(ctx context.Context, brief StrategyBrief, considerations []KeyedConsideration,
	current *DraftedStrategy, feedback string) (*DraftedStrategy, Spend, error) {

	payload := struct {
		Brief          StrategyBrief        `json:"brief"`
		Considerations []KeyedConsideration `json:"considerations"`
		CurrentDraft   *DraftedStrategy     `json:"current_draft"`
		Feedback       string               `json:"feedback"`
	}{brief, considerations, current, feedback}

	payloadJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, Spend{}, fmt.Errorf("marshal revision: %w", err)
	}

	userMessage := fmt.Sprintf(`Revise this drafted strategy:

%s

Act on the feedback and leave alone what it did not touch.`, string(payloadJSON))

	return s.sendWithRetry(ctx, strategistReviseSystemPrompt, userMessage)
}

// sendWithRetry makes one more attempt when the model returns an empty draft.
//
// That happens intermittently -- the response satisfies the schema with every
// field blank -- and it is worth a second try rather than a dead end, because
// the user is sitting in front of a spinner and the alternative is asking them
// to click a retry button that does exactly this.
//
// Only an empty draft is retried. A transport error or a malformed response is
// reported as-is; retrying those would just double the wait before the same
// failure. Spend from both attempts is summed, because both were paid for.
func (s *Strategist) sendWithRetry(ctx context.Context, system, userMessage string) (*DraftedStrategy, Spend, error) {
	drafted, spend, err := s.send(ctx, system, userMessage)
	if !errors.Is(err, errUnusableDraft) {
		return drafted, spend, err
	}

	retried, retrySpend, retryErr := s.send(ctx, system, userMessage+"\n\n"+emptyRetryNudge)
	spend.Tokens.Add(retrySpend.Tokens)
	if spend.Model == "" {
		spend.Model = retrySpend.Model
	}
	if retryErr != nil {
		return nil, spend, retryErr
	}
	return retried, spend, nil
}

func (s *Strategist) send(ctx context.Context, system, userMessage string) (*DraftedStrategy, Spend, error) {
	resp, err := s.client.SendMessageWithSchema(ctx, system, []Message{
		{Role: "user", Content: userMessage},
	}, 8192, StrategistResponseSchema())
	if err != nil {
		return nil, Spend{}, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()

	// Two shapes are accepted, because the model has been observed to return
	// both: the wrapper the schema asks for, and the bare strategy object.
	// json.Unmarshal ignores unknown fields, so a bare object parses into the
	// wrapper without error and leaves it empty -- a silent success that
	// persists an empty strategy and tells the user nothing.
	var wrapped StrategistResponse
	if err := json.Unmarshal([]byte(content), &wrapped); err != nil {
		return nil, resp.Spend(), fmt.Errorf("parse response: %w (content: %s)", err, content)
	}
	drafted := wrapped.Strategy
	if len(drafted.Objectives) == 0 {
		var bare DraftedStrategy
		if err := json.Unmarshal([]byte(content), &bare); err == nil {
			drafted = bare
		}
	}
	if defect := draftDefect(drafted); defect != "" {
		return nil, resp.Spend(), wrapUnusableDraft(defect, resp.StopReason, content)
	}

	return &drafted, resp.Spend(), nil
}

// wrapUnusableDraft attaches the diagnostic detail to the retryable sentinel.
// The reason and the raw content both go in because the alternative --
// "drafting failed" -- tells whoever reads the log nothing about why.
func wrapUnusableDraft(reason, stopReason, content string) error {
	return fmt.Errorf("%w: %s (stop reason %q, content: %s)",
		errUnusableDraft, reason, stopReason, content)
}

// draftDefect names what is wrong with a draft, or "" if it is usable.
//
// Each of these is something the prompt asks for unconditionally, so its
// absence means the generation went wrong rather than that the brief was thin.
// Catching them here costs one retry; letting them through puts a review screen
// titled "Initial strategy" in front of someone, with one vague objective on it
// and nothing to say Mendel got it wrong.
func draftDefect(d DraftedStrategy) string {
	if len(d.Objectives) == 0 {
		return "no objectives"
	}
	if strings.TrimSpace(d.StrategyName) == "" {
		return "no strategy name"
	}
	for i, obj := range d.Objectives {
		if strings.TrimSpace(obj.Description) == "" {
			return fmt.Sprintf("objective %d has no description", i+1)
		}
		if len(obj.KeyResults) == 0 {
			return fmt.Sprintf("objective %d has no key results", i+1)
		}
	}
	return ""
}
