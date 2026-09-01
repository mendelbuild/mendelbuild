package web

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// The OKR timeline: one row per Key Result on a shared time axis, grouped under
// the Objective it serves.
//
// The reading it draws is one comparison made visible. A bar's fill is how much
// of the goal is done and the line down the page is how much of the time is
// gone, so fill ahead of the line is on track. Two unlike quantities are made
// comparable by putting them on the same track -- the trick the budget meter
// already uses, so it is a reading people will have seen before.
//
// See dev/claude_plans/15_key_result_measurement.md.

// TimelineView is the whole panel.
type TimelineView struct {
	Start, End time.Time
	Months     []TimelineTick
	// TodayPercent positions the line, clamped into the window so a project
	// whose deadlines have all passed still draws.
	TodayPercent float64
	Objectives   []TimelineObjective
	Attainment   domain.Attainment
}

// HasRows reports whether the panel is worth drawing. A strategy whose Key
// Results carry no dates has no axis to draw them on.
func (v *TimelineView) HasRows() bool {
	if v == nil {
		return false
	}
	for _, o := range v.Objectives {
		if len(o.Rows) > 0 {
			return true
		}
	}
	return false
}

// TimelineTick is a month label on the axis.
type TimelineTick struct {
	Label   string
	Percent float64
}

// TimelineObjective is one group: the Objective, the Hops working on it, and
// its Key Result rows.
type TimelineObjective struct {
	Description string
	Hops        []TimelineHop
	Rows        []TimelineRow
}

// TimelineHop is a pill.
//
// Hops hang off the Objective rather than off individual Key Results, because
// that is the only link the data records and because some Hops genuinely serve
// the whole Objective. They sit in the label column rather than over the axis:
// a Hop has no date at all -- DESIGN.md section 2.1 sequences them by
// dependency, not by the calendar -- and anything laid across a time axis reads
// as positioned in time.
type TimelineHop struct {
	Name   string
	Href   string
	Status domain.StatusView
}

// TimelineRow is one Key Result.
type TimelineRow struct {
	KeyResult domain.KeyResult
	Reading   domain.KeyResultReading

	// Where the bar sits on the axis, and how much of it is filled. All three
	// are percentages, because the axis is fluid and the server cannot know how
	// wide it will be drawn.
	LeftPercent  float64
	WidthPercent float64
	FillPercent  float64
}

// FillTone is the bar's colour class, empty when there is nothing to draw.
func (r TimelineRow) FillTone() string { return string(r.Reading.FillTone()) }

// buildTimeline assembles the panel for a strategy, or nil when there is
// nothing to draw.
func (s *Server) buildTimeline(ctx context.Context, projectID, strategyID uuid.UUID, now time.Time) *TimelineView {
	objectives, err := s.db.GetObjectivesByStrategy(ctx, strategyID)
	if err != nil || len(objectives) == 0 {
		return nil
	}
	first, err := s.db.GetFirstMeasurements(ctx, strategyID)
	if err != nil {
		return nil
	}
	latest, err := s.db.GetLatestMeasurements(ctx, strategyID)
	if err != nil {
		return nil
	}
	hopsByObjective := s.hopsByObjective(ctx, projectID, strategyID)

	// The window spans from the earliest Key Result to the last deadline. Only
	// dated Key Results appear: one with no deadline cannot be behind, because
	// there is nothing for it to be behind.
	var start, end time.Time
	type pending struct {
		objective domain.Objective
		krs       []domain.KeyResult
	}
	var groups []pending
	for _, obj := range objectives {
		krs, err := s.db.GetKeyResultsByObjective(ctx, obj.ID)
		if err != nil {
			continue
		}
		var dated []domain.KeyResult
		for _, kr := range krs {
			if kr.TargetDate == nil {
				continue
			}
			dated = append(dated, kr)
			if start.IsZero() || kr.CreatedAt.Before(start) {
				start = kr.CreatedAt
			}
			if end.IsZero() || kr.TargetDate.After(end) {
				end = *kr.TargetDate
			}
		}
		groups = append(groups, pending{obj, dated})
	}
	if start.IsZero() || !end.After(start) {
		return nil
	}

	view := &TimelineView{Start: start, End: end}
	span := end.Sub(start).Seconds()
	pos := func(t time.Time) float64 {
		return clampPercent(t.Sub(start).Seconds() / span * 100)
	}
	view.TodayPercent = pos(now)
	view.Months = monthTicks(start, end, now, pos)

	var readings []domain.KeyResultReading
	for _, g := range groups {
		to := TimelineObjective{
			Description: g.objective.Description,
			Hops:        hopsByObjective[g.objective.ID],
		}
		for _, kr := range g.krs {
			var f, l *domain.KeyResultHistory
			if m, ok := first[kr.ID]; ok {
				v := m
				f = &v
			}
			if m, ok := latest[kr.ID]; ok {
				v := m
				l = &v
			}
			reading := domain.ReadKeyResult(kr, f, l, now)
			readings = append(readings, reading)

			left, right := pos(kr.CreatedAt), pos(*kr.TargetDate)
			row := TimelineRow{
				KeyResult:    kr,
				Reading:      reading,
				LeftPercent:  left,
				WidthPercent: right - left,
			}
			if reading.HasProgress {
				row.FillPercent = reading.Progress * 100
			}
			to.Rows = append(to.Rows, row)
		}
		if len(to.Rows) > 0 || len(to.Hops) > 0 {
			view.Objectives = append(view.Objectives, to)
		}
	}

	view.Attainment = domain.ReadAttainment(readings)
	if !view.HasRows() {
		return nil
	}
	return view
}

// hopsByObjective groups a strategy's Hops by the Objectives they serve.
func (s *Server) hopsByObjective(ctx context.Context, projectID, strategyID uuid.UUID) map[uuid.UUID][]TimelineHop {
	out := map[uuid.UUID][]TimelineHop{}
	hops, err := s.db.GetHopsByStrategy(ctx, strategyID)
	if err != nil {
		return out
	}
	for i := range hops {
		hop := &hops[i]
		if hop.Params == nil {
			continue
		}
		var params struct {
			ObjectiveIDs []string `json:"objective_ids"`
		}
		if json.Unmarshal(hop.Params, &params) != nil {
			continue
		}
		// Variations are not loaded: a pill has room for a Hop's position, not
		// for the detail that distinguishes "building three" from "building
		// one", and the Hop's own page says that in full.
		pill := TimelineHop{
			Name:   hop.Name,
			Href:   fmt.Sprintf("/p/%s/hops/%s", projectID, hop.ID),
			Status: domain.HopStatusView(hop),
		}
		for _, raw := range params.ObjectiveIDs {
			if id, err := uuid.Parse(raw); err == nil {
				out[id] = append(out[id], pill)
			}
		}
	}
	return out
}

// monthTicks labels the axis, skipping the month the "today" marker lands on so
// the two labels do not overprint each other.
func monthTicks(start, end, now time.Time, pos func(time.Time) float64) []TimelineTick {
	var out []TimelineTick
	d := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, start.Location())
	if d.Before(start) {
		d = d.AddDate(0, 1, 0)
	}
	for !d.After(end) {
		if d.Year() != now.Year() || d.Month() != now.Month() {
			out = append(out, TimelineTick{Label: d.Format("Jan"), Percent: pos(d)})
		}
		d = d.AddDate(0, 1, 0)
	}
	return out
}

func clampPercent(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 100:
		return 100
	default:
		return f
	}
}
