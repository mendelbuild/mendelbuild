package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// Key Result measurements.
//
// key_result_history existed from the first schema and nothing ever wrote to
// it, so no Key Result could say where it stood. These are the read and write
// sides of that. See dev/claude_plans/15_key_result_measurement.md.

// RecordKeyResultMeasurements writes a set of measurements in one transaction.
//
// One call per answered request rather than one per value: the reader supplied
// them together and they describe the same moment in the project, so a partial
// write would leave a record nobody intended.
//
// Recording the same Key Result at the same instant twice updates rather than
// duplicates. That instant is the reader's "as of", not the clock, so a
// resubmission with a corrected figure is a correction and not a second
// reading.
func (db *DB) RecordKeyResultMeasurements(ctx context.Context, ms []domain.KeyResultHistory) error {
	if len(ms) == 0 {
		return nil
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, m := range ms {
		if m.ID == uuid.Nil {
			m.ID = uuid.New()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO key_result_history (id, key_result_id, measured_value, measured_at, source)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (key_result_id, measured_at) DO UPDATE SET
				measured_value = EXCLUDED.measured_value,
				source = EXCLUDED.source
		`, m.ID, m.KeyResultID, m.MeasuredValue, m.MeasuredAt, m.Source); err != nil {
			return fmt.Errorf("record measurement for key result %s: %w", m.KeyResultID, err)
		}
	}
	return tx.Commit(ctx)
}

// GetLatestMeasurements returns the most recent measurement for each Key Result
// in a strategy, keyed by Key Result ID.
//
// A Key Result with no measurement is absent from the map rather than present
// with a zero: never measured and measured as zero are different facts, and a
// map that conflated them would make the timeline claim the wrong one.
func (db *DB) GetLatestMeasurements(ctx context.Context, strategyID uuid.UUID) (map[uuid.UUID]domain.KeyResultHistory, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT ON (h.key_result_id)
		       h.id, h.key_result_id, h.measured_value, h.measured_at, h.source
		FROM key_result_history h
		JOIN key_results kr ON kr.id = h.key_result_id
		WHERE kr.strategy_id = $1 AND kr.deleted_at IS NULL
		ORDER BY h.key_result_id, h.measured_at DESC
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[uuid.UUID]domain.KeyResultHistory)
	for rows.Next() {
		var m domain.KeyResultHistory
		if err := rows.Scan(&m.ID, &m.KeyResultID, &m.MeasuredValue, &m.MeasuredAt, &m.Source); err != nil {
			return nil, err
		}
		out[m.KeyResultID] = m
	}
	return out, rows.Err()
}

// GetKeyResultHistory returns every measurement for one Key Result, oldest
// first, which is the order a trend is read in.
func (db *DB) GetKeyResultHistory(ctx context.Context, keyResultID uuid.UUID) ([]domain.KeyResultHistory, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, key_result_id, measured_value, measured_at, source
		FROM key_result_history
		WHERE key_result_id = $1
		ORDER BY measured_at
	`, keyResultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.KeyResultHistory
	for rows.Next() {
		var m domain.KeyResultHistory
		if err := rows.Scan(&m.ID, &m.KeyResultID, &m.MeasuredValue, &m.MeasuredAt, &m.Source); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetKeyResultsByStrategy returns a strategy's live Key Results, together with
// the Objective each belongs to.
//
// The Objective comes along because a Key Result is read in its company: a list
// of eight bare targets says much less than the same eight grouped under what
// they are for.
func (db *DB) GetKeyResultsByStrategy(ctx context.Context, strategyID uuid.UUID) ([]domain.KeyResult, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, description, target_comparator, target_value, target_unit,
		       target_date, tune_score, tune_feedback, deleted_at, created_at, updated_at
		FROM key_results
		WHERE strategy_id = $1 AND deleted_at IS NULL
		ORDER BY created_at
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.KeyResult
	for rows.Next() {
		var kr domain.KeyResult
		if err := rows.Scan(&kr.ID, &kr.StrategyID, &kr.Description,
			&kr.TargetComparator, &kr.TargetValue, &kr.TargetUnit,
			&kr.TargetDate, &kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt,
			&kr.CreatedAt, &kr.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, kr)
	}
	return out, rows.Err()
}

// MarkMeasurementsAsked records that a strategy has been asked for its values.
func (db *DB) MarkMeasurementsAsked(ctx context.Context, strategyID uuid.UUID, at time.Time) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE strategies SET measurements_asked_at = $2, updated_at = NOW() WHERE id = $1`,
		strategyID, at)
	return err
}

// StrategyMeasurementCandidate is a strategy that could be asked for values,
// with what the decision needs: when it was last asked, and whether it has any
// Key Results to ask about.
type StrategyMeasurementCandidate struct {
	StrategyID     uuid.UUID
	ProjectID      uuid.UUID
	AskedAt        *time.Time
	KeyResultCount int
}

// ListStrategyMeasurementCandidates returns every approved strategy with at
// least one Key Result carrying a target date.
//
// The target date is the filter, not decoration: a Key Result with no date to
// be judged by cannot be on track or behind, so asking for its value produces a
// number with nothing to compare it against.
func (db *DB) ListStrategyMeasurementCandidates(ctx context.Context) ([]StrategyMeasurementCandidate, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT s.id, s.project_id, s.measurements_asked_at, COUNT(kr.id)
		FROM strategies s
		JOIN key_results kr ON kr.strategy_id = s.id
		WHERE s.okrs_approved_at IS NOT NULL
		  AND kr.deleted_at IS NULL
		  AND kr.target_date IS NOT NULL
		GROUP BY s.id, s.project_id, s.measurements_asked_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StrategyMeasurementCandidate
	for rows.Next() {
		var c StrategyMeasurementCandidate
		if err := rows.Scan(&c.StrategyID, &c.ProjectID, &c.AskedAt, &c.KeyResultCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
