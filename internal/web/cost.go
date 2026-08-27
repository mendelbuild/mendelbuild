package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/codegen/executor"
	"github.com/bhs/mendelbuild/internal/cost"
	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
)

// recorder returns a cost recorder over this server's store.
func (s *Server) recorder() *cost.Recorder { return cost.NewRecorder(s.db) }

// strategyAttribution files a charge against a strategy and its project.
func (s *Server) strategyAttribution(ctx context.Context, strategyID uuid.UUID) (cost.Attribution, error) {
	strategy, err := s.db.GetStrategy(ctx, strategyID)
	if err != nil {
		return cost.Attribution{}, err
	}
	id := strategyID
	return cost.Attribution{ProjectID: strategy.ProjectID, StrategyID: &id}, nil
}

// recordAgentSpend prices an agent call and appends it to the ledger.
//
// Every agent call goes through here. Before this existed, only code generation
// was counted, so roadmap proposals, evaluations, OKR tuning, and audits were
// all bought and never billed to anything -- a project's true cost was
// systematically understated by whatever the planning work happened to cost.
func (s *Server) recordAgentSpend(ctx context.Context, attr cost.Attribution, component string, sp agent.Spend) {
	if sp.Tokens.IsZero() || sp.Model == "" {
		return
	}
	if _, err := s.recorder().RecordModelUsage(ctx, attr, component, sp.Model, sp.Tokens); err != nil {
		// A missing ledger row must not fail the user's request; it is a
		// reporting loss, not a correctness one.
		log.Printf("cost: could not record %s spend: %v", component, err)
	}
}

// recordStrategySpend is the common case: an agent call made while working on a
// strategy, with no single Hop to attribute it to.
func (s *Server) recordStrategySpend(ctx context.Context, strategyID uuid.UUID, component string, sp agent.Spend) {
	attr, err := s.strategyAttribution(ctx, strategyID)
	if err != nil {
		log.Printf("cost: could not attribute %s spend: %v", component, err)
		return
	}
	s.recordAgentSpend(ctx, attr, component, sp)
}

// recordHopSpend attributes an agent call to a specific Hop.
func (s *Server) recordHopSpend(ctx context.Context, hopID uuid.UUID, component string, sp agent.Spend) {
	projectID, strategyID, err := s.db.ResolveHopAttribution(ctx, hopID)
	if err != nil {
		log.Printf("cost: could not attribute %s spend: %v", component, err)
		return
	}
	id := hopID
	s.recordAgentSpend(ctx, cost.Attribution{
		ProjectID:  projectID,
		StrategyID: &strategyID,
		HopID:      &id,
	}, component, sp)
}

// recordHopEstimate stores a proposed Hop's cost estimate and grants it a
// matching spend ceiling.
//
// The estimate and the ceiling are recorded separately on purpose: the estimate
// is a prediction that will later be scored against actuals, while the ceiling
// is a governance decision a human can change without rewriting history.
func (s *Server) recordHopEstimate(
	ctx context.Context,
	hopID, strategyID uuid.UUID,
	ph agent.ProposedHop,
	audit *agent.CostAuditResponse,
) {
	if ph.EstimatedCostUSD <= 0 {
		return
	}

	est := &domain.HopCostEstimate{
		HopID:     hopID,
		AmountUSD: ph.EstimatedCostUSD,
		Estimator: domain.EstimatorProposer,
	}
	if ph.CostConfidence > 0 {
		c := ph.CostConfidence
		est.Confidence = &c
	}
	if ph.CostBasis != "" {
		b := ph.CostBasis
		est.Basis = &b
	}
	if err := s.db.CreateHopCostEstimate(ctx, est); err != nil {
		log.Printf("cost: could not record estimate for hop %s: %v", hopID, err)
	}

	// Both estimates are kept. Scoring the proposer and the auditor separately
	// against actuals is the only way to learn which one to believe, and the
	// history would be lost if the auditor simply overwrote the proposal.
	ceiling := ph.EstimatedCostUSD
	if v := audit.VerdictFor(ph.Name); v != nil && v.RevisedEstimateUSD > 0 {
		confidence := v.Confidence
		reasoning := v.Reasoning
		auditEst := &domain.HopCostEstimate{
			HopID:      hopID,
			AmountUSD:  v.RevisedEstimateUSD,
			Estimator:  domain.EstimatorAuditor,
			Confidence: &confidence,
			Basis:      &reasoning,
		}
		if err := s.db.CreateHopCostEstimate(ctx, auditEst); err != nil {
			log.Printf("cost: could not record audit estimate for hop %s: %v", hopID, err)
		}

		// The ceiling follows the higher of the two. Budgeting to the lower
		// figure would guarantee the Hop trips its limit whenever the auditor
		// was right, which is the case the ceiling exists to catch.
		if v.RevisedEstimateUSD > ceiling {
			ceiling = v.RevisedEstimateUSD
		}
	}

	source, err := s.db.GetPrimaryFundingSource(ctx, strategyID)
	if err != nil || source == nil {
		return // No budget configured; the estimate still stands on its own.
	}
	if err := s.db.CreateBudgetAllocation(ctx, hopID, source.ID, ceiling); err != nil {
		log.Printf("cost: could not allocate budget for hop %s: %v", hopID, err)
	}
}

// HopCostView is everything the UI shows about one Hop's money.
type HopCostView struct {
	EstimateUSD *float64
	LimitUSD    *float64
	ActualUSD   float64
	Tokens      domain.TokenCounts

	// Confidence and Basis come from the estimate, so a reader can see how much
	// the figure was worth trusting rather than just the figure.
	Confidence *float64
	Basis      *string
	Estimator  string
}

// HasEstimate reports whether this Hop was ever estimated.
func (v *HopCostView) HasEstimate() bool { return v != nil && v.EstimateUSD != nil }

// OverrunRatio is actual over estimate. The bool is false when there is no
// estimate to compare against.
func (v *HopCostView) OverrunRatio() (float64, bool) {
	if v == nil || v.EstimateUSD == nil || *v.EstimateUSD <= 0 {
		return 0, false
	}
	return v.ActualUSD / *v.EstimateUSD, true
}

// OverBudget reports whether actual spend has passed the Hop's ceiling.
func (v *HopCostView) OverBudget() bool {
	return v != nil && v.LimitUSD != nil && v.ActualUSD > *v.LimitUSD
}

// Variance renders the gap between estimate and actual in words, which is what
// a reader actually wants to know from an estimate after the fact.
func (v *HopCostView) Variance() string {
	ratio, ok := v.OverrunRatio()
	if !ok {
		return ""
	}
	switch {
	case ratio > 1.1:
		return fmt.Sprintf("%.1fx over estimate", ratio)
	case ratio < 0.9:
		return fmt.Sprintf("%.0f%% of estimate", ratio*100)
	default:
		return "on estimate"
	}
}

// hopCostView assembles the cost picture for one Hop.
func (s *Server) hopCostView(ctx context.Context, hopID uuid.UUID) *HopCostView {
	summary, err := s.db.GetHopCostSummary(ctx, hopID)
	if err != nil {
		return nil
	}

	view := &HopCostView{
		ActualUSD: summary.AmountUSD,
		Tokens:    summary.TokenCounts,
	}

	if est, _ := s.db.GetLatestHopCostEstimate(ctx, hopID); est != nil {
		amount := est.AmountUSD
		view.EstimateUSD = &amount
		view.Confidence = est.Confidence
		view.Basis = est.Basis
		view.Estimator = string(est.Estimator)
	}

	if allocs, _ := s.db.GetBudgetAllocationsByHop(ctx, hopID); len(allocs) > 0 {
		var limit float64
		for _, a := range allocs {
			limit += a.LimitUSD
		}
		view.LimitUSD = &limit
	}

	return view
}

// tokensUsedPtr renders a Spend as the total token count stored alongside a
// conversation message. The ledger keeps the full per-model breakdown; this is
// just the at-a-glance figure shown next to the message.
func tokensUsedPtr(sp agent.Spend) *int {
	if sp.Tokens.IsZero() {
		return nil
	}
	total := sp.Tokens.Total()
	return &total
}

// RemainingUSD is what a Hop has left against its ceiling. With no ceiling set,
// it reports zero, which the caller should read as "no budget configured"
// rather than "no money left".
func (v *HopCostView) RemainingUSD() float64 {
	if v == nil || v.LimitUSD == nil {
		return 0
	}
	if r := *v.LimitUSD - v.ActualUSD; r > 0 {
		return r
	}
	return 0
}

// FundingSourceView is one budget with the OKR milestones it is meant to buy.
type FundingSourceView struct {
	Source     domain.FundingSource
	KeyResults []db.FundedKeyResult
}

// StrategyCostView is the whole money picture for a Strategy.
type StrategyCostView struct {
	BudgetUSD float64
	SpentUSD  float64
	Tokens    domain.TokenCounts

	Sources    []FundingSourceView
	Components []db.ComponentCost

	// ElapsedFraction is how far through the budget period we are, when any
	// funding source declares one. Comparing it against the burned fraction is
	// what turns a spend figure into a judgement about pace.
	ElapsedFraction float64
	HasPeriod       bool
}

// HasBudget reports whether any budget has been configured.
func (v *StrategyCostView) HasBudget() bool { return v != nil && v.BudgetUSD > 0 }

// RemainingUSD is budget minus spend, floored at zero.
func (v *StrategyCostView) RemainingUSD() float64 {
	if r := v.BudgetUSD - v.SpentUSD; r > 0 {
		return r
	}
	return 0
}

// BurnedFraction is the share of the budget already spent.
func (v *StrategyCostView) BurnedFraction() float64 {
	if !v.HasBudget() {
		return 0
	}
	return v.SpentUSD / v.BudgetUSD
}

// OverBudget reports whether spend has passed the budget.
func (v *StrategyCostView) OverBudget() bool {
	return v.HasBudget() && v.SpentUSD > v.BudgetUSD
}

// Pace compares money burned against time elapsed. This is the answer to "is
// this project on track", which neither figure gives on its own: spending 60%
// of a budget is fine at two-thirds through a quarter and alarming at a tenth.
func (v *StrategyCostView) Pace() string {
	if !v.HasPeriod || !v.HasBudget() {
		return ""
	}
	burned, elapsed := v.BurnedFraction(), v.ElapsedFraction

	// Before any meaningful time has passed, any spend looks like a runaway.
	if elapsed < 0.05 {
		return ""
	}
	switch ratio := burned / elapsed; {
	case ratio > 1.25:
		return fmt.Sprintf("spending %.0f%% faster than the schedule", (ratio-1)*100)
	case ratio < 0.75:
		return fmt.Sprintf("spending %.0f%% slower than the schedule", (1-ratio)*100)
	default:
		return "on pace"
	}
}

// strategyCostView assembles the money picture for a Strategy, including the
// Key Results each budget is meant to move.
func (s *Server) strategyCostView(ctx context.Context, strategyID uuid.UUID) *StrategyCostView {
	summary, err := s.db.GetStrategyCostSummary(ctx, strategyID)
	if err != nil {
		return nil
	}

	view := &StrategyCostView{
		SpentUSD: summary.AmountUSD,
		Tokens:   summary.TokenCounts,
	}
	view.Components, _ = s.db.GetCostByComponent(ctx, strategyID)

	sources, _ := s.db.GetFundingSourcesByStrategy(ctx, strategyID)
	now := time.Now()
	for _, src := range sources {
		view.BudgetUSD += src.AmountUSD

		krs, _ := s.db.GetFundedKeyResults(ctx, src.ID)
		view.Sources = append(view.Sources, FundingSourceView{Source: src, KeyResults: krs})

		// Where budgets declare different periods, the one furthest along
		// governs: it is the first deadline the project actually faces.
		if elapsed, ok := src.Elapsed(now); ok {
			view.HasPeriod = true
			if elapsed > view.ElapsedFraction {
				view.ElapsedFraction = elapsed
			}
		}
	}

	return view
}

// VariationCostView is what generating one Variation cost.
type VariationCostView struct {
	AmountUSD float64
	Tokens    domain.TokenCounts
}

// HasSpend reports whether anything was recorded against this Variation.
func (v *VariationCostView) HasSpend() bool { return v != nil && !v.Tokens.IsZero() }

// CachedShare is the fraction of the prompt that came from cache. Worth showing
// because it is usually most of it, and because it is the difference between
// the token count a reader expects and the one they see.
func (v *VariationCostView) CachedShare() float64 {
	if v == nil || v.Tokens.PromptTokens() == 0 {
		return 0
	}
	return float64(v.Tokens.CacheReadTokens) / float64(v.Tokens.PromptTokens())
}

// variationCostView assembles the cost of one Variation.
func (s *Server) variationCostView(ctx context.Context, variationID uuid.UUID) *VariationCostView {
	summary, err := s.db.GetVariationCostSummary(ctx, variationID)
	if err != nil || summary.Entries == 0 {
		return nil
	}
	return &VariationCostView{AmountUSD: summary.AmountUSD, Tokens: summary.TokenCounts}
}

// recordVariationRun files an executor run against its Variation and Hop.
//
// Fix and demo-repair runs are full agentic loops that cost as much as an
// ordinary generation. They were previously unbilled entirely, which meant a
// Variation that needed three repair attempts reported the cost of one.
func (s *Server) recordVariationRun(ctx context.Context, variation *domain.Variation, stats executor.Stats, component string) {
	tokens := stats.Tokens()
	if tokens.IsZero() {
		return
	}

	projectID, strategyID, err := s.db.ResolveHopAttribution(ctx, variation.HopID)
	if err != nil {
		log.Printf("cost: could not attribute %s spend: %v", component, err)
		return
	}

	hopID, variationID := variation.HopID, variation.ID
	if _, err := s.recorder().RecordModelUsage(ctx, cost.Attribution{
		ProjectID:   projectID,
		StrategyID:  &strategyID,
		HopID:       &hopID,
		VariationID: &variationID,
	}, component, stats.Model, tokens); err != nil {
		log.Printf("cost: could not record %s spend: %v", component, err)
	}
}

// recordVariationRunByID is recordVariationRun for callers that only carry the
// Variation's id, such as the background demo-fix worker.
func (s *Server) recordVariationRunByID(ctx context.Context, variationID uuid.UUID, stats executor.Stats, component string) {
	if stats.Tokens().IsZero() {
		return
	}
	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		log.Printf("cost: could not load variation %s to record %s spend: %v", variationID, component, err)
		return
	}
	s.recordVariationRun(ctx, variation, stats, component)
}

// auditRoadmapCosts fact-checks a proposed roadmap's estimates and stores the
// verdict on the input request.
//
// This runs as a separate call with its own prompt rather than asking the
// proposer to grade its own arithmetic, which reliably produces agreement
// instead of scrutiny. A failed audit is not fatal: the roadmap is still
// reviewable, just without a second opinion on its numbers.
func (s *Server) auditRoadmapCosts(
	ctx context.Context,
	client *agent.Client,
	inputRequestID uuid.UUID,
	strategyID uuid.UUID,
	strategyContext agent.StrategyContext,
	roadmap *agent.ProposedRoadmap,
) {
	if roadmap == nil || len(roadmap.Hops) == 0 {
		return
	}

	audit, spend, err := agent.NewCostAuditor(client).Audit(ctx, agent.CostAuditInput{
		Strategy: strategyContext,
		Roadmap:  *roadmap,
	})
	if err != nil {
		log.Printf("cost: roadmap audit failed: %v", err)
		return
	}
	s.recordStrategySpend(ctx, strategyID, "cost_auditor", spend)

	raw, err := json.Marshal(audit)
	if err != nil {
		log.Printf("cost: could not serialise audit: %v", err)
		return
	}
	if err := s.db.SetInputRequestCostAudit(ctx, inputRequestID, raw); err != nil {
		log.Printf("cost: could not store audit: %v", err)
	}
}

// loadCostAudit reads a stored audit back for display.
func (s *Server) loadCostAudit(ctx context.Context, inputRequestID uuid.UUID) *agent.CostAuditResponse {
	raw, err := s.db.GetInputRequestCostAudit(ctx, inputRequestID)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var audit agent.CostAuditResponse
	if err := json.Unmarshal(raw, &audit); err != nil {
		return nil
	}
	return &audit
}
