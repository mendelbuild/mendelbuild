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

	// FocusVariationID is set on a Variation page and empty on a Hop page. It
	// is what the panel fills with the accent colour, and what it scrolls to
	// when the Hop has more Variations than fit.
	FocusVariationID string

	HopCount  int
	HopsJSON  template.JS
	EdgesJSON template.JS
}

// HasContext reports whether the panel is worth drawing. A roadmap of one Hop
// tells the reader nothing they cannot see from the page they are already on.
func (m *MiniRoadmap) HasContext() bool { return m != nil && m.HopCount > 1 }

// buildMiniRoadmap assembles the roadmap payload focused on hop, and on one of
// its Variations when the reader is looking at one (pass uuid.Nil otherwise).
// It returns nil if the graph cannot be loaded; the panel is contextual, so
// failing to build it must never fail the page.
func (s *Server) buildMiniRoadmap(ctx context.Context, projectID uuid.UUID, hop *domain.Hop, focusVariationID uuid.UUID) *MiniRoadmap {
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

	focusVariation := ""
	if focusVariationID != uuid.Nil {
		focusVariation = focusVariationID.String()
	}

	return &MiniRoadmap{
		ProjectID:        projectID.String(),
		FocusHopID:       hop.ID.String(),
		FocusVariationID: focusVariation,
		HopCount:         len(hops),
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

// PageAction is one thing the reader can do, rendered into the ribbon's foot.
//
// The point of routing actions through here is that a page then has exactly one
// place to look for "what can I do". The old Variation page scattered its
// buttons across a details table, a sidebar card, and a demo panel, so the
// answer depended on where you happened to be reading.
type PageAction struct {
	Label string

	// Exactly one of Href or Post. Href is a link; Post submits a form, which
	// is what anything with a side effect must be.
	Href string
	Post string

	// Role picks the button treatment: "primary", "secondary", or "danger".
	// There must be at most one primary in a ribbon — it is the thing the page
	// wants you to do.
	Role string

	// Confirm, when set, is the message shown before a Post is sent.
	Confirm string

	// Note is a short clause explaining the action, shown beside it. Use it
	// when the label alone would be ambiguous or alarming.
	Note string
}

// RibbonView is a Ribbon plus the actions the page offers on it.
//
// It embeds domain.Ribbon, so the partial reads Headline, Tone, and Tracks
// exactly as before; the wrapper adds only what the domain has no business
// knowing, namely URLs.
type RibbonView struct {
	domain.Ribbon
	Actions []PageAction
}

// ribbonView wraps a Ribbon with the actions a page offers.
func ribbonView(r domain.Ribbon, actions ...PageAction) *RibbonView {
	return &RibbonView{Ribbon: r, Actions: actions}
}
