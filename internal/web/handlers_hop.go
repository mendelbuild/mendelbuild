package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/codegen"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// VariationWithLogs holds a variation and its recent logs.
type VariationWithLogs struct {
	Variation  domain.Variation
	RecentLogs []domain.VariationLog
}

// HopDetailView holds data for rendering the hop detail page.
type HopDetailView struct {
	Hop           *domain.Hop
	Strategy      *domain.Strategy
	Project       *domain.Project
	Variations    []VariationWithLogs
	Objectives    []domain.Objective
	Allocations   []domain.BudgetAllocation
	PendingReview *domain.Decision
}

func (s *Server) handleHopDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	hopID, err := uuid.Parse(chi.URLParam(r, "hopID"))
	if err != nil {
		http.Error(w, "invalid hop ID", http.StatusBadRequest)
		return
	}

	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	hop, err := s.db.GetHop(ctx, hopID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	strategy, err := s.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		http.Error(w, "strategy not found", http.StatusNotFound)
		return
	}

	rawVariations, _ := s.db.GetVariationsByHop(ctx, hopID)
	allocations, _ := s.db.GetBudgetAllocationsByHop(ctx, hopID)

	// Fetch recent logs for each variation
	var variations []VariationWithLogs
	for _, v := range rawVariations {
		logs, _ := s.db.GetRecentVariationLogs(ctx, v.ID, 5)
		// Reverse logs so oldest is first (they come back newest first)
		for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
			logs[i], logs[j] = logs[j], logs[i]
		}
		variations = append(variations, VariationWithLogs{
			Variation:  v,
			RecentLogs: logs,
		})
	}

	// Parse objective IDs from hop params
	var objectives []domain.Objective
	if hop.Params != nil {
		var params struct {
			ObjectiveIDs []string `json:"objective_ids"`
		}
		if err := json.Unmarshal(hop.Params, &params); err == nil {
			for _, objIDStr := range params.ObjectiveIDs {
				if objID, err := uuid.Parse(objIDStr); err == nil {
					allObjs, _ := s.db.GetObjectivesByStrategy(ctx, strategy.ID)
					for _, obj := range allObjs {
						if obj.ID == objID {
							objectives = append(objectives, obj)
							break
						}
					}
				}
			}
		}
	}

	// Check for pending variation review decision
	decisions, _ := s.db.GetDecisionsBySubject(ctx, "hop", hopID)
	var pendingReview *domain.Decision
	for i := range decisions {
		d := &decisions[i]
		if d.Kind == domain.DecisionKindVariationReview && d.Status != domain.DecisionStatusResolved {
			pendingReview = d
			break
		}
	}

	view := &HopDetailView{
		Hop:           hop,
		Strategy:      strategy,
		Project:       project,
		Variations:    variations,
		Objectives:    objectives,
		Allocations:   allocations,
		PendingReview: pendingReview,
	}

	data := map[string]interface{}{
		"Title":     "Hop: " + hop.Name,
		"ProjectID": projectID,
		"View":      view,
	}

	if err := renderPage(w, "hop_detail.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleProposeVariations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	hopID, err := uuid.Parse(chi.URLParam(r, "hopID"))
	if err != nil {
		http.Error(w, "invalid hop ID", http.StatusBadRequest)
		return
	}

	hop, err := s.db.GetHop(ctx, hopID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	strategy, err := s.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		http.Error(w, "strategy not found", http.StatusNotFound)
		return
	}

	// Get objectives from hop params
	var objectiveDescs []string
	if hop.Params != nil {
		var params struct {
			ObjectiveIDs []string `json:"objective_ids"`
		}
		if err := json.Unmarshal(hop.Params, &params); err == nil {
			allObjs, _ := s.db.GetObjectivesByStrategy(ctx, strategy.ID)
			for _, objIDStr := range params.ObjectiveIDs {
				if objID, err := uuid.Parse(objIDStr); err == nil {
					for _, obj := range allObjs {
						if obj.ID == objID {
							objectiveDescs = append(objectiveDescs, obj.Description)
							break
						}
					}
				}
			}
		}
	}

	// Get repository info
	repo, err := s.db.GetRepositoryByProject(ctx, strategy.ProjectID)
	if err != nil {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}

	// Get budget allocation for tokens
	allocations, _ := s.db.GetBudgetAllocationsByHop(ctx, hopID)
	availableBudget := 100000 // Default
	for _, alloc := range allocations {
		// Get funding source to check if it's claude_tokens
		sources, _ := s.db.GetFundingSourcesByStrategy(ctx, strategy.ID)
		for _, src := range sources {
			if src.ID == alloc.FundingSourceID && src.ResourceType == domain.ResourceTypeClaudeTokens {
				availableBudget = int(alloc.LimitAmount)
				break
			}
		}
	}

	// Build hop context
	hopContext := agent.HopContext{
		ID:         hop.ID.String(),
		Name:       hop.Name,
		Commentary: hop.Commentary,
		Objectives: objectiveDescs,
	}

	repoURL := ""
	if repo.URL != nil {
		repoURL = *repo.URL
	}

	input := agent.VariationProposerInput{
		Hop:             hopContext,
		RepositoryURL:   repoURL,
		AvailableBudget: availableBudget,
		NumVariations:   2, // Start with 2 variations
	}

	// Call variation proposer
	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "error creating agent client: "+err.Error(), http.StatusInternalServerError)
		return
	}

	proposer := agent.NewVariationProposer(client)
	proposal, tokens, err := proposer.ProposeVariations(ctx, input)
	if err != nil {
		http.Error(w, "error generating variations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Generate evaluation criteria if the hop doesn't have them yet
	if len(hop.EvaluationCriteria) == 0 {
		criteriaInput := agent.EvaluationCriteriaInput{
			HopName:       hop.Name,
			HopCommentary: hop.Commentary,
			Objectives:    objectiveDescs,
		}

		criteriaGenerator := agent.NewEvaluationCriteriaGenerator(client)
		criteria, _, err := criteriaGenerator.GenerateCriteria(ctx, criteriaInput)
		if err == nil && criteria != nil {
			criteriaJSON, err := json.Marshal(criteria)
			if err == nil {
				if err := s.db.UpdateHopEvaluationCriteria(ctx, hopID, criteriaJSON); err != nil {
					fmt.Printf("Warning: failed to save evaluation criteria: %v\n", err)
				}
			}
		}
	}

	// Convert to VariationProposalData for storage
	proposalData := codegen.VariationProposalData{
		HopID: hopID,
	}
	for _, v := range proposal.Variations {
		proposalData.Variations = append(proposalData.Variations, codegen.ProposedVariationData{
			Name:            v.Name,
			Approach:        v.Approach,
			Differentiation: v.Differentiation,
			EstimatedTokens: v.EstimatedTokens,
		})
	}

	// Create decision
	now := time.Now()
	proposalJSON, _ := json.MarshalIndent(proposalData, "", "  ")
	proposalStr := string(proposalJSON)

	decision := &domain.Decision{
		ID:               uuid.New(),
		Kind:             domain.DecisionKindVariationReview,
		Title:            fmt.Sprintf("Variation Review: %s", hop.Name),
		Details:          &proposalStr,
		ObjectivityScore: 0.4,
		ImportanceScore:  0.7,
		Status:           domain.DecisionStatusNeedsAssignment,
		SubjectType:      strPtr("hop"),
		SubjectID:        &hopID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := s.db.CreateDecision(ctx, decision); err != nil {
		http.Error(w, "error creating decision: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create agent message
	tokensUsed := tokens
	agentMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decision.ID,
		Role:       "agent",
		Content:    fmt.Sprintf("Generated %d variation proposals.\n\nRationale: %s", len(proposal.Variations), proposal.Rationale),
		TokensUsed: &tokensUsed,
		CreatedAt:  now,
	}
	s.db.CreateDecisionMessage(ctx, agentMsg)

	// Redirect to decision page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/decisions/%s", projectID, decision.ID), http.StatusSeeOther)
}

// VariationDetailView holds data for rendering the variation detail page.
type VariationDetailView struct {
	Variation *domain.Variation
	Hop       *domain.Hop
	Logs      []domain.VariationLog
}

func (s *Server) handleVariationDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	hop, err := s.db.GetHop(ctx, variation.HopID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	logs, _ := s.db.GetVariationLogs(ctx, variationID, 100)

	view := &VariationDetailView{
		Variation: variation,
		Hop:       hop,
		Logs:      logs,
	}

	data := map[string]interface{}{
		"Title":     "Variation: " + variation.Name,
		"ProjectID": projectID,
		"View":      view,
	}

	if err := renderPage(w, "variation_detail.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleRetryVariation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	// Only allow retry for error status
	if variation.Status != domain.VariationStatusError {
		http.Error(w, "can only retry variations in error status", http.StatusBadRequest)
		return
	}

	// Reset status to creating - background worker will pick it up
	variation.Status = domain.VariationStatusCreating
	variation.UpdatedAt = time.Now()
	if err := s.db.UpdateVariation(ctx, variation); err != nil {
		http.Error(w, "error updating variation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Record state transition
	s.db.CreateVariationStateTransition(ctx, variationID, string(domain.VariationStatusError), string(domain.VariationStatusCreating), "manual retry")

	// Redirect back to hop detail
	http.Redirect(w, r, fmt.Sprintf("/p/%s/hops/%s", projectID, variation.HopID), http.StatusSeeOther)
}
