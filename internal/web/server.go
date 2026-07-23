package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/codegen"
	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/demo"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// Server is the HTTP server for the MendelBuild webapp.
type Server struct {
	db                  *db.DB
	addr                string
	router              chi.Router
	orchestrator        *codegen.Orchestrator
	stopWorker          chan struct{}
	processingHops      map[uuid.UUID]bool // tracks hops currently being processed
	processingHopsMutex sync.Mutex
}

// NewServer creates a new Server.
func NewServer(database *db.DB, addr string) *Server {
	s := &Server{
		db:             database,
		addr:           addr,
		orchestrator:   codegen.NewOrchestrator(database, codegen.DefaultConcurrency),
		stopWorker:     make(chan struct{}),
		processingHops: make(map[uuid.UUID]bool),
	}
	s.setupRoutes()
	s.cleanupStaleDemos()
	s.startVariationWorker()
	return s
}

// cleanupStaleDemos marks any demos that were "running" or "starting" before
// a restart as stopped. This handles the case where the server restarts and
// Docker containers are no longer running.
func (s *Server) cleanupStaleDemos() {
	ctx := context.Background()

	demos, err := s.db.GetAllRunningDemos(ctx)
	if err != nil {
		fmt.Printf("[startup] Warning: could not check for stale demos: %v\n", err)
		return
	}

	if len(demos) == 0 {
		return
	}

	fmt.Printf("[startup] Checking %d demos marked as running/starting...\n", len(demos))

	for _, d := range demos {
		// Extract work_dir from process info
		var processInfo map[string]interface{}
		if d.ProcessInfo != nil {
			json.Unmarshal(d.ProcessInfo, &processInfo)
		}
		workDir, _ := processInfo["work_dir"].(string)
		if workDir == "" {
			workDir = git.WorkDirForVariation("unknown", d.VariationID.String())
		}

		// Check if Docker containers are actually running
		if demo.IsComposeRunning(workDir) {
			fmt.Printf("[startup] Demo %s is still running\n", d.ID)
			continue
		}

		// Not running - mark as stopped
		fmt.Printf("[startup] Marking stale demo %s as stopped\n", d.ID)
		s.db.UpdateDemoInstanceStatus(ctx, d.ID, domain.DemoInstanceStatusStopped, nil)
	}
}

// startVariationWorker starts a background goroutine that polls for
// variations in "creating" status and runs code generation for them.
// Also handles creating selection Decisions and updating hop statuses.
func (s *Server) startVariationWorker() {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-s.stopWorker:
				return
			case <-ticker.C:
				s.processVariationProposals()
				s.processCreatingVariations()
				s.processSelectionDecisions()
				s.processHopStatusUpdates()
			}
		}
	}()
}

// processCreatingVariations finds hops with variations in "creating" status
// and triggers code generation for them.
func (s *Server) processCreatingVariations() {
	ctx := context.Background()

	// Find all hops that have variations in "creating" status
	hops, err := s.db.GetHopsWithCreatingVariations(ctx)
	if err != nil {
		fmt.Printf("[worker] Error finding hops with creating variations: %v\n", err)
		return
	}

	for _, hop := range hops {
		// Process hops that are active OR selecting (for retries of failed variations)
		if hop.Status != domain.HopStatusActive && hop.Status != domain.HopStatusSelecting {
			continue
		}

		// Check if this hop is already being processed
		s.processingHopsMutex.Lock()
		if s.processingHops[hop.ID] {
			s.processingHopsMutex.Unlock()
			continue // Skip - already being processed
		}
		s.processingHops[hop.ID] = true
		s.processingHopsMutex.Unlock()

		// Run in goroutine so we don't block the worker
		go func(h domain.Hop) {
			defer func() {
				s.processingHopsMutex.Lock()
				delete(s.processingHops, h.ID)
				s.processingHopsMutex.Unlock()
			}()

			fmt.Printf("[worker] Starting code generation for hop '%s'\n", h.Name)
			result, err := s.orchestrator.RunForExistingVariations(ctx, h.ID)
			if err != nil {
				fmt.Printf("[worker] Error generating variations for hop '%s': %v\n", h.Name, err)
				return
			}
			fmt.Printf("[worker] Completed code generation for hop '%s': %d succeeded, %d failed\n",
				h.Name, result.SuccessCount, result.FailureCount)
		}(hop)
	}
}

// processSelectionDecisions creates variation_selection Decisions for hops
// that have at least one pending variation but no selection Decision yet.
func (s *Server) processSelectionDecisions() {
	ctx := context.Background()

	hops, err := s.db.GetHopsNeedingSelectionDecision(ctx)
	if err != nil {
		fmt.Printf("[worker] Error finding hops needing selection decision: %v\n", err)
		return
	}

	for _, hop := range hops {
		if err := s.createSelectionDecision(ctx, &hop); err != nil {
			fmt.Printf("[worker] Error creating selection decision for hop %s: %v\n", hop.ID, err)
		} else {
			fmt.Printf("[worker] Created variation_selection decision for hop '%s'\n", hop.Name)
		}
	}
}

// processHopStatusUpdates updates hop status to 'selecting' when all variations are done.
func (s *Server) processHopStatusUpdates() {
	ctx := context.Background()

	hops, err := s.db.GetHopsReadyForSelection(ctx)
	if err != nil {
		fmt.Printf("[worker] Error finding hops ready for selection: %v\n", err)
		return
	}

	for _, hop := range hops {
		if err := s.db.UpdateHopStatus(ctx, hop.ID, domain.HopStatusSelecting); err != nil {
			fmt.Printf("[worker] Error updating hop %s status: %v\n", hop.Name, err)
		} else {
			fmt.Printf("[worker] Updated hop '%s' status to 'selecting'\n", hop.Name)
		}
	}
}

// processVariationProposals automatically proposes variations for active hops
// that don't have any variations or variation_review Decisions yet.
func (s *Server) processVariationProposals() {
	ctx := context.Background()

	hops, err := s.db.GetHopsNeedingVariationProposal(ctx)
	if err != nil {
		fmt.Printf("[worker] Error finding hops needing variation proposal: %v\n", err)
		return
	}

	for _, hop := range hops {
		fmt.Printf("[worker] Proposing variations for hop '%s'\n", hop.Name)
		if err := s.proposeVariationsForHop(ctx, &hop); err != nil {
			fmt.Printf("[worker] Error proposing variations for hop '%s': %v\n", hop.Name, err)
		} else {
			fmt.Printf("[worker] Created variation_review decision for hop '%s'\n", hop.Name)
		}
	}
}

// proposeVariationsForHop runs the variation proposer agent and creates a variation_review Decision.
func (s *Server) proposeVariationsForHop(ctx context.Context, hop *domain.Hop) error {
	strategy, err := s.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		return fmt.Errorf("get strategy: %w", err)
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
		return fmt.Errorf("get repository: %w", err)
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
		return fmt.Errorf("create agent client: %w", err)
	}

	proposer := agent.NewVariationProposer(client)
	proposal, tokens, err := proposer.ProposeVariations(ctx, input)
	if err != nil {
		return fmt.Errorf("propose variations: %w", err)
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
				if err := s.db.UpdateHopEvaluationCriteria(ctx, hop.ID, criteriaJSON); err != nil {
					fmt.Printf("[worker] Warning: failed to save evaluation criteria: %v\n", err)
				} else {
					// Invalidate cached evaluation scores since criteria changed
					s.db.ClearDecisionCacheBySubject(ctx, "hop", hop.ID)
				}
			}
		}
	}

	// Convert to VariationProposalData for storage
	proposalData := codegen.VariationProposalData{
		HopID: hop.ID,
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
		SubjectID:        &hop.ID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := s.db.CreateDecision(ctx, decision); err != nil {
		return fmt.Errorf("create decision: %w", err)
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

	return nil
}

// createSelectionDecision creates a variation_selection Decision for a hop.
// Before creating the selection decision, it runs a conflict audit on all
// variation migrations. If conflicts are detected, it creates a variation_review
// decision instead to let the user address the conflicts.
func (s *Server) createSelectionDecision(ctx context.Context, hop *domain.Hop) error {
	// Get all variations for this hop
	variations, err := s.db.GetVariationsByHop(ctx, hop.ID)
	if err != nil {
		return fmt.Errorf("get variations: %w", err)
	}

	// Get all migrations for this hop
	migrations, err := s.db.GetVariationMigrationsForHop(ctx, hop.ID)
	if err != nil {
		return fmt.Errorf("get migrations: %w", err)
	}

	// Run conflict audit if there are any migrations
	if len(migrations) > 0 {
		auditInput := agent.BuildConflictAuditInput(hop, variations, migrations)

		client, err := agent.NewClient("")
		if err != nil {
			return fmt.Errorf("create agent client: %w", err)
		}

		auditResult, err := client.AuditHopVariationConflicts(ctx, auditInput)
		if err != nil {
			// Log error but don't block - we can proceed without the audit
			fmt.Printf("[worker] Warning: conflict audit failed for hop %s: %v\n", hop.Name, err)
		} else if auditResult.HasConflicts {
			// Conflicts detected - create a variation_review decision instead
			return s.createConflictReviewDecision(ctx, hop, variations, auditResult)
		}
	}

	// No conflicts - create the selection decision
	return s.createSelectionDecisionInternal(ctx, hop, variations)
}

// createConflictReviewDecision creates a variation_review decision when
// migration conflicts are detected between variations.
func (s *Server) createConflictReviewDecision(ctx context.Context, hop *domain.Hop, variations []domain.Variation, auditResult *agent.ConflictAuditResponse) error {
	// Build conflict details for the decision
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

	decision := &domain.Decision{
		ID:               uuid.New(),
		Kind:             domain.DecisionKindVariationReview,
		Title:            fmt.Sprintf("Migration Conflicts: %s", hop.Name),
		Details:          &detailsStr,
		ObjectivityScore: 0.3, // Requires human judgment to resolve conflicts
		ImportanceScore:  0.9, // Very important - blocking selection
		Status:           domain.DecisionStatusNeedsAssignment,
		SubjectType:      strPtr("hop"),
		SubjectID:        &hop.ID,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	fmt.Printf("[worker] Migration conflicts detected for hop '%s': %s\n", hop.Name, auditResult.Summary)

	return s.db.CreateDecision(ctx, decision)
}

// createSelectionDecisionInternal creates the actual selection decision.
func (s *Server) createSelectionDecisionInternal(ctx context.Context, hop *domain.Hop, variations []domain.Variation) error {
	// Build details JSON with variation info
	type variationInfo struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Approach  string `json:"approach"`
		Status    string `json:"status"`
		CommitRef string `json:"commit_ref,omitempty"`
	}

	var varInfos []variationInfo
	for _, v := range variations {
		vi := variationInfo{
			ID:       v.ID.String(),
			Name:     v.Name,
			Approach: v.Approach,
			Status:   string(v.Status),
		}
		if v.CommitRef != nil {
			vi.CommitRef = *v.CommitRef
		}
		varInfos = append(varInfos, vi)
	}

	details := struct {
		HopID              string          `json:"hop_id"`
		HopName            string          `json:"hop_name"`
		EvaluationCriteria string          `json:"evaluation_criteria,omitempty"`
		Variations         []variationInfo `json:"variations"`
	}{
		HopID:      hop.ID.String(),
		HopName:    hop.Name,
		Variations: varInfos,
	}
	if len(hop.EvaluationCriteria) > 0 {
		var criteria agent.EvaluationCriteria
		if err := json.Unmarshal(hop.EvaluationCriteria, &criteria); err == nil {
			details.EvaluationCriteria = agent.FormatCriteriaAsText(&criteria)
		}
	}

	detailsJSON, _ := json.MarshalIndent(details, "", "  ")
	detailsStr := string(detailsJSON)

	decision := &domain.Decision{
		ID:               uuid.New(),
		Kind:             domain.DecisionKindVariationSelection,
		Title:            fmt.Sprintf("Select Variation: %s", hop.Name),
		Details:          &detailsStr,
		ObjectivityScore: 0.4, // Partially objective (some criteria are measurable)
		ImportanceScore:  0.7, // Important - affects what gets merged
		Status:           domain.DecisionStatusNeedsAssignment,
		SubjectType:      strPtr("hop"),
		SubjectID:        &hop.ID,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	return s.db.CreateDecision(ctx, decision)
}

func (s *Server) setupRoutes() {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Static files (embedded)
	staticSubFS, _ := fs.Sub(staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSubFS))))

	// Global pages
	r.Get("/", s.handleDashboard)

	// Project-scoped pages
	r.Route("/p/{projectID}", func(r chi.Router) {
		r.Get("/", s.handleProjectDashboard)
		r.Get("/strategy", s.handleStrategy)
		r.Get("/roadmap", s.handleRoadmap)
		r.Get("/settings", s.handleProjectSettings)
		r.Post("/settings", s.handleSaveProjectSettings)

		// OKR Editor routes
		r.Get("/okr", s.handleOKREditor)
		r.Get("/okr/objectives/{objectiveID}", s.handleObjectiveDetail)
		r.Post("/okr/objectives", s.handleCreateObjective)
		r.Post("/okr/objectives/{objectiveID}", s.handleUpdateObjective)
		r.Post("/okr/objectives/{objectiveID}/delete", s.handleDeleteObjective)
		r.Post("/okr/key-results", s.handleCreateKeyResult)
		r.Post("/okr/key-results/{keyResultID}", s.handleUpdateKeyResult)
		r.Post("/okr/key-results/{keyResultID}/delete", s.handleDeleteKeyResult)
		r.Post("/okr/objectives/{objectiveID}/link-kr", s.handleLinkKeyResult)
		r.Post("/okr/objectives/{objectiveID}/unlink-kr/{keyResultID}", s.handleUnlinkKeyResult)

		// Hop routes
		r.Get("/hops/{hopID}", s.handleHopDetail)
		r.Post("/hops/{hopID}/propose-variations", s.handleProposeVariations)

		// Variation routes
		r.Get("/variations/{variationID}", s.handleVariationDetail)
		r.Post("/variations/{variationID}/retry", s.handleRetryVariation)
		r.Post("/variations/{variationID}/terminate", s.handleTerminateVariation)
		r.Post("/variations/{variationID}/start-demo", s.handleStartDemo)
		r.Post("/variations/{variationID}/stop-demo", s.handleStopDemo)
		r.Post("/variations/{variationID}/retry-demo", s.handleRetryDemo)
		r.Post("/variations/{variationID}/restart-demo", s.handleRestartDemo)
		r.Post("/variations/{variationID}/prune", s.handlePruneVariation)

		// Decision routes
		r.Get("/decisions", s.handleDecisions)
		r.Get("/decisions/{decisionID}", s.handleDecisionDetail)
		r.Post("/decisions/{decisionID}/message", s.handleSendMessage)
		r.Post("/decisions/{decisionID}/regenerate", s.handleRegenerate)
		r.Post("/decisions/{decisionID}/roadmap", s.handleUpdateRoadmap)
		r.Post("/decisions/{decisionID}/approve", s.handleApprove)
		r.Post("/decisions/{decisionID}/reject", s.handleReject)
		r.Post("/decisions/{decisionID}/select", s.handleSelectWinner)
		r.Post("/decisions/{decisionID}/reject-all", s.handleRejectAllVariations)
		r.Post("/decisions/{decisionID}/request-more-variations", s.handleRequestMoreVariations)
		r.Post("/decisions/{decisionID}/resolve-conflicts", s.handleResolveConflicts)
		r.Post("/roadmap/propose", s.handleProposeRoadmap)
	})

	// API endpoints (for htmx)
	r.Route("/api", func(r chi.Router) {
		r.Get("/projects", s.apiListProjects)
		r.Get("/projects/{projectID}/strategy", s.apiGetStrategy)
		r.Get("/projects/{projectID}/hops/{hopID}/evaluate", s.apiEvaluateVariations)
		r.Post("/projects/{projectID}/okr/tune", s.apiTuneOKRs)
		r.Get("/demos/{demoID}/logs", s.apiGetDemoLogs)
		r.Get("/demos/{demoID}/status", s.apiGetDemoStatus)
	})

	s.router = r
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	return http.ListenAndServe(s.addr, s.router)
}
