package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// buildGitHubBranchURL constructs a GitHub URL for viewing a branch or commit.
// Handles both HTTPS and SSH repo URLs.
func buildGitHubBranchURL(repoURL, branchName string, commitRef *string) string {
	// Convert repo URL to GitHub base URL
	// https://github.com/user/repo.git -> https://github.com/user/repo
	// git@github.com:user/repo.git -> https://github.com/user/repo
	base := repoURL
	base = strings.TrimSuffix(base, ".git")

	if strings.HasPrefix(base, "git@github.com:") {
		base = strings.Replace(base, "git@github.com:", "https://github.com/", 1)
	}

	// Only works for GitHub URLs
	if !strings.Contains(base, "github.com") {
		return ""
	}

	// If we have a commit ref, link to that specific commit
	if commitRef != nil && *commitRef != "" {
		return fmt.Sprintf("%s/tree/%s", base, *commitRef)
	}

	// Otherwise link to the branch
	return fmt.Sprintf("%s/tree/%s", base, branchName)
}

// buildGitHubDiffURL constructs a GitHub compare URL (main...branch).
func buildGitHubDiffURL(repoURL, mainBranch, branchName string) string {
	base := repoURL
	base = strings.TrimSuffix(base, ".git")

	if strings.HasPrefix(base, "git@github.com:") {
		base = strings.Replace(base, "git@github.com:", "https://github.com/", 1)
	}

	if !strings.Contains(base, "github.com") {
		return ""
	}

	return fmt.Sprintf("%s/compare/%s...%s", base, mainBranch, branchName)
}

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
	PendingSelection      *domain.Decision
	HasCreatingVariations bool
	HasPendingVariations  bool
	IsStuck               bool // No pending variations and no unresolved decisions
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

	// Check for pending decisions
	decisions, _ := s.db.GetDecisionsBySubject(ctx, "hop", hopID)
	var pendingReview *domain.Decision
	var pendingSelection *domain.Decision
	for i := range decisions {
		d := &decisions[i]
		if d.Status != domain.DecisionStatusResolved {
			if d.Kind == domain.DecisionKindVariationReview {
				pendingReview = d
			} else if d.Kind == domain.DecisionKindVariationSelection {
				pendingSelection = d
			}
		}
	}

	// Check variation statuses
	hasCreatingVariations := false
	hasPendingVariations := false
	for _, v := range variations {
		if v.Variation.Status == domain.VariationStatusCreating {
			hasCreatingVariations = true
		}
		if v.Variation.Status == domain.VariationStatusPending {
			hasPendingVariations = true
		}
	}

	// Detect stuck state: has variations but none pending/creating, no unresolved decisions
	isStuck := len(variations) > 0 && !hasCreatingVariations && !hasPendingVariations && pendingReview == nil && pendingSelection == nil

	view := &HopDetailView{
		Hop:                   hop,
		Strategy:              strategy,
		Project:               project,
		Variations:            variations,
		Objectives:            objectives,
		Allocations:           allocations,
		PendingReview:         pendingReview,
		PendingSelection:      pendingSelection,
		HasCreatingVariations: hasCreatingVariations,
		HasPendingVariations:  hasPendingVariations,
		IsStuck:               isStuck,
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
	Variation    *domain.Variation
	Hop          *domain.Hop
	Logs         []domain.VariationLog
	DemoInstance *domain.DemoInstance  // Current or recent demo instance
	DemoLogs     []domain.VariationLog // Logs specific to the current demo
	GitHubURL    string                // Link to branch on GitHub (if applicable)
	DiffURL      string                // Link to GitHub compare (main...branch)
}

func (s *Server) handleVariationDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

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

	// Get codegen logs only (not demo logs)
	logs, _ := s.db.GetVariationLogsByType(ctx, variationID, domain.SourceTypeCodegen, 100)

	// Get the most recent demo instance (any status) for display
	demoInstance, _ := s.db.GetLatestDemoByVariation(ctx, variationID)

	// Get demo-specific logs if there's a demo instance
	var demoLogs []domain.VariationLog
	if demoInstance != nil {
		demoLogs, _ = s.db.GetVariationLogsBySource(ctx, domain.SourceTypeDemo, demoInstance.ID, 200)
	}

	// Build GitHub URL for the branch
	branchName := fmt.Sprintf("mendel/%s/%s", hop.Name, variation.Name)
	var githubURL, diffURL string
	if repo, err := s.db.GetRepositoryByProject(ctx, projectID); err == nil && repo != nil && repo.URL != nil {
		githubURL = buildGitHubBranchURL(*repo.URL, branchName, variation.CommitRef)
		// Get main branch for diff URL
		mainBranch := "main"
		if repo.Config != nil {
			var repoConfig struct {
				MainBranch string `json:"main_branch"`
			}
			if err := json.Unmarshal(repo.Config, &repoConfig); err == nil && repoConfig.MainBranch != "" {
				mainBranch = repoConfig.MainBranch
			}
		}
		diffURL = buildGitHubDiffURL(*repo.URL, mainBranch, branchName)
	}

	view := &VariationDetailView{
		Variation:    variation,
		Hop:          hop,
		Logs:         logs,
		DemoInstance: demoInstance,
		DemoLogs:     demoLogs,
		GitHubURL:    githubURL,
		DiffURL:      diffURL,
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

	// Only allow retry for error or terminated status
	if variation.Status != domain.VariationStatusError && variation.Status != domain.VariationStatusTerminated {
		http.Error(w, "can only retry variations in error or terminated status", http.StatusBadRequest)
		return
	}

	oldStatus := variation.Status

	// Reset status to creating - background worker will pick it up
	variation.Status = domain.VariationStatusCreating
	variation.UpdatedAt = time.Now()
	if err := s.db.UpdateVariation(ctx, variation); err != nil {
		http.Error(w, "error updating variation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Record state transition
	s.db.CreateVariationStateTransition(ctx, variationID, string(oldStatus), string(domain.VariationStatusCreating), "manual retry")

	// Redirect back to variation detail to watch progress
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

func (s *Server) handleTerminateVariation(w http.ResponseWriter, r *http.Request) {
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

	// Only allow terminate for creating status (stuck generations)
	if variation.Status != domain.VariationStatusCreating {
		http.Error(w, "can only terminate variations in creating status", http.StatusBadRequest)
		return
	}

	// Set status to terminated
	variation.Status = domain.VariationStatusTerminated
	variation.UpdatedAt = time.Now()
	if err := s.db.UpdateVariation(ctx, variation); err != nil {
		http.Error(w, "error updating variation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Record state transition
	s.db.CreateVariationStateTransition(ctx, variationID, string(domain.VariationStatusCreating), string(domain.VariationStatusTerminated), "manual termination")

	// Redirect back to variation detail
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}
