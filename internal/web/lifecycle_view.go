package web

import (
	"context"
	"encoding/json"
	"html/template"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// MiniRoadmap is the project roadmap, embedded on a page about one Hop and
// scrolled to that Hop.
//
// It carries the same payload the full roadmap page does and is drawn by the
// same JavaScript renderer. Showing a reduced, bespoke summary of the graph was
// the obvious alternative and the wrong one: a second drawing of the roadmap
// would drift from the real one, and the shape of the graph — how much is
// upstream, how much is still ahead — is most of what the panel is for.
type MiniRoadmap struct {
	ProjectID  string
	FocusHopID string
	HopCount   int
	HopsJSON   template.JS
	EdgesJSON  template.JS
}

// HasContext reports whether the panel is worth drawing. A roadmap of one Hop
// tells the reader nothing they cannot see from the page they are already on.
func (m *MiniRoadmap) HasContext() bool { return m != nil && m.HopCount > 1 }

// buildMiniRoadmap assembles the roadmap payload focused on hop. It returns nil
// if the graph cannot be loaded; the panel is contextual, so failing to build it
// must never fail the page.
func (s *Server) buildMiniRoadmap(ctx context.Context, projectID uuid.UUID, hop *domain.Hop) *MiniRoadmap {
	if hop == nil {
		return nil
	}

	hops, edges, err := s.buildRoadmapGraph(ctx, hop.StrategyID)
	if err != nil || len(hops) == 0 {
		return nil
	}

	hopsJSON, err := json.Marshal(hops)
	if err != nil {
		return nil
	}
	edgesJSON, err := json.Marshal(edges)
	if err != nil {
		return nil
	}

	return &MiniRoadmap{
		ProjectID:  projectID.String(),
		FocusHopID: hop.ID.String(),
		HopCount:   len(hops),
		// template.JS, not string: html/template escapes strings in a script
		// context and would corrupt the JSON. See CLAUDE.md.
		HopsJSON:  template.JS(hopsJSON),
		EdgesJSON: template.JS(edgesJSON),
	}
}

// buildRoadmapGraph loads every Hop in a strategy, its Variations, and the
// dependency edges between them, in the shape the roadmap renderer expects.
//
// Hops with no Variations yet fall back to the proposed Variations sitting in an
// unresolved review, so a Hop that is mid-proposal does not read as empty.
func (s *Server) buildRoadmapGraph(ctx context.Context, strategyID uuid.UUID) ([]RoadmapHopView, []RoadmapEdge, error) {
	hops, err := s.db.GetHopsByStrategy(ctx, strategyID)
	if err != nil {
		return nil, nil, err
	}

	hopViews := make([]RoadmapHopView, 0, len(hops))
	for _, hop := range hops {
		variations, _ := s.db.GetVariationsByHop(ctx, hop.ID)
		varViews := make([]RoadmapVariationView, 0)

		if len(variations) > 0 {
			for _, v := range variations {
				varViews = append(varViews, RoadmapVariationView{
					ID:     v.ID.String(),
					Name:   v.Name,
					Status: string(v.Status),
				})
			}
		} else {
			varViews = append(varViews, proposedVariations(ctx, s, hop.ID)...)
		}

		hopViews = append(hopViews, RoadmapHopView{
			ID:         hop.ID.String(),
			Name:       hop.Name,
			Status:     string(hop.Status),
			Variations: varViews,
		})
	}

	deps, err := s.db.GetHopDependenciesByStrategy(ctx, strategyID)
	if err != nil {
		return nil, nil, err
	}
	edges := make([]RoadmapEdge, 0, len(deps))
	for _, d := range deps {
		edges = append(edges, RoadmapEdge{
			From: d.DependsOnHopID.String(),
			To:   d.HopID.String(),
		})
	}

	return hopViews, edges, nil
}

// proposedVariations reads the Variations named in a Hop's open variation review,
// which exist only inside the request's details until the review is approved.
func proposedVariations(ctx context.Context, s *Server, hopID uuid.UUID) []RoadmapVariationView {
	ir, err := s.db.GetInputRequestBySubjectAndKind(ctx, "hop", hopID, domain.InputRequestKindVariationReview)
	if err != nil || ir == nil || ir.Status == domain.InputRequestStatusResolved || ir.Details == nil {
		return nil
	}

	var proposal struct {
		Variations []struct {
			Name string `json:"name"`
		} `json:"variations"`
	}
	if json.Unmarshal([]byte(*ir.Details), &proposal) != nil {
		return nil
	}

	out := make([]RoadmapVariationView, 0, len(proposal.Variations))
	for _, v := range proposal.Variations {
		out = append(out, RoadmapVariationView{
			ID:     "", // No ID yet, so not clickable.
			Name:   v.Name,
			Status: "proposed",
		})
	}
	return out
}
