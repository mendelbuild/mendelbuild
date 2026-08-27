package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

const costAuditorSystemPrompt = `You are a cost auditor reviewing budget estimates for a software roadmap. Your job is to be the check on an estimate, not a second opinion that agrees with it.

The estimates you are auditing were produced by another model. Treat its stated reasoning as a claim to verify, not as evidence. An estimate that sounds thorough but rests on nothing is worse than one that admits uncertainty, because it invites a human to trust it.

What actually drives cost:
- Model spend on generating Variations dominates. A Hop's cost is roughly the number of Variations it needs times the cost of generating one.
- Long agentic coding runs are cache-heavy. Most of the prompt is re-read context, so cost scales with how many files an approach has to touch and how many rounds it takes, not with lines of code written.
- Hops that fail and retry cost multiples of their estimate. A Hop touching unfamiliar or highly-coupled code should carry a higher figure than its scope suggests.
- Hosting is usually a rounding error next to model spend, unless something is left running.

How to judge:
1. Anchor to the calibration data. If the project has completed Hops, their actual costs are the strongest evidence available. Find the closest comparable and reason from it.
2. Apply the observed bias. If estimate_bias_ratio is above 1.0, past estimates on this project ran low, and new ones likely do too.
3. Where there is no history, say so. Mark such estimates 'unsupported' and keep your confidence low. Do not manufacture precision to seem useful - an honest "we cannot know this yet" is the correct answer for a project with no track record, and a fabricated figure that a human then plans against is a real harm.
4. Judge the budget against what is left, not the original total.

Be specific and falsifiable. Name the Hops you compared against and the assumptions that would have to break for your figure to be wrong.`

// CostAuditor fact-checks the roadmap proposer's cost estimates against this
// project's observed spend history.
//
// It exists because an estimate nothing ever challenges is indistinguishable
// from a guess. The auditor is deliberately a separate call with a separate
// prompt: asking the proposer to grade its own arithmetic reliably produces
// agreement rather than scrutiny.
type CostAuditor struct {
	client *Client
}

// NewCostAuditor creates a CostAuditor.
func NewCostAuditor(client *Client) *CostAuditor {
	return &CostAuditor{client: client}
}

// CostAuditResponseSchema returns the JSON schema for the auditor's output.
func CostAuditResponseSchema() json.RawMessage {
	return SchemaFromType(reflect.TypeOf(CostAuditResponse{}))
}

// Audit reviews a proposed roadmap's cost estimates.
func (a *CostAuditor) Audit(ctx context.Context, input CostAuditInput) (*CostAuditResponse, Spend, error) {
	if len(input.Roadmap.Hops) == 0 {
		return &CostAuditResponse{}, Spend{}, nil
	}

	inputJSON, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return nil, Spend{}, fmt.Errorf("marshal input: %w", err)
	}

	history := "This project has no completed Hops yet, so there is no observed cost history to check these estimates against."
	if input.Strategy.Calibration.HasHistory() {
		history = fmt.Sprintf(
			"This project has %d completed Hops of observed cost history. Use them.",
			input.Strategy.Calibration.SampleSize)
	}

	userMessage := fmt.Sprintf(`Audit the cost estimates in this roadmap.

%s

%s

Return a verdict for every hop, a revised total, and a plain judgement on whether the remaining budget covers it.`, history, string(inputJSON))

	resp, err := a.client.SendMessageWithSchema(ctx, costAuditorSystemPrompt, []Message{
		{Role: "user", Content: userMessage},
	}, 8192, CostAuditResponseSchema())
	if err != nil {
		return nil, Spend{}, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()
	var result CostAuditResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, Spend{}, fmt.Errorf("parse response: %w (content: %s)", err, content)
	}

	return &result, resp.Spend(), nil
}
