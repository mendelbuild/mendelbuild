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
	Decision         *domain.Decision
	Messages         []domain.DecisionMessage
	Roadmap          *agent.ProposedRoadmap
	Strategy         *domain.Strategy
	Hop              *domain.Hop
	VariationProposal *VariationProposalView
}

// VariationProposalView holds parsed variation proposal data.
type VariationProposalView struct {
	HopID      string
	Variations []ProposedVariationView
}

// ProposedVariationView holds a single proposed variation.
type ProposedVariationView struct {
	Name            string
	Approach        string
	Differentiation string
	EstimatedTokens int
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

	view := &DecisionDetailView{
		Decision: decision,
		Messages: messages,
	}

	templateName := "decision_roadmap.html"

	switch decision.Kind {
	case domain.DecisionKindRoadmapReview:
		// Parse roadmap from details
		if decision.Details != nil && *decision.Details != "" {
			var rm agent.ProposedRoadmap
			if err := json.Unmarshal([]byte(*decision.Details), &rm); err == nil {
				view.Roadmap = &rm
			}
		}
		// Load strategy
		if decision.SubjectType != nil && *decision.SubjectType == "strategy" && decision.SubjectID != nil {
			view.Strategy, _ = s.db.GetStrategy(ctx, *decision.SubjectID)
		}

	case domain.DecisionKindVariationReview:
		templateName = "decision_variation.html"
		// Parse variation proposal from details
		if decision.Details != nil && *decision.Details != "" {
			var proposal struct {
				HopID      string `json:"hop_id"`
				Variations []struct {
					Name            string `json:"name"`
					Approach        string `json:"approach"`
					Differentiation string `json:"differentiation"`
					EstimatedTokens int    `json:"estimated_tokens"`
				} `json:"variations"`
			}
			if err := json.Unmarshal([]byte(*decision.Details), &proposal); err == nil {
				vpv := &VariationProposalView{
					HopID: proposal.HopID,
				}
				for _, v := range proposal.Variations {
					vpv.Variations = append(vpv.Variations, ProposedVariationView{
						Name:            v.Name,
						Approach:        v.Approach,
						Differentiation: v.Differentiation,
						EstimatedTokens: v.EstimatedTokens,
					})
				}
				view.VariationProposal = vpv
			}
		}
		// Load hop
		if decision.SubjectType != nil && *decision.SubjectType == "hop" && decision.SubjectID != nil {
			view.Hop, _ = s.db.GetHop(ctx, *decision.SubjectID)
		}
	}

	data := map[string]interface{}{
		"Title":     "Decision: " + decision.Title,
		"ProjectID": projectID,
		"View":      view,
	}

	if err := renderPage(w, templateName, data); err != nil {
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

	// Handle based on decision kind
	switch decision.Kind {
	case domain.DecisionKindRoadmapReview:
		s.sendMessageRoadmap(w, r, decision, feedback)
	case domain.DecisionKindVariationReview:
		s.sendMessageVariation(w, r, decision, feedback)
	default:
		http.Error(w, "unsupported decision kind for messages", http.StatusBadRequest)
	}
}

func (s *Server) sendMessageRoadmap(w http.ResponseWriter, r *http.Request, decision *domain.Decision, feedback string) {
	ctx := r.Context()

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
		DecisionID: decision.ID,
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
	http.Redirect(w, r, fmt.Sprintf("/p/%s/decisions/%s", projectID, decision.ID), http.StatusSeeOther)
}

func (s *Server) sendMessageVariation(w http.ResponseWriter, r *http.Request, decision *domain.Decision, feedback string) {
	ctx := r.Context()

	if decision.SubjectID == nil {
		http.Error(w, "no hop associated", http.StatusBadRequest)
		return
	}

	// Load hop
	hop, err := s.db.GetHop(ctx, *decision.SubjectID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// Parse current proposal
	var currentProposal struct {
		HopID      string `json:"hop_id"`
		Variations []struct {
			Name            string `json:"name"`
			Approach        string `json:"approach"`
			Differentiation string `json:"differentiation"`
			EstimatedTokens int    `json:"estimated_tokens"`
		} `json:"variations"`
	}
	if decision.Details != nil {
		json.Unmarshal([]byte(*decision.Details), &currentProposal)
	}

	// Get strategy for objectives
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

	repoURL := ""
	if repo.URL != nil {
		repoURL = *repo.URL
	}

	// Build revision input - include current proposal and feedback
	revisionInput := agent.VariationRevisionInput{
		Hop: agent.HopContext{
			ID:         hop.ID.String(),
			Name:       hop.Name,
			Commentary: hop.Commentary,
			Objectives: objectiveDescs,
		},
		RepositoryURL:    repoURL,
		CurrentVariations: make([]agent.CurrentVariation, 0, len(currentProposal.Variations)),
		Feedback:         feedback,
	}
	for _, v := range currentProposal.Variations {
		revisionInput.CurrentVariations = append(revisionInput.CurrentVariations, agent.CurrentVariation{
			Name:            v.Name,
			Approach:        v.Approach,
			Differentiation: v.Differentiation,
			EstimatedTokens: v.EstimatedTokens,
		})
	}

	// Call variation proposer for revision
	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "error creating agent client", http.StatusInternalServerError)
		return
	}

	proposer := agent.NewVariationProposer(client)
	revisedProposal, tokens, err := proposer.ReviseVariations(ctx, revisionInput)
	if err != nil {
		http.Error(w, "error revising variations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update decision with new proposal
	proposalJSON, _ := json.MarshalIndent(revisedProposal, "", "  ")
	proposalStr := string(proposalJSON)
	decision.Details = &proposalStr
	decision.UpdatedAt = time.Now()

	if err := s.db.UpdateDecision(ctx, decision); err != nil {
		http.Error(w, "error updating decision", http.StatusInternalServerError)
		return
	}

	// Save agent response message
	agentMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decision.ID,
		Role:       "agent",
		Content:    fmt.Sprintf("Revised variations based on feedback. Now has %d variations.", len(revisedProposal.Variations)),
		TokensUsed: &tokens,
		CreatedAt:  time.Now(),
	}
	if err := s.db.CreateDecisionMessage(ctx, agentMsg); err != nil {
		http.Error(w, "error saving agent message", http.StatusInternalServerError)
		return
	}

	// Redirect back to decision page
	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, fmt.Sprintf("/p/%s/decisions/%s", projectID, decision.ID), http.StatusSeeOther)
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

	// Handle based on decision kind
	switch decision.Kind {
	case domain.DecisionKindRoadmapReview:
		s.regenerateRoadmap(w, r, decision)
	case domain.DecisionKindVariationReview:
		s.regenerateVariations(w, r, decision)
	default:
		http.Error(w, "unsupported decision kind for regeneration", http.StatusBadRequest)
	}
}

func (s *Server) regenerateRoadmap(w http.ResponseWriter, r *http.Request, decision *domain.Decision) {
	ctx := r.Context()

	if decision.SubjectID == nil {
		http.Error(w, "no strategy associated", http.StatusBadRequest)
		return
	}

	strategy, err := s.db.GetStrategy(ctx, *decision.SubjectID)
	if err != nil {
		http.Error(w, "strategy not found", http.StatusNotFound)
		return
	}

	// Load strategy context
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
		DecisionID: decision.ID,
		Role:       "system",
		Content:    "Roadmap regenerated from scratch.",
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Save agent message
	agentMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decision.ID,
		Role:       "agent",
		Content:    fmt.Sprintf("Generated new roadmap proposal with %d hops.", len(roadmap.Hops)),
		TokensUsed: &tokens,
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, agentMsg)

	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, fmt.Sprintf("/p/%s/decisions/%s", projectID, decision.ID), http.StatusSeeOther)
}

func (s *Server) regenerateVariations(w http.ResponseWriter, r *http.Request, decision *domain.Decision) {
	ctx := r.Context()

	if decision.SubjectID == nil {
		http.Error(w, "no hop associated", http.StatusBadRequest)
		return
	}

	hop, err := s.db.GetHop(ctx, *decision.SubjectID)
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

	repoURL := ""
	if repo.URL != nil {
		repoURL = *repo.URL
	}

	// Get budget allocation for tokens
	allocations, _ := s.db.GetBudgetAllocationsByHop(ctx, hop.ID)
	availableBudget := 100000 // Default
	for _, alloc := range allocations {
		sources, _ := s.db.GetFundingSourcesByStrategy(ctx, strategy.ID)
		for _, src := range sources {
			if src.ID == alloc.FundingSourceID && src.ResourceType == domain.ResourceTypeClaudeTokens {
				availableBudget = int(alloc.LimitAmount)
				break
			}
		}
	}

	input := agent.VariationProposerInput{
		Hop: agent.HopContext{
			ID:         hop.ID.String(),
			Name:       hop.Name,
			Commentary: hop.Commentary,
			Objectives: objectiveDescs,
		},
		RepositoryURL:   repoURL,
		AvailableBudget: availableBudget,
		NumVariations:   2,
	}

	// Generate new proposal
	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "error creating agent client", http.StatusInternalServerError)
		return
	}

	proposer := agent.NewVariationProposer(client)
	proposal, tokens, err := proposer.ProposeVariations(ctx, input)
	if err != nil {
		http.Error(w, "error generating variations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to storage format
	proposalData := struct {
		HopID      uuid.UUID `json:"hop_id"`
		Variations []struct {
			Name            string `json:"name"`
			Approach        string `json:"approach"`
			Differentiation string `json:"differentiation"`
			EstimatedTokens int    `json:"estimated_tokens"`
		} `json:"variations"`
	}{
		HopID: hop.ID,
	}
	for _, v := range proposal.Variations {
		proposalData.Variations = append(proposalData.Variations, struct {
			Name            string `json:"name"`
			Approach        string `json:"approach"`
			Differentiation string `json:"differentiation"`
			EstimatedTokens int    `json:"estimated_tokens"`
		}{
			Name:            v.Name,
			Approach:        v.Approach,
			Differentiation: v.Differentiation,
			EstimatedTokens: v.EstimatedTokens,
		})
	}

	// Update decision
	proposalJSON, _ := json.MarshalIndent(proposalData, "", "  ")
	proposalStr := string(proposalJSON)
	decision.Details = &proposalStr
	decision.UpdatedAt = time.Now()

	if err := s.db.UpdateDecision(ctx, decision); err != nil {
		http.Error(w, "error updating decision", http.StatusInternalServerError)
		return
	}

	// Save system message
	sysMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decision.ID,
		Role:       "system",
		Content:    "Variations regenerated from scratch.",
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Save agent message
	agentMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decision.ID,
		Role:       "agent",
		Content:    fmt.Sprintf("Generated new variation proposal with %d variations.\n\nRationale: %s", len(proposal.Variations), proposal.Rationale),
		TokensUsed: &tokens,
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, agentMsg)

	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, fmt.Sprintf("/p/%s/decisions/%s", projectID, decision.ID), http.StatusSeeOther)
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
		http.Error(w, "no subject associated", http.StatusBadRequest)
		return
	}

	// Handle based on decision kind
	switch decision.Kind {
	case domain.DecisionKindRoadmapReview:
		s.approveRoadmap(w, r, decision, projectID)
	case domain.DecisionKindVariationReview:
		s.approveVariations(w, r, decision, projectID)
	default:
		http.Error(w, "unsupported decision kind", http.StatusBadRequest)
	}
}

func (s *Server) approveRoadmap(w http.ResponseWriter, r *http.Request, decision *domain.Decision, projectID string) {
	ctx := r.Context()

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

		params, _ := json.Marshal(map[string]interface{}{
			"objective_ids": ph.ObjectiveIDs,
		})

		hop := &domain.Hop{
			ID:         hopID,
			StrategyID: *decision.SubjectID,
			Name:       ph.Name,
			Commentary: ph.Commentary,
			Params:     params,
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
		DecisionID: decision.ID,
		Role:       "system",
		Content:    fmt.Sprintf("Roadmap approved. Created %d hops.", len(roadmap.Hops)),
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Redirect to strategy page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/strategy", projectID), http.StatusSeeOther)
}

func (s *Server) approveVariations(w http.ResponseWriter, r *http.Request, decision *domain.Decision, projectID string) {
	ctx := r.Context()

	if decision.Details == nil {
		http.Error(w, "no variations to approve", http.StatusBadRequest)
		return
	}

	// Parse variation proposal
	var proposal struct {
		HopID      uuid.UUID `json:"hop_id"`
		Variations []struct {
			Name            string `json:"name"`
			Approach        string `json:"approach"`
			Differentiation string `json:"differentiation"`
			EstimatedTokens int    `json:"estimated_tokens"`
		} `json:"variations"`
	}
	if err := json.Unmarshal([]byte(*decision.Details), &proposal); err != nil {
		http.Error(w, "error parsing variations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update decision status first
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

	// Save system message about approval
	sysMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decision.ID,
		Role:       "system",
		Content:    fmt.Sprintf("Variations approved. Starting code generation for %d variations.", len(proposal.Variations)),
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Note: Code generation is triggered asynchronously or via CLI command
	// For now, we just redirect to the hop detail page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/hops/%s", projectID, decision.SubjectID.String()), http.StatusSeeOther)
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
