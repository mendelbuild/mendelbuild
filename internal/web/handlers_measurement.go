package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
)

// The recurring ask for Key Result values.
//
// Mendel polls for these rather than waiting for someone to remember a form,
// because a number nobody enters is a number nobody has. It goes through the
// Input Needed queue, which is what that queue is for: things only a person can
// supply. See dev/claude_plans/15_key_result_measurement.md.

const measurementSubject = "strategy"

// processMeasurementRequests files, updates, or escalates the measurement ask
// for every strategy that has Key Results to measure.
//
// Called on a slow ticker. Everything it does is idempotent, so a restart mid
// cycle repeats work rather than losing or duplicating it.
func (s *Server) processMeasurementRequests() {
	ctx := context.Background()
	now := time.Now()

	candidates, err := s.db.ListStrategyMeasurementCandidates(ctx)
	if err != nil {
		log.Printf("measurement: could not list strategies: %v", err)
		return
	}

	for _, c := range candidates {
		if err := s.syncMeasurementRequest(ctx, c, now); err != nil {
			log.Printf("measurement: strategy %s: %v", c.StrategyID, err)
		}
	}
}

// syncMeasurementRequest brings one strategy's ask into line with the clock.
//
// There is at most one open request per strategy, ever. When the period rolls
// over on an unanswered one it is updated in place rather than joined by a
// second: a project that has ignored the ask for a month should face one
// insistent request, not four polite ones.
func (s *Server) syncMeasurementRequest(ctx context.Context, c db.StrategyMeasurementCandidate, now time.Time) error {
	existing, err := s.db.GetInputRequestBySubjectAndKind(
		ctx, measurementSubject, c.StrategyID, domain.InputRequestKindMeasurement)
	if err != nil {
		return fmt.Errorf("look up existing ask: %w", err)
	}
	open := existing != nil && existing.Status != domain.InputRequestStatusResolved

	// How out of date the project's numbers are, taken from the oldest Key
	// Result rather than the newest: a project is only as measured as its
	// least measured goal.
	staleness, err := s.strategyStaleness(ctx, c.StrategyID, now)
	if err != nil {
		return err
	}

	if open {
		// Already asking. The only thing that changes is how loudly.
		title := measurementTitle(staleness)
		if existing.Title == title && existing.ImportanceScore == domain.MeasurementImportance(staleness.Overdue) {
			return nil
		}
		existing.Title = title
		existing.ImportanceScore = domain.MeasurementImportance(staleness.Overdue)
		return s.db.UpdateInputRequest(ctx, existing)
	}

	if !domain.MeasurementDue(c.AskedAt, now) {
		return nil
	}

	req := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        c.ProjectID,
		Kind:             domain.InputRequestKindMeasurement,
		Title:            measurementTitle(staleness),
		Status:           domain.InputRequestStatusNeedsAssignment,
		ImportanceScore:  domain.MeasurementImportance(staleness.Overdue),
		SubjectType:      strPtr(measurementSubject),
		SubjectID:        &c.StrategyID,
		CreatedAt:        now,
	}
	// Wholly subjective: only a person knows what the number is.
	req.ObjectivityScore = 0

	if err := s.db.CreateInputRequest(ctx, req); err != nil {
		return fmt.Errorf("file the ask: %w", err)
	}
	return s.db.MarkMeasurementsAsked(ctx, c.StrategyID, now)
}

// measurementTitle names the ask, and is the only part of it that escalates.
//
// The tone and the importance score do the rest. A title that shouted from the
// day it was filed would teach people that the queue exaggerates.
func measurementTitle(st domain.MeasurementStaleness) string {
	if st.Overdue {
		return "Key result values are badly out of date"
	}
	return "Record this period's key result values"
}

// strategyStaleness reads the whole strategy as its least measured Key Result.
//
// Taking the newest measurement instead would let one diligently updated goal
// hide five that nobody has touched.
func (s *Server) strategyStaleness(ctx context.Context, strategyID uuid.UUID, now time.Time) (domain.MeasurementStaleness, error) {
	krs, err := s.db.GetKeyResultsByStrategy(ctx, strategyID)
	if err != nil {
		return domain.MeasurementStaleness{}, fmt.Errorf("load key results: %w", err)
	}
	latest, err := s.db.GetLatestMeasurements(ctx, strategyID)
	if err != nil {
		return domain.MeasurementStaleness{}, fmt.Errorf("load measurements: %w", err)
	}

	worst := domain.MeasurementStaleness{Measured: true}
	for _, kr := range krs {
		if kr.TargetDate == nil {
			continue
		}
		var at *time.Time
		if m, ok := latest[kr.ID]; ok {
			at = &m.MeasuredAt
		}
		st := domain.ReadStaleness(at, kr.CreatedAt, now)
		if !st.Measured {
			worst.Measured = false
		}
		if st.Stale {
			worst.Stale = true
		}
		if st.Overdue {
			worst.Overdue = true
		}
		if st.Age > worst.Age {
			worst.Age = st.Age
		}
	}
	return worst, nil
}

// MeasurementRow is one Key Result on the measurement form: its target, where
// it last stood, and how old that reading is.
type MeasurementRow struct {
	KeyResult domain.KeyResult
	Objective string
	Latest    *domain.KeyResultHistory
	Staleness domain.MeasurementStaleness
}

// LastValue renders the previous reading, or empty when there is none. It
// prefills nothing: a form that pre-answers itself collects last week's number
// again from anyone who taps through it.
func (r MeasurementRow) LastValue() string {
	if r.Latest == nil {
		return ""
	}
	return strconv.FormatFloat(r.Latest.MeasuredValue, 'f', -1, 64)
}

// measurementRows assembles the form for a strategy.
func (s *Server) measurementRows(ctx context.Context, strategyID uuid.UUID, now time.Time) ([]MeasurementRow, error) {
	krs, err := s.db.GetKeyResultsByStrategy(ctx, strategyID)
	if err != nil {
		return nil, err
	}
	latest, err := s.db.GetLatestMeasurements(ctx, strategyID)
	if err != nil {
		return nil, err
	}

	rows := make([]MeasurementRow, 0, len(krs))
	for _, kr := range krs {
		row := MeasurementRow{KeyResult: kr}
		var at *time.Time
		if m, ok := latest[kr.ID]; ok {
			measured := m
			row.Latest = &measured
			at = &measured.MeasuredAt
		}
		row.Staleness = domain.ReadStaleness(at, kr.CreatedAt, now)
		rows = append(rows, row)
	}

	// Least measured first: the reader's attention is finite and the stale
	// values are the ones the ask exists to collect.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Staleness.Measured != rows[j].Staleness.Measured {
			return !rows[i].Staleness.Measured
		}
		return rows[i].Staleness.Age > rows[j].Staleness.Age
	})
	return rows, nil
}

// handleRecordMeasurements takes the answered form.
func (s *Server) handleRecordMeasurements(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	requestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid request ID", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	req, err := s.db.GetInputRequest(ctx, requestID)
	if err != nil || req.SubjectID == nil {
		http.Error(w, "request not found", http.StatusNotFound)
		return
	}
	strategyID := *req.SubjectID

	krs, err := s.db.GetKeyResultsByStrategy(ctx, strategyID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	measurements, err := measurementsFromForm(krs, r.Form.Get, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.db.RecordKeyResultMeasurements(ctx, measurements); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Answered, so the ask closes even if some rows were skipped. Leaving it
	// open until every Key Result has a number would make one unmeasurable goal
	// hold the request open forever.
	now := time.Now()
	req.Status = domain.InputRequestStatusResolved
	req.ResolvedAt = &now
	req.Resolution = strPtr(fmt.Sprintf("%d of %d key results measured", len(measurements), len(krs)))
	if err := s.db.UpdateInputRequest(ctx, req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.MarkMeasurementsAsked(ctx, strategyID, now); err != nil {
		log.Printf("measurement: could not record the ask time: %v", err)
	}

	http.Redirect(w, r, fmt.Sprintf("/p/%s/strategy", projectID), http.StatusSeeOther)
}

// measurementsFromForm reads the answered rows.
//
// A blank value is a skip and records nothing. That distinction matters: a Key
// Result nobody could measure this week must not end up recorded as zero, which
// would read as a collapse rather than a gap.
func measurementsFromForm(krs []domain.KeyResult, field func(string) string, now time.Time) ([]domain.KeyResultHistory, error) {
	var out []domain.KeyResultHistory
	for _, kr := range krs {
		prefix := "kr_" + kr.ID.String() + "_"

		var value float64
		if kr.IsBoolean() {
			if field(prefix+"done") == "" {
				continue
			}
			value = 1
		} else {
			raw := strings.TrimSpace(field(prefix + "value"))
			if raw == "" {
				continue
			}
			raw = strings.ReplaceAll(raw, ",", "")
			raw = strings.TrimPrefix(raw, "$")
			parsed, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, fmt.Errorf("%q is not a number, for %q", strings.TrimSpace(field(prefix+"value")), kr.Description)
			}
			value = parsed
		}

		// "As of" defaults to now but may be backdated: someone answering on
		// Thursday often knows Monday's figure, and recording it as Thursday's
		// would put the trend line in the wrong place.
		at := now
		if raw := strings.TrimSpace(field(prefix + "at")); raw != "" {
			parsed, err := time.Parse("2006-01-02", raw)
			if err != nil {
				return nil, fmt.Errorf("%q is not a date, for %q", raw, kr.Description)
			}
			at = parsed
		}

		out = append(out, domain.KeyResultHistory{
			ID:            uuid.New(),
			KeyResultID:   kr.ID,
			MeasuredValue: value,
			MeasuredAt:    at,
			Source:        strPtr("entered by hand"),
		})
	}
	return out, nil
}
