package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// The grader and the drafter have to share a rubric, or the grade is noise
// relative to the draft.
//
// They did not. This prompt used to ask whether an objective was "specific
// enough to guide action", which rewards naming the mechanism -- exactly what
// the strategist forbids. On the first real project it scored a textbook
// too-tactical objective at 0.75 and praised it as "specific enough to guide
// development". A grade like that is worse than no grade: it tells a user who
// cannot yet tell a good objective from a bad one that this one is fine.
//
// So the objective half of this rubric is the strategist's own test, asked
// backwards. The key result half was already aligned and is unchanged.
const okrTunerSystemPrompt = `You are grading Objectives and Key Results that were drafted for a project, before a person who is not an OKR practitioner approves them. Your score decides what they look at hardest, so grade what actually matters about the line rather than how confident it sounds.

For Objectives, the test is outcome versus mechanism:
- Does it name who ends up better off, and how? That is an outcome. Score it well.
- Does it describe what the software does, or list what a user can do? That is the mechanism. It reads specific and actionable, and it is the most common way a drafted objective goes wrong. Score it poorly and say so.
  "An official can create a poll, send it to a sample, and collect responses without technical help" is a feature list with a subject in front of it, and belongs below 0.4 however clearly it is written.
  "Officials can be successful with this on their own, without technical help" is the same intent as an outcome.
- Would it still read true if the design changed completely? If a different implementation satisfying the same brief would falsify the objective, it is pinned to a mechanism.
- Is it plain? No "leverage", "delight", "world-class", "seamless".
- Clarity is necessary and not sufficient. A vague objective and a clear feature list are both bad, and the clear feature list is the more dangerous of the two because it survives review.

For Key Results, evaluate:
- Is the target measurable and quantifiable?
- Could someone put a number to it every week? A key result that can only be
  settled at the end of the period cannot say whether the work is going well
  while there is still time to change it.
- A "done, or not" target is weaker than a number for that reason, and should
  score lower unless there is genuinely nothing to count.
- Is the unit clear (e.g., "100 users" vs just "more users")?
- Is it ambitious but realistic?
- Does it have a clear success threshold?

Scoring guide (0.0 to 1.0):
- 0.8-1.0: Excellent - an outcome that survives a change of design, measured well
- 0.6-0.8: Good - right in kind, and one edit away
- 0.4-0.6: Needs work - drifting into mechanism, or not measurable as written
- 0.0-0.4: Poor - a feature list, or a target nobody could settle

Provide brief, actionable feedback (1-2 sentences) for each item. Where an objective names the mechanism, say what the actions were for -- that is the edit the user cannot make on their own.`

// OKRTuner evaluates the quality of Objectives and Key Results.
type OKRTuner struct {
	client *Client
}

// NewOKRTuner creates a new OKRTuner.
func NewOKRTuner(client *Client) *OKRTuner {
	return &OKRTuner{client: client}
}

// TuneOKRs evaluates the quality of objectives and key results.
// Uses Claude Haiku for cost-effectiveness.
func (t *OKRTuner) TuneOKRs(ctx context.Context, input OKRTuneInput) (*OKRTuneResponse, Spend, error) {
	if len(input.Objectives) == 0 && len(input.KeyResults) == 0 {
		return &OKRTuneResponse{}, Spend{}, nil
	}

	inputJSON, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return nil, Spend{}, fmt.Errorf("marshal input: %w", err)
	}

	userMessage := fmt.Sprintf(`Evaluate the quality of these OKRs:

%s

Score each item from 0.0 to 1.0 and provide brief feedback.`, string(inputJSON))

	// Use Haiku for cost-effectiveness
	originalModel := t.client.model
	t.client.model = "claude-haiku-4-5"
	defer func() { t.client.model = originalModel }()

	resp, err := t.client.SendMessageWithSchema(ctx, okrTunerSystemPrompt, []Message{
		{Role: "user", Content: userMessage},
	}, 4096, OKRTuneResponseSchema())
	if err != nil {
		return nil, Spend{}, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()
	var result OKRTuneResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, Spend{}, fmt.Errorf("parse response: %w (content: %s)", err, content)
	}

	return &result, resp.Spend(), nil
}
