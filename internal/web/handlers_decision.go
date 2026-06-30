package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// DecisionDetailView holds data for rendering a decision detail page.
type DecisionDetailView struct {
	Decision           *domain.Decision
	Messages           []domain.DecisionMessage
	Roadmap            *agent.ProposedRoadmap
	Strategy           *domain.Strategy
	Hop                *domain.Hop
	VariationProposal  *VariationProposalView
	ExistingVariations []ExistingVariationView // Already-created variations (immutable in review)
	SelectionData      *SelectionDataView
	EvaluationCriteria *agent.EvaluationCriteria
	CanSelect          bool // True if all variations are done and user can pick winner
	PendingCount       int
	FailedCount        int
	TotalCount         int
	HopBudget          int    // Total token budget for the hop
	Resolution         string // Dereferenced resolution for template comparison
}

// ExistingVariationView holds an existing variation for display in variation review.
type ExistingVariationView struct {
	ID       string
	Name     string
	Approach string
	Status   string
}

// VariationProposalView holds parsed variation proposal data.
type VariationProposalView struct {
	HopID              string
	Variations         []ProposedVariationView
	TotalEstimatedTokens int
}

// ProposedVariationView holds a single proposed variation.
type ProposedVariationView struct {
	Index           int
	Name            string
	Approach        string
	Differentiation string
	EstimatedTokens int
}

// SelectionDataView holds data for variation selection.
type SelectionDataView struct {
	HopID        string
	HopName      string
	Variations   []SelectionVariationView
	Criteria     []string           // Criterion names for table headers
	Summary      string             // AI summary comparing variations
}

// SelectionVariationView holds a single variation for selection.
type SelectionVariationView struct {
	ID        string
	Name      string
	Approach  string
	Status    string
	CommitRef string
	BranchURL string             // GitHub branch URL
	Grades    map[string]float64 // Criterion name -> score (0.0-1.0)
}

// VariationGrade holds a score with rationale for display.
type VariationGrade struct {
	Score     float64
	Rationale string
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

	resolution := ""
	if decision.Resolution != nil {
		resolution = *decision.Resolution
	}

	view := &DecisionDetailView{
		Decision:   decision,
		Messages:   messages,
		Resolution: resolution,
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
				totalTokens := 0
				for i, v := range proposal.Variations {
					vpv.Variations = append(vpv.Variations, ProposedVariationView{
						Index:           i,
						Name:            v.Name,
						Approach:        v.Approach,
						Differentiation: v.Differentiation,
						EstimatedTokens: v.EstimatedTokens,
					})
					totalTokens += v.EstimatedTokens
				}
				vpv.TotalEstimatedTokens = totalTokens
				view.VariationProposal = vpv
			}
		}
		// Load hop and budget
		if decision.SubjectType != nil && *decision.SubjectType == "hop" && decision.SubjectID != nil {
			view.Hop, _ = s.db.GetHop(ctx, *decision.SubjectID)
			if view.Hop != nil {
				// Get budget allocation for tokens
				allocations, _ := s.db.GetBudgetAllocationsByHop(ctx, view.Hop.ID)
				strategy, _ := s.db.GetStrategy(ctx, view.Hop.StrategyID)
				if strategy != nil {
					for _, alloc := range allocations {
						sources, _ := s.db.GetFundingSourcesByStrategy(ctx, strategy.ID)
						for _, src := range sources {
							if src.ID == alloc.FundingSourceID && src.ResourceType == domain.ResourceTypeClaudeTokens {
								view.HopBudget = int(alloc.LimitAmount)
								break
							}
						}
					}
				}

				// Load existing variations (already created, shown as immutable)
				// Include: pending, creating, rejected (with code), merged
				existingVars, _ := s.db.GetVariationsByHop(ctx, view.Hop.ID)
				for _, v := range existingVars {
					shouldInclude := false
					switch v.Status {
					case domain.VariationStatusPending, domain.VariationStatusCreating:
						shouldInclude = true
					case domain.VariationStatusRejected, domain.VariationStatusMerged:
						// Only show if code was generated (has commit ref)
						shouldInclude = v.CommitRef != nil
					}
					if shouldInclude {
						view.ExistingVariations = append(view.ExistingVariations, ExistingVariationView{
							ID:       v.ID.String(),
							Name:     v.Name,
							Approach: v.Approach,
							Status:   string(v.Status),
						})
					}
				}
			}
		}

	case domain.DecisionKindVariationSelection:
		templateName = "decision_selection.html"
		// Load hop and variations
		if decision.SubjectType != nil && *decision.SubjectType == "hop" && decision.SubjectID != nil {
			view.Hop, _ = s.db.GetHop(ctx, *decision.SubjectID)
			if view.Hop != nil {
				// Parse evaluation criteria
				var criteria agent.EvaluationCriteria
				if len(view.Hop.EvaluationCriteria) > 0 {
					if err := json.Unmarshal(view.Hop.EvaluationCriteria, &criteria); err == nil {
						view.EvaluationCriteria = &criteria
					}
				}

				// Get strategy and repository for branch URLs
				strategy, _ := s.db.GetStrategy(ctx, view.Hop.StrategyID)
				var repoURL string
				if strategy != nil {
					repo, _ := s.db.GetRepositoryByProject(ctx, strategy.ProjectID)
					if repo != nil && repo.URL != nil {
						repoURL = *repo.URL
					}
				}

				// Get variations
				variations, _ := s.db.GetVariationsByHop(ctx, view.Hop.ID)

				selectionData := &SelectionDataView{
					HopID:   view.Hop.ID.String(),
					HopName: view.Hop.Name,
				}

				// Extract criterion names for table headers
				for _, c := range criteria.Criteria {
					selectionData.Criteria = append(selectionData.Criteria, c.Name)
				}

				pendingCount := 0
				failedCount := 0
				creatingCount := 0

				for _, v := range variations {
					sv := SelectionVariationView{
						ID:       v.ID.String(),
						Name:     v.Name,
						Approach: v.Approach,
						Status:   string(v.Status),
						Grades:   make(map[string]float64),
					}
					if v.CommitRef != nil {
						sv.CommitRef = *v.CommitRef
					}

					// Construct branch URL
					if repoURL != "" {
						branchName := fmt.Sprintf("mendel/%s/%s", sanitizeBranchName(view.Hop.Name), sanitizeBranchName(v.Name))
						sv.BranchURL = constructGitHubBranchURL(repoURL, branchName)
					}

					selectionData.Variations = append(selectionData.Variations, sv)

					switch v.Status {
					case domain.VariationStatusPending:
						pendingCount++
					case domain.VariationStatusError, domain.VariationStatusTerminated:
						failedCount++
					case domain.VariationStatusCreating:
						creatingCount++
					}
				}

				// Note: Evaluation is done via AJAX to avoid blocking page load
				// See apiEvaluateVariations handler and decision_selection.html

				view.SelectionData = selectionData
				view.PendingCount = pendingCount
				view.FailedCount = failedCount
				view.TotalCount = len(variations)
				// Can select if all variations are done (none creating) and at least one is pending
				view.CanSelect = creatingCount == 0 && pendingCount > 0 && decision.Status != domain.DecisionStatusResolved
			}
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

		// Evaluation criteria will be generated later during variation proposal
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

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	if decision.Details == nil {
		http.Error(w, "no variations to approve", http.StatusBadRequest)
		return
	}

	// Get selected variation indices
	selectedIndices := make(map[int]bool)
	for _, v := range r.Form["variations"] {
		var idx int
		if _, err := fmt.Sscanf(v, "%d", &idx); err == nil {
			selectedIndices[idx] = true
		}
	}

	if len(selectedIndices) == 0 {
		http.Error(w, "no variations selected", http.StatusBadRequest)
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

	// Filter to only selected variations
	var selectedVariations []struct {
		Name            string `json:"name"`
		Approach        string `json:"approach"`
		Differentiation string `json:"differentiation"`
		EstimatedTokens int    `json:"estimated_tokens"`
	}
	var selectedNames []string
	for i, v := range proposal.Variations {
		if selectedIndices[i] {
			selectedVariations = append(selectedVariations, v)
			selectedNames = append(selectedNames, v.Name)
		}
	}

	// Get hop and repository info
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

	repo, err := s.db.GetRepositoryByProject(ctx, strategy.ProjectID)
	if err != nil {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}

	// Get existing variations to avoid creating duplicates
	existingVariations, _ := s.db.GetVariationsByHop(ctx, hop.ID)
	existingNames := make(map[string]bool)
	for _, v := range existingVariations {
		existingNames[v.Name] = true
	}

	// Create Variation records for selected variations (skipping existing ones)
	now := time.Now()
	createdCount := 0
	for _, v := range selectedVariations {
		// Skip if a variation with this name already exists
		if existingNames[v.Name] {
			continue
		}

		variation := &domain.Variation{
			ID:           uuid.New(),
			HopID:        hop.ID,
			Name:         v.Name,
			Approach:     v.Approach,
			RepositoryID: &repo.ID,
			Status:       domain.VariationStatusCreating,
			CreatedAt:    now,
			UpdatedAt:    now,
		}

		if err := s.db.CreateVariation(ctx, variation); err != nil {
			http.Error(w, "error creating variation: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Record initial state
		s.db.CreateVariationStateTransition(ctx, variation.ID, "", string(domain.VariationStatusCreating), "variation created from approved proposal")
		createdCount++
	}

	// Update hop status to active
	if err := s.db.UpdateHopStatus(ctx, hop.ID, domain.HopStatusActive); err != nil {
		http.Error(w, "error updating hop status", http.StatusInternalServerError)
		return
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

	// Save system message about approval
	var msgContent string
	if createdCount > 0 {
		msgContent = fmt.Sprintf("Approved and created %d new variation(s). Code generation will start automatically.", createdCount)
	} else {
		msgContent = "Approved. No new variations to create (selected variations already exist)."
	}
	sysMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decision.ID,
		Role:       "system",
		Content:    msgContent,
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Background worker will pick up variations in "creating" status

	// Redirect to the hop detail page
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

func (s *Server) handleSelectWinner(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	decisionID, err := uuid.Parse(chi.URLParam(r, "decisionID"))
	if err != nil {
		http.Error(w, "invalid decision ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	winnerID, err := uuid.Parse(r.FormValue("winner"))
	if err != nil {
		http.Error(w, "no winner selected", http.StatusBadRequest)
		return
	}

	decision, err := s.db.GetDecision(ctx, decisionID)
	if err != nil {
		http.Error(w, "decision not found", http.StatusNotFound)
		return
	}

	if decision.SubjectID == nil {
		http.Error(w, "no hop associated", http.StatusBadRequest)
		return
	}

	// Get hop
	hop, err := s.db.GetHop(ctx, *decision.SubjectID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// Get winning variation
	winner, err := s.db.GetVariation(ctx, winnerID)
	if err != nil {
		http.Error(w, "winning variation not found", http.StatusNotFound)
		return
	}

	// Get all variations to update losers
	variations, err := s.db.GetVariationsByHop(ctx, hop.ID)
	if err != nil {
		http.Error(w, "error getting variations", http.StatusInternalServerError)
		return
	}

	// Merge winner branch to main
	if err := s.mergeWinnerToMain(ctx, hop, winner); err != nil {
		http.Error(w, "error merging winner: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update variation statuses
	for _, v := range variations {
		if v.ID == winnerID {
			v.Status = domain.VariationStatusMerged
		} else if v.Status == domain.VariationStatusPending {
			v.Status = domain.VariationStatusRejected
		}
		// Leave error/terminated variations as-is
		s.db.UpdateVariation(ctx, &v)
	}

	// Update hop status to completed
	if err := s.db.UpdateHopStatus(ctx, hop.ID, domain.HopStatusCompleted); err != nil {
		http.Error(w, "error updating hop status", http.StatusInternalServerError)
		return
	}

	// Activate dependent hops
	activated, err := s.db.ActivateDependentHops(ctx, hop.ID)
	if err != nil {
		fmt.Printf("Error activating dependent hops: %v\n", err)
	} else if activated > 0 {
		fmt.Printf("Activated %d dependent hops\n", activated)
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
		Content:    fmt.Sprintf("Winner selected: %s\nBranch merged to main.", winner.Name),
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Redirect to strategy page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/strategy", projectID), http.StatusSeeOther)
}

func (s *Server) handleRejectAllVariations(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "no hop associated", http.StatusBadRequest)
		return
	}

	// Get hop
	hop, err := s.db.GetHop(ctx, *decision.SubjectID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// NOTE: We do NOT reject existing variations - they stay pending so user can
	// compare them against new variations in the variation review

	// Update decision status
	decision.Status = domain.DecisionStatusResolved
	resolution := "requested_more"
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
		Content:    "Requested additional variations. Returning to variation review.",
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Create a new VariationReview decision so user can request more variations
	s.createMoreVariationsDecision(ctx, w, r, decision, hop, projectID)
}

// handleRequestMoreVariations handles requesting additional variations from selection page.
func (s *Server) handleRequestMoreVariations(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "no hop associated", http.StatusBadRequest)
		return
	}

	hop, err := s.db.GetHop(ctx, *decision.SubjectID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// Resolve current selection decision as "requested_more"
	decision.Status = domain.DecisionStatusResolved
	resolution := "requested_more"
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
		Content:    "Requested additional variations. Returning to variation review.",
		CreatedAt:  time.Now(),
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Create a new VariationReview decision
	s.createMoreVariationsDecision(ctx, w, r, decision, hop, projectID)
}

// createMoreVariationsDecision creates a new VariationReview decision for proposing more variations.
func (s *Server) createMoreVariationsDecision(ctx context.Context, w http.ResponseWriter, r *http.Request, oldDecision *domain.Decision, hop *domain.Hop, projectID string) {
	now := time.Now()

	// Create empty proposal - user will request new variations via feedback
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
	proposalJSON, _ := json.MarshalIndent(proposalData, "", "  ")
	proposalStr := string(proposalJSON)

	newDecision := &domain.Decision{
		ID:               uuid.New(),
		Kind:             domain.DecisionKindVariationReview,
		Title:            fmt.Sprintf("Variation Review: %s (additional)", hop.Name),
		Details:          &proposalStr,
		ObjectivityScore: 0.5,
		ImportanceScore:  0.7,
		Status:           domain.DecisionStatusNeedsAssignment,
		SubjectType:      strPtr("hop"),
		SubjectID:        &hop.ID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := s.db.CreateDecision(ctx, newDecision); err != nil {
		http.Error(w, "error creating decision: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get count of existing variations for the message
	existingVariations, _ := s.db.GetVariationsByHop(ctx, hop.ID)
	pendingCount := 0
	for _, v := range existingVariations {
		if v.Status == domain.VariationStatusPending {
			pendingCount++
		}
	}

	// Create system message
	sysMsg := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: newDecision.ID,
		Role:       "system",
		Content:    fmt.Sprintf("Variation review opened for additional proposals.\n\nThere are %d existing pending variation(s) that will be retained. Use the feedback form to request new variations to compare against them.", pendingCount),
		CreatedAt:  now,
	}
	s.db.CreateDecisionMessage(ctx, sysMsg)

	// Redirect to the new decision page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/decisions/%s", projectID, newDecision.ID), http.StatusSeeOther)
}

// mergeWinnerToMain merges the winning variation's branch into main.
func (s *Server) mergeWinnerToMain(ctx context.Context, hop *domain.Hop, winner *domain.Variation) error {
	// Get strategy and repository info
	strategy, err := s.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		return fmt.Errorf("get strategy: %w", err)
	}

	repo, err := s.db.GetRepositoryByProject(ctx, strategy.ProjectID)
	if err != nil {
		return fmt.Errorf("get repository: %w", err)
	}

	// Parse repository config
	var repoConfig struct {
		MainBranch string `json:"main_branch"`
		AuthToken  string `json:"auth_token"`
	}
	if repo.Config != nil {
		json.Unmarshal(repo.Config, &repoConfig)
	}
	if repoConfig.MainBranch == "" {
		repoConfig.MainBranch = "main"
	}

	// Clone repository
	workDir := git.WorkDirForVariation("merge-" + winner.ID.String())
	gitClient := git.NewClient(workDir)
	defer gitClient.Cleanup()

	if repo.URL == nil {
		return fmt.Errorf("repository has no URL")
	}

	if err := gitClient.Clone(ctx, *repo.URL, repoConfig.MainBranch, repoConfig.AuthToken); err != nil {
		return fmt.Errorf("clone: %w", err)
	}

	// Merge the winner's branch
	branchName := fmt.Sprintf("mendel/%s/%s", hop.Name, winner.Name)
	if err := gitClient.MergeRemoteBranch(ctx, branchName, repoConfig.AuthToken); err != nil {
		return fmt.Errorf("merge: %w", err)
	}

	// Push to main
	if err := gitClient.Push(ctx, repoConfig.AuthToken); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	return nil
}

// apiEvaluateVariations evaluates variations for a hop and returns JSON.
func (s *Server) apiEvaluateVariations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
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

	// Parse evaluation criteria
	var criteria agent.EvaluationCriteria
	if len(hop.EvaluationCriteria) > 0 {
		if err := json.Unmarshal(hop.EvaluationCriteria, &criteria); err != nil {
			http.Error(w, "invalid evaluation criteria", http.StatusInternalServerError)
			return
		}
	}

	if len(criteria.Criteria) == 0 {
		// No criteria to evaluate against
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"evaluations": []interface{}{},
			"summary":     "",
		})
		return
	}

	// Get variations
	variations, err := s.db.GetVariationsByHop(ctx, hopID)
	if err != nil {
		http.Error(w, "failed to get variations", http.StatusInternalServerError)
		return
	}

	// Build list for evaluation (only pending variations)
	var variationsForEval []agent.VariationForEvaluation
	for _, v := range variations {
		if v.Status == domain.VariationStatusPending {
			variationsForEval = append(variationsForEval, agent.VariationForEvaluation{
				ID:       v.ID.String(),
				Name:     v.Name,
				Approach: v.Approach,
			})
		}
	}

	if len(variationsForEval) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"evaluations": []interface{}{},
			"summary":     "",
		})
		return
	}

	// Evaluate
	evalInput := agent.VariationEvaluationInput{
		HopName:    hop.Name,
		Criteria:   criteria.Criteria,
		Variations: variationsForEval,
	}

	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "failed to create agent client", http.StatusInternalServerError)
		return
	}

	evaluator := agent.NewVariationEvaluator(client)
	evalResult, _, err := evaluator.Evaluate(ctx, evalInput)
	if err != nil {
		http.Error(w, "evaluation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(evalResult)
}

// sanitizeBranchName converts a name to a git-safe branch name component.
func sanitizeBranchName(name string) string {
	result := ""
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			result += string(c)
		} else if c == ' ' {
			result += "-"
		}
	}
	return result
}

// constructGitHubBranchURL constructs a GitHub URL to view a branch.
func constructGitHubBranchURL(repoURL, branchName string) string {
	// Handle various GitHub URL formats
	// https://github.com/user/repo.git -> https://github.com/user/repo/tree/branch
	// https://github.com/user/repo -> https://github.com/user/repo/tree/branch
	// git@github.com:user/repo.git -> https://github.com/user/repo/tree/branch

	url := repoURL

	// Remove .git suffix
	if len(url) > 4 && url[len(url)-4:] == ".git" {
		url = url[:len(url)-4]
	}

	// Convert SSH URLs to HTTPS
	if len(url) > 15 && url[:15] == "git@github.com:" {
		url = "https://github.com/" + url[15:]
	}

	// Ensure HTTPS
	if len(url) > 4 && url[:4] != "http" {
		return "" // Unsupported format
	}

	return url + "/tree/" + branchName
}
