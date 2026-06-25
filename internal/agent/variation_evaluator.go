package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

const variationEvaluatorSystemPrompt = `You are a code evaluation expert. You evaluate implementation variations against specified criteria.

Given a set of variations with their approaches and a set of evaluation criteria, provide scores (0.0 to 1.0) for each variation on each criterion.

Scoring guidelines:
- 1.0 = Excellent - fully addresses the criterion
- 0.8 = Good - addresses most aspects of the criterion
- 0.6 = Adequate - partially addresses the criterion
- 0.4 = Weak - minimally addresses the criterion
- 0.2 = Poor - barely addresses the criterion
- 0.0 = N/A or Unknown - cannot evaluate based on available information

Be honest about uncertainty. If you cannot evaluate a criterion based on the approach description alone, use a lower score or 0.0 for unknown.`

// VariationEvaluator evaluates variations against criteria.
type VariationEvaluator struct {
	client *Client
}

// NewVariationEvaluator creates a new evaluator.
func NewVariationEvaluator(client *Client) *VariationEvaluator {
	return &VariationEvaluator{client: client}
}

// VariationForEvaluation represents a variation to be evaluated.
type VariationForEvaluation struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Approach string `json:"approach"`
}

// VariationEvaluationInput is the input to the evaluator.
type VariationEvaluationInput struct {
	HopName    string                   `json:"hop_name"`
	Criteria   []EvaluationCriterion    `json:"criteria"`
	Variations []VariationForEvaluation `json:"variations"`
}

// VariationScore is a score for a single criterion.
type VariationScore struct {
	CriterionName string  `json:"criterion_name" desc:"The name of the criterion being scored"`
	Score         float64 `json:"score" desc:"Score from 0.0 to 1.0"`
	Rationale     string  `json:"rationale" desc:"Brief explanation for this score (1 sentence)"`
}

// VariationEvaluation is the evaluation result for one variation.
type VariationEvaluation struct {
	VariationID string           `json:"variation_id" desc:"The ID of the variation being evaluated"`
	Scores      []VariationScore `json:"scores" desc:"Scores for each evaluation criterion"`
}

// VariationEvaluationResponse is the response from the evaluator.
type VariationEvaluationResponse struct {
	Evaluations []VariationEvaluation `json:"evaluations" desc:"Evaluation results for each variation"`
	Summary     string                `json:"summary" desc:"Brief summary comparing the variations (2-3 sentences)"`
}

// Evaluate evaluates variations against criteria.
func (e *VariationEvaluator) Evaluate(ctx context.Context, input VariationEvaluationInput) (*VariationEvaluationResponse, int, error) {
	inputJSON, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return nil, 0, fmt.Errorf("marshal input: %w", err)
	}

	userMessage := fmt.Sprintf(`Evaluate these variations against the criteria:

%s

For each variation, provide a score (0.0-1.0) for each criterion with a brief rationale.`, string(inputJSON))

	resp, err := e.client.SendMessageWithSchema(ctx, variationEvaluatorSystemPrompt, []Message{
		{Role: "user", Content: userMessage},
	}, 4096, VariationEvaluationResponseSchema())
	if err != nil {
		return nil, 0, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()
	var result VariationEvaluationResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, 0, fmt.Errorf("parse response: %w (content: %s)", err, content)
	}

	return &result, resp.Usage.TotalTokens(), nil
}
