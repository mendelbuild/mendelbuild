package agent

// ResourceEstimate is an estimated cost for a single resource type.
type ResourceEstimate struct {
	ResourceType string  `json:"resource_type"` // "dollars", "claude_tokens", etc.
	Amount       float64 `json:"amount"`
}

// ProposedHop is a hop proposal within a roadmap.
type ProposedHop struct {
	Name           string             `json:"name"`
	Kind           string             `json:"kind"`
	Commentary     string             `json:"commentary"`
	ObjectiveIDs   []string           `json:"objective_ids"`   // Which objectives this advances
	EstimatedCosts []ResourceEstimate `json:"estimated_costs"` // Per-resource estimates
	DependsOn      []string           `json:"depends_on"`      // Names of hops this depends on
}

// ProposedRoadmap is an AI-generated roadmap proposal.
type ProposedRoadmap struct {
	Hops             []ProposedHop `json:"hops"`
	FeasibilityNotes string        `json:"feasibility_notes"`
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
	ID          string          `json:"id"`
	Description string          `json:"description"`
	KeyResults  []KeyResultInfo `json:"key_results"`
}

// KeyResultInfo is a simplified key result representation.
type KeyResultInfo struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	TargetUnits string  `json:"target_units"`
	TargetDate  *string `json:"target_date,omitempty"`
}

// StrategyContext is the strategy info passed to the proposer.
type StrategyContext struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Objectives []ObjectiveInfo    `json:"objectives"`
	Funding    []ResourceEstimate `json:"funding"`
}

// RevisionRequest is the structured input for roadmap revision (JSON in, JSON out).
type RevisionRequest struct {
	CurrentRoadmap ProposedRoadmap `json:"current_roadmap"`
	Feedback       string          `json:"feedback"`
	Strategy       StrategyContext `json:"strategy"`
}

// ProposerResponse is the structured output from the proposer.
type ProposerResponse struct {
	Roadmap ProposedRoadmap `json:"roadmap"`
}
