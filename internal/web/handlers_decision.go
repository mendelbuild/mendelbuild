package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// DecisionDetailView holds data for rendering a decision detail page.
type DecisionDetailView struct {
	Decision *domain.Decision
	Messages []domain.DecisionMessage
	Roadmap  *agent.ProposedRoadmap
	Strategy *domain.Strategy
}

func (s *Server) handleDecisionDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	decisionID, err := uuid.Parse(chi.URLParam(r, "decisionID"))
	if err != nil {
		http.Error(w, "invalid decision ID", http.StatusBadRequest)
		return
	}

	decision, err := s.db.GetDecision(ctx, decisionID)
	if err != nil {
		http.Error(w, "decision not found", http.StatusNotFound)
		return
	}

	messages, err := s.db.GetDecisionMessages(ctx, decisionID)
	if err != nil {
		http.Error(w, "error loading messages", http.StatusInternalServerError)
		return
	}

	var roadmap *agent.ProposedRoadmap
	if decision.Details != nil && *decision.Details != "" {
		var rm agent.ProposedRoadmap
		if err := json.Unmarshal([]byte(*decision.Details), &rm); err == nil {
			roadmap = &rm
		}
	}

	var strategy *domain.Strategy
	if decision.SubjectType != nil && *decision.SubjectType == "strategy" && decision.SubjectID != nil {
		strategy, _ = s.db.GetStrategy(ctx, *decision.SubjectID)
	}

	view := &DecisionDetailView{
		Decision: decision,
		Messages: messages,
		Roadmap:  roadmap,
		Strategy: strategy,
	}

	data := map[string]interface{}{
		"Title":     "Decision: " + decision.Title,
		"ProjectID": projectID,
		"View":      view,
	}

	if err := renderPage(w, "decision_roadmap.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	decisionID, err := uuid.Parse(chi.URLParam(r, "decisionID"))
	if err != nil {
		http.Error(w, "invalid decision ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	feedback := r.FormValue("feedback")
	if feedback == "" {
		http.Error(w, "feedback is required", http.StatusBadRequest)
		return
	}

	decision, err := s.db.GetDecision(ctx, decisionID)
	if err != nil {
		http.Error(w, "decision not found", http.StatusNotFound)
		return
	}

	// Save user message
	now := time.Now()
	userMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decisionID,
		Role:       "user",
		Content:    feedback,
		CreatedAt:  now,
	}
	if err := s.db.CreateDecisionMessage(ctx, userMsg); err != nil {
		http.Error(w, "error saving message", http.StatusInternalServerError)
		return
	}

	// Parse current roadmap
	var currentRoadmap agent.ProposedRoadmap
	if decision.Details != nil {
		if err := json.Unmarshal([]byte(*decision.Details), &currentRoadmap); err != nil {
			http.Error(w, "error parsing roadmap", http.StatusInternalServerError)
			return
		}
	}

	// Load strategy context
	if decision.SubjectID == nil {
		http.Error(w, "no strategy associated", http.StatusBadRequest)
		return
	}

	strategy, err := s.db.GetStrategy(ctx, *decision.SubjectID)
	if err != nil {
		http.Error(w, "strategy not found", http.StatusNotFound)
		return
	}

	objectives, err := s.db.GetObjectivesByStrategy(ctx, strategy.ID)
	if err != nil {
		http.Error(w, "error loading objectives", http.StatusInternalServerError)
		return
	}

	var objInfos []agent.ObjectiveInfo
	for _, obj := range objectives {
		krs, _ := s.db.GetKeyResultsByObjective(ctx, obj.ID)
		var krInfos []agent.KeyResultInfo
		for _, kr := range krs {
			krInfo := agent.KeyResultInfo{
				ID:          kr.ID.String(),
				Description: kr.Description,
				TargetUnits: kr.TargetUnits,
			}
			if kr.TargetDate != nil {
				date := kr.TargetDate.Format("2006-01-02")
				krInfo.TargetDate = &date
			}
			krInfos = append(krInfos, krInfo)
		}
		objInfos = append(objInfos, agent.ObjectiveInfo{
			ID:          obj.ID.String(),
			Description: obj.Description,
			KeyResults:  krInfos,
		})
	}

	funding, _ := s.db.GetFundingSourcesByStrategy(ctx, strategy.ID)
	var fundingEstimates []agent.ResourceEstimate
	for _, f := range funding {
		fundingEstimates = append(fundingEstimates, agent.ResourceEstimate{
			ResourceType: string(f.ResourceType),
			Amount:       f.Amount,
		})
	}

	strategyContext := agent.StrategyContext{
		ID:         strategy.ID.String(),
		Name:       strategy.Name,
		Objectives: objInfos,
		Funding:    fundingEstimates,
	}

	// Call proposer for revision
	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "error creating agent client", http.StatusInternalServerError)
		return
	}

	proposer := agent.NewProposer(client)
	revReq := agent.RevisionRequest{
		CurrentRoadmap: currentRoadmap,
		Feedback:       feedback,
		Strategy:       strategyContext,
	}

	revisedRoadmap, tokens, err := proposer.ReviseRoadmap(ctx, revReq)
	if err != nil {
		http.Error(w, "error revising roadmap: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update decision with new roadmap
	roadmapJSON, _ := json.MarshalIndent(revisedRoadmap, "", "  ")
	roadmapStr := string(roadmapJSON)
	decision.Details = &roadmapStr
	decision.UpdatedAt = time.Now()

	if err := s.db.UpdateDecision(ctx, decision); err != nil {
		http.Error(w, "error updating decision", http.StatusInternalServerError)
		return
	}

	// Save agent response message
	agentMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decisionID,
		Role:       "agent",
		Content:    fmt.Sprintf("Revised roadmap based on feedback. Now has %d hops.", len(revisedRoadmap.Hops)),
		TokensUsed: &tokens,
		CreatedAt:  time.Now(),
	}
	if err := s.db.CreateDecisionMessage(ctx, agentMsg); err != nil {
		http.Error(w, "error saving agent message", http.StatusInternalServerError)
		return
	}

	// Redirect back to decision page
	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, fmt.Sprintf("/p/%s/decisions/%s", projectID, decisionID), http.StatusSeeOther)
}

func (s *Server) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	decisionID, err := uuid.Parse(chi.URLParam(r, "decisionID"))
	if err != nil {
		http.Error(w, "invalid decision ID", http.StatusBadRequest)
		return
	}

	decision, err := s.db.GetDecision(ctx, decisionID)
	if err != nil {
		http.Error(w, "decision not found", http.StatusNotFound)
		return
	}

	if decision.SubjectID == nil {
		http.Error(w, "no strategy associated", http.StatusBadRequest)
		return
	}

	strategy, err := s.db.GetStrategy(ctx, *decision.SubjectID)
	if err != nil {
		http.Error(w, "strategy not found", http.StatusNotFound)
		return
	}

	// Load strategy context (same as handleSendMessage)
	objectives, _ := s.db.GetObjectivesByStrategy(ctx, strategy.ID)
	var objInfos []agent.ObjectiveInfo
	for _, obj := range objectives {
		krs, _ := s.db.GetKeyResultsByObjective(ctx, obj.ID)
		var krInfos []agent.KeyResultInfo
		for _, kr := range krs {
			krInfo := agent.KeyResultInfo{
				ID:          kr.ID.String(),
				Description: kr.Description,
				TargetUnits: kr.TargetUnits,
			}
			if kr.TargetDate != nil {
				date := kr.TargetDate.Format("2006-01-02")
				krInfo.TargetDate = &date
			}
			krInfos = append(krInfos, krInfo)
		}
		objInfos = append(objInfos, agent.ObjectiveInfo{
			ID:          obj.ID.String(),
			Description: obj.Description,
			KeyResults:  krInfos,
		})
	}

	funding, _ := s.db.GetFundingSourcesByStrategy(ctx, strategy.ID)
	var fundingEstimates []agent.ResourceEstimate
	for _, f := range funding {
		fundingEstimates = append(fundingEstimates, agent.ResourceEstimate{
			ResourceType: string(f.ResourceType),
			Amount:       f.Amount,
		})
	}

	strategyContext := agent.StrategyContext{
		ID:         strategy.ID.String(),
		Name:       strategy.Name,
		Objectives: objInfos,
		Funding:    fundingEstimates,
	}

	// Generate new proposal
	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "error creating agent client", http.StatusInternalServerError)
		return
	}

	proposer := agent.NewProposer(client)
	roadmap, tokens, err := proposer.ProposeRoadmap(ctx, strategyContext)
	if err != nil {
		http.Error(w, "error generating roadmap: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update decision
	roadmapJSON, _ := json.MarshalIndent(roadmap, "", "  ")
	roadmapStr := string(roadmapJSON)
	decision.Details = &roadmapStr
	decision.UpdatedAt = time.Now()

	if err := s.db.UpdateDecision(ctx, decision); err != nil {
		http.Error(w, "error updating decision", http.StatusInternalServerError)
		return
	}

	// Save system message
	sysMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decisionID,
		Role:       "system",
		Content:    "Roadmap regenerated from scratch.",
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Save agent message
	agentMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decisionID,
		Role:       "agent",
		Content:    fmt.Sprintf("Generated new roadmap proposal with %d hops.", len(roadmap.Hops)),
		TokensUsed: &tokens,
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, agentMsg)

	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, fmt.Sprintf("/p/%s/decisions/%s", projectID, decisionID), http.StatusSeeOther)
}

func (s *Server) handleUpdateRoadmap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	decisionID, err := uuid.Parse(chi.URLParam(r, "decisionID"))
	if err != nil {
		http.Error(w, "invalid decision ID", http.StatusBadRequest)
		return
	}

	decision, err := s.db.GetDecision(ctx, decisionID)
	if err != nil {
		http.Error(w, "decision not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	roadmapJSON := r.FormValue("roadmap")
	if roadmapJSON == "" {
		http.Error(w, "roadmap is required", http.StatusBadRequest)
		return
	}

	// Validate JSON
	var roadmap agent.ProposedRoadmap
	if err := json.Unmarshal([]byte(roadmapJSON), &roadmap); err != nil {
		http.Error(w, "invalid roadmap JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Update decision
	decision.Details = &roadmapJSON
	decision.UpdatedAt = time.Now()

	if err := s.db.UpdateDecision(ctx, decision); err != nil {
		http.Error(w, "error updating decision", http.StatusInternalServerError)
		return
	}

	// Save system message
	sysMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decisionID,
		Role:       "system",
		Content:    "Roadmap manually edited.",
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, fmt.Sprintf("/p/%s/decisions/%s", projectID, decisionID), http.StatusSeeOther)
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	decisionID, err := uuid.Parse(chi.URLParam(r, "decisionID"))
	if err != nil {
		http.Error(w, "invalid decision ID", http.StatusBadRequest)
		return
	}

	decision, err := s.db.GetDecision(ctx, decisionID)
	if err != nil {
		http.Error(w, "decision not found", http.StatusNotFound)
		return
	}

	if decision.SubjectID == nil {
		http.Error(w, "no strategy associated", http.StatusBadRequest)
		return
	}

	// Parse roadmap
	var roadmap agent.ProposedRoadmap
	if decision.Details == nil {
		http.Error(w, "no roadmap to approve", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal([]byte(*decision.Details), &roadmap); err != nil {
		http.Error(w, "error parsing roadmap", http.StatusInternalServerError)
		return
	}

	// Validate roadmap
	if err := validateRoadmap(&roadmap); err != nil {
		http.Error(w, "invalid roadmap: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Create hops and dependencies in transaction
	now := time.Now()
	hopNameToID := make(map[string]uuid.UUID)

	// First pass: create all hops
	for _, ph := range roadmap.Hops {
		hopID := uuid.New()
		hopNameToID[ph.Name] = hopID

		kindParams, _ := json.Marshal(map[string]interface{}{
			"objective_ids": ph.ObjectiveIDs,
		})

		hop := &domain.Hop{
			ID:         hopID,
			StrategyID: *decision.SubjectID,
			Name:       ph.Name,
			Commentary: &ph.Commentary,
			Kind:       ph.Kind,
			KindParams: kindParams,
			Status:     domain.HopStatusPending,
			CreatedAt:  now,
			UpdatedAt:  now,
		}

		if err := s.db.CreateHop(ctx, hop); err != nil {
			http.Error(w, "error creating hop: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Create budget allocations
		for _, cost := range ph.EstimatedCosts {
			fundingSource, err := s.db.GetFundingSourceByType(ctx, *decision.SubjectID, cost.ResourceType)
			if err != nil {
				continue // Skip if funding source doesn't exist
			}
			s.db.CreateBudgetAllocation(ctx, hopID, fundingSource.ID, cost.Amount)
		}
	}

	// Second pass: create dependencies
	for _, ph := range roadmap.Hops {
		hopID := hopNameToID[ph.Name]
		for _, depName := range ph.DependsOn {
			depID, ok := hopNameToID[depName]
			if !ok {
				continue // Skip invalid dependency
			}
			s.db.CreateHopDependency(ctx, hopID, depID)
		}
	}

	// Update decision status
	decision.Status = domain.DecisionStatusResolved
	resolution := "approved"
	decision.Resolution = &resolution
	resolvedAt := time.Now()
	decision.ResolvedAt = &resolvedAt
	decision.UpdatedAt = resolvedAt

	if err := s.db.UpdateDecision(ctx, decision); err != nil {
		http.Error(w, "error updating decision", http.StatusInternalServerError)
		return
	}

	// Save system message
	sysMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decisionID,
		Role:       "system",
		Content:    fmt.Sprintf("Roadmap approved. Created %d hops.", len(roadmap.Hops)),
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Redirect to strategy page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/strategy", projectID), http.StatusSeeOther)
}

func validateRoadmap(r *agent.ProposedRoadmap) error {
	if len(r.Hops) == 0 {
		return fmt.Errorf("roadmap has no hops")
	}

	hopNames := make(map[string]bool)
	for _, hop := range r.Hops {
		if hop.Name == "" {
			return fmt.Errorf("hop has empty name")
		}
		if hopNames[hop.Name] {
			return fmt.Errorf("duplicate hop name: %s", hop.Name)
		}
		hopNames[hop.Name] = true
	}

	// Check dependencies exist
	for _, hop := range r.Hops {
		for _, dep := range hop.DependsOn {
			if !hopNames[dep] {
				return fmt.Errorf("hop %q depends on non-existent hop %q", hop.Name, dep)
			}
		}
	}

	// Check for cycles using DFS
	if hasCycle(r.Hops) {
		return fmt.Errorf("roadmap has circular dependencies")
	}

	return nil
}

func hasCycle(hops []agent.ProposedHop) bool {
	// Build adjacency list
	adj := make(map[string][]string)
	for _, hop := range hops {
		adj[hop.Name] = hop.DependsOn
	}

	// DFS with coloring: 0=white (unvisited), 1=gray (visiting), 2=black (done)
	color := make(map[string]int)

	var dfs func(node string) bool
	dfs = func(node string) bool {
		color[node] = 1 // gray
		for _, dep := range adj[node] {
			if color[dep] == 1 {
				return true // back edge = cycle
			}
			if color[dep] == 0 {
				if dfs(dep) {
					return true
				}
			}
		}
		color[node] = 2 // black
		return false
	}

	for _, hop := range hops {
		if color[hop.Name] == 0 {
			if dfs(hop.Name) {
				return true
			}
		}
	}
	return false
}

func (s *Server) handleProposeRoadmap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	// Get strategy
	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		http.Error(w, "no strategy found", http.StatusNotFound)
		return
	}
	strategy := strategies[0]

	// Build strategy context
	objectives, _ := s.db.GetObjectivesByStrategy(ctx, strategy.ID)
	var objInfos []agent.ObjectiveInfo
	for _, obj := range objectives {
		krs, _ := s.db.GetKeyResultsByObjective(ctx, obj.ID)
		var krInfos []agent.KeyResultInfo
		for _, kr := range krs {
			krInfo := agent.KeyResultInfo{
				ID:          kr.ID.String(),
				Description: kr.Description,
				TargetUnits: kr.TargetUnits,
			}
			if kr.TargetDate != nil {
				date := kr.TargetDate.Format("2006-01-02")
				krInfo.TargetDate = &date
			}
			krInfos = append(krInfos, krInfo)
		}
		objInfos = append(objInfos, agent.ObjectiveInfo{
			ID:          obj.ID.String(),
			Description: obj.Description,
			KeyResults:  krInfos,
		})
	}

	funding, _ := s.db.GetFundingSourcesByStrategy(ctx, strategy.ID)
	var fundingEstimates []agent.ResourceEstimate
	for _, f := range funding {
		fundingEstimates = append(fundingEstimates, agent.ResourceEstimate{
			ResourceType: string(f.ResourceType),
			Amount:       f.Amount,
		})
	}

	strategyContext := agent.StrategyContext{
		ID:         strategy.ID.String(),
		Name:       strategy.Name,
		Objectives: objInfos,
		Funding:    fundingEstimates,
	}

	// Generate proposal
	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "error creating agent client: "+err.Error(), http.StatusInternalServerError)
		return
	}

	proposer := agent.NewProposer(client)
	roadmap, tokens, err := proposer.ProposeRoadmap(ctx, strategyContext)
	if err != nil {
		http.Error(w, "error generating roadmap: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create decision
	now := time.Now()
	roadmapJSON, _ := json.MarshalIndent(roadmap, "", "  ")
	roadmapStr := string(roadmapJSON)

	decision := &domain.Decision{
		ID:               uuid.New(),
		Kind:             domain.DecisionKindRoadmapReview,
		Title:            fmt.Sprintf("Roadmap Review: %s", strategy.Name),
		Details:          &roadmapStr,
		ObjectivityScore: 0.3,
		ImportanceScore:  0.8,
		Status:           domain.DecisionStatusNeedsAssignment,
		SubjectType:      strPtr("strategy"),
		SubjectID:        &strategy.ID,
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
		Content:    fmt.Sprintf("Generated roadmap proposal with %d hops.", len(roadmap.Hops)),
		TokensUsed: &tokensUsed,
		CreatedAt:  now,
	}
	s.db.CreateDecisionMessage(ctx, agentMsg)

	// Redirect to decision page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/decisions/%s", projectID, decision.ID), http.StatusSeeOther)
}

func strPtr(s string) *string {
	return &s
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	decisionID, err := uuid.Parse(chi.URLParam(r, "decisionID"))
	if err != nil {
		http.Error(w, "invalid decision ID", http.StatusBadRequest)
		return
	}

	decision, err := s.db.GetDecision(ctx, decisionID)
	if err != nil {
		http.Error(w, "decision not found", http.StatusNotFound)
		return
	}

	// Update decision status
	decision.Status = domain.DecisionStatusResolved
	resolution := "rejected"
	decision.Resolution = &resolution
	resolvedAt := time.Now()
	decision.ResolvedAt = &resolvedAt
	decision.UpdatedAt = resolvedAt

	if err := s.db.UpdateDecision(ctx, decision); err != nil {
		http.Error(w, "error updating decision", http.StatusInternalServerError)
		return
	}

	// Save system message
	sysMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decisionID,
		Role:       "system",
		Content:    "Roadmap proposal rejected.",
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Redirect to decisions list
	http.Redirect(w, r, fmt.Sprintf("/p/%s/decisions", projectID), http.StatusSeeOther)
}
