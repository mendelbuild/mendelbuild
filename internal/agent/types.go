package agent

// ResourceEstimate is an estimated cost for a single resource type.
type ResourceEstimate struct {
	ResourceType string  `json:"resource_type" desc:"Resource type matching a funding source (e.g., 'dollars', 'claude_tokens')"`
	Amount       float64 `json:"amount" desc:"Estimated consumption of this resource. Be realistic based on complexity."`
}

// ProposedHop is a hop proposal within a roadmap.
type ProposedHop struct {
	Name           string             `json:"name" desc:"Short kebab-case identifier (e.g., 'core-budget-calculator', 'user-onboarding-flow')"`
	Commentary     string             `json:"commentary" desc:"Explains what this hop achieves, why it matters, and its expected impact. 2-4 sentences."`
	ObjectiveIDs   []string           `json:"objective_ids" desc:"UUIDs of objectives this hop is meant to advance. Use the exact IDs from the strategy input."`
	EstimatedCosts []ResourceEstimate `json:"estimated_costs" desc:"Resource estimates for this hop. Include entries for each relevant resource type from the strategy's funding sources. Missing funding sources are assumed to be zero cost estimates."`
	DependsOn      []string           `json:"depends_on" desc:"Names of other hops in this roadmap that must complete first. Use exact hop names. Empty array if no dependencies."`
}

// ProposedRoadmap is an AI-generated roadmap proposal.
type ProposedRoadmap struct {
	Hops             []ProposedHop `json:"hops" desc:"Ordered list of hops to execute."`
	FeasibilityNotes string        `json:"feasibility_notes" desc:"Overall assessment of roadmap feasibility, key risks, assumptions, and budget concerns. 2-4 sentences."`
}

// TotalEstimatedCosts returns aggregated costs across all hops, per resource type.
func (r *ProposedRoadmap) TotalEstimatedCosts() []ResourceEstimate {
	totals := make(map[string]float64)
	for _, hop := range r.Hops {
		for _, cost := range hop.EstimatedCosts {
			totals[cost.ResourceType] += cost.Amount
		}
	}

	var result []ResourceEstimate
	for rt, amount := range totals {
		result = append(result, ResourceEstimate{
			ResourceType: rt,
			Amount:       amount,
		})
	}
	return result
}

// BudgetUtilization computes utilization per resource type given available funding.
func (r *ProposedRoadmap) BudgetUtilization(funding []ResourceEstimate) map[string]float64 {
	fundingByType := make(map[string]float64)
	for _, f := range funding {
		fundingByType[f.ResourceType] = f.Amount
	}

	result := make(map[string]float64)
	for _, cost := range r.TotalEstimatedCosts() {
		if budget, ok := fundingByType[cost.ResourceType]; ok && budget > 0 {
			result[cost.ResourceType] = cost.Amount / budget
		}
	}
	return result
}

// ObjectiveInfo is a simplified objective representation for the proposer context.
type ObjectiveInfo struct {
	ID          string          `json:"id" desc:"UUID of the objective"`
	Description string          `json:"description" desc:"Plain-English description of the objective"`
	KeyResults  []KeyResultInfo `json:"key_results" desc:"Quantitative targets for this objective"`
}

// KeyResultInfo is a simplified key result representation.
type KeyResultInfo struct {
	ID          string  `json:"id" desc:"UUID of the key result"`
	Description string  `json:"description" desc:"What this key result measures"`
	TargetUnits string  `json:"target_units" desc:"Target value with units (e.g., '100 users', '99.9%')"`
	TargetDate  *string `json:"target_date,omitempty" desc:"When target should be achieved (ISO 8601 date)"`
}

// StrategyContext is the strategy info passed to the proposer.
type StrategyContext struct {
	ID         string             `json:"id" desc:"UUID of the strategy"`
	Name       string             `json:"name" desc:"Human-readable strategy name"`
	Objectives []ObjectiveInfo    `json:"objectives" desc:"Strategic objectives with their key results"`
	Funding    []ResourceEstimate `json:"funding" desc:"Available budget by resource type"`
}

// RevisionRequest is the structured input for roadmap revision.
type RevisionRequest struct {
	CurrentRoadmap ProposedRoadmap `json:"current_roadmap" desc:"The existing roadmap to revise"`
	Feedback       string          `json:"feedback" desc:"User's requested changes to the roadmap"`
	Strategy       StrategyContext `json:"strategy" desc:"Full strategy context for reference"`
}

// ProposerResponse is the structured output from the proposer.
type ProposerResponse struct {
	Roadmap ProposedRoadmap `json:"roadmap" desc:"The complete roadmap proposal"`
}

// ProposedVariation is a single variation approach within a hop.
type ProposedVariation struct {
	Name            string `json:"name" desc:"Short kebab-case identifier for this approach (e.g., 'redis-cache', 'in-memory-cache')"`
	Approach        string `json:"approach" desc:"Detailed description of the implementation approach. Include key technical decisions, libraries to use, and architecture. 3-6 sentences."`
	Differentiation string `json:"differentiation" desc:"Explains how this approach differs from the others and why someone might prefer it. 2-3 sentences."`
	EstimatedTokens int    `json:"estimated_tokens" desc:"Estimated Claude tokens needed to implement this approach. Consider complexity and code volume."`
}

// VariationProposal is the output from the variation proposer.
type VariationProposal struct {
	HopID      string              `json:"hop_id" desc:"UUID of the hop these variations are for"`
	Variations []ProposedVariation `json:"variations" desc:"2-4 differentiated implementation approaches"`
	Rationale  string              `json:"rationale" desc:"Overall rationale for why these specific approaches were chosen. Explains the design space explored. 2-4 sentences."`
}

// HopContext provides hop information to the variation proposer.
type HopContext struct {
	ID         string   `json:"id" desc:"UUID of the hop"`
	Name       string   `json:"name" desc:"Hop name (kebab-case identifier)"`
	Commentary string   `json:"commentary" desc:"What this hop achieves and why it matters"`
	Objectives []string `json:"objectives" desc:"Objective descriptions this hop advances"`
}

// VariationProposerInput is the input to the variation proposer.
type VariationProposerInput struct {
	Hop             HopContext `json:"hop" desc:"The hop to propose variations for"`
	RepositoryURL   string     `json:"repository_url" desc:"URL of the code repository"`
	RepositorySummary string   `json:"repository_summary,omitempty" desc:"Optional summary of the repository structure and tech stack"`
	AvailableBudget int        `json:"available_budget" desc:"Available Claude tokens for this hop"`
	NumVariations   int        `json:"num_variations" desc:"Number of variations to propose (typically 2-4)"`
}

// VariationProposerResponse is the structured output from the variation proposer.
type VariationProposerResponse struct {
	Proposal VariationProposal `json:"proposal" desc:"The variation proposal"`
}

// CurrentVariation represents an existing variation in a revision request.
type CurrentVariation struct {
	Name            string `json:"name" desc:"Current variation name"`
	Approach        string `json:"approach" desc:"Current implementation approach"`
	Differentiation string `json:"differentiation" desc:"Current differentiation rationale"`
	EstimatedTokens int    `json:"estimated_tokens" desc:"Current token estimate"`
}

// VariationRevisionInput is the input for revising variations based on feedback.
type VariationRevisionInput struct {
	Hop               HopContext         `json:"hop" desc:"The hop context"`
	RepositoryURL     string             `json:"repository_url" desc:"URL of the code repository"`
	CurrentVariations []CurrentVariation `json:"current_variations" desc:"The current variation proposals to revise"`
	Feedback          string             `json:"feedback" desc:"User feedback requesting changes"`
}

// EvaluationCriterion is a single criterion for comparing Variations.
type EvaluationCriterion struct {
	Name        string `json:"name" desc:"Short descriptive name for this criterion (e.g., 'Code Clarity', 'Test Coverage', 'Performance')"`
	Description string `json:"description" desc:"What this criterion measures and why it matters for this hop. 1-2 sentences."`
	Measurable  bool   `json:"measurable" desc:"True if this criterion can be objectively measured (e.g., test count), false if subjective (e.g., code elegance)"`
	Weight      int    `json:"weight" desc:"Relative importance from 1-5, where 5 is most important"`
}

// EvaluationCriteria is the set of criteria for comparing Variations within a Hop.
type EvaluationCriteria struct {
	Criteria   []EvaluationCriterion `json:"criteria" desc:"3-6 criteria for comparing Variations, ordered by importance"`
	Rationale  string                `json:"rationale" desc:"Why these specific criteria matter for this hop. 2-3 sentences."`
	Tradeoffs  string                `json:"tradeoffs" desc:"Expected tradeoffs between criteria that the human should consider. 2-3 sentences."`
}

// EvaluationCriteriaInput is the input to the evaluation criteria generator.
type EvaluationCriteriaInput struct {
	HopName       string   `json:"hop_name" desc:"The hop name (kebab-case identifier)"`
	HopCommentary string   `json:"hop_commentary" desc:"What this hop achieves and why it matters"`
	Objectives    []string `json:"objectives" desc:"Objective descriptions this hop advances"`
}

// EvaluationCriteriaResponse is the structured output from the criteria generator.
type EvaluationCriteriaResponse struct {
	Criteria EvaluationCriteria `json:"criteria" desc:"The evaluation criteria for this hop"`
}

// =====================================================
// OKR Tuner Types (for quality feedback on O's and KR's)
// =====================================================

// ObjectiveForTuning is an objective to be evaluated for quality.
type ObjectiveForTuning struct {
	ID          string `json:"id" desc:"UUID of the objective"`
	Description string `json:"description" desc:"The objective description to evaluate"`
}

// KeyResultForTuning is a key result to be evaluated for quality.
type KeyResultForTuning struct {
	ID          string `json:"id" desc:"UUID of the key result"`
	Description string `json:"description" desc:"What this key result measures"`
	TargetUnits string `json:"target_units" desc:"Target value with units (e.g., '100 users', '99.9%')"`
}

// OKRTuneInput is the input to the OKR tuner.
type OKRTuneInput struct {
	Objectives []ObjectiveForTuning `json:"objectives" desc:"Objectives to evaluate for quality"`
	KeyResults []KeyResultForTuning `json:"key_results" desc:"Key results to evaluate for quality"`
}

// ItemTuning is the quality feedback for a single item.
type ItemTuning struct {
	ID       string  `json:"id" desc:"UUID of the objective or key result"`
	Score    float64 `json:"score" desc:"Quality score 0.0-1.0: How well-written, specific, and measurable is this?"`
	Feedback string  `json:"feedback" desc:"Brief feedback (1-2 sentences) on clarity, specificity, and actionability"`
}

// OKRTuneResponse is the output from the OKR tuner.
type OKRTuneResponse struct {
	ObjectiveScores []ItemTuning `json:"objective_scores" desc:"Quality feedback for each objective"`
	KeyResultScores []ItemTuning `json:"key_result_scores" desc:"Quality feedback for each key result"`
}
