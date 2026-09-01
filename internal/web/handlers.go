package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/js/*.js static/css/*.css static/*.png
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

	"usd": formatUSD,

	// num prints a target value without a trailing ".0" -- a key result of
	// "1000 users" should not render its target as "1000.0".
	"num": func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) },
	"add": func(a, b float64) float64 { return a + b },

	// Status renderers. Templates must never switch on a raw status string, so
	// every status a page shows arrives as a StatusView carrying a word and a
	// tone. See internal/domain/status_view.go.
	"revisionStatus":   domain.RevisionStatus,
	"demoStatus":       domain.DemoStatus,
	"deploymentStatus": domain.DeploymentStatus,
	"validationStatus": domain.ValidationStatus,
	"memberRole":       domain.MemberRole,
	"hopStatus":        domain.HopStatusView,
	"decisionKind":       domain.DecisionKindLabel,
	"decisionStatus":     domain.DecisionStatusView,
	"decisionResolution": domain.DecisionResolution,
	"messageAuthor":      domain.MessageAuthor,
	"decisionWeight":   domain.DecisionImportance,
	"usdPtr": func(f *float64) string {
		if f == nil {
			return "-"
		}
		if *f < 0.01 && *f > 0 {
			return fmt.Sprintf("$%.4f", *f)
		}
		return fmt.Sprintf("$%.2f", *f)
	},
	"pct": func(f float64) string {
		return fmt.Sprintf("%.0f%%", f*100)
	},
	// pctWidth is pct as a bare number for CSS widths, clamped so an overspent
	// budget does not draw a bar past the end of its track.
	"pctWidth": func(f float64) float64 {
		if f > 1 {
			return 100
		}
		if f < 0 {
			return 0
		}
		return f * 100
	},
	// toks abbreviates token counts, which routinely run to millions on a
	// cache-heavy agentic run.
	"toks": func(n int) string {
		switch {
		case n >= 1_000_000:
			return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
		case n >= 1_000:
			return fmt.Sprintf("%.0fk", float64(n)/1_000)
		default:
			return fmt.Sprintf("%d", n)
		}
	},
}

// formatUSD renders money at a precision that stays honest at both ends: cents
// for real sums, four decimals for the sub-cent charges a single agent call
// produces, so they do not all render as "$0.00".
//
// Named rather than inline in templateFuncs because Go code composing a
// sentence about money must spell it the same way the templates do.
func formatUSD(f float64) string {
	switch {
	case f == 0:
		return "$0"
	case f < 0.01:
		return fmt.Sprintf("$%.4f", f)
	case f < 100:
		return fmt.Sprintf("$%.2f", f)
	default:
		return fmt.Sprintf("$%.0f", f)
	}
}

// parsePageTemplate creates a template from layout + shared partials + a
// specific page template. This avoids conflicts when multiple pages define the
// same block name, while making the partials in partials.html (the lifecycle
// ribbon, the roadmap panel) available to every page.
func parsePageTemplate(pageName string) *template.Template {
	return template.Must(template.New("").Funcs(templateFuncs).ParseFS(
		templatesFS,
		"templates/layout.html",
		"templates/partials.html",
		"templates/"+pageName,
	))
}

// renderPage renders a page template with the layout.
//
// Prefer renderPageFor. This is the escape hatch for the handful of pages
// whose data is not a map (the login screen).
func renderPage(w http.ResponseWriter, pageName string, data interface{}) error {
	t := parsePageTemplate(pageName)
	return t.ExecuteTemplate(w, "layout", data)
}

// renderPageFor renders a page and stamps the chrome the layout needs but no
// handler should have to remember: who is signed in, and which navigation
// section is current.
//
// Every handler used to add the user by hand and five of them forgot, so the
// Hop and Variation pages showed no signed-in user and no way to log out.
// Deriving it here means a new page cannot be born missing it.
func (s *Server) renderPageFor(w http.ResponseWriter, r *http.Request, pageName string, data map[string]interface{}) error {
	s.addChrome(r, data)
	return renderPage(w, pageName, data)
}

// addChrome stamps everything the layout needs and no page should have to
// remember: who is signed in, which nav section is current, and how many
// requests are open.
//
// One call rather than three, because three is three chances to lose one — and
// two of them were lost. Handlers used to add the user by hand and five forgot,
// so the Hop and Variation pages offered no way to log out; the fix put it here
// and a later edit dropped it again along with the open-request count, which is
// how a deployed build came to show no account and no badge on any page at all.
// There is a test below whose only job is to notice if this call goes missing a
// third time.
func (s *Server) addChrome(r *http.Request, data map[string]interface{}) {
	s.addUserToData(r, data)
	data["Nav"] = navSection(r.URL.Path)
	s.addOpenInputCount(r.Context(), data)
}

// navSection maps a request path to the navigation item that should read as
// current. It returns "" for pages that sit under no section, which renders
// the nav with nothing highlighted rather than guessing.
func navSection(path string) string {
	rest, ok := strings.CutPrefix(path, "/p/")
	if !ok {
		if path == "/" || strings.HasPrefix(path, "/new") {
			return "projects"
		}
		return ""
	}
	// Drop the project ID; what follows is the section.
	_, rest, _ = strings.Cut(rest, "/")
	section, _, _ := strings.Cut(rest, "/")

	switch section {
	case "":
		// The project root is the overview.
		return "overview"
	case "strategy", "settings", "inputs", "costs":
		return section
	case "okr", "roadmap", "setup":
		// Objectives and the Hop DAG belong to the Strategy that contains them.
		return "strategy"
	case "deployment":
		// Deployment configuration is part of settings, and the nav should say
		// so even while it still lives at its own URL.
		return "settings"
	case "hops", "variations":
		// A Hop is a step in the roadmap, and the roadmap is part of Strategy,
		// so that is the section that stays lit while you are inside one.
		return "strategy"
	default:
		return ""
	}
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

	if err := s.renderPageFor(w, r, "dashboard.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	t := template.Must(template.New("").ParseFS(templatesFS, "templates/login.html"))
	t.ExecuteTemplate(w, "login", nil)
}

// handleStrategy renders the objectives and key results, and only those.
//
// It used to render everything: budget, per-model costs, the hops table, the
// deployment channel, a propose-roadmap form and the decision queue. Each of
// those now has a page whose subject it actually is, which leaves this one able
// to be about what the project is trying to achieve.
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

	// The money is shown beside the Key Results it is meant to buy; the Costs
	// page carries the detail.
	var costView *StrategyCostView
	if view != nil && view.Strategy != nil {
		costView = s.strategyCostView(ctx, projectID, view.Strategy.ID)
	}

	// The timeline is the join between the objectives and the roadmap: the
	// Strategy page could say what the project is for and the Roadmap what it
	// was doing, and nothing put the two on one axis.
	var timeline *TimelineView
	if view != nil && view.Strategy != nil {
		timeline = s.buildTimeline(ctx, projectID, view.Strategy.ID, time.Now())
	}

	data := map[string]interface{}{
		"Title":       "Strategy",
		"ProjectID":   projectID.String(),
		"StrategyTab": "objectives",
		"Strategy":    view,
		"Cost":        costView,
		"Timeline":    timeline,
	}
	s.addProjectReadiness(ctx, data)
	s.addOnboardingRibbon(ctx, data)

	if err := s.renderPageFor(w, r, "strategy.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleCosts renders what the project has spent and whether that is on track.
//
// Cost is something you go and look at. On the front page it greeted every
// visit with a burn-down chart and a per-model breakdown, above the work.
func (s *Server) handleCosts(w http.ResponseWriter, r *http.Request) {
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

	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		http.Error(w, "no strategy found", http.StatusNotFound)
		return
	}

	data := map[string]interface{}{
		"Title":     "Costs",
		"ProjectID": projectID.String(),
		"Project":   project,
		"Cost":      s.strategyCostView(ctx, projectID, strategies[0].ID),
	}
	s.addProjectReadiness(ctx, data)

	if err := s.renderPageFor(w, r, "costs.html", data); err != nil {
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
	s.addProjectReadiness(ctx, data)
	s.addOnboardingRibbon(ctx, data)

	if err := s.renderPageFor(w, r, "input_requests.html", data); err != nil {
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

// HopRow is one Hop in the roadmap's table, carrying the same plain-English
// reading its own page gives rather than a raw status.
type HopRow struct {
	Hop            domain.Hop
	Status         domain.StatusView
	VariationCount int
	WaitingOnYou   bool
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

	// Same graph the embedded panel on Hop and Variation pages draws.
	hopViews, edges, err := s.buildRoadmapGraph(ctx, strategy.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	hopsJSON, edgesJSON, err := marshalRoadmap(hopViews, edges)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// The graph and the table are the same information drawn two ways, and they
	// were on separate pages: the picture here, the list on /strategy. Reading
	// one to find a Hop and the other to find its state was needless.
	hops, _ := s.db.GetHopsByStrategy(ctx, strategy.ID)
	hopRows := make([]HopRow, 0, len(hops))
	for i := range hops {
		hop := &hops[i]
		variations, _ := s.db.GetVariationsByHop(ctx, hop.ID)
		ribbon := domain.HopLifecycle(hop, variations)
		hopRows = append(hopRows, HopRow{
			Hop:            *hop,
			Status:         domain.StatusView{Label: ribbon.Headline, Tone: ribbon.Tone},
			VariationCount: len(variations),
			WaitingOnYou:   ribbon.WaitingOnYou(),
		})
	}

	data := map[string]interface{}{
		"Title":       "Roadmap",
		"ProjectID":   projectID.String(),
		"StrategyTab": "roadmap",
		"Project":   project,
		"Strategy":  strategy,
		"HopsJSON":  hopsJSON,
		"EdgesJSON": edgesJSON,
		"Hops":      hopRows,
	}
	s.addProjectReadiness(ctx, data)

	if err := s.renderPageFor(w, r, "roadmap.html", data); err != nil {
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
		SELECT id, name, brief, created_at, updated_at FROM projects ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []domain.Project
	for rows.Next() {
		var p domain.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Brief, &p.CreatedAt, &p.UpdatedAt); err != nil {
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

	return &StrategyView{
		Project:    project,
		Strategy:   &strategy,
		Objectives: objViews,
		Hops:       hops,
	}, nil
}
