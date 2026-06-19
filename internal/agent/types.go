package agent

// ResourceEstimate is an estimated cost for a single resource type.
type ResourceEstimate struct {
	ResourceType string  `json:"resource_type" desc:"Resource type matching a funding source (e.g., 'dollars', 'claude_tokens')"`
	Amount       float64 `json:"amount" desc:"Estimated consumption of this resource. Be realistic based on complexity."`
}

// ProposedHop is a hop proposal within a roadmap.
type ProposedHop struct {
	Name           string             `json:"name" desc:"Short kebab-case identifier (e.g., 'core-budget-calculator', 'user-onboarding-flow')"`
	Kind           string             `json:"kind" desc:"Hop category. Must be one of: 'feature', 'infrastructure', 'performance', 'code_quality', 'user_engagement', 'cost_reduction'"`
	Commentary     string             `json:"commentary" desc:"Explains what this hop achieves, why it matters, and its expected impact. 2-4 sentences."`
	ObjectiveIDs   []string           `json:"objective_ids" desc:"UUIDs of objectives this hop advances. Copy exact IDs from the strategy input."`
	EstimatedCosts []ResourceEstimate `json:"estimated_costs" desc:"Resource estimates for this hop. Include entries for each relevant resource type from the strategy's funding sources."`
	DependsOn      []string           `json:"depends_on" desc:"Names of other hops in this roadmap that must complete first. Use exact hop names. Empty array if no dependencies."`
}

// ProposedRoadmap is an AI-generated roadmap proposal.
type ProposedRoadmap struct {
	Hops             []ProposedHop `json:"hops" desc:"Ordered list of hops to execute. Earlier hops should be foundational."`
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
