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
	Hops       []domain.Hop
}

// ObjectiveView holds an objective with its key results and hop coverage.
type ObjectiveView struct {
	Objective  domain.Objective
	KeyResults []domain.KeyResult
	HopCount   int
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

	// Get decisions for sidebar
	decisions, _ := s.db.GetDecisionsByProject(ctx, projectID)
	var pendingDecision *domain.Decision
	var recentDecisions []domain.Decision
	for i := range decisions {
		d := &decisions[i]
		if d.Kind == domain.DecisionKindRoadmapReview && d.Status != domain.DecisionStatusResolved {
			pendingDecision = d
		}
		if len(recentDecisions) < 5 {
			recentDecisions = append(recentDecisions, *d)
		}
	}

	data := map[string]interface{}{
		"Title":           "Strategy: " + view.Strategy.Name,
		"ProjectID":       projectID,
		"Strategy":        view,
		"PendingDecision": pendingDecision,
		"RecentDecisions": recentDecisions,
	}

	if err := renderPage(w, "strategy.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// DecisionView is a template-friendly view of a Decision with dereferenced pointer fields.
type DecisionView struct {
	domain.Decision
	ResolutionStr string
}

func (s *Server) handleDecisions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	decisions, err := s.db.GetDecisionsByProject(ctx, projectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Separate open and resolved decisions, converting to view types
	var openDecisions, resolvedDecisions []DecisionView
	for _, d := range decisions {
		view := DecisionView{Decision: d}
		if d.Resolution != nil {
			view.ResolutionStr = *d.Resolution
		}
		if d.Status == domain.DecisionStatusResolved {
			resolvedDecisions = append(resolvedDecisions, view)
		} else {
			openDecisions = append(openDecisions, view)
		}
	}

	// Get active tab from query param, default to "open"
	activeTab := r.URL.Query().Get("tab")
	if activeTab != "resolved" {
		activeTab = "open"
	}

	data := map[string]interface{}{
		"Title":             "Decisions",
		"ProjectID":         projectID,
		"OpenDecisions":     openDecisions,
		"ResolvedDecisions": resolvedDecisions,
		"ActiveTab":         activeTab,
	}

	if err := renderPage(w, "decisions.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// RoadmapHopView holds hop data for the roadmap DAG visualization.
type RoadmapHopView struct {
	ID         string
	Name       string
	Status     string
	Variations []RoadmapVariationView
}

// RoadmapVariationView holds variation data for the roadmap DAG.
type RoadmapVariationView struct {
	ID     string
	Name   string
	Status string
}

// RoadmapEdge represents a dependency edge in the DAG.
type RoadmapEdge struct {
	From string `json:"from"` // depends_on_hop_id (the dependency)
	To   string `json:"to"`   // hop_id (the dependent)
}

func (s *Server) handleRoadmap(w http.ResponseWriter, r *http.Request) {
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

	// Get strategy for this project
	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		http.Error(w, "no strategy found", http.StatusNotFound)
		return
	}
	strategy := strategies[0]

	// Get all hops for the strategy
	hops, err := s.db.GetHopsByStrategy(ctx, strategy.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get variations for each hop (including proposed ones from pending decisions)
	hopViews := make([]RoadmapHopView, 0, len(hops))
	for _, hop := range hops {
		variations, _ := s.db.GetVariationsByHop(ctx, hop.ID)
		varViews := make([]RoadmapVariationView, 0)

		if len(variations) > 0 {
			// Show actual variations
			for _, v := range variations {
				varViews = append(varViews, RoadmapVariationView{
					ID:     v.ID.String(),
					Name:   v.Name,
					Status: string(v.Status),
				})
			}
		} else {
			// Check for pending variation_review decision with proposed variations
			decision, err := s.db.GetDecisionBySubjectAndKind(ctx, "hop", hop.ID, domain.DecisionKindVariationReview)
			if err == nil && decision != nil && decision.Status != domain.DecisionStatusResolved && decision.Details != nil {
				// Parse proposed variations from decision details
				var proposal struct {
					Variations []struct {
						Name string `json:"name"`
					} `json:"variations"`
				}
				if json.Unmarshal([]byte(*decision.Details), &proposal) == nil {
					for _, v := range proposal.Variations {
						varViews = append(varViews, RoadmapVariationView{
							ID:     "", // No ID yet - not clickable
							Name:   v.Name,
							Status: "proposed",
						})
					}
				}
			}
		}

		hopViews = append(hopViews, RoadmapHopView{
			ID:         hop.ID.String(),
			Name:       hop.Name,
			Status:     string(hop.Status),
			Variations: varViews,
		})
	}

	// Get all dependencies
	deps, err := s.db.GetHopDependenciesByStrategy(ctx, strategy.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	edges := make([]RoadmapEdge, 0, len(deps))
	for _, d := range deps {
		edges = append(edges, RoadmapEdge{
			From: d.DependsOnHopID.String(),
			To:   d.HopID.String(),
		})
	}

	// Convert to JSON for JavaScript
	hopsJSON, _ := json.Marshal(hopViews)
	edgesJSON, _ := json.Marshal(edges)

	data := map[string]interface{}{
		"Title":     "Roadmap",
		"ProjectID": projectID,
		"Project":   project,
		"Strategy":  strategy,
		"HopsJSON":  template.JS(hopsJSON),
		"EdgesJSON": template.JS(edgesJSON),
	}

	if err := renderPage(w, "roadmap.html", data); err != nil {
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

	hops, err := s.db.GetHopsByStrategy(ctx, strategy.ID)
	if err != nil {
		return nil, err
	}

	// Build objective ID to hop count map
	objHopCount := make(map[string]int)
	for _, hop := range hops {
		if hop.Params != nil {
			var params struct {
				ObjectiveIDs []string `json:"objective_ids"`
			}
			if err := json.Unmarshal(hop.Params, &params); err == nil {
				for _, objID := range params.ObjectiveIDs {
					objHopCount[objID]++
				}
			}
		}
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
			HopCount:   objHopCount[obj.ID.String()],
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
		Hops:       hops,
	}, nil
}
