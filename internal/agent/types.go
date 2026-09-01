package agent

// CompletedHopCost is one observed estimate-vs-actual outcome, passed to the
// estimating agents so their guesses are anchored to what this project has
// actually cost rather than to intuition.
type CompletedHopCost struct {
	Name        string  `json:"name" desc:"Name of the completed hop"`
	Commentary  string  `json:"commentary" desc:"What that hop set out to achieve, so you can judge whether a new hop is comparable in scope"`
	EstimatedUSD float64 `json:"estimated_usd" desc:"What this hop was estimated to cost in USD before it ran. Zero if it was never estimated."`
	ActualUSD   float64 `json:"actual_usd" desc:"What this hop actually cost in USD, summed from the cost ledger"`
	Variations  int     `json:"variations" desc:"How many variations were generated, the main driver of a hop's cost"`
}

// CostCalibration is the observed cost history handed to estimating agents.
//
// This is the difference between an estimate and a guess. Without it the model
// is inventing dollar figures from nothing; with it, it has this project's own
// track record, including how wrong the last estimates were.
type CostCalibration struct {
	Model             string             `json:"model" desc:"The model these figures were produced by, and the one the new work will run on. Costs from a different model do not transfer: prices differ several-fold and prompt caching changes them again by roughly a factor of six."`
	CompletedHops     []CompletedHopCost `json:"completed_hops" desc:"Recent completed hops with their estimated and actual costs, all built by this same model. Treat these as the strongest available evidence for what a new hop will cost."`
	MedianHopUSD      float64            `json:"median_hop_usd" desc:"Median actual USD cost of a completed hop in this project. Zero when there is no history yet."`
	MedianVariationUSD float64           `json:"median_variation_usd" desc:"Median actual USD cost of generating one variation. Multiply by the expected number of variations for a first-order estimate."`
	EstimateBiasRatio float64            `json:"estimate_bias_ratio" desc:"Median of actual/estimated across completed hops. Above 1.0 means past estimates were too low and you should revise new estimates upward by roughly this factor. Zero when there is not enough history."`
	SampleSize        int                `json:"sample_size" desc:"How many completed hops these figures are drawn from. Below 3, treat them as weak evidence and say so in your confidence."`
}

// HasHistory reports whether there is enough observed data to calibrate against.
func (c *CostCalibration) HasHistory() bool {
	return c != nil && c.SampleSize > 0
}

// ProposedHop is a hop proposal within a roadmap.
type ProposedHop struct {
	Name             string   `json:"name" desc:"Short kebab-case identifier (e.g., 'core-budget-calculator', 'user-onboarding-flow')"`
	Commentary       string   `json:"commentary" desc:"Explains what this hop achieves, why it matters, and its expected impact. 2-4 sentences."`
	ObjectiveIDs     []string `json:"objective_ids" desc:"UUIDs of objectives this hop is meant to advance. Use the exact IDs from the strategy input."`
	EstimatedCostUSD float64  `json:"estimated_cost_usd" desc:"Estimated total cost of this hop in US dollars, covering model spend for every variation you expect it to need plus any hosting. Anchor this to the calibration data in the strategy input: if median_hop_usd is non-zero, a hop of ordinary scope should land near it, and estimate_bias_ratio above 1.0 means you should scale up. Do not invent a figure when history is available."`
	CostBasis        string   `json:"cost_basis" desc:"One or two sentences stating how you arrived at the figure: which completed hops you treated as comparable, how many variations you assumed, and what would make it wrong. This is shown to a human for fact-checking, so be concrete and falsifiable rather than reassuring."`
	CostConfidence   float64  `json:"cost_confidence" desc:"Your confidence in this estimate from 0.0 to 1.0. Be honest: with no calibration history, nothing above 0.4 is defensible. Reserve values above 0.7 for hops closely comparable to several completed ones."`
	DependsOn        []string `json:"depends_on" desc:"Names of other hops in this roadmap that must complete first. Use exact hop names. Empty array if no dependencies."`
}

// ProposedRoadmap is an AI-generated roadmap proposal.
type ProposedRoadmap struct {
	Hops             []ProposedHop `json:"hops" desc:"Ordered list of hops to execute."`
	FeasibilityNotes string        `json:"feasibility_notes" desc:"Overall assessment of roadmap feasibility, key risks, assumptions, and budget concerns. State plainly if the roadmap does not fit the available budget. 2-4 sentences."`
}

// TotalEstimatedUSD is the summed estimate across every proposed hop.
func (r *ProposedRoadmap) TotalEstimatedUSD() float64 {
	var total float64
	for _, hop := range r.Hops {
		total += hop.EstimatedCostUSD
	}
	return total
}

// BudgetUtilization is the fraction of the available budget this roadmap plans
// to consume. The bool is false when there is no budget to compare against.
func (r *ProposedRoadmap) BudgetUtilization(budgetUSD float64) (float64, bool) {
	if budgetUSD <= 0 {
		return 0, false
	}
	return r.TotalEstimatedUSD() / budgetUSD, true
}

// OverBudget reports whether the roadmap's own estimate exceeds the budget.
func (r *ProposedRoadmap) OverBudget(budgetUSD float64) bool {
	return budgetUSD > 0 && r.TotalEstimatedUSD() > budgetUSD
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
	Target      string  `json:"target" desc:"The target as a phrase, e.g. '>= 100 users', '< 200 ms p99'"`
	TargetDate  *string `json:"target_date,omitempty" desc:"When target should be achieved (ISO 8601 date)"`
}

// StrategyContext is the strategy info passed to the proposer.
type StrategyContext struct {
	ID          string          `json:"id" desc:"UUID of the strategy"`
	Name        string          `json:"name" desc:"Human-readable strategy name"`
	Objectives  []ObjectiveInfo `json:"objectives" desc:"Strategic objectives with their key results"`
	BudgetUSD   float64         `json:"budget_usd" desc:"Total budget available to this strategy, in US dollars. The roadmap's estimated costs must fit inside this."`
	BudgetStart *string         `json:"budget_start,omitempty" desc:"Date the budget period begins (ISO 8601). Use with budget_end to judge whether the roadmap fits in the time available, not just the money."`
	BudgetEnd   *string         `json:"budget_end,omitempty" desc:"Date the budget period ends (ISO 8601)"`
	SpentUSD    float64         `json:"spent_usd" desc:"USD already spent against this strategy. Remaining budget is budget_usd minus this."`

	Calibration *CostCalibration `json:"calibration,omitempty" desc:"This project's observed cost history. When present, it is the primary basis for every cost estimate you produce; ignore it only if you can say why the new work is not comparable."`
}

// ExistingHop represents a hop already in the database with its current status.
type ExistingHop struct {
	Name       string `json:"name" desc:"The hop's name (kebab-case identifier)"`
	Commentary string `json:"commentary" desc:"What this hop achieves"`
	Status     string `json:"status" desc:"Current status: pending, active, selecting, completed, rejected, or abandoned"`
	IsTerminal bool   `json:"is_terminal" desc:"True if status is completed/rejected/abandoned - these hops are IMMUTABLE"`
}

// RevisionRequest is the structured input for roadmap revision.
type RevisionRequest struct {
	CurrentRoadmap ProposedRoadmap `json:"current_roadmap" desc:"The existing roadmap to revise"`
	ExistingHops   []ExistingHop   `json:"existing_hops,omitempty" desc:"Hops already in the database with their statuses. Terminal hops (is_terminal=true) MUST remain unchanged."`
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
	Differentiation string  `json:"differentiation" desc:"Explains how this approach differs from the others and why someone might prefer it. 2-3 sentences."`
	EstimatedCostUSD float64 `json:"estimated_cost_usd" desc:"Estimated cost in US dollars to generate this variation. Anchor to median_variation_usd from the calibration data when it is available; a more complex approach touching more files costs proportionally more."`
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

// CompletedDependencyHop represents a completed dependency hop and its selected variation.
// All fields are pulled directly from the database - no LLM regeneration.
type CompletedDependencyHop struct {
	HopName           string `json:"hop_name" desc:"Name of the completed dependency hop"`
	HopCommentary     string `json:"hop_commentary" desc:"What this hop achieved"`
	VariationName     string `json:"variation_name" desc:"Name of the selected/merged variation"`
	VariationApproach string `json:"variation_approach" desc:"The implementation approach that was selected and is now in the codebase"`
}

// VariationProposerInput is the input to the variation proposer.
type VariationProposerInput struct {
	Hop             HopContext `json:"hop" desc:"The hop to propose variations for"`
	RepositoryURL   string     `json:"repository_url" desc:"URL of the code repository"`
	RepositorySummary string   `json:"repository_summary,omitempty" desc:"Optional summary of the repository structure and tech stack"`
	AvailableBudgetUSD float64 `json:"available_budget_usd" desc:"USD still available for this hop, after spend already incurred. The variations you propose should collectively fit inside it."`
	Calibration     *CostCalibration `json:"calibration,omitempty" desc:"Observed cost history for this project, to anchor per-variation estimates against what generation has actually cost here."`
	NumVariations   int        `json:"num_variations" desc:"Number of variations to propose (typically 2-4)"`
	CompletedDependencies []CompletedDependencyHop `json:"completed_dependencies,omitempty" desc:"Completed dependency hops with their selected variations. These decisions are already implemented in the codebase and MUST be respected."`
}

// VariationProposerResponse is the structured output from the variation proposer.
type VariationProposerResponse struct {
	Proposal VariationProposal `json:"proposal" desc:"The variation proposal"`
}

// CurrentVariation represents an existing variation in a revision request.
type CurrentVariation struct {
	Name            string `json:"name" desc:"Current variation name"`
	Approach        string `json:"approach" desc:"Current implementation approach"`
	Differentiation  string  `json:"differentiation" desc:"Current differentiation rationale"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd" desc:"Current USD cost estimate"`
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

// CostAuditInput is what the cost auditor reviews: a proposed roadmap, the
// budget it must fit inside, and the project's observed cost history.
type CostAuditInput struct {
	Strategy StrategyContext `json:"strategy" desc:"Strategy context including the available budget, spend to date, and calibration history"`
	Roadmap  ProposedRoadmap `json:"roadmap" desc:"The proposed roadmap whose cost estimates you are auditing"`
}

// CostVerdict classifies one hop's estimate.
type CostVerdict string

const (
	CostVerdictSound       CostVerdict = "sound"       // Estimate is defensible as written
	CostVerdictUnderstated CostVerdict = "understated" // Real cost is likely materially higher
	CostVerdictOverstated  CostVerdict = "overstated"  // Real cost is likely materially lower
	CostVerdictUnsupported CostVerdict = "unsupported" // No basis to judge the figure at all
)

// HopCostVerdict is the auditor's judgement on a single hop's estimate.
type HopCostVerdict struct {
	HopName            string  `json:"hop_name" desc:"Exact name of the hop being audited, copied from the roadmap"`
	Verdict            string  `json:"verdict" desc:"One of: 'sound' if the estimate is defensible as written; 'understated' if the real cost is likely materially higher; 'overstated' if likely materially lower; 'unsupported' if there is no basis to judge it. Default to 'unsupported' when the calibration history is empty and the hop's stated basis is vague."`
	RevisedEstimateUSD float64 `json:"revised_estimate_usd" desc:"Your own estimate in USD. Repeat the proposer's figure when the verdict is 'sound'. Ground this in the calibration data rather than in the proposer's reasoning, which you should treat as a claim to be checked, not evidence."`
	Confidence         float64 `json:"confidence" desc:"Confidence in your revised figure, 0.0 to 1.0. With fewer than three comparable completed hops, do not exceed 0.5."`
	Reasoning          string  `json:"reasoning" desc:"Why you reached this verdict, naming the specific completed hops or figures you compared against. One to three sentences. A human will read this to decide whether to trust the number, so state what would falsify it."`
}

// CostAuditResponse is the auditor's review of a roadmap's cost estimates.
type CostAuditResponse struct {
	Hops            []HopCostVerdict `json:"hops" desc:"One verdict per hop in the roadmap, in the same order. Every hop must appear."`
	TotalRevisedUSD float64          `json:"total_revised_usd" desc:"Sum of your revised per-hop estimates in USD"`
	BudgetVerdict   string           `json:"budget_verdict" desc:"One of: 'fits' if the revised total leaves comfortable headroom against the remaining budget; 'tight' if it consumes most of it; 'exceeds' if it goes over. Judge against budget_usd minus spent_usd, not the full budget."`
	Summary         string           `json:"summary" desc:"Plain assessment of whether this roadmap can be delivered within budget. Lead with the answer. If the estimates rest on no history, say so rather than implying more rigour than exists. 2-4 sentences."`
	Risks           []string         `json:"risks" desc:"Specific things that would push actual cost above the revised total, e.g. 'auth-refactor touches 40+ files and may need more than 3 variations'. Empty if none are material."`
}

// TotalUnderstatedUSD is how much the auditor added to the proposer's figures,
// which is the headline number when estimates come back low.
func (r *CostAuditResponse) TotalUnderstatedUSD(proposed float64) float64 {
	return r.TotalRevisedUSD - proposed
}

// FlaggedHops returns the hops whose estimates the auditor did not endorse.
func (r *CostAuditResponse) FlaggedHops() []HopCostVerdict {
	var out []HopCostVerdict
	for _, h := range r.Hops {
		if CostVerdict(h.Verdict) != CostVerdictSound {
			out = append(out, h)
		}
	}
	return out
}

// VerdictFor returns the auditor's judgement on a named hop, or nil if the
// auditor did not cover it.
func (r *CostAuditResponse) VerdictFor(hopName string) *HopCostVerdict {
	if r == nil {
		return nil
	}
	for i := range r.Hops {
		if r.Hops[i].HopName == hopName {
			return &r.Hops[i]
		}
	}
	return nil
}

// Disputed reports whether the auditor disagreed with the proposer's figure.
func (v *HopCostVerdict) Disputed() bool {
	return v != nil && CostVerdict(v.Verdict) != CostVerdictSound
}

// BudgetExceeded reports whether the audited total goes over what is left.
func (r *CostAuditResponse) BudgetExceeded() bool {
	return r != nil && r.BudgetVerdict == "exceeds"
}

//------------------------------------------------------------------------------
// Strategist: drafting OKRs from a plain-English brief
//------------------------------------------------------------------------------

// StrategyBrief is what a person types on the "new project" screen: what they
// want built, by when, and for how much. It is the entire input to the first
// draft, so the drafting agent has to be explicit about what it assumed.
type StrategyBrief struct {
	ProjectName string  `json:"project_name" desc:"The name the user gave this project."`
	Brief       string  `json:"brief" desc:"The user's own description of what they want built, verbatim. Do not assume it is complete or precise."`
	DeadlineISO string  `json:"deadline" desc:"The date the user wants this done by, YYYY-MM-DD. Empty if they did not give one."`
	BudgetUSD   float64 `json:"budget_usd" desc:"Total US dollars the user is willing to spend. Zero if they did not give a figure."`
	TodayISO    string  `json:"today" desc:"Today's date, YYYY-MM-DD. Every target date must fall between this and the deadline."`
}

// DraftedKeyResult is one measurable target in a drafted strategy.
type DraftedKeyResult struct {
	Description string `json:"description" desc:"What is being measured, in plain English. One sentence, no target figure in it. It must be something a person could put a number to every week: a key result that can only be measured at the end of the quarter cannot tell anyone whether the work is going well while there is still time to change it."`
	TargetComparator string  `json:"target_comparator" desc:"How a measurement is compared against the target. One of: >=, <=, >, <, =. Use >= for things that should grow and <= for things that should shrink."`
	TargetValue      float64 `json:"target_value" desc:"The number to compare against. Just the number: no unit, no symbol, no thousands separator."`
	TargetUnit       string  `json:"target_unit" desc:"What the number counts, for display: 'users', '%', 'ms p99', 'signups per week'. Carries any qualifier, since only the number is compared."`
	TargetDate  string `json:"target_date" desc:"When this should be hit, YYYY-MM-DD. Must fall on or before the deadline and on or after today. Empty only if the user gave no deadline."`
}

// DraftedObjective is one objective and the key results that measure it.
type DraftedObjective struct {
	Description string             `json:"description" desc:"The outcome, in plain English: who ends up better off and how. Not the mechanism -- an objective that lists actions ('the user can do X, then Y, then Z') is a feature list; say what those actions were for instead. It should still read true if the design changed. One or two sentences, no jargon."`
	KeyResults  []DraftedKeyResult `json:"key_results" desc:"2 to 3 key results that together tell you whether this objective was met."`
}

// DraftedStrategy is the drafting agent's first pass at a strategy: what the
// brief means in terms Mendel can plan against.
//
// Assumptions and OpenQuestions exist because a brief is almost never complete.
// The agent is asked to say what it filled in rather than to present invented
// specifics as though the user had supplied them -- the user is about to
// validate this, and can only correct what they can see.
type DraftedStrategy struct {
	StrategyName  string             `json:"strategy_name" desc:"A short name for this phase of work, e.g. 'MVP Launch' or 'Private Beta'. Two to four words."`
	Summary       string             `json:"summary" desc:"What you understood the user to be asking for, in two or three sentences of plain English. This is shown back to them as a check on your reading, so restate their intent rather than praising it."`
	Objectives    []DraftedObjective `json:"objectives" desc:"2 to 4 objectives covering the work. Fewer is better than padding: every objective here becomes work someone pays for."`
	BudgetName    string             `json:"budget_name" desc:"A short label for the budget covering this work, e.g. 'MVP build' or 'Q3 build'. Two or three words."`
	Assumptions   []string           `json:"assumptions" desc:"Specifics you filled in that the brief did not state -- platform, audience, scale, tech choices. One short sentence each. Empty array only if the brief genuinely left nothing open."`
	OpenQuestions []string           `json:"open_questions" desc:"Questions whose answers would change these objectives, phrased for the user to answer. One sentence each. Empty array if there are none worth asking."`
	BudgetNote    string             `json:"budget_note" desc:"Whether the stated budget and deadline look like enough for this scope, and what you would cut first if they are not. Say plainly when you cannot tell. Do not invent dollar figures for individual pieces of work -- you have no cost history to base them on. 1-3 sentences."`
}

// StrategistResponse is the drafting agent's structured output.
type StrategistResponse struct {
	Strategy DraftedStrategy `json:"strategy" desc:"The drafted strategy."`
}
