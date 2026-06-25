package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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
	Hop                   *domain.Hop
	Strategy              *domain.Strategy
	Project               *domain.Project
	Variations            []VariationWithLogs
	Objectives            []domain.Objective
	Allocations           []domain.BudgetAllocation
	PendingReview         *domain.Decision
	HasCreatingVariations bool
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

	// Check if any variations are in creating status
	hasCreatingVariations := false
	for _, v := range variations {
		if v.Variation.Status == domain.VariationStatusCreating {
			hasCreatingVariations = true
			break
		}
	}

	view := &HopDetailView{
		Hop:                   hop,
		Strategy:              strategy,
		Project:               project,
		Variations:            variations,
		Objectives:            objectives,
		Allocations:           allocations,
		PendingReview:         pendingReview,
		HasCreatingVariations: hasCreatingVariations,
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

	// Use the shared function to propose variations
	if err := s.proposeVariationsForHop(ctx, hop); err != nil {
		http.Error(w, "error proposing variations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get the created decision to redirect to it
	decision, err := s.db.GetDecisionBySubjectAndKind(ctx, "hop", hopID, domain.DecisionKindVariationReview)
	if err != nil {
		// Decision was created but we can't find it - redirect to hop page
		http.Redirect(w, r, fmt.Sprintf("/p/%s/hops/%s", projectID, hopID), http.StatusSeeOther)
		return
	}

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
