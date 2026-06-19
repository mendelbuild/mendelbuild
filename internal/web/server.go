package web

import (
	"net/http"

	"github.com/bhs/mendelbuild/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Server is the HTTP server for the MendelBuild webapp.
type Server struct {
	db     *db.DB
	addr   string
	router chi.Router
}

// NewServer creates a new Server.
func NewServer(database *db.DB, addr string) *Server {
	s := &Server{
		db:   database,
		addr: addr,
	}
	s.setupRoutes()
	return s
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
		r.Get("/decisions", s.handleDecisions)
		r.Get("/decisions/{decisionID}", s.handleDecisionDetail)
		r.Post("/decisions/{decisionID}/message", s.handleSendMessage)
		r.Post("/decisions/{decisionID}/regenerate", s.handleRegenerate)
		r.Post("/decisions/{decisionID}/roadmap", s.handleUpdateRoadmap)
		r.Post("/decisions/{decisionID}/approve", s.handleApprove)
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
