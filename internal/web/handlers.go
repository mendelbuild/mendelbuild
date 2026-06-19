package web

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

//go:embed templates/*.html
var templatesFS embed.FS

// parsePageTemplate creates a template from layout + a specific page template.
// This avoids conflicts when multiple pages define the same block name.
func parsePageTemplate(pageName string) *template.Template {
	return template.Must(template.ParseFS(templatesFS, "templates/layout.html", "templates/"+pageName))
}

// renderPage renders a page template with the layout.
func renderPage(w http.ResponseWriter, pageName string, data interface{}) error {
	t := parsePageTemplate(pageName)
	return t.ExecuteTemplate(w, "layout", data)
}

// StrategyView holds data for rendering the strategy page.
type StrategyView struct {
	Project    *domain.Project
	Strategy   *domain.Strategy
	Objectives []ObjectiveView
	Funding    []domain.FundingSource
}

// ObjectiveView holds an objective with its key results.
type ObjectiveView struct {
	Objective  domain.Objective
	KeyResults []domain.KeyResult
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get all projects (for now, just show the first one if any)
	projects, err := s.listProjects(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":    "MendelBuild Dashboard",
		"Projects": projects,
	}

	if err := renderPage(w, "dashboard.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleProjectDashboard(w http.ResponseWriter, r *http.Request) {
	// For now, redirect to strategy page
	projectID := chi.URLParam(r, "projectID")
	http.Redirect(w, r, "/p/"+projectID+"/strategy", http.StatusFound)
}

func (s *Server) handleStrategy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	view, err := s.getStrategyViewByProject(ctx, project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":     "Strategy: " + view.Strategy.Name,
		"ProjectID": projectID,
		"Strategy":  view,
	}

	if err := renderPage(w, "strategy.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleDecisions(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")

	data := map[string]interface{}{
		"Title":     "Decisions",
		"ProjectID": projectID,
		"Decisions": []domain.Decision{}, // TODO: implement
	}

	if err := renderPage(w, "decisions.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) apiListProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projects, err := s.listProjects(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(projects)
}

func (s *Server) apiGetStrategy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	view, err := s.getStrategyView(ctx, project.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(view)
}

// listProjects returns all projects.
func (s *Server) listProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, name, created_at, updated_at FROM projects ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []domain.Project
	for rows.Next() {
		var p domain.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, nil
}

// getStrategyView builds the full strategy view for a project by name.
func (s *Server) getStrategyView(ctx context.Context, projectName string) (*StrategyView, error) {
	project, err := s.db.GetProjectByName(ctx, projectName)
	if err != nil {
		return nil, err
	}
	return s.getStrategyViewByProject(ctx, project)
}

// getStrategyViewByProject builds the full strategy view for a project.
func (s *Server) getStrategyViewByProject(ctx context.Context, project *domain.Project) (*StrategyView, error) {
	strategies, err := s.db.GetStrategiesByProject(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	if len(strategies) == 0 {
		return nil, nil
	}

	strategy := strategies[0] // Use first strategy for now

	objectives, err := s.db.GetObjectivesByStrategy(ctx, strategy.ID)
	if err != nil {
		return nil, err
	}

	var objViews []ObjectiveView
	for _, obj := range objectives {
		krs, err := s.db.GetKeyResultsByObjective(ctx, obj.ID)
		if err != nil {
			return nil, err
		}
		objViews = append(objViews, ObjectiveView{
			Objective:  obj,
			KeyResults: krs,
		})
	}

	funding, err := s.db.GetFundingSourcesByStrategy(ctx, strategy.ID)
	if err != nil {
		return nil, err
	}

	return &StrategyView{
		Project:    project,
		Strategy:   &strategy,
		Objectives: objViews,
		Funding:    funding,
	}, nil
}
