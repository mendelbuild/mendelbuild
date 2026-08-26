package web

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// stripNeighbors caps how many Hops are shown on either side of the current
// one. The strip is a header element, so it has to stay on one line; anything
// beyond the cap is reported as a count with a link to the full roadmap.
const stripNeighbors = 3

// StripHop is one Hop rendered in the roadmap strip.
type StripHop struct {
	ID      uuid.UUID
	Name    string
	Tone    domain.Tone
	Current bool
}

// RoadmapStrip places a Hop among its immediate neighbours in the roadmap DAG.
// It answers "where am I and what is next to me" on pages that would otherwise
// show a Hop with no surrounding context.
type RoadmapStrip struct {
	ProjectID  string
	Before     []StripHop // Hops this one depends on
	Current    StripHop
	After      []StripHop // Hops that depend on this one
	MoreBefore int        // Predecessors not shown because of the cap
	MoreAfter  int        // Successors not shown because of the cap
}

// HasNeighbors reports whether there is anything to show besides the current
// Hop. An isolated Hop renders no strip at all rather than a lonely single node.
func (r *RoadmapStrip) HasNeighbors() bool {
	return r != nil && (len(r.Before) > 0 || len(r.After) > 0 || r.MoreBefore > 0 || r.MoreAfter > 0)
}

// buildRoadmapStrip assembles the neighbourhood around hop. It returns nil if
// the surrounding Hops cannot be loaded; the strip is contextual, so failing to
// build it must never fail the page.
func (s *Server) buildRoadmapStrip(ctx context.Context, projectID uuid.UUID, hop *domain.Hop) *RoadmapStrip {
	if hop == nil {
		return nil
	}

	all, err := s.db.GetHopsByStrategy(ctx, hop.StrategyID)
	if err != nil {
		return nil
	}
	byID := make(map[uuid.UUID]domain.Hop, len(all))
	for _, h := range all {
		byID[h.ID] = h
	}

	strip := &RoadmapStrip{
		ProjectID: projectID.String(),
		Current:   stripHopFrom(hop, true),
	}

	// Predecessors: the Hops this one depends on.
	predIDs, _ := s.db.GetHopDependsOn(ctx, hop.ID)
	strip.Before, strip.MoreBefore = resolveStripHops(predIDs, byID)

	// Successors: the Hops that depend on this one. GetHopDependencies selects
	// rows where depends_on_hop_id is this Hop, so the dependent side is HopID.
	deps, _ := s.db.GetHopDependencies(ctx, hop.ID)
	succIDs := make([]uuid.UUID, 0, len(deps))
	for _, d := range deps {
		succIDs = append(succIDs, d.HopID)
	}
	strip.After, strip.MoreAfter = resolveStripHops(succIDs, byID)

	return strip
}

// resolveStripHops turns Hop IDs into renderable entries, sorted by name for a
// stable order, and truncated to the neighbour cap.
func resolveStripHops(ids []uuid.UUID, byID map[uuid.UUID]domain.Hop) ([]StripHop, int) {
	out := make([]StripHop, 0, len(ids))
	for _, id := range ids {
		h, ok := byID[id]
		if !ok {
			continue
		}
		hop := h
		out = append(out, stripHopFrom(&hop, false))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	if len(out) > stripNeighbors {
		return out[:stripNeighbors], len(out) - stripNeighbors
	}
	return out, 0
}

func stripHopFrom(h *domain.Hop, current bool) StripHop {
	return StripHop{
		ID:      h.ID,
		Name:    h.Name,
		Tone:    domain.HopLifecycle(h, nil).Tone,
		Current: current,
	}
}
