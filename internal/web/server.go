package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/codegen"
	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// Server is the HTTP server for the MendelBuild webapp.
type Server struct {
	db           *db.DB
	addr         string
	router       chi.Router
	orchestrator *codegen.Orchestrator
	stopWorker   chan struct{}
}

// NewServer creates a new Server.
func NewServer(database *db.DB, addr string) *Server {
	s := &Server{
		db:           database,
		addr:         addr,
		orchestrator: codegen.NewOrchestrator(database, codegen.DefaultConcurrency),
		stopWorker:   make(chan struct{}),
	}
	s.setupRoutes()
	s.startVariationWorker()
	return s
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
		// Check if hop is active (approved for generation)
		if hop.Status != domain.HopStatusActive {
			continue
		}

		fmt.Printf("[worker] Starting code generation for hop '%s'\n", hop.Name)
		result, err := s.orchestrator.RunForExistingVariations(ctx, hop.ID)
		if err != nil {
			fmt.Printf("[worker] Error generating variations for hop '%s': %v\n", hop.Name, err)
			continue
		}
		fmt.Printf("[worker] Completed code generation for hop '%s': %d succeeded, %d failed\n",
			hop.Name, result.SuccessCount, result.FailureCount)
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

// createSelectionDecision creates a variation_selection Decision for a hop.
func (s *Server) createSelectionDecision(ctx context.Context, hop *domain.Hop) error {
	// Get all variations for this hop
	variations, err := s.db.GetVariationsByHop(ctx, hop.ID)
	if err != nil {
		return fmt.Errorf("get variations: %w", err)
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
		HopID              string           `json:"hop_id"`
		HopName            string           `json:"hop_name"`
		EvaluationCriteria string           `json:"evaluation_criteria,omitempty"`
		Variations         []variationInfo  `json:"variations"`
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

	// Global pages
	r.Get("/", s.handleDashboard)

	// Project-scoped pages
	r.Route("/p/{projectID}", func(r chi.Router) {
		r.Get("/", s.handleProjectDashboard)
		r.Get("/strategy", s.handleStrategy)
		r.Get("/settings", s.handleProjectSettings)
		r.Post("/settings", s.handleSaveProjectSettings)

		// Hop routes
		r.Get("/hops/{hopID}", s.handleHopDetail)
		r.Post("/hops/{hopID}/propose-variations", s.handleProposeVariations)

		// Variation routes
		r.Get("/variations/{variationID}", s.handleVariationDetail)
		r.Post("/variations/{variationID}/retry", s.handleRetryVariation)

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
		r.Post("/roadmap/propose", s.handleProposeRoadmap)
	})

	// API endpoints (for htmx)
	r.Route("/api", func(r chi.Router) {
		r.Get("/projects", s.apiListProjects)
		r.Get("/projects/{projectID}/strategy", s.apiGetStrategy)
	})

	s.router = r
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	return http.ListenAndServe(s.addr, s.router)
}
