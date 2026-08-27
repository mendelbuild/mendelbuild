package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

const variationProposerSystemPrompt = `You are a variation proposer for MendelBuild, an evolutionary software development system.

Your task is to propose multiple differentiated "Variations" - alternative implementation approaches for a single Hop (experiment). Each variation will be implemented in parallel by separate Claude CLI instances.

Guidelines:
1. Propose approaches that are genuinely different, not just minor tweaks
2. Consider different architectural patterns, libraries, or algorithms
3. Balance innovation with pragmatism - some approaches can be safe, others exploratory
4. Cost estimates are in US dollars and cover generating that variation. Anchor them
   to median_variation_usd in the calibration data when it is present; an approach that
   has to read and touch more of the codebase costs proportionally more. With no
   history to go on, say so rather than inventing a confident figure. The variations
   you propose should collectively fit inside available_budget_usd.
5. Each approach should be self-contained and independently implementable
6. Use kebab-case for variation names (e.g., 'redis-cache', 'postgresql-native')
7. Differentiation should explain trade-offs clearly (performance vs simplicity, etc.)

IMPORTANT - Respecting Completed Dependencies:
If completed_dependencies is provided, these represent decisions from prior Hops that are ALREADY IMPLEMENTED in the codebase. You MUST:
- Build upon these existing choices, not contradict them
- Use the same infrastructure, libraries, and patterns they established
- Not propose alternatives to decisions already made (e.g., if a dependency chose Render for hosting, don't propose AWS)
- Reference these dependencies when relevant to explain why certain choices are constrained`

const variationRevisionSystemPrompt = `You are a variation proposer for MendelBuild. You are revising existing variation proposals based on user feedback.

Apply the user's feedback to modify the variations. You may:
- Add, remove, or modify variation approaches
- Adjust estimated dollar costs
- Change differentiation rationale
- Rename variations

Guidelines:
1. Propose approaches that are genuinely different, not just minor tweaks
2. Consider different architectural patterns, libraries, or algorithms
3. Balance innovation with pragmatism - some approaches can be safe, others exploratory
4. Cost estimates are in US dollars and cover generating that variation. Anchor them
   to median_variation_usd in the calibration data when it is present; an approach that
   has to read and touch more of the codebase costs proportionally more. With no
   history to go on, say so rather than inventing a confident figure. The variations
   you propose should collectively fit inside available_budget_usd.
5. Each approach should be self-contained and independently implementable
6. Use kebab-case for variation names (e.g., 'redis-cache', 'postgresql-native')
7. Differentiation should explain trade-offs clearly (performance vs simplicity, etc.)

IMPORTANT - Respecting Completed Dependencies:
If completed_dependencies is provided, these represent decisions from prior Hops that are ALREADY IMPLEMENTED in the codebase. You MUST:
- Build upon these existing choices, not contradict them
- Use the same infrastructure, libraries, and patterns they established
- Not propose alternatives to decisions already made
- Reference these dependencies when relevant to explain why certain choices are constrained`

// VariationProposer generates variation proposals for hops.
type VariationProposer struct {
	client *Client
}

// NewVariationProposer creates a new VariationProposer.
func NewVariationProposer(client *Client) *VariationProposer {
	return &VariationProposer{client: client}
}

// ProposeVariations generates variation proposals for a hop.
func (p *VariationProposer) ProposeVariations(ctx context.Context, input VariationProposerInput) (*VariationProposal, Spend, error) {
	inputJSON, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return nil, Spend{}, fmt.Errorf("marshal input: %w", err)
	}

	userMessage := fmt.Sprintf(`Propose %d differentiated implementation approaches for this hop:

%s

Generate variations that explore different parts of the design space. Each should be viable but offer different trade-offs.`, input.NumVariations, string(inputJSON))

	resp, err := p.client.SendMessageWithSchema(ctx, variationProposerSystemPrompt, []Message{
		{Role: "user", Content: userMessage},
	}, 8192, VariationProposerResponseSchema())
	if err != nil {
		return nil, Spend{}, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()
	var result VariationProposerResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, Spend{}, fmt.Errorf("parse response: %w (content: %s)", err, content)
	}

	// Set the hop ID from the input
	result.Proposal.HopID = input.Hop.ID

	return &result.Proposal, resp.Spend(), nil
}

// ReviseVariations revises existing variation proposals based on user feedback.
func (p *VariationProposer) ReviseVariations(ctx context.Context, input VariationRevisionInput) (*VariationProposal, Spend, error) {
	inputJSON, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return nil, Spend{}, fmt.Errorf("marshal input: %w", err)
	}

	userMessage := fmt.Sprintf(`Revise the variation proposals based on this feedback:

%s

Apply the feedback to update the variations.`, string(inputJSON))

	resp, err := p.client.SendMessageWithSchema(ctx, variationRevisionSystemPrompt, []Message{
		{Role: "user", Content: userMessage},
	}, 8192, VariationProposerResponseSchema())
	if err != nil {
		return nil, Spend{}, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()
	var result VariationProposerResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, Spend{}, fmt.Errorf("parse response: %w (content: %s)", err, content)
	}

	// Set the hop ID from the input
	result.Proposal.HopID = input.Hop.ID

	return &result.Proposal, resp.Spend(), nil
}
