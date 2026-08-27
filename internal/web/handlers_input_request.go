package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/cost"
	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// InputRequestDetailView holds data for rendering an input request detail page.
type InputRequestDetailView struct {
	InputRequest               *domain.InputRequest
	Messages                   []domain.InputRequestMessage
	Roadmap                    *agent.ProposedRoadmap
	CostAudit                  *agent.CostAuditResponse // Fact-check of the roadmap's estimates
	StrategyCost               *StrategyCostView        // Budget the roadmap has to fit inside
	Strategy                   *domain.Strategy
	Hop                        *domain.Hop
	Variation                  *domain.Variation        // For credential_request: the blocked variation
	VariationProposal          *VariationProposalView
	ExistingVariations         []ExistingVariationView // Already-created variations (immutable in review)
	SelectionData              *SelectionDataView
	EvaluationCriteria         *agent.EvaluationCriteria
	ConflictInfo               *ConflictInfoView // Migration conflicts if detected
	CanSelect                  bool              // True if all variations are done and user can pick winner
	PendingCount               int
	FailedCount                int
	TotalCount                 int
	HopCost                    *HopCostView // Estimate, ceiling and spend to date for the hop
	Resolution                 string       // Dereferenced resolution for template comparison
	ExistingHopsJSON           template.JS  // JSON of existing hops for DAG rendering (template.JS to avoid HTML escaping)
	ObjectivesJSON             template.JS  // JSON map of objective ID to description
	NeedsProductionCredentials bool         // requires_production but no credentials configured
	HostingPlatforms           []HostingPlatformOption // For hosting_platform kind
}

// HostingPlatformOption represents a hosting platform choice.
type HostingPlatformOption struct {
	ID          string
	Name        string
	Description string
}

// ExistingVariationView holds an existing variation for display in variation review.
type ExistingVariationView struct {
	ID          string
	Name        string
	Approach    string
	Status      string
	HasConflict bool     // True if this variation has migration conflicts
	ConflictsWith []string // Names of other variations this conflicts with
}

// VariationProposalView holds parsed variation proposal data.
type VariationProposalView struct {
	HopID            string
	Variations       []ProposedVariationView
	TotalEstimatedUSD float64
}

// ProposedVariationView holds a single proposed variation.
type ProposedVariationView struct {
	Index           int
	Name            string
	Approach        string
	Differentiation string
	EstimatedCostUSD float64
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
	ID           string
	Name         string
	Approach     string
	Status       string
	CommitRef    string
	BranchURL    string             // GitHub branch URL
	DiffURL      string             // GitHub compare URL (main...branch)
	DemoURL      string             // Running demo URL (if any)
	Grades       map[string]float64 // Criterion name -> score (0.0-1.0)
	FilesChanged int                // Number of files changed vs main
	Additions    int                // Lines added vs main
	Deletions    int                // Lines deleted vs main
}

// VariationGrade holds a score with rationale for display.
type VariationGrade struct {
	Score     float64
	Rationale string
}

// ConflictInfoView holds parsed conflict data for display.
type ConflictInfoView struct {
	Summary   string
	Conflicts []ConflictView
}

// ConflictView holds a single conflict for display.
type ConflictView struct {
	VariationNames []string
	ConflictType   string
	Description    string
	AffectedSchema string
}

func (s *Server) handleInputRequestDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid input request ID", http.StatusBadRequest)
		return
	}

	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "input request not found", http.StatusNotFound)
		return
	}

	messages, err := s.db.GetInputRequestMessages(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "error loading messages", http.StatusInternalServerError)
		return
	}

	resolution := ""
	if inputRequest.Resolution != nil {
		resolution = *inputRequest.Resolution
	}

	view := &InputRequestDetailView{
		InputRequest: inputRequest,
		Messages:     messages,
		Resolution:   resolution,
	}

	templateName := "input_request_roadmap.html"

	switch inputRequest.Kind {
	case domain.InputRequestKindRoadmapReview:
		// Parse roadmap from details
		if inputRequest.Details != nil && *inputRequest.Details != "" {
			var rm agent.ProposedRoadmap
			if err := json.Unmarshal([]byte(*inputRequest.Details), &rm); err == nil {
				view.Roadmap = &rm
			}
		}
		// The auditor's verdict on those estimates, so a reviewer sees the
		// challenge next to the claim rather than having to take it on trust.
		view.CostAudit = s.loadCostAudit(ctx, inputRequest.ID)
		// Load strategy and existing hops
		if inputRequest.SubjectType != nil && *inputRequest.SubjectType == "strategy" && inputRequest.SubjectID != nil {
			view.Strategy, _ = s.db.GetStrategy(ctx, *inputRequest.SubjectID)
			view.StrategyCost = s.strategyCostView(ctx, *inputRequest.SubjectID)

			// Load existing hops with their statuses for DAG rendering
			existingHops, _ := s.db.GetHopsByStrategy(ctx, *inputRequest.SubjectID)
			if len(existingHops) > 0 {
				type ExistingHopInfo struct {
					Name       string `json:"name"`
					Status     string `json:"status"`
					IsTerminal bool   `json:"is_terminal"`
				}
				var hopInfos []ExistingHopInfo
				for _, h := range existingHops {
					isTerminal := h.Status == domain.HopStatusCompleted ||
						h.Status == domain.HopStatusRejected ||
						h.Status == domain.HopStatusAbandoned
					hopInfos = append(hopInfos, ExistingHopInfo{
						Name:       h.Name,
						Status:     string(h.Status),
						IsTerminal: isTerminal,
					})
				}
				if jsonBytes, err := json.Marshal(hopInfos); err == nil {
					view.ExistingHopsJSON = template.JS(jsonBytes)
				}
			}

			// Load objectives for displaying names
			objectives, _ := s.db.GetObjectivesByStrategy(ctx, *inputRequest.SubjectID)
			if len(objectives) > 0 {
				objMap := make(map[string]string)
				for _, obj := range objectives {
					objMap[obj.ID.String()] = obj.Description
				}
				if jsonBytes, err := json.Marshal(objMap); err == nil {
					view.ObjectivesJSON = template.JS(jsonBytes)
				}
			}
		}

	case domain.InputRequestKindVariationReview:
		templateName = "input_request_variation.html"

		// Build a map of variation name -> conflicting variation names
		conflictMap := make(map[string][]string)

		// Parse details - could be conflicts or variation proposal
		if inputRequest.Details != nil && *inputRequest.Details != "" {
			// First check for conflicts
			var conflictCheck struct {
				Conflicts []struct {
					VariationNames []string `json:"variation_names"`
					ConflictType   string   `json:"conflict_type"`
					Description    string   `json:"description"`
					AffectedSchema string   `json:"affected_schema"`
				} `json:"conflicts"`
				Summary string `json:"summary"`
			}
			if err := json.Unmarshal([]byte(*inputRequest.Details), &conflictCheck); err == nil && len(conflictCheck.Conflicts) > 0 {
				// Store conflict info for display
				civ := &ConflictInfoView{
					Summary: conflictCheck.Summary,
				}
				for _, c := range conflictCheck.Conflicts {
					civ.Conflicts = append(civ.Conflicts, ConflictView{
						VariationNames: c.VariationNames,
						ConflictType:   c.ConflictType,
						Description:    c.Description,
						AffectedSchema: c.AffectedSchema,
					})
					// Build conflict map: for each variation, track what it conflicts with
					for _, name := range c.VariationNames {
						for _, otherName := range c.VariationNames {
							if name != otherName {
								conflictMap[name] = append(conflictMap[name], otherName)
							}
						}
					}
				}
				view.ConflictInfo = civ
			} else {
				// Normal variation proposal
				var proposal agent.VariationProposal
				if err := json.Unmarshal([]byte(*inputRequest.Details), &proposal); err == nil {
					vpv := &VariationProposalView{HopID: proposal.HopID}
					for i, v := range proposal.Variations {
						vpv.Variations = append(vpv.Variations, ProposedVariationView{
							Index:            i,
							Name:             v.Name,
							Approach:         v.Approach,
							Differentiation:  v.Differentiation,
							EstimatedCostUSD: v.EstimatedCostUSD,
						})
						vpv.TotalEstimatedUSD += v.EstimatedCostUSD
					}
					view.VariationProposal = vpv
				}
			}
		}
		// Load hop and budget
		if inputRequest.SubjectType != nil && *inputRequest.SubjectType == "hop" && inputRequest.SubjectID != nil {
			view.Hop, _ = s.db.GetHop(ctx, *inputRequest.SubjectID)
			if view.Hop != nil {
				view.HopCost = s.hopCostView(ctx, view.Hop.ID)

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
						ev := ExistingVariationView{
							ID:       v.ID.String(),
							Name:     v.Name,
							Approach: v.Approach,
							Status:   string(v.Status),
						}
						// Check if this variation has conflicts
						if conflicts, ok := conflictMap[v.Name]; ok {
							ev.HasConflict = true
							ev.ConflictsWith = conflicts
						}
						view.ExistingVariations = append(view.ExistingVariations, ev)
					}
				}
			}
		}

	case domain.InputRequestKindCredentialRequest:
		templateName = "input_request_credential.html"
		// Load the blocked variation
		if inputRequest.SubjectType != nil && *inputRequest.SubjectType == "variation" && inputRequest.SubjectID != nil {
			variation, err := s.db.GetVariation(ctx, *inputRequest.SubjectID)
			if err == nil {
				view.Variation = variation
				// Get hop for context
				view.Hop, _ = s.db.GetHop(ctx, variation.HopID)
			}
		}

	case domain.InputRequestKindVariationSelection:
		templateName = "input_request_selection.html"
		// Load hop and variations
		if inputRequest.SubjectType != nil && *inputRequest.SubjectType == "hop" && inputRequest.SubjectID != nil {
			view.Hop, _ = s.db.GetHop(ctx, *inputRequest.SubjectID)
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

				// Check if production credentials are needed but missing
				if view.Hop.RequiresProduction && strategy != nil {
					creds, err := s.db.ListProjectCredentials(ctx, strategy.ProjectID)
					if err != nil || len(creds) == 0 {
						view.NeedsProductionCredentials = true
					}
				}

				var repoURL string
				var mainBranch string
				if strategy != nil {
					repo, _ := s.db.GetRepositoryByProject(ctx, strategy.ProjectID)
					if repo != nil && repo.URL != nil {
						repoURL = *repo.URL
					}
					if repo != nil && repo.Config != nil {
						var repoConfig struct {
							MainBranch string `json:"main_branch"`
						}
						if err := json.Unmarshal(repo.Config, &repoConfig); err == nil {
							mainBranch = repoConfig.MainBranch
						}
					}
				}
				if mainBranch == "" {
					mainBranch = "main"
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
					if v.DiffFilesChanged != nil {
						sv.FilesChanged = *v.DiffFilesChanged
					}
					if v.DiffAdditions != nil {
						sv.Additions = *v.DiffAdditions
					}
					if v.DiffDeletions != nil {
						sv.Deletions = *v.DiffDeletions
					}

					// Look up running demo URL from demo_instances
					if demo, err := s.db.GetRunningDemoByVariation(ctx, v.ID); err == nil && demo != nil {
						sv.DemoURL = demo.URL
					}

					// Construct branch and diff URLs
					if repoURL != "" {
						branchName := fmt.Sprintf("mendel/%s/%s", sanitizeBranchName(view.Hop.Name), sanitizeBranchName(v.Name))
						sv.BranchURL = constructGitHubBranchURL(repoURL, branchName)
						sv.DiffURL = constructGitHubDiffURL(repoURL, mainBranch, branchName)
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
				// See apiEvaluateVariations handler and input_request_selection.html

				view.SelectionData = selectionData
				view.PendingCount = pendingCount
				view.FailedCount = failedCount
				view.TotalCount = len(variations)
				// Can select if all variations are done (none creating) and at least one is pending
				view.CanSelect = creatingCount == 0 && pendingCount > 0 && inputRequest.Status != domain.InputRequestStatusResolved
			}
		}

	case domain.InputRequestKindHostingPlatform:
		templateName = "input_request_hosting.html"
		// Provide common hosting platform options
		// In the future, AI could suggest based on project context
		view.HostingPlatforms = []HostingPlatformOption{
			{ID: "cloud-run", Name: "Google Cloud Run", Description: "Serverless containers on GCP. Good for projects already using Google Cloud."},
			{ID: "fly-io", Name: "Fly.io", Description: "Deploy containers globally. Simple CLI, generous free tier."},
			{ID: "railway", Name: "Railway", Description: "Deploy from GitHub with zero config. Great for Node.js/Python apps."},
			{ID: "vercel", Name: "Vercel", Description: "Optimized for frontend/Next.js. Automatic previews on PRs."},
			{ID: "render", Name: "Render", Description: "Managed containers and databases. Good all-around choice."},
		}
	}

	data := map[string]interface{}{
		"Title":     "Input: " + inputRequest.Title,
		"ProjectID": projectID,
		"View":      view,
	}
	s.addOpenInputCount(ctx, data)

	if err := renderPage(w, templateName, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid input request ID", http.StatusBadRequest)
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

	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "input request not found", http.StatusNotFound)
		return
	}

	// Save user message
	now := time.Now()
	userMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequestID,
		Role:           "user",
		Content:        feedback,
		CreatedAt:      now,
	}
	if err := s.db.CreateInputRequestMessage(ctx, userMsg); err != nil {
		http.Error(w, "error saving message", http.StatusInternalServerError)
		return
	}

	// Handle based on input request kind
	switch inputRequest.Kind {
	case domain.InputRequestKindRoadmapReview:
		s.sendMessageRoadmap(w, r, inputRequest, feedback)
	case domain.InputRequestKindVariationReview:
		s.sendMessageVariation(w, r, inputRequest, feedback)
	default:
		http.Error(w, "unsupported input request kind for messages", http.StatusBadRequest)
	}
}

func (s *Server) sendMessageRoadmap(w http.ResponseWriter, r *http.Request, inputRequest *domain.InputRequest, feedback string) {
	ctx := r.Context()

	// Parse current roadmap
	var currentRoadmap agent.ProposedRoadmap
	if inputRequest.Details != nil {
		if err := json.Unmarshal([]byte(*inputRequest.Details), &currentRoadmap); err != nil {
			http.Error(w, "error parsing roadmap", http.StatusInternalServerError)
			return
		}
	}

	// Load strategy context
	if inputRequest.SubjectID == nil {
		http.Error(w, "no strategy associated", http.StatusBadRequest)
		return
	}

	strategy, err := s.db.GetStrategy(ctx, *inputRequest.SubjectID)
	if err != nil {
		http.Error(w, "strategy not found", http.StatusNotFound)
		return
	}

	// Includes the budget, spend to date, and this project's observed cost
	// history, so the proposer's estimates are anchored rather than invented.
	strategyContext, err := cost.BuildStrategyContext(ctx, s.db, strategy)
	if err != nil {
		http.Error(w, "error loading strategy context: "+err.Error(), http.StatusInternalServerError)
		return
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

	revisedRoadmap, spend, err := proposer.ReviseRoadmap(ctx, revReq)
	if err != nil {
		http.Error(w, "error revising roadmap: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.recordStrategySpend(ctx, strategy.ID, "roadmap_proposer", spend)
	s.auditRoadmapCosts(ctx, client, inputRequest.ID, strategy.ID, strategyContext, revisedRoadmap)

	// Update input request with new roadmap
	roadmapJSON, _ := json.MarshalIndent(revisedRoadmap, "", "  ")
	roadmapStr := string(roadmapJSON)
	inputRequest.Details = &roadmapStr
	inputRequest.UpdatedAt = time.Now()

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error updating input request", http.StatusInternalServerError)
		return
	}

	// Save agent response message
	agentMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequest.ID,
		Role:           "agent",
		Content:        fmt.Sprintf("Revised roadmap based on feedback. Now has %d hops.", len(revisedRoadmap.Hops)),
		TokensUsed:     tokensUsedPtr(spend),
		CreatedAt:      time.Now(),
	}
	if err := s.db.CreateInputRequestMessage(ctx, agentMsg); err != nil {
		http.Error(w, "error saving agent message", http.StatusInternalServerError)
		return
	}

	// Redirect back to input request page
	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, inputRequest.ID), http.StatusSeeOther)
}

func (s *Server) sendMessageVariation(w http.ResponseWriter, r *http.Request, inputRequest *domain.InputRequest, feedback string) {
	ctx := r.Context()

	if inputRequest.SubjectID == nil {
		http.Error(w, "no hop associated", http.StatusBadRequest)
		return
	}

	// Load hop
	hop, err := s.db.GetHop(ctx, *inputRequest.SubjectID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// Parse current proposal
	var currentProposal agent.VariationProposal
	if inputRequest.Details != nil {
		json.Unmarshal([]byte(*inputRequest.Details), &currentProposal)
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
			EstimatedCostUSD: v.EstimatedCostUSD,
		})
	}

	// Call variation proposer for revision
	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "error creating agent client", http.StatusInternalServerError)
		return
	}

	proposer := agent.NewVariationProposer(client)
	revisedProposal, spend, err := proposer.ReviseVariations(ctx, revisionInput)
	if err != nil {
		http.Error(w, "error revising variations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update input request with new proposal
	proposalJSON, _ := json.MarshalIndent(revisedProposal, "", "  ")
	proposalStr := string(proposalJSON)
	inputRequest.Details = &proposalStr
	inputRequest.UpdatedAt = time.Now()

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error updating input request", http.StatusInternalServerError)
		return
	}

	// Save agent response message
	agentMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequest.ID,
		Role:           "agent",
		Content:        fmt.Sprintf("Revised variations based on feedback. Now has %d variations.", len(revisedProposal.Variations)),
		TokensUsed:     tokensUsedPtr(spend),
		CreatedAt:      time.Now(),
	}
	if err := s.db.CreateInputRequestMessage(ctx, agentMsg); err != nil {
		http.Error(w, "error saving agent message", http.StatusInternalServerError)
		return
	}

	// Redirect back to input request page
	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, inputRequest.ID), http.StatusSeeOther)
}

func (s *Server) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid input request ID", http.StatusBadRequest)
		return
	}

	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "input request not found", http.StatusNotFound)
		return
	}

	// Handle based on input request kind
	switch inputRequest.Kind {
	case domain.InputRequestKindRoadmapReview:
		s.regenerateRoadmap(w, r, inputRequest)
	case domain.InputRequestKindVariationReview:
		s.regenerateVariations(w, r, inputRequest)
	default:
		http.Error(w, "unsupported input request kind for regeneration", http.StatusBadRequest)
	}
}

func (s *Server) regenerateRoadmap(w http.ResponseWriter, r *http.Request, inputRequest *domain.InputRequest) {
	ctx := r.Context()

	if inputRequest.SubjectID == nil {
		http.Error(w, "no strategy associated", http.StatusBadRequest)
		return
	}

	strategy, err := s.db.GetStrategy(ctx, *inputRequest.SubjectID)
	if err != nil {
		http.Error(w, "strategy not found", http.StatusNotFound)
		return
	}

	// Includes the budget, spend to date, and this project's observed cost
	// history, so the proposer's estimates are anchored rather than invented.
	strategyContext, err := cost.BuildStrategyContext(ctx, s.db, strategy)
	if err != nil {
		http.Error(w, "error loading strategy context: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Generate new proposal
	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "error creating agent client", http.StatusInternalServerError)
		return
	}

	proposer := agent.NewProposer(client)
	roadmap, spend, err := proposer.ProposeRoadmap(ctx, strategyContext)
	if err != nil {
		http.Error(w, "error generating roadmap: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.recordStrategySpend(ctx, strategy.ID, "roadmap_proposer", spend)
	s.auditRoadmapCosts(ctx, client, inputRequest.ID, strategy.ID, strategyContext, roadmap)

	// Update input request
	roadmapJSON, _ := json.MarshalIndent(roadmap, "", "  ")
	roadmapStr := string(roadmapJSON)
	inputRequest.Details = &roadmapStr
	inputRequest.UpdatedAt = time.Now()

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error updating input request", http.StatusInternalServerError)
		return
	}

	// Save system message
	sysMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequest.ID,
		Role:           "system",
		Content:        "Roadmap regenerated from scratch.",
		CreatedAt:      time.Now(),
	}
	s.db.CreateInputRequestMessage(ctx, sysMsg)

	// Save agent message
	agentMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequest.ID,
		Role:           "agent",
		Content:    fmt.Sprintf("Generated new roadmap proposal with %d hops.", len(roadmap.Hops)),
		TokensUsed: tokensUsedPtr(spend),
		CreatedAt:  time.Now(),
	}
	s.db.CreateInputRequestMessage(ctx, agentMsg)

	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, inputRequest.ID), http.StatusSeeOther)
}

func (s *Server) regenerateVariations(w http.ResponseWriter, r *http.Request, inputRequest *domain.InputRequest) {
	ctx := r.Context()

	if inputRequest.SubjectID == nil {
		http.Error(w, "no hop associated", http.StatusBadRequest)
		return
	}

	hop, err := s.db.GetHop(ctx, *inputRequest.SubjectID)
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

	// What this Hop has left to spend, and what generation has actually cost on
	// this project, so the proposed approaches are sized against real money.
	availableBudget := s.hopCostView(ctx, hop.ID).RemainingUSD()
	calibration, _ := cost.BuildCalibration(ctx, s.db, strategy.ProjectID)

	// Get completed transitive dependencies for context
	completedDeps, _ := s.db.GetCompletedTransitiveDependencies(ctx, hop.ID)
	var completedDependencies []agent.CompletedDependencyHop
	for _, dep := range completedDeps {
		completedDependencies = append(completedDependencies, agent.CompletedDependencyHop{
			HopName:           dep.HopName,
			HopCommentary:     dep.HopCommentary,
			VariationName:     dep.VariationName,
			VariationApproach: dep.VariationApproach,
		})
	}

	input := agent.VariationProposerInput{
		Hop: agent.HopContext{
			ID:         hop.ID.String(),
			Name:       hop.Name,
			Commentary: hop.Commentary,
			Objectives: objectiveDescs,
		},
		RepositoryURL:         repoURL,
		AvailableBudgetUSD:    availableBudget,
		Calibration:           calibration,
		NumVariations:         2,
		CompletedDependencies: completedDependencies,
	}

	// Generate new proposal
	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "error creating agent client", http.StatusInternalServerError)
		return
	}

	proposer := agent.NewVariationProposer(client)
	proposal, spend, err := proposer.ProposeVariations(ctx, input)
	if err != nil {
		http.Error(w, "error generating variations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	proposalData := *proposal
	proposalData.HopID = hop.ID.String()

	// Update input request
	proposalJSON, _ := json.MarshalIndent(proposalData, "", "  ")
	proposalStr := string(proposalJSON)
	inputRequest.Details = &proposalStr
	inputRequest.UpdatedAt = time.Now()

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error updating input request", http.StatusInternalServerError)
		return
	}

	// Save system message
	sysMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequest.ID,
		Role:           "system",
		Content:        "Variations regenerated from scratch.",
		CreatedAt:      time.Now(),
	}
	s.db.CreateInputRequestMessage(ctx, sysMsg)

	// Save agent message
	agentMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequest.ID,
		Role:           "agent",
		Content:        fmt.Sprintf("Generated new variation proposal with %d variations.\n\nRationale: %s", len(proposal.Variations), proposal.Rationale),
		TokensUsed:     tokensUsedPtr(spend),
		CreatedAt:      time.Now(),
	}
	s.db.CreateInputRequestMessage(ctx, agentMsg)

	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, inputRequest.ID), http.StatusSeeOther)
}

func (s *Server) handleUpdateRoadmap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid input request ID", http.StatusBadRequest)
		return
	}

	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "input request not found", http.StatusNotFound)
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

	// Update input request
	inputRequest.Details = &roadmapJSON
	inputRequest.UpdatedAt = time.Now()

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error updating input request", http.StatusInternalServerError)
		return
	}

	// Save system message
	sysMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequestID,
		Role:           "system",
		Content:        "Roadmap manually edited.",
		CreatedAt:      time.Now(),
	}
	s.db.CreateInputRequestMessage(ctx, sysMsg)

	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, inputRequestID), http.StatusSeeOther)
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid input request ID", http.StatusBadRequest)
		return
	}

	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "input request not found", http.StatusNotFound)
		return
	}

	if inputRequest.SubjectID == nil {
		http.Error(w, "no subject associated", http.StatusBadRequest)
		return
	}

	// Handle based on input request kind
	switch inputRequest.Kind {
	case domain.InputRequestKindRoadmapReview:
		s.approveRoadmap(w, r, inputRequest, projectID)
	case domain.InputRequestKindVariationReview:
		s.approveVariations(w, r, inputRequest, projectID)
	default:
		http.Error(w, "unsupported input request kind", http.StatusBadRequest)
	}
}

func (s *Server) approveRoadmap(w http.ResponseWriter, r *http.Request, inputRequest *domain.InputRequest, projectID string) {
	ctx := r.Context()

	// Parse roadmap
	var roadmap agent.ProposedRoadmap
	if inputRequest.Details == nil {
		http.Error(w, "no roadmap to approve", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal([]byte(*inputRequest.Details), &roadmap); err != nil {
		http.Error(w, "error parsing roadmap", http.StatusInternalServerError)
		return
	}

	// Validate roadmap
	if err := validateRoadmap(&roadmap); err != nil {
		http.Error(w, "invalid roadmap: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Get existing hops to check for terminal ones that must be preserved
	existingHops, _ := s.db.GetHopsByStrategy(ctx, *inputRequest.SubjectID)
	existingHopsByName := make(map[string]*domain.Hop)
	terminalHops := make(map[string]bool)
	for i := range existingHops {
		h := &existingHops[i]
		existingHopsByName[h.Name] = h
		if h.Status == domain.HopStatusCompleted || h.Status == domain.HopStatusRejected || h.Status == domain.HopStatusAbandoned {
			terminalHops[h.Name] = true
		}
	}

	// Validate that all terminal hops are present in the proposal
	proposedNames := make(map[string]bool)
	for _, ph := range roadmap.Hops {
		proposedNames[ph.Name] = true
	}
	for name := range terminalHops {
		if !proposedNames[name] {
			http.Error(w, fmt.Sprintf("Terminal hop '%s' cannot be removed from roadmap", name), http.StatusBadRequest)
			return
		}
	}

	// The auditor's verdicts, carried into hop creation so each Hop lands with
	// both the estimate it was proposed at and the one it was challenged with.
	costAudit := s.loadCostAudit(ctx, inputRequest.ID)

	// Create hops and dependencies
	now := time.Now()
	hopNameToID := make(map[string]uuid.UUID)
	newHopCount := 0

	// First pass: create new hops (skip existing ones)
	for _, ph := range roadmap.Hops {
		if existing, ok := existingHopsByName[ph.Name]; ok {
			// Hop already exists - use its ID
			hopNameToID[ph.Name] = existing.ID
			continue
		}

		// New hop - create it
		hopID := uuid.New()
		hopNameToID[ph.Name] = hopID

		params, _ := json.Marshal(map[string]interface{}{
			"objective_ids": ph.ObjectiveIDs,
		})

		hop := &domain.Hop{
			ID:         hopID,
			StrategyID: *inputRequest.SubjectID,
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
		newHopCount++

		// Create budget allocations
		s.recordHopEstimate(ctx, hopID, *inputRequest.SubjectID, ph, costAudit)
	}

	// Second pass: create dependencies for new hops only
	for _, ph := range roadmap.Hops {
		if _, existed := existingHopsByName[ph.Name]; existed {
			continue // Skip existing hops - their dependencies are already set
		}
		hopID := hopNameToID[ph.Name]
		for _, depName := range ph.DependsOn {
			depID, ok := hopNameToID[depName]
			if !ok {
				continue // Skip invalid dependency
			}
			s.db.CreateHopDependency(ctx, hopID, depID)
		}
	}

	// Activate root hops (those with no dependencies)
	if _, err := s.db.ActivateRootHops(ctx, *inputRequest.SubjectID); err != nil {
		http.Error(w, "error activating root hops: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update input request status
	inputRequest.Status = domain.InputRequestStatusResolved
	resolution := "approved"
	inputRequest.Resolution = &resolution
	resolvedAt := time.Now()
	inputRequest.ResolvedAt = &resolvedAt
	inputRequest.UpdatedAt = resolvedAt

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error updating input request", http.StatusInternalServerError)
		return
	}

	// Save system message
	var msgContent string
	if newHopCount == len(roadmap.Hops) {
		msgContent = fmt.Sprintf("Roadmap approved. Created %d hops.", newHopCount)
	} else {
		msgContent = fmt.Sprintf("Roadmap approved. Created %d new hops (%d existing preserved).", newHopCount, len(existingHops))
	}
	sysMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequest.ID,
		Role:           "system",
		Content:        msgContent,
		CreatedAt:      time.Now(),
	}
	s.db.CreateInputRequestMessage(ctx, sysMsg)

	// Redirect to strategy page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/strategy", projectID), http.StatusSeeOther)
}

func (s *Server) approveVariations(w http.ResponseWriter, r *http.Request, inputRequest *domain.InputRequest, projectID string) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	if inputRequest.Details == nil {
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
	var proposal agent.VariationProposal
	if err := json.Unmarshal([]byte(*inputRequest.Details), &proposal); err != nil {
		http.Error(w, "error parsing variations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Filter to only selected variations
	var selectedVariations []agent.ProposedVariation
	var selectedNames []string
	for i, v := range proposal.Variations {
		if selectedIndices[i] {
			selectedVariations = append(selectedVariations, v)
			selectedNames = append(selectedNames, v.Name)
		}
	}

	// Get hop and repository info
	hop, err := s.db.GetHop(ctx, *inputRequest.SubjectID)
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

	// Update hop comparison requirements from form
	requiresDemo := r.FormValue("requires_demo") == "on"
	requiresProduction := r.FormValue("requires_production") == "on"
	if err := s.db.UpdateHopComparisonRequirements(ctx, hop.ID, requiresDemo, requiresProduction); err != nil {
		http.Error(w, "error updating hop requirements", http.StatusInternalServerError)
		return
	}

	// Update hop status to active
	if err := s.db.UpdateHopStatus(ctx, hop.ID, domain.HopStatusActive); err != nil {
		http.Error(w, "error updating hop status", http.StatusInternalServerError)
		return
	}

	// Update input request status
	inputRequest.Status = domain.InputRequestStatusResolved
	resolution := "approved"
	inputRequest.Resolution = &resolution
	resolvedAt := time.Now()
	inputRequest.ResolvedAt = &resolvedAt
	inputRequest.UpdatedAt = resolvedAt

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error updating input request", http.StatusInternalServerError)
		return
	}

	// Save system message about approval
	var msgContent string
	if createdCount > 0 {
		msgContent = fmt.Sprintf("Approved and created %d new variation(s). Code generation will start automatically.", createdCount)
	} else {
		msgContent = "Approved. No new variations to create (selected variations already exist)."
	}
	sysMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequest.ID,
		Role:           "system",
		Content:        msgContent,
		CreatedAt:      time.Now(),
	}
	s.db.CreateInputRequestMessage(ctx, sysMsg)

	// Background worker will pick up variations in "creating" status

	// Redirect to the hop detail page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/hops/%s", projectID, inputRequest.SubjectID.String()), http.StatusSeeOther)
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

	// Includes the budget, spend to date, and this project's observed cost
	// history, so the proposer's estimates are anchored rather than invented.
	strategyContext, err := cost.BuildStrategyContext(ctx, s.db, &strategy)
	if err != nil {
		http.Error(w, "error loading strategy context: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Check for existing hops
	existingHops, _ := s.db.GetHopsByStrategy(ctx, strategy.ID)

	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "error creating agent client: "+err.Error(), http.StatusInternalServerError)
		return
	}

	proposer := agent.NewProposer(client)
	var roadmap *agent.ProposedRoadmap
	var spend agent.Spend

	if len(existingHops) > 0 {
		// Build existing hop info with terminal status
		var existingHopInfos []agent.ExistingHop
		var currentHops []agent.ProposedHop
		for _, h := range existingHops {
			isTerminal := h.Status == domain.HopStatusCompleted ||
				h.Status == domain.HopStatusRejected ||
				h.Status == domain.HopStatusAbandoned

			existingHopInfos = append(existingHopInfos, agent.ExistingHop{
				Name:       h.Name,
				Commentary: h.Commentary,
				Status:     string(h.Status),
				IsTerminal: isTerminal,
			})

			// Build current roadmap from existing hops
			currentHops = append(currentHops, agent.ProposedHop{
				Name:       h.Name,
				Commentary: h.Commentary,
			})
		}

		// Use revision flow with existing hops context
		revReq := agent.RevisionRequest{
			CurrentRoadmap: agent.ProposedRoadmap{Hops: currentHops},
			ExistingHops:   existingHopInfos,
			Feedback:       "Propose improvements or additions to the roadmap while preserving all terminal (completed/rejected/abandoned) hops exactly as they are.",
			Strategy:       strategyContext,
		}

		roadmap, spend, err = proposer.ReviseRoadmap(ctx, revReq)
		if err != nil {
			http.Error(w, "error revising roadmap: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// No existing hops - generate from scratch
		roadmap, spend, err = proposer.ProposeRoadmap(ctx, strategyContext)
		if err != nil {
			http.Error(w, "error generating roadmap: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Create input request
	now := time.Now()
	roadmapJSON, _ := json.MarshalIndent(roadmap, "", "  ")
	roadmapStr := string(roadmapJSON)

	inputRequest := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        strategy.ProjectID,
		Kind:             domain.InputRequestKindRoadmapReview,
		Title:            fmt.Sprintf("Roadmap Review: %s", strategy.Name),
		Details:          &roadmapStr,
		ObjectivityScore: 0.3,
		ImportanceScore:  0.8,
		Status:           domain.InputRequestStatusNeedsAssignment,
		SubjectType:      strPtr("strategy"),
		SubjectID:        &strategy.ID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := s.db.CreateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error creating input request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.recordStrategySpend(ctx, strategy.ID, "roadmap_proposer", spend)
	s.auditRoadmapCosts(ctx, client, inputRequest.ID, strategy.ID, strategyContext, roadmap)

	// Create agent message
	tokensUsed := spend.Tokens.Total()
	msgContent := fmt.Sprintf("Generated roadmap proposal with %d hops.", len(roadmap.Hops))
	if len(existingHops) > 0 {
		terminalCount := 0
		for _, h := range existingHops {
			if h.Status == domain.HopStatusCompleted || h.Status == domain.HopStatusRejected || h.Status == domain.HopStatusAbandoned {
				terminalCount++
			}
		}
		if terminalCount > 0 {
			msgContent = fmt.Sprintf("Revised roadmap proposal with %d hops (%d terminal hops preserved).", len(roadmap.Hops), terminalCount)
		}
	}
	agentMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequest.ID,
		Role:           "agent",
		Content:        msgContent,
		TokensUsed:     &tokensUsed,
		CreatedAt:  now,
	}
	s.db.CreateInputRequestMessage(ctx, agentMsg)

	// Redirect to input request page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, inputRequest.ID), http.StatusSeeOther)
}

func strPtr(s string) *string {
	return &s
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid input request ID", http.StatusBadRequest)
		return
	}

	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "input request not found", http.StatusNotFound)
		return
	}

	// Update input request status
	inputRequest.Status = domain.InputRequestStatusResolved
	resolution := "rejected"
	inputRequest.Resolution = &resolution
	resolvedAt := time.Now()
	inputRequest.ResolvedAt = &resolvedAt
	inputRequest.UpdatedAt = resolvedAt

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error updating input request", http.StatusInternalServerError)
		return
	}

	// Save system message
	sysMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequestID,
		Role:           "system",
		Content:        "Roadmap proposal rejected.",
		CreatedAt:      time.Now(),
	}
	s.db.CreateInputRequestMessage(ctx, sysMsg)

	// Redirect to input requests list
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs", projectID), http.StatusSeeOther)
}

func (s *Server) handleSelectWinner(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid input request ID", http.StatusBadRequest)
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

	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "input request not found", http.StatusNotFound)
		return
	}

	if inputRequest.SubjectID == nil {
		http.Error(w, "no hop associated", http.StatusBadRequest)
		return
	}

	// Get hop
	hop, err := s.db.GetHop(ctx, *inputRequest.SubjectID)
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

	// Update variation statuses, revert migrations, and cleanup work directories
	for _, v := range variations {
		if v.ID == winnerID {
			v.Status = domain.VariationStatusMerged
		} else if v.Status == domain.VariationStatusPending {
			v.Status = domain.VariationStatusRejected
		}
		// Leave error/terminated variations as-is
		s.db.UpdateVariation(ctx, &v)

		// Revert migration for ALL variations (winner too - real migration is in merged code)
		if err := s.revertVariationMigration(ctx, projectID, v.ID); err != nil {
			fmt.Printf("[selection] Warning: failed to revert migration for variation %s: %v\n", v.ID, err)
		}

		// Cleanup work directory for resolved variations
		if v.Status == domain.VariationStatusMerged || v.Status == domain.VariationStatusRejected {
			if err := s.cleanupVariationWorkDir(projectID, v.ID); err != nil {
				fmt.Printf("[selection] Warning: failed to cleanup work dir for variation %s: %v\n", v.ID, err)
			}
		}
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

	// Update input request status
	inputRequest.Status = domain.InputRequestStatusResolved
	resolution := "approved"
	inputRequest.Resolution = &resolution
	resolvedAt := time.Now()
	inputRequest.ResolvedAt = &resolvedAt
	inputRequest.UpdatedAt = resolvedAt

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error updating input request", http.StatusInternalServerError)
		return
	}

	// Save system message with migration reminder if applicable
	msgContent := fmt.Sprintf("Winner selected: %s\nBranch merged to main.", winner.Name)

	// Check if winner had a migration - remind user to run it
	winnerMigration, _ := s.db.GetVariationMigration(ctx, winnerID)
	if winnerMigration != nil {
		msgContent += "\n\nNote: This variation included database migrations. The temporary demo migration has been reverted. Please run your project's migration command to apply the permanent schema changes from the merged code."
	}

	sysMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequestID,
		Role:           "system",
		Content:        msgContent,
		CreatedAt:      time.Now(),
	}
	s.db.CreateInputRequestMessage(ctx, sysMsg)

	// Redirect to strategy page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/strategy", projectID), http.StatusSeeOther)
}

func (s *Server) handleRejectAllVariations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid decision ID", http.StatusBadRequest)
		return
	}

	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "decision not found", http.StatusNotFound)
		return
	}

	if inputRequest.SubjectID == nil {
		http.Error(w, "no hop associated", http.StatusBadRequest)
		return
	}

	// Get hop
	hop, err := s.db.GetHop(ctx, *inputRequest.SubjectID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// NOTE: We do NOT reject existing variations - they stay pending so user can
	// compare them against new variations in the variation review

	// Update input request status
	inputRequest.Status = domain.InputRequestStatusResolved
	resolution := "requested_more"
	inputRequest.Resolution = &resolution
	resolvedAt := time.Now()
	inputRequest.ResolvedAt = &resolvedAt
	inputRequest.UpdatedAt = resolvedAt

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error updating input request", http.StatusInternalServerError)
		return
	}

	// Save system message
	sysMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequestID,
		Role:           "system",
		Content:        "Requested additional variations. Returning to variation review.",
		CreatedAt:      time.Now(),
	}
	s.db.CreateInputRequestMessage(ctx, sysMsg)

	// Create a new VariationReview input request so user can request more variations
	s.createMoreVariationsInputRequest(ctx, w, r, inputRequest, hop, projectID)
}

// handleRequestMoreVariations handles requesting additional variations from selection page.
func (s *Server) handleRequestMoreVariations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid input request ID", http.StatusBadRequest)
		return
	}

	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "input request not found", http.StatusNotFound)
		return
	}

	if inputRequest.SubjectID == nil {
		http.Error(w, "no hop associated", http.StatusBadRequest)
		return
	}

	hop, err := s.db.GetHop(ctx, *inputRequest.SubjectID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// Resolve current selection input request as "requested_more"
	inputRequest.Status = domain.InputRequestStatusResolved
	resolution := "requested_more"
	inputRequest.Resolution = &resolution
	resolvedAt := time.Now()
	inputRequest.ResolvedAt = &resolvedAt
	inputRequest.UpdatedAt = resolvedAt

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "error updating input request", http.StatusInternalServerError)
		return
	}

	// Save system message
	sysMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequestID,
		Role:           "system",
		Content:        "Requested additional variations. Returning to variation review.",
		CreatedAt:      time.Now(),
	}
	s.db.CreateInputRequestMessage(ctx, sysMsg)

	// Create a new VariationReview input request
	s.createMoreVariationsInputRequest(ctx, w, r, inputRequest, hop, projectID)
}

// createMoreVariationsInputRequest creates a new VariationReview input request for proposing more variations.
func (s *Server) createMoreVariationsInputRequest(ctx context.Context, w http.ResponseWriter, r *http.Request, oldInputRequest *domain.InputRequest, hop *domain.Hop, projectID string) {
	// Get strategy for project ID
	strategy, err := s.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		http.Error(w, "error getting strategy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	now := time.Now()

	// Create empty proposal - user will request new variations via feedback
	proposalData := agent.VariationProposal{HopID: hop.ID.String()}
	proposalJSON, _ := json.MarshalIndent(proposalData, "", "  ")
	proposalStr := string(proposalJSON)

	newInputRequest := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        strategy.ProjectID,
		Kind:             domain.InputRequestKindVariationReview,
		Title:            fmt.Sprintf("Variation Review: %s (additional)", hop.Name),
		Details:          &proposalStr,
		ObjectivityScore: 0.5,
		ImportanceScore:  0.7,
		Status:           domain.InputRequestStatusNeedsAssignment,
		SubjectType:      strPtr("hop"),
		SubjectID:        &hop.ID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := s.db.CreateInputRequest(ctx, newInputRequest); err != nil {
		http.Error(w, "error creating input request: "+err.Error(), http.StatusInternalServerError)
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
	sysMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: newInputRequest.ID,
		Role:           "system",
		Content:        fmt.Sprintf("Variation review opened for additional proposals.\n\nThere are %d existing pending variation(s) that will be retained. Use the feedback form to request new variations to compare against them.", pendingCount),
		CreatedAt:      now,
	}
	s.db.CreateInputRequestMessage(ctx, sysMsg)

	// Redirect to the new input request page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, newInputRequest.ID), http.StatusSeeOther)
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

	// Clone repository to a temporary merge directory
	workDir := git.WorkDirForVariation(strategy.ProjectID.String(), "merge-"+winner.ID.String())
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
// Scores are cached per-variation to avoid re-evaluating when new variations complete.
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

	// Split pending variations into cached vs needing evaluation
	var cachedEvaluations []agent.VariationEvaluation
	var needsEvaluation []agent.VariationForEvaluation

	for _, v := range variations {
		if v.Status != domain.VariationStatusPending {
			continue
		}

		if len(v.EvaluationScores) > 0 {
			// Has cached scores - parse and use
			var scores []agent.VariationScore
			if err := json.Unmarshal(v.EvaluationScores, &scores); err == nil {
				cachedEvaluations = append(cachedEvaluations, agent.VariationEvaluation{
					VariationID: v.ID.String(),
					Scores:      scores,
				})
				continue
			}
		}

		// Needs evaluation
		needsEvaluation = append(needsEvaluation, agent.VariationForEvaluation{
			ID:       v.ID.String(),
			Name:     v.Name,
			Approach: v.Approach,
		})
	}

	// If everything is cached, return immediately
	if len(needsEvaluation) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"evaluations": cachedEvaluations,
			"summary":     "",
		})
		return
	}

	// Call LLM for variations needing evaluation
	evalInput := agent.VariationEvaluationInput{
		HopName:    hop.Name,
		Criteria:   criteria.Criteria,
		Variations: needsEvaluation,
	}

	client, err := agent.NewClient("")
	if err != nil {
		http.Error(w, "failed to create agent client", http.StatusInternalServerError)
		return
	}

	evaluator := agent.NewVariationEvaluator(client)
	evalResult, spend, err := evaluator.Evaluate(ctx, evalInput)
	if err != nil {
		http.Error(w, "evaluation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.recordHopSpend(ctx, hopID, "variation_evaluator", spend)

	// Cache new scores on each variation
	for _, eval := range evalResult.Evaluations {
		varID, err := uuid.Parse(eval.VariationID)
		if err != nil {
			continue
		}
		scoresJSON, err := json.Marshal(eval.Scores)
		if err != nil {
			continue
		}
		s.db.UpdateVariationEvaluationScores(ctx, varID, scoresJSON)
	}

	// Combine cached + new evaluations
	allEvaluations := append(cachedEvaluations, evalResult.Evaluations...)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"evaluations": allEvaluations,
		"summary":     evalResult.Summary,
	})
}

// handleProvideCredential handles providing a credential inline from an InputRequest page.
func (s *Server) handleProvideCredential(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid decision ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	name := r.FormValue("credential_name")
	value := r.FormValue("credential_value")

	if name == "" {
		http.Error(w, "credential name is required", http.StatusBadRequest)
		return
	}

	// Get the inputRequest
	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "decision not found", http.StatusNotFound)
		return
	}

	// Encrypt and save the credential
	key, err := crypto.GetKey()
	if err != nil {
		http.Error(w, "encryption not configured: "+err.Error(), http.StatusInternalServerError)
		return
	}

	encryptedValue, err := crypto.Encrypt([]byte(value), key)
	if err != nil {
		http.Error(w, "failed to encrypt credential: "+err.Error(), http.StatusInternalServerError)
		return
	}

	cred := &domain.ProjectCredential{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Name:           name,
		EncryptedValue: encryptedValue,
	}

	if err := s.db.CreateProjectCredential(ctx, cred); err != nil {
		http.Error(w, "failed to save credential: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Resolve the input request
	inputRequest.Status = domain.InputRequestStatusResolved
	resolution := "credential_provided"
	inputRequest.Resolution = &resolution
	now := time.Now()
	inputRequest.ResolvedAt = &now
	inputRequest.UpdatedAt = now

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "failed to update input request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Auto-resolve any other credential requests for this credential name
	_ = s.db.ResolveCredentialRequestsByName(ctx, projectID, name)

	// Save system message
	sysMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequestID,
		Role:           "system",
		Content:        fmt.Sprintf("Credential '%s' provided. Blocked workflows will resume.", name),
		CreatedAt:      now,
	}
	s.db.CreateInputRequestMessage(ctx, sysMsg)

	// Redirect back to the input request page (now resolved)
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, inputRequestID), http.StatusSeeOther)
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

// constructGitHubDiffURL constructs a GitHub compare URL (main...branch).
func constructGitHubDiffURL(repoURL, mainBranch, branchName string) string {
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

	return url + "/compare/" + mainBranch + "..." + branchName
}

// handlePruneVariation marks a variation as pruned, removing it from selection consideration.
func (s *Server) handlePruneVariation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	// Get the variation
	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	// Only allow pruning pending variations
	if variation.Status != domain.VariationStatusPending {
		http.Error(w, "can only prune pending variations", http.StatusBadRequest)
		return
	}

	// Update status to pruned
	variation.Status = domain.VariationStatusPruned
	variation.UpdatedAt = time.Now()
	if err := s.db.UpdateVariation(ctx, variation); err != nil {
		http.Error(w, "failed to update variation", http.StatusInternalServerError)
		return
	}

	// Record state transition
	s.db.CreateVariationStateTransition(ctx, variationID, string(domain.VariationStatusPending), string(domain.VariationStatusPruned), "pruned by user to resolve migration conflict")

	// Cleanup work directory for pruned variation
	if err := s.cleanupVariationWorkDir(projectID, variationID); err != nil {
		fmt.Printf("[prune] Warning: failed to cleanup work dir for variation %s: %v\n", variationID, err)
	}

	// Redirect back to the referring decision page
	referer := r.Header.Get("Referer")
	if referer != "" {
		http.Redirect(w, r, referer, http.StatusSeeOther)
	} else {
		// Fallback: redirect to hop page
		http.Redirect(w, r, fmt.Sprintf("/p/%s/hops/%s", projectID, variation.HopID), http.StatusSeeOther)
	}
}

// handleResolveConflicts re-runs the conflict audit after user has pruned variations.
// If conflicts are resolved, marks the decision as resolved and creates a selection inputRequest.
func (s *Server) handleResolveConflicts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid decision ID", http.StatusBadRequest)
		return
	}

	// Get the inputRequest
	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "decision not found", http.StatusNotFound)
		return
	}

	// Must be a variation_review input request
	if inputRequest.Kind != domain.InputRequestKindVariationReview {
		http.Error(w, "invalid input request kind", http.StatusBadRequest)
		return
	}

	// Get the hop
	if inputRequest.SubjectID == nil {
		http.Error(w, "input request has no subject", http.StatusBadRequest)
		return
	}
	hop, err := s.db.GetHop(ctx, *inputRequest.SubjectID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// Get remaining pending variations
	variations, err := s.db.GetVariationsByHop(ctx, hop.ID)
	if err != nil {
		http.Error(w, "failed to get variations", http.StatusInternalServerError)
		return
	}

	// Filter to only pending variations
	var pendingVariations []domain.Variation
	for _, v := range variations {
		if v.Status == domain.VariationStatusPending {
			pendingVariations = append(pendingVariations, v)
		}
	}

	// Get migrations for remaining variations
	migrations, err := s.db.GetVariationMigrationsForHop(ctx, hop.ID)
	if err != nil {
		http.Error(w, "failed to get migrations", http.StatusInternalServerError)
		return
	}

	// Filter migrations to only include pending variations
	filteredMigrations := make(map[string]*domain.VariationMigration)
	for _, v := range pendingVariations {
		if m, ok := migrations[v.ID.String()]; ok {
			filteredMigrations[v.ID.String()] = m
		}
	}

	// Re-run conflict audit
	if len(filteredMigrations) > 0 {
		auditInput := agent.BuildConflictAuditInput(hop, pendingVariations, filteredMigrations)

		client, err := agent.NewClient("")
		if err != nil {
			http.Error(w, "failed to create agent client", http.StatusInternalServerError)
			return
		}

		auditResult, err := client.AuditHopVariationConflicts(ctx, auditInput)
		if err != nil {
			// Log but don't block
			fmt.Printf("[resolve-conflicts] Warning: audit failed: %v\n", err)
		} else if auditResult.HasConflicts {
			// Still have conflicts - update the decision details and show again
			type conflictInfo struct {
				VariationNames []string `json:"variation_names"`
				ConflictType   string   `json:"conflict_type"`
				Description    string   `json:"description"`
				AffectedSchema string   `json:"affected_schema"`
			}
			var conflicts []conflictInfo
			for _, c := range auditResult.Conflicts {
				conflicts = append(conflicts, conflictInfo{
					VariationNames: c.VariationNames,
					ConflictType:   c.ConflictType,
					Description:    c.Description,
					AffectedSchema: c.AffectedSchema,
				})
			}
			details := struct {
				HopID     string         `json:"hop_id"`
				HopName   string         `json:"hop_name"`
				Summary   string         `json:"summary"`
				Conflicts []conflictInfo `json:"conflicts"`
			}{
				HopID:     hop.ID.String(),
				HopName:   hop.Name,
				Summary:   auditResult.Summary,
				Conflicts: conflicts,
			}
			detailsJSON, _ := json.MarshalIndent(details, "", "  ")
			detailsStr := string(detailsJSON)
			inputRequest.Details = &detailsStr
			inputRequest.UpdatedAt = time.Now()
			s.db.UpdateInputRequest(ctx, inputRequest)

			// Redirect back to decision with updated conflicts
			http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, inputRequestID), http.StatusSeeOther)
			return
		}
	}

	// No conflicts! Resolve this input request and create selection input request
	resolution := "conflicts_resolved"
	inputRequest.Status = domain.InputRequestStatusResolved
	inputRequest.Resolution = &resolution
	now := time.Now()
	inputRequest.ResolvedAt = &now
	inputRequest.UpdatedAt = now
	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "failed to update input request", http.StatusInternalServerError)
		return
	}

	// Create selection input request
	if err := s.createSelectionInputRequestInternal(ctx, hop, pendingVariations); err != nil {
		http.Error(w, "failed to create selection input request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to hop page where they'll see the new selection input request
	http.Redirect(w, r, fmt.Sprintf("/p/%s/hops/%s", projectID, hop.ID), http.StatusSeeOther)
}
