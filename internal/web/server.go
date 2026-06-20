package web

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/bhs/mendelbuild/internal/codegen"
	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
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
		fmt.Printf("Error finding hops with creating variations: %v\n", err)
		return
	}

	for _, hop := range hops {
		// Check if hop is active (approved for generation)
		if hop.Status != domain.HopStatusActive {
			continue
		}

		fmt.Printf("Processing variations for hop %s (%s)\n", hop.Name, hop.ID)
		result, err := s.orchestrator.RunForExistingVariations(ctx, hop.ID)
		if err != nil {
			fmt.Printf("Error generating variations for hop %s: %v\n", hop.ID, err)
			continue
		}
		fmt.Printf("Completed hop %s: %d succeeded, %d failed\n",
			hop.ID, result.SuccessCount, result.FailureCount)
	}
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

		// Hop routes
		r.Get("/hops/{hopID}", s.handleHopDetail)
		r.Post("/hops/{hopID}/propose-variations", s.handleProposeVariations)

		// Decision routes
		r.Get("/decisions", s.handleDecisions)
		r.Get("/decisions/{decisionID}", s.handleDecisionDetail)
		r.Post("/decisions/{decisionID}/message", s.handleSendMessage)
		r.Post("/decisions/{decisionID}/regenerate", s.handleRegenerate)
		r.Post("/decisions/{decisionID}/roadmap", s.handleUpdateRoadmap)
		r.Post("/decisions/{decisionID}/approve", s.handleApprove)
		r.Post("/decisions/{decisionID}/reject", s.handleReject)
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
