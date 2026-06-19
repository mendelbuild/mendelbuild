package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

const proposerSystemPrompt = `You are a strategic roadmap proposer for MendelBuild, an evolutionary software development system.

Your task is to propose a roadmap of "Hops" - evolutionary experiments that advance strategic objectives.

Guidelines:
1. Each hop should clearly advance one or more strategic objectives
2. Estimated costs should be realistic based on the hop's scope
3. Dependencies should form a valid DAG (no cycles)
4. Order hops logically - foundational work before dependent work
5. Consider budget constraints from funding sources
6. Keep hop names short but descriptive (use kebab-case)
7. Commentary should explain the "why" and expected impact
8. Valid hop kinds: 'code_quality', 'performance', 'user_engagement', 'cost_reduction', 'feature', 'infrastructure'`

const revisionSystemPrompt = `You are a strategic roadmap proposer for MendelBuild. You are revising an existing roadmap based on user feedback.

Apply the user's feedback to modify the roadmap. You may:
- Add, remove, or modify hops
- Adjust estimated costs
- Change dependencies
- Update the feasibility notes

Guidelines:
1. Each hop should clearly advance one or more strategic objectives
2. Estimated costs should be realistic based on the hop's scope
3. Dependencies should form a valid DAG (no cycles)
4. Order hops logically - foundational work before dependent work
5. Consider budget constraints from funding sources
6. Keep hop names short but descriptive (use kebab-case)
7. Commentary should explain the "why" and expected impact
8. Valid hop kinds: 'code_quality', 'performance', 'user_engagement', 'cost_reduction', 'feature', 'infrastructure'`

// Proposer generates roadmap proposals.
type Proposer struct {
	client *Client
}

// NewProposer creates a new Proposer.
func NewProposer(client *Client) *Proposer {
	return &Proposer{client: client}
}

// ProposeRoadmap generates an initial roadmap proposal for a strategy.
func (p *Proposer) ProposeRoadmap(ctx context.Context, strategy StrategyContext) (*ProposedRoadmap, int, error) {
	strategyJSON, err := json.MarshalIndent(strategy, "", "  ")
	if err != nil {
		return nil, 0, fmt.Errorf("marshal strategy: %w", err)
	}

	userMessage := fmt.Sprintf(`Propose a roadmap for the following strategy:

%s

Generate a roadmap that advances the stated objectives within the available budget.`, string(strategyJSON))

	resp, err := p.client.SendMessageWithSchema(ctx, proposerSystemPrompt, []Message{
		{Role: "user", Content: userMessage},
	}, 8192, ProposerResponseSchema())
	if err != nil {
		return nil, 0, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()
	var result ProposerResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, 0, fmt.Errorf("parse response: %w (content: %s)", err, content)
	}

	return &result.Roadmap, resp.Usage.TotalTokens(), nil
}

// ReviseRoadmap revises an existing roadmap based on user feedback.
func (p *Proposer) ReviseRoadmap(ctx context.Context, req RevisionRequest) (*ProposedRoadmap, int, error) {
	reqJSON, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	userMessage := fmt.Sprintf(`Revise the roadmap based on this revision request:

%s

Apply the feedback to update the roadmap.`, string(reqJSON))

	resp, err := p.client.SendMessageWithSchema(ctx, revisionSystemPrompt, []Message{
		{Role: "user", Content: userMessage},
	}, 8192, ProposerResponseSchema())
	if err != nil {
		return nil, 0, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()
	var result ProposerResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, 0, fmt.Errorf("parse response: %w (content: %s)", err, content)
	}

	return &result.Roadmap, resp.Usage.TotalTokens(), nil
}
