package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/codegen/executor"
	"github.com/bhs/mendelbuild/internal/cost"
	"github.com/bhs/mendelbuild/internal/auth"
	"github.com/bhs/mendelbuild/internal/codegen"
	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// Server is the HTTP server for the MendelBuild webapp.
type Server struct {
	db                  *db.DB
	addr                string
	version             string
	buildTime           string
	router              chi.Router
	orchestrator        *codegen.Orchestrator
	auth                *auth.Auth
	authEnabled         bool
	stopWorker          chan struct{}
	processingHops      map[uuid.UUID]bool // tracks hops currently being processed
	processingHopsMutex sync.Mutex
}

type contextKey string

const userContextKey contextKey = "user"

// UserFromContext extracts the user from the request context.
func UserFromContext(ctx context.Context) *domain.User {
	user, _ := ctx.Value(userContextKey).(*domain.User)
	return user
}

// NewServer creates a new Server.
func NewServer(database *db.DB, addr, version, buildTime string) *Server {
	s := &Server{
		db:             database,
		addr:           addr,
		version:        version,
		buildTime:      buildTime,
		orchestrator:   codegen.NewOrchestrator(database, codegen.DefaultConcurrency),
		stopWorker:     make(chan struct{}),
		processingHops: make(map[uuid.UUID]bool),
	}

	// Initialize auth if configured
	authConfig, err := auth.ConfigFromEnv()
	if err != nil {
		fmt.Printf("[startup] Auth not configured: %v\n", err)
		fmt.Printf("[startup] Running without authentication (set GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, SESSION_SECRET to enable)\n")
	} else {
		s.auth = auth.New(authConfig, database)
		s.authEnabled = true
		fmt.Printf("[startup] Authentication enabled\n")
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
		// On restart, mark demos as stopped - cloud resources may still be running
		// but we have no way to check. User can teardown manually via the stored command.
		fmt.Printf("[startup] Marking stale demo %s as stopped (may need manual cleanup)\n", d.ID)
		errMsg := "Mendel restarted - demo may need manual teardown"
		s.db.UpdateDemoInstanceStatus(ctx, d.ID, domain.DemoInstanceStatusStopped, &errMsg)
	}
}

// startVariationWorker starts a background goroutine that polls for
// variations in "creating" status and runs code generation for them.
// Also handles creating selection input requests and updating hop statuses.
func (s *Server) startVariationWorker() {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		// Hosting accrues continuously rather than per event, so it is metered
		// on its own slower cadence: often enough that a running deployment's
		// cost is visible while it runs, rarely enough not to fill the ledger
		// with near-empty rows.
		hostingTicker := time.NewTicker(10 * time.Minute)
		defer hostingTicker.Stop()

		for {
			select {
			case <-s.stopWorker:
				return
			case <-ticker.C:
				s.processVariationProposals()
				s.processCreatingVariations()
				s.processSelectionInputRequests()
				s.processHopStatusUpdates()
			case <-hostingTicker.C:
				s.settleHostingSpend()
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

// processSelectionInputRequests creates variation_selection input requests for hops
// that have at least one pending variation but no selection input request yet.
func (s *Server) processSelectionInputRequests() {
	ctx := context.Background()

	hops, err := s.db.GetHopsNeedingSelectionInputRequest(ctx)
	if err != nil {
		fmt.Printf("[worker] Error finding hops needing selection input request: %v\n", err)
		return
	}

	for _, hop := range hops {
		if err := s.createSelectionInputRequest(ctx, &hop); err != nil {
			fmt.Printf("[worker] Error creating selection input request for hop %s: %v\n", hop.ID, err)
		} else {
			fmt.Printf("[worker] Created variation_selection input request for hop '%s'\n", hop.Name)
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
// that don't have any variations or variation_review input requests yet.
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
			fmt.Printf("[worker] Created variation_review input request for hop '%s'\n", hop.Name)
		}
	}
}

// proposeVariationsForHop runs the variation proposer agent and creates a variation_review input request.
func (s *Server) proposeVariationsForHop(ctx context.Context, hop *domain.Hop) error {
	strategy, err := s.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		return fmt.Errorf("get strategy: %w", err)
	}

	// Check project readiness before proceeding
	readiness, _ := s.db.GetProjectReadiness(ctx, strategy.ProjectID)
	if !readiness.IsReady() {
		return fmt.Errorf("project not ready: missing %v", readiness.MissingSettings())
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

	// What this Hop has left to spend, and what generation has actually cost on
	// this project, so the proposed approaches are sized against real money.
	availableBudget := s.hopCostView(ctx, hop.ID).RemainingUSD()
	calibration, _ := cost.BuildCalibration(ctx, s.db, strategy.ProjectID, executor.DefaultModel)

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
		Hop:                   hopContext,
		RepositoryURL:         repoURL,
		AvailableBudgetUSD:    availableBudget,
		Calibration:           calibration,
		NumVariations:         2, // Start with 2 variations
		CompletedDependencies: completedDependencies,
	}

	// Call variation proposer
	client, err := agent.NewClient("")
	if err != nil {
		return fmt.Errorf("create agent client: %w", err)
	}

	proposer := agent.NewVariationProposer(client)
	proposal, spend, err := proposer.ProposeVariations(ctx, input)
	if err != nil {
		return fmt.Errorf("propose variations: %w", err)
	}
	s.recordHopSpend(ctx, hop.ID, "variation_proposer", spend)

	// Generate evaluation criteria if the hop doesn't have them yet
	if len(hop.EvaluationCriteria) == 0 {
		criteriaInput := agent.EvaluationCriteriaInput{
			HopName:       hop.Name,
			HopCommentary: hop.Commentary,
			Objectives:    objectiveDescs,
		}

		criteriaGenerator := agent.NewEvaluationCriteriaGenerator(client)
		criteria, criteriaSpend, err := criteriaGenerator.GenerateCriteria(ctx, criteriaInput)
		s.recordHopSpend(ctx, hop.ID, "evaluation_criteria", criteriaSpend)
		if err == nil && criteria != nil {
			criteriaJSON, err := json.Marshal(criteria)
			if err == nil {
				if err := s.db.UpdateHopEvaluationCriteria(ctx, hop.ID, criteriaJSON); err != nil {
					fmt.Printf("[worker] Warning: failed to save evaluation criteria: %v\n", err)
				} else {
					// Invalidate cached evaluation scores since criteria changed
					s.db.ClearInputRequestCacheBySubject(ctx, "hop", hop.ID)
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
			EstimatedCostUSD: v.EstimatedCostUSD,
		})
	}

	// Create input request
	now := time.Now()
	proposalJSON, _ := json.MarshalIndent(proposalData, "", "  ")
	proposalStr := string(proposalJSON)

	inputRequest := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        strategy.ProjectID,
		Kind:             domain.InputRequestKindVariationReview,
		Title:            fmt.Sprintf("Variation Review: %s", hop.Name),
		Details:          &proposalStr,
		ObjectivityScore: 0.4,
		ImportanceScore:  0.7,
		Status:           domain.InputRequestStatusNeedsAssignment,
		SubjectType:      strPtr("hop"),
		SubjectID:        &hop.ID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := s.db.CreateInputRequest(ctx, inputRequest); err != nil {
		return fmt.Errorf("create input request: %w", err)
	}

	// Create agent message
	tokensUsed := spend.Tokens.Total()
	agentMsg := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequest.ID,
		Role:           "agent",
		Content:        fmt.Sprintf("Generated %d variation proposals.\n\nRationale: %s", len(proposal.Variations), proposal.Rationale),
		TokensUsed:     &tokensUsed,
		CreatedAt:      now,
	}
	s.db.CreateInputRequestMessage(ctx, agentMsg)

	return nil
}

// createSelectionInputRequest creates a variation_selection input request for a hop.
// Before creating the selection input request, it runs a conflict audit on all
// variation migrations. If conflicts are detected, it creates a variation_review
// input request instead to let the user address the conflicts.
func (s *Server) createSelectionInputRequest(ctx context.Context, hop *domain.Hop) error {
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
			// Conflicts detected - create a variation_review input request instead
			return s.createConflictReviewInputRequest(ctx, hop, variations, auditResult)
		}
	}

	// No conflicts - create the selection input request
	return s.createSelectionInputRequestInternal(ctx, hop, variations)
}

// createConflictReviewInputRequest creates a variation_review input request when
// migration conflicts are detected between variations.
func (s *Server) createConflictReviewInputRequest(ctx context.Context, hop *domain.Hop, variations []domain.Variation, auditResult *agent.ConflictAuditResponse) error {
	// Get strategy for project ID
	strategy, err := s.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		return fmt.Errorf("get strategy: %w", err)
	}

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

	inputRequest := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        strategy.ProjectID,
		Kind:             domain.InputRequestKindVariationReview,
		Title:            fmt.Sprintf("Migration Conflicts: %s", hop.Name),
		Details:          &detailsStr,
		ObjectivityScore: 0.3, // Requires human judgment to resolve conflicts
		ImportanceScore:  0.9, // Very important - blocking selection
		Status:           domain.InputRequestStatusNeedsAssignment,
		SubjectType:      strPtr("hop"),
		SubjectID:        &hop.ID,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	fmt.Printf("[worker] Migration conflicts detected for hop '%s': %s\n", hop.Name, auditResult.Summary)

	return s.db.CreateInputRequest(ctx, inputRequest)
}

// createSelectionInputRequestInternal creates the actual selection input request.
func (s *Server) createSelectionInputRequestInternal(ctx context.Context, hop *domain.Hop, variations []domain.Variation) error {
	// Get strategy for project ID
	strategy, err := s.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		return fmt.Errorf("get strategy: %w", err)
	}

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

	inputRequest := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        strategy.ProjectID,
		Kind:             domain.InputRequestKindVariationSelection,
		Title:            fmt.Sprintf("Select Variation: %s", hop.Name),
		Details:          &detailsStr,
		ObjectivityScore: 0.4, // Partially objective (some criteria are measurable)
		ImportanceScore:  0.7, // Important - affects what gets merged
		Status:           domain.InputRequestStatusNeedsAssignment,
		SubjectType:      strPtr("hop"),
		SubjectID:        &hop.ID,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	return s.db.CreateInputRequest(ctx, inputRequest)
}

func (s *Server) setupRoutes() {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Static files (embedded)
	staticSubFS, _ := fs.Sub(staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSubFS))))

	// Health check (public, for load balancer)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Version info (public)
	r.Get("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		info := map[string]interface{}{
			"version": s.version,
		}
		if s.buildTime != "" {
			info["build_time_unix"] = s.buildTime
			// Parse unix timestamp to human-readable
			if ts, err := strconv.ParseInt(s.buildTime, 10, 64); err == nil {
				info["build_time"] = time.Unix(ts, 0).UTC().Format(time.RFC3339)
			}
		}
		json.NewEncoder(w).Encode(info)
	})

	// Auth routes (public)
	if s.authEnabled {
		r.Get("/auth/login", s.handleLogin)
		r.Get("/auth/start", s.auth.HandleLogin)
		r.Get("/auth/callback", s.auth.HandleCallback)
		r.Get("/auth/logout", s.auth.HandleLogout)
	}

	// All other routes require auth (when enabled)
	r.Group(func(r chi.Router) {
		if s.authEnabled {
			r.Use(s.requireAuth)
		}

		// Global pages
		r.Get("/", s.handleDashboard)

		// Project-scoped pages
		r.Route("/p/{projectID}", func(r chi.Router) {
			if s.authEnabled {
				r.Use(s.requireProjectAccess)
			}
		r.Get("/", s.handleProjectDashboard)
		r.Get("/strategy", s.handleStrategy)
		r.Get("/roadmap", s.handleRoadmap)
		r.Get("/settings", s.handleProjectSettings)
		r.Post("/settings", s.handleSaveProjectSettings)
		r.Post("/settings/credentials", s.handleAddCloudCredential)
		r.Post("/settings/credentials/{credentialID}", s.handleUpdateCloudCredential)
		r.Post("/settings/credentials/{credentialID}/delete", s.handleDeleteCloudCredential)
		r.Post("/settings/members", s.handleAddMember)
		r.Post("/settings/members/{userID}/remove", s.handleRemoveMember)
		r.Post("/redeploy", s.handleRedeploy)

		// Deployment channel routes
		r.Get("/deployment", s.handleDeploymentChannel)
		r.Post("/deployment/channel", s.handleSetDeploymentChannel)
		r.Post("/deployment/validate-demo", s.handleValidateDemoPath)
		r.Post("/deployment/validate-prod", s.handleValidateProdPath)

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
		r.Post("/variations/{variationID}/retry-fix", s.handleRetryWithFix)
		r.Post("/variations/{variationID}/terminate", s.handleTerminateVariation)
		r.Post("/variations/{variationID}/rebase", s.handleRebaseVariation)
		r.Post("/variations/{variationID}/request-change", s.handleRequestChange)
		r.Post("/variations/{variationID}/start-demo", s.handleStartDemo)
		r.Post("/variations/{variationID}/stop-demo", s.handleStopDemo)
		r.Post("/variations/{variationID}/retry-demo", s.handleRetryDemo)
		r.Post("/variations/{variationID}/restart-demo", s.handleRestartDemo)
		r.Post("/variations/{variationID}/prune", s.handlePruneVariation)
		r.Post("/variations/{variationID}/requirements/{requirementID}/value", s.handleSetRequirementValue)
		r.Post("/variations/{variationID}/requirements/{requirementID}/acknowledge", s.handleAcknowledgeRequirement)
		r.Post("/variations/{variationID}/requirements/{requirementID}/retract", s.handleRetractAcknowledgement)

		// Debug route (prototype only)
		r.Get("/debug", s.handleDebug)

		// Input request routes
		r.Get("/inputs", s.handleInputRequests)
		r.Get("/inputs/{inputRequestID}", s.handleInputRequestDetail)
		r.Post("/inputs/{inputRequestID}/message", s.handleSendMessage)
		r.Post("/inputs/{inputRequestID}/regenerate", s.handleRegenerate)
		r.Post("/inputs/{inputRequestID}/roadmap", s.handleUpdateRoadmap)
		r.Post("/inputs/{inputRequestID}/approve", s.handleApprove)
		r.Post("/inputs/{inputRequestID}/reject", s.handleReject)
		r.Post("/inputs/{inputRequestID}/select", s.handleSelectWinner)
		r.Post("/inputs/{inputRequestID}/reject-all", s.handleRejectAllVariations)
		r.Post("/inputs/{inputRequestID}/request-more-variations", s.handleRequestMoreVariations)
		r.Post("/inputs/{inputRequestID}/resolve-conflicts", s.handleResolveConflicts)
			r.Post("/inputs/{inputRequestID}/provide-credential", s.handleProvideCredential)
			r.Post("/roadmap/propose", s.handleProposeRoadmap)
		})

		// API endpoints (for htmx)
		r.Route("/api", func(r chi.Router) {
			r.Get("/projects", s.apiListProjects)
			r.Get("/projects/{projectID}/strategy", s.apiGetStrategy)
			r.Get("/projects/{projectID}/hops/{hopID}/evaluate", s.apiEvaluateVariations)
			r.Post("/projects/{projectID}/okr/tune", s.apiTuneOKRs)
			r.Get("/demos/{demoID}/status", s.apiGetDemoStatus)

			// Log feeds for the in-place tailer (see logtail.go).
			r.Get("/variations/{variationID}/logs", s.apiVariationLogs)
			r.Get("/demos/{demoID}/logs", s.apiDemoLogs)
			r.Get("/deployments/{deploymentID}/logs", s.apiDeploymentLogs)
		})
	}) // end auth group

	s.router = r
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	return http.ListenAndServe(s.addr, s.router)
}

// requireAuth middleware redirects to login if no valid session.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.auth.UserFromRequest(r)
		if err != nil {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireProjectAccess middleware checks user is a member of the project.
func (s *Server) requireProjectAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		if user == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		projectIDStr := chi.URLParam(r, "projectID")
		projectID, err := uuid.Parse(projectIDStr)
		if err != nil {
			http.Error(w, "Invalid project ID", http.StatusBadRequest)
			return
		}

		isMember, err := s.db.IsProjectMember(r.Context(), projectID, user.ID)
		if err != nil || !isMember {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// settleHostingSpend tops up the ledger with hosting cost accrued since the
// last reading. An app left running is the spend a project is most likely to
// lose track of, so it is metered while running rather than only at teardown.
func (s *Server) settleHostingSpend() {
	n, err := cost.SettleHostingSpend(context.Background(), s.db)
	if err != nil {
		fmt.Printf("[worker] Warning: could not settle hosting spend: %v\n", err)
		return
	}
	if n > 0 {
		fmt.Printf("[worker] Metered hosting spend for %d deployment(s)\n", n)
	}
}
