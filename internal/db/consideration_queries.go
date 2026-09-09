package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// DraftConsideration is one Strategic Consideration as the first drafting pass
// wrote it, before it has an identity or a coverage decision.
type DraftConsideration struct {
	Kind      string
	Statement string
}

// ConsiderationCoverage records what the drafting pass decided about one
// consideration: the objective made responsible for it, or the reason none was.
//
// Both empty is allowed and means the pass did not say. That is a real third
// state -- the model answered without mentioning this one -- and it is recorded
// as silence rather than converted into a decline, because inventing a reason
// the agent never gave would put words in its mouth on the screen where the user
// decides whether to argue with it.
type ConsiderationCoverage struct {
	ConsiderationID uuid.UUID
	ObjectiveID     *uuid.UUID
	UncoveredReason string
}

// ReplaceStrategicConsiderations swaps a strategy's considerations for a freshly
// drawn set, returning their ids in the order given.
//
// A redraft replaces them rather than accumulating: the first pass is given the
// previous list and asked to refine it, so what comes back is the whole answer
// and not an addition to it. Coverage is not set here -- the second pass has not
// run yet -- so every row lands unjudged.
func (db *DB) ReplaceStrategicConsiderations(ctx context.Context, strategyID uuid.UUID,
	considerations []DraftConsideration) ([]uuid.UUID, error) {

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`DELETE FROM strategic_considerations WHERE strategy_id = $1`, strategyID); err != nil {
		return nil, fmt.Errorf("clear considerations: %w", err)
	}

	now := time.Now()
	ids := make([]uuid.UUID, 0, len(considerations))
	for i, c := range considerations {
		id := uuid.New()
		ids = append(ids, id)
		if _, err := tx.Exec(ctx, `
			INSERT INTO strategic_considerations (id, strategy_id, kind, statement, position, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6)
		`, id, strategyID, c.Kind, c.Statement, i, now); err != nil {
			return nil, fmt.Errorf("insert consideration: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}

// SetConsiderationCoverage records what the drafting pass decided about each
// consideration it was given.
//
// Rows not named here keep whatever they had, which after
// ReplaceStrategicConsiderations is nothing. A consideration the agent never
// mentioned therefore stays unjudged rather than becoming a silent decline.
func (db *DB) SetConsiderationCoverage(ctx context.Context, coverage []ConsiderationCoverage) error {
	if len(coverage) == 0 {
		return nil
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, c := range coverage {
		var reason *string
		if c.ObjectiveID == nil && c.UncoveredReason != "" {
			r := c.UncoveredReason
			reason = &r
		}
		if _, err := tx.Exec(ctx, `
			UPDATE strategic_considerations
			SET covered_by_objective_id = $2, uncovered_reason = $3, updated_at = NOW()
			WHERE id = $1
		`, c.ConsiderationID, c.ObjectiveID, reason); err != nil {
			return fmt.Errorf("set coverage: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// GetStrategicConsiderations returns a strategy's considerations in the order
// the drafting pass produced them.
func (db *DB) GetStrategicConsiderations(ctx context.Context, strategyID uuid.UUID) ([]domain.StrategicConsideration, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, kind, statement, covered_by_objective_id,
		       uncovered_reason, position, created_at, updated_at
		FROM strategic_considerations
		WHERE strategy_id = $1
		ORDER BY position
	`, strategyID)
	if err != nil {
		return nil, fmt.Errorf("query considerations: %w", err)
	}
	defer rows.Close()

	var out []domain.StrategicConsideration
	for rows.Next() {
		var c domain.StrategicConsideration
		if err := rows.Scan(&c.ID, &c.StrategyID, &c.Kind, &c.Statement,
			&c.CoveredByObjectiveID, &c.UncoveredReason, &c.Position,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan consideration: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
