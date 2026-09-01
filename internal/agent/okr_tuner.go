package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

const okrTunerSystemPrompt = `You are an OKR quality expert. Evaluate Objectives and Key Results for clarity, specificity, and actionability.

For Objectives, evaluate:
- Is it clear and understandable?
- Is it inspiring and motivating?
- Is it achievable within a reasonable timeframe (typically a quarter)?
- Is it specific enough to guide action without being too narrow?

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
- 0.8-1.0: Excellent - Clear, specific, measurable, actionable
- 0.6-0.8: Good - Minor improvements could be made
- 0.4-0.6: Needs work - Missing key elements of clarity or measurability
- 0.0-0.4: Poor - Vague, unmeasurable, or unclear

Provide brief, actionable feedback (1-2 sentences) for each item.`

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
