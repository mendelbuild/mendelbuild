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

//go:embed static/js/*.js static/*.png
var staticFS embed.FS

// templateFuncs provides custom functions for templates.
var templateFuncs = template.FuncMap{
	"tuneScoreClass": func(score *float64) string {
		if score == nil {
			return ""
		}
		s := *score
		if s >= 0.8 {
			return "excellent"
		} else if s >= 0.6 {
			return "good"
		} else if s >= 0.4 {
			return "needs-work"
		}
		return "poor"
	},
	"mul100": func(score *float64) float64 {
		if score == nil {
			return 0
		}
		return *score * 100
	},
	"derefString": func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	},
	"derefFloat": func(f *float64) float64 {
		if f == nil {
			return 0
		}
		return *f
	},
	"div": func(a, b int) int {
		if b == 0 {
			return 0
		}
		return a / b
	},
}

// parsePageTemplate creates a template from layout + shared partials + a
// specific page template. This avoids conflicts when multiple pages define the
// same block name, while making the partials in partials.html (the lifecycle
// ribbon, the roadmap strip) available to every page.
func parsePageTemplate(pageName string) *template.Template {
	return template.Must(template.New("").Funcs(templateFuncs).ParseFS(
		templatesFS,
		"templates/layout.html",
		"templates/partials.html",
		"templates/"+pageName,
	))
}

// renderPage renders a page template with the layout.
func renderPage(w http.ResponseWriter, pageName string, data interface{}) error {
	t := parsePageTemplate(pageName)
	return t.ExecuteTemplate(w, "layout", data)
}

// addUserToData adds the current user to template data (if authenticated).
func (s *Server) addUserToData(r *http.Request, data map[string]interface{}) {
	if user := UserFromContext(r.Context()); user != nil {
		data["User"] = user
	}
}

// addOpenInputCount adds the open input request count to template data for the nav badge.
func (s *Server) addOpenInputCount(ctx context.Context, data map[string]interface{}) {
	projectIDStr, ok := data["ProjectID"].(string)
	if !ok || projectIDStr == "" {
		return
	}
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return
	}
	count, err := s.db.CountOpenInputRequestsByProject(ctx, projectID)
	if err != nil {
		return
	}
	if count > 0 {
		data["OpenInputCount"] = count
	}
}

// addProjectReadiness adds project readiness info to template data.
func (s *Server) addProjectReadiness(ctx context.Context, data map[string]interface{}) {
	projectIDStr, ok := data["ProjectID"].(string)
	if !ok || projectIDStr == "" {
		return
	}
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return
	}
	readiness, err := s.db.GetProjectReadiness(ctx, projectID)
	if err != nil {
		return
	}
	data["ProjectReadiness"] = readiness
	if !readiness.IsReady() {
		data["MissingSettings"] = readiness.MissingSettings()
	}
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

	// Get projects for the current user (or all if auth not enabled)
	var projects []struct {
		Project *domain.Project
		Role    domain.ProjectMemberRole
	}

	user := UserFromContext(ctx)
	if user != nil && s.authEnabled {
		userProjects, err := s.db.GetUserProjects(ctx, user.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, p := range userProjects {
			proj := p.Project
			projects = append(projects, struct {
				Project *domain.Project
				Role    domain.ProjectMemberRole
			}{Project: &proj, Role: p.Role})
		}
	} else {
		allProjects, err := s.listProjects(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for i := range allProjects {
			projects = append(projects, struct {
				Project *domain.Project
				Role    domain.ProjectMemberRole
			}{Project: &allProjects[i], Role: domain.ProjectMemberRoleOwner})
		}
	}

	data := map[string]interface{}{
		"Title":    "MendelBuild Dashboard",
		"Projects": projects,
	}
	s.addUserToData(r, data)

	if err := renderPage(w, "dashboard.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	t := template.Must(template.New("").ParseFS(templatesFS, "templates/login.html"))
	t.ExecuteTemplate(w, "login", nil)
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

	// Get input requests for sidebar
	inputRequests, _ := s.db.GetInputRequestsByProject(ctx, projectID)
	var pendingInputRequest *domain.InputRequest
	var pendingInputRequests []domain.InputRequest
	for i := range inputRequests {
		ir := &inputRequests[i]
		if ir.Kind == domain.InputRequestKindRoadmapReview && ir.Status != domain.InputRequestStatusResolved {
			pendingInputRequest = ir
		}
		// Collect all non-resolved input requests
		if ir.Status != domain.InputRequestStatusResolved && len(pendingInputRequests) < 5 {
			pendingInputRequests = append(pendingInputRequests, *ir)
		}
	}

	// Get production deployment info
	var productionURL string
	var productionDeployedAt string
	deployment, err := s.db.GetLatestRunningDeploymentByProject(ctx, projectID)
	if err == nil && deployment != nil {
		if deployment.PublicURL != nil {
			productionURL = *deployment.PublicURL
		} else {
			productionURL = deployment.URL
		}
		productionDeployedAt = deployment.DeployedAt.Format("2006-01-02 15:04")
	}

	// Get deployment channel info
	var deploymentChannel *domain.ProjectDeploymentChannel
	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err == nil {
		deploymentChannel = channel
	}

	// Get supported combos for channel setup
	supportedCombos, _ := s.db.ListSupportedDeploymentCombos(ctx)

	// Get project-level token totals
	projectTokens, _ := s.db.GetProjectTokenTotals(ctx, projectID)

	data := map[string]interface{}{
		"Title":                "Strategy: " + view.Strategy.Name,
		"ProjectID":            projectID.String(),
		"Strategy":             view,
		"PendingInputRequest":  pendingInputRequest,
		"PendingInputRequests": pendingInputRequests,
		"ProductionURL":        productionURL,
		"ProductionDeployedAt": productionDeployedAt,
		"DeploymentChannel":    deploymentChannel,
		"SupportedCombos":      supportedCombos,
		"TotalInputTokens":     projectTokens.InputTokens,
		"TotalOutputTokens":    projectTokens.OutputTokens,
	}
	s.addOpenInputCount(ctx, data)
	s.addProjectReadiness(ctx, data)
	s.addUserToData(r, data)

	if err := renderPage(w, "strategy.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// InputRequestView is a template-friendly view of an InputRequest with dereferenced pointer fields.
type InputRequestView struct {
	domain.InputRequest
	ResolutionStr string
}

func (s *Server) handleInputRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	inputRequests, err := s.db.GetInputRequestsByProject(ctx, projectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Separate open and resolved input requests, converting to view types
	var openInputRequests, resolvedInputRequests []InputRequestView
	for _, ir := range inputRequests {
		view := InputRequestView{InputRequest: ir}
		if ir.Resolution != nil {
			view.ResolutionStr = *ir.Resolution
		}
		if ir.Status == domain.InputRequestStatusResolved {
			resolvedInputRequests = append(resolvedInputRequests, view)
		} else {
			openInputRequests = append(openInputRequests, view)
		}
	}

	// Get active tab from query param, default to "open"
	activeTab := r.URL.Query().Get("tab")
	if activeTab != "resolved" {
		activeTab = "open"
	}

	data := map[string]interface{}{
		"Title":             "Input Needed",
		"ProjectID":         projectID.String(),
		"OpenInputRequests":     openInputRequests,
		"ResolvedInputRequests": resolvedInputRequests,
		"ActiveTab":         activeTab,
	}
	s.addOpenInputCount(ctx, data)
	s.addProjectReadiness(ctx, data)
	s.addUserToData(r, data)

	if err := renderPage(w, "input_requests.html", data); err != nil {
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
			// Check for pending variation_review input request with proposed variations
			inputRequest, err := s.db.GetInputRequestBySubjectAndKind(ctx, "hop", hop.ID, domain.InputRequestKindVariationReview)
			if err == nil && inputRequest != nil && inputRequest.Status != domain.InputRequestStatusResolved && inputRequest.Details != nil {
				// Parse proposed variations from input request details
				var proposal struct {
					Variations []struct {
						Name string `json:"name"`
					} `json:"variations"`
				}
				if json.Unmarshal([]byte(*inputRequest.Details), &proposal) == nil {
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
		"ProjectID": projectID.String(),
		"Project":   project,
		"Strategy":  strategy,
		"HopsJSON":  template.JS(hopsJSON),
		"EdgesJSON": template.JS(edgesJSON),
	}
	s.addOpenInputCount(ctx, data)
	s.addProjectReadiness(ctx, data)
	s.addUserToData(r, data)

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
