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
2. Dependencies should form a valid DAG (no cycles)
3. Order hops logically - foundational work before dependent work
4. Keep hop names short but descriptive (use kebab-case)
5. Commentary should explain the "why" and expected impact (2-4 sentences)

Cost estimates:
Estimates are in US dollars and will be checked against what the work actually
costs, so treat them as predictions you will be held to rather than as decoration.

- Anchor every estimate to the calibration data in the strategy input. When
  median_hop_usd is non-zero, a hop of ordinary scope should land near it, and a
  hop you believe is unusually large or small needs its cost_basis to say why.
- When estimate_bias_ratio is above 1.0, past estimates on this project ran low.
  Scale your figures up by roughly that factor rather than repeating the mistake.
- Cost is driven mainly by how many Variations a hop needs and how much code
  each one has to read and touch. A hop reaching into unfamiliar or highly
  coupled code costs more than its scope suggests, because attempts fail and retry.
- With no calibration history, say so in cost_basis and keep cost_confidence at
  0.4 or below. A number invented to look decisive is worse than an admitted
  unknown, because someone will plan against it.
- The roadmap's total must fit inside budget_usd minus spent_usd. If it cannot,
  still give honest per-hop figures and say plainly in feasibility_notes that the
  roadmap exceeds the budget. Never trim estimates to make a roadmap appear to fit.`

const revisionSystemPrompt = `You are a strategic roadmap proposer for MendelBuild. You are revising an existing roadmap based on user feedback.

CRITICAL: If existing_hops is provided, hops marked is_terminal=true are IMMUTABLE:
- You MUST include them in the output exactly as they appear (same name, same commentary)
- You CANNOT remove, rename, or modify terminal hops in any way
- You CAN add new hops that depend on terminal hops
- Terminal hops represent completed historical work and must remain in the record

For non-terminal hops, you may:
- Modify or remove them based on feedback
- Change their dependencies or cost estimates

For new hops, you may:
- Add them freely
- Set dependencies on any existing hop (terminal or not)

Guidelines:
1. Each hop should clearly advance one or more strategic objectives
2. Dependencies should form a valid DAG (no cycles)
3. Order hops logically - foundational work before dependent work
4. Keep hop names short but descriptive (use kebab-case)
5. Commentary should explain the "why" and expected impact (2-4 sentences)

Cost estimates:
Estimates are in US dollars and will be checked against what the work actually
costs, so treat them as predictions you will be held to rather than as decoration.

- Anchor every estimate to the calibration data in the strategy input. When
  median_hop_usd is non-zero, a hop of ordinary scope should land near it, and a
  hop you believe is unusually large or small needs its cost_basis to say why.
- When estimate_bias_ratio is above 1.0, past estimates on this project ran low.
  Scale your figures up by roughly that factor rather than repeating the mistake.
- Cost is driven mainly by how many Variations a hop needs and how much code
  each one has to read and touch. A hop reaching into unfamiliar or highly
  coupled code costs more than its scope suggests, because attempts fail and retry.
- With no calibration history, say so in cost_basis and keep cost_confidence at
  0.4 or below. A number invented to look decisive is worse than an admitted
  unknown, because someone will plan against it.
- The roadmap's total must fit inside budget_usd minus spent_usd. If it cannot,
  still give honest per-hop figures and say plainly in feasibility_notes that the
  roadmap exceeds the budget. Never trim estimates to make a roadmap appear to fit.`

// Proposer generates roadmap proposals.
type Proposer struct {
	client *Client
}

// NewProposer creates a new Proposer.
func NewProposer(client *Client) *Proposer {
	return &Proposer{client: client}
}

// ProposeRoadmap generates an initial roadmap proposal for a strategy.
func (p *Proposer) ProposeRoadmap(ctx context.Context, strategy StrategyContext) (*ProposedRoadmap, Spend, error) {
	strategyJSON, err := json.MarshalIndent(strategy, "", "  ")
	if err != nil {
		return nil, Spend{}, fmt.Errorf("marshal strategy: %w", err)
	}

	userMessage := fmt.Sprintf(`Propose a roadmap for the following strategy:

%s

Generate a roadmap that advances the stated objectives within the available budget. Ground every cost estimate in the calibration data if any is present.`, string(strategyJSON))

	resp, err := p.client.SendMessageWithSchema(ctx, proposerSystemPrompt, []Message{
		{Role: "user", Content: userMessage},
	}, 8192, ProposerResponseSchema())
	if err != nil {
		return nil, Spend{}, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()
	var result ProposerResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, Spend{}, fmt.Errorf("parse response: %w (content: %s)", err, content)
	}

	return &result.Roadmap, resp.Spend(), nil
}

// ReviseRoadmap revises an existing roadmap based on user feedback.
func (p *Proposer) ReviseRoadmap(ctx context.Context, req RevisionRequest) (*ProposedRoadmap, Spend, error) {
	reqJSON, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, Spend{}, fmt.Errorf("marshal request: %w", err)
	}

	userMessage := fmt.Sprintf(`Revise the roadmap based on this revision request:

%s

Apply the feedback to update the roadmap.`, string(reqJSON))

	resp, err := p.client.SendMessageWithSchema(ctx, revisionSystemPrompt, []Message{
		{Role: "user", Content: userMessage},
	}, 8192, ProposerResponseSchema())
	if err != nil {
		return nil, Spend{}, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()
	var result ProposerResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, Spend{}, fmt.Errorf("parse response: %w (content: %s)", err, content)
	}

	return &result.Roadmap, resp.Spend(), nil
}
