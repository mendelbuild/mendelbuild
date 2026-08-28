package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

const strategistSystemPrompt = `You turn a plain-English project brief into a strategy Mendel can plan against: a small set of Objectives, each measured by Key Results with dates.

The person reading your output wrote the brief and is about to approve or correct what you produce. They are not an OKR practitioner. Write for them.

Objectives:
- 2 to 4 of them. Fewer is better than padding. Every objective becomes work someone pays for.
- Each states what will be true when it succeeds, not what tasks get done. "New users can set up an account without help" is an objective; "build the signup form" is a task.
- Plain language. No "leverage", "delight", "world-class", "seamless".
- Cover what the brief actually asked for. Do not add an objective for something the user never mentioned just because it is good practice; put it in open_questions instead.

Key Results:
- 2 to 3 per objective. Each must be checkable: a reader must be able to say yes or no.
- target_units carries the number, the unit, and the comparison: ">= 100 completed signups", "< 200ms p99", ">= 80%".
- Prefer things this project can actually measure. A brand-new product has no users yet, so "50000 monthly actives" is not a key result, it is a wish. Say so in open_questions if the brief implies traction that does not exist.
- Every target_date falls between today and the deadline. Spread them: some things are true early, some at the end.

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

The same rules apply as when drafting: 2 to 4 plain-English objectives, 2 to 3 checkable key results each with target_units carrying number-unit-comparison, target dates between today and the deadline, and no invented dollar figures.

Beyond that:
- The feedback is the point. Act on it directly rather than producing a differently-worded version of the same draft.
- Keep the parts the user did not object to. Rewriting untouched objectives wastes their re-reading.
- If the feedback conflicts with something you believe matters, do what they asked and record your concern in open_questions rather than quietly ignoring them.
- If the feedback is too vague to act on, say what you would need to know in open_questions instead of guessing at length.`

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
func (s *Strategist) DraftStrategy(ctx context.Context, brief StrategyBrief) (*DraftedStrategy, Spend, error) {
	briefJSON, err := json.MarshalIndent(brief, "", "  ")
	if err != nil {
		return nil, Spend{}, fmt.Errorf("marshal brief: %w", err)
	}

	userMessage := fmt.Sprintf(`Draft a strategy for this project:

%s

Fill in every field. Return 2 to 4 objectives, each with 2 to 3 key results, that
the person who wrote this brief would recognize as what they asked for. An empty
objectives list is not an answer: a brief this size always supports a first pass,
and anything the brief left open belongs in assumptions and open_questions, not
in a blank response.`, string(briefJSON))

	return s.send(ctx, strategistSystemPrompt, userMessage)
}

// ReviseStrategy redrafts a strategy from the user's feedback on the previous
// draft. The brief travels with it: the feedback is a correction to the reading
// of the brief, not a replacement for it.
func (s *Strategist) ReviseStrategy(ctx context.Context, brief StrategyBrief, current *DraftedStrategy, feedback string) (*DraftedStrategy, Spend, error) {
	payload := struct {
		Brief        StrategyBrief    `json:"brief"`
		CurrentDraft *DraftedStrategy `json:"current_draft"`
		Feedback     string           `json:"feedback"`
	}{brief, current, feedback}

	payloadJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, Spend{}, fmt.Errorf("marshal revision: %w", err)
	}

	userMessage := fmt.Sprintf(`Revise this drafted strategy:

%s

Act on the feedback and leave alone what it did not touch.`, string(payloadJSON))

	return s.send(ctx, strategistReviseSystemPrompt, userMessage)
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
	if isEmptyDraft(drafted) {
		var bare DraftedStrategy
		if err := json.Unmarshal([]byte(content), &bare); err == nil {
			drafted = bare
		}
	}
	if isEmptyDraft(drafted) {
		return nil, resp.Spend(), fmt.Errorf("the model returned no objectives (stop reason %q, content: %s)", resp.StopReason, content)
	}

	return &drafted, resp.Spend(), nil
}

// isEmptyDraft reports whether a parse produced nothing usable. A strategy with
// no objectives is not a strategy, whatever else came back with it.
func isEmptyDraft(d DraftedStrategy) bool {
	return len(d.Objectives) == 0
}
