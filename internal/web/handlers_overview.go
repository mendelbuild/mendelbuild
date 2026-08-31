package web

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// The project overview: the page you land on, answering "what should I do
// right now?" before anything else.
//
// It replaces a redirect to /strategy, which was a 346-line page carrying
// objectives, a hops table, a burn-down chart, a per-model cost breakdown, a
// deployment card and a propose-roadmap form. The answer to "what needs me"
// was in there, below the cost table, and also on /inputs, and also implied by
// each Hop's ribbon. Three places, none of them the front door.
//
// The organising rule here is whose move it is. domain.Ribbon already knows,
// for Hops and for Decisions alike, so this page is mostly a matter of asking
// everything in the project and sorting the answers into two lists.

// OverviewItem is one thing on the overview's lists.
type OverviewItem struct {
	Kind  string // "Decision" or "Hop", shown as an eyebrow
	Title string
	Note  string
	Href  string
	Tone  domain.Tone
}

// DeploymentSummary is the overview's one-line reading of where the project
// deploys, and whether that is ready.
type DeploymentSummary struct {
	Configured bool
	Channel    string // "container → Fly.io"
	Status     domain.StatusView
	URL        string // Live production URL, if any
	ShortCommit string
	Href       string
}

// OverviewView is everything the overview page renders.
type OverviewView struct {
	Project  *domain.Project
	Strategy *domain.Strategy

	// NeedsYou is work stopped until a person acts. InFlight is what Mendel is
	// doing unaided. Anything terminal appears in neither: a finished Hop is
	// not news.
	NeedsYou []OverviewItem
	InFlight []OverviewItem

	HopCount   int
	Roadmap    *MiniRoadmap
	Deployment DeploymentSummary
	Cost       *StrategyCostView
}

// Quiet reports whether nothing at all is happening, which is a real state for
// a project that has just been set up and deserves saying rather than showing
// two empty lists.
func (v *OverviewView) Quiet() bool { return len(v.NeedsYou) == 0 && len(v.InFlight) == 0 }

func (s *Server) handleProjectOverview(w http.ResponseWriter, r *http.Request) {
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

	view := &OverviewView{Project: project}

	// A project with no strategy yet has nothing to summarize; the onboarding
	// ribbon carries the whole page in that case.
	if strategies, err := s.db.GetStrategiesByProject(ctx, projectID); err == nil && len(strategies) > 0 {
		strategy := strategies[0]
		view.Strategy = &strategy
		view.Cost = s.strategyCostView(ctx, projectID, strategy.ID)
		view.Roadmap = s.buildProjectRoadmap(ctx, projectID, strategy.ID)

		hops, _ := s.db.GetHopsByStrategy(ctx, strategy.ID)
		view.HopCount = len(hops)
		for i := range hops {
			hop := &hops[i]
			variations, _ := s.db.GetVariationsByHop(ctx, hop.ID)
			ribbon := domain.HopLifecycle(hop, variations)
			if ribbon.Terminal() {
				continue
			}
			item := OverviewItem{
				Kind:  "Hop",
				Title: hop.Name,
				Note:  ribbon.Headline,
				Href:  fmt.Sprintf("/p/%s/hops/%s", projectID, hop.ID),
				Tone:  ribbon.Tone,
			}
			if ribbon.WaitingOnYou() {
				view.NeedsYou = append(view.NeedsYou, item)
			} else {
				view.InFlight = append(view.InFlight, item)
			}
		}
	}

	// Open decisions are the other half of "your move", and the more urgent
	// half: a Hop waiting on you is usually waiting via one of these.
	if requests, err := s.db.GetInputRequestsByProject(ctx, projectID); err == nil {
		for i := range requests {
			ir := &requests[i]
			if ir.Status == domain.InputRequestStatusResolved {
				continue
			}
			ribbon := domain.DecisionLifecycle(ir)
			item := OverviewItem{
				Kind:  domain.DecisionKindLabel(ir.Kind),
				Title: ir.Title,
				Note:  ribbon.NextAction,
				Href:  fmt.Sprintf("/p/%s/inputs/%s", projectID, ir.ID),
				Tone:  ribbon.Tone,
			}
			if ribbon.WaitingOnYou() {
				// Decisions go first: they are the concrete thing to do, where
				// a Hop is only the place it is happening.
				view.NeedsYou = append([]OverviewItem{item}, view.NeedsYou...)
			} else {
				view.InFlight = append(view.InFlight, item)
			}
		}
	}

	view.Deployment = s.deploymentSummary(ctx, projectID)

	data := map[string]interface{}{
		"Title":     project.Name,
		"ProjectID": projectID.String(),
		"View":      view,
	}
	s.addOpenInputCount(ctx, data)
	s.addProjectReadiness(ctx, data)
	s.addOnboardingRibbon(ctx, data)

	if err := s.renderPageFor(w, r, "overview.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// deploymentSummary reads the project's deployment channel as one line.
//
// The same three facts were rendered three different ways on the strategy page,
// the deployment page and the variation page. Reading them once means they
// cannot disagree.
func (s *Server) deploymentSummary(ctx context.Context, projectID uuid.UUID) DeploymentSummary {
	href := fmt.Sprintf("/p/%s/deployment", projectID)
	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil {
		channel = nil
	}
	deployment, err := s.db.GetCurrentProdDeployment(ctx, projectID)
	if err != nil {
		deployment = nil
	}
	return summarizeDeployment(href, channel, deployment)
}

// summarizeDeployment is the mapping itself, kept free of the database so every
// state it can produce is reachable in a test.
func summarizeDeployment(href string, channel *domain.ProjectDeploymentChannel,
	deployment *domain.HostingDeployment) DeploymentSummary {

	sum := DeploymentSummary{Href: href}

	if channel == nil {
		sum.Status = domain.StatusView{Label: "Not configured", Tone: domain.ToneNeutral}
		return sum
	}

	sum.Configured = true
	platform := "(platform missing)"
	if channel.HostingPlatform != nil {
		platform = channel.HostingPlatform.Name
	}
	sum.Channel = fmt.Sprintf("%s → %s", channel.ArtifactKind, platform)

	if deployment != nil {
		sum.URL = derefStr(deployment.URL)
		sum.ShortCommit = deployment.ShortCommit()
		sum.Status = domain.StatusView{Label: "Live in production", Tone: domain.ToneSuccess}
		return sum
	}

	// Never deployed, so the useful fact is how far validation has got. A
	// configured, validated channel that has simply not shipped yet must not
	// read as unfinished setup — that was the old card's actual bug.
	switch {
	case channel.IsProdValidated():
		sum.Status = domain.StatusView{Label: "Ready to deploy", Tone: domain.ToneSuccess}
	case channel.IsDemoValidated():
		sum.Status = domain.StatusView{Label: "Ready for demos", Tone: domain.ToneSuccess}
	default:
		sum.Status = domain.ValidationStatus(
			channel.IsDemoValidating(), false, derefStr(channel.DemoValidationError))
	}
	return sum
}

// buildProjectRoadmap assembles the roadmap panel with nothing focused, for
// pages about the project as a whole.
func (s *Server) buildProjectRoadmap(ctx context.Context, projectID, strategyID uuid.UUID) *MiniRoadmap {
	hops, edges, err := s.buildRoadmapGraph(ctx, strategyID)
	if err != nil || len(hops) == 0 {
		return nil
	}
	hopsJSON, edgesJSON, err := marshalRoadmap(hops, edges)
	if err != nil {
		return nil
	}
	return &MiniRoadmap{
		ProjectID: projectID.String(),
		HopCount:  len(hops),
		HopsJSON:  hopsJSON,
		EdgesJSON: edgesJSON,
	}
}

// derefStr reads an optional string, treating absent as empty.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
