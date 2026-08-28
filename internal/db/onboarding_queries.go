package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CreateProjectWithStrategy creates a new project and its top-level strategy in
// one transaction, recording the brief the user wrote.
//
// The project exists before any agent runs, deliberately: the drafting call's
// spend has to be filed against a real strategy, and an agent charge that
// cannot be attributed makes the project's cost understate itself.
func (db *DB) CreateProjectWithStrategy(ctx context.Context, name, brief, strategyName string, ownerID *uuid.UUID) (projectID, strategyID uuid.UUID, err error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	now := time.Now()
	projectID = uuid.New()
	strategyID = uuid.New()

	if _, err = tx.Exec(ctx, `
		INSERT INTO projects (id, name, brief, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)
	`, projectID, name, brief, now); err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("insert project: %w", err)
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO strategies (id, project_id, name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)
	`, strategyID, projectID, strategyName, now); err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("insert strategy: %w", err)
	}

	if ownerID != nil {
		if _, err = tx.Exec(ctx, `
			INSERT INTO project_members (id, project_id, user_id, role, created_at)
			VALUES ($1, $2, $3, 'owner', $4)
		`, uuid.New(), projectID, *ownerID, now); err != nil {
			return uuid.Nil, uuid.Nil, fmt.Errorf("insert owner: %w", err)
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("commit: %w", err)
	}
	return projectID, strategyID, nil
}

// RenameStrategy updates a strategy's name, which the drafting agent picks.
func (db *DB) RenameStrategy(ctx context.Context, strategyID uuid.UUID, name string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE strategies SET name = $2, updated_at = NOW() WHERE id = $1
	`, strategyID, name)
	return err
}

// SetStrategyDraftNotes stores the drafting agent's commentary on its draft.
func (db *DB) SetStrategyDraftNotes(ctx context.Context, strategyID uuid.UUID, notes *domain.StrategyDraftNotes) error {
	encoded, err := json.Marshal(notes)
	if err != nil {
		return fmt.Errorf("marshal draft notes: %w", err)
	}
	_, err = db.Pool.Exec(ctx, `
		UPDATE strategies SET draft_notes = $2, updated_at = NOW() WHERE id = $1
	`, strategyID, encoded)
	return err
}

// ApproveOKRs records that a human validated this strategy's OKRs.
func (db *DB) ApproveOKRs(ctx context.Context, strategyID uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE strategies SET okrs_approved_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND okrs_approved_at IS NULL
	`, strategyID)
	return err
}

// ReplaceDraftOKRs swaps a strategy's entire OKR set for a freshly drafted one.
//
// Only safe on an unapproved strategy, which the caller must check: it hard
// deletes the objectives and key results it replaces rather than soft deleting
// them, because a draft nobody has looked at is not history worth keeping and
// leaving tombstones behind would clutter the OKR editor from day one.
func (db *DB) ReplaceDraftOKRs(ctx context.Context, strategyID uuid.UUID, objectives []DraftObjective) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		DELETE FROM objective_key_result_pairs
		WHERE objective_id IN (SELECT id FROM objectives WHERE strategy_id = $1)
	`, strategyID); err != nil {
		return fmt.Errorf("clear okr links: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM funding_success_criteria
		WHERE key_result_id IN (SELECT id FROM key_results WHERE strategy_id = $1)
	`, strategyID); err != nil {
		return fmt.Errorf("clear funding links: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM key_results WHERE strategy_id = $1`, strategyID); err != nil {
		return fmt.Errorf("clear key results: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM objectives WHERE strategy_id = $1`, strategyID); err != nil {
		return fmt.Errorf("clear objectives: %w", err)
	}

	now := time.Now()
	for _, obj := range objectives {
		objID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO objectives (id, strategy_id, description, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)
		`, objID, strategyID, obj.Description, now); err != nil {
			return fmt.Errorf("insert objective: %w", err)
		}

		for _, kr := range obj.KeyResults {
			krID := uuid.New()
			if _, err := tx.Exec(ctx, `
				INSERT INTO key_results (id, strategy_id, description, target_units, target_date, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $6)
			`, krID, strategyID, kr.Description, kr.TargetUnits, kr.TargetDate, now); err != nil {
				return fmt.Errorf("insert key result: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO objective_key_result_pairs (objective_id, key_result_id, created_at)
				VALUES ($1, $2, $3)
			`, objID, krID, now); err != nil {
				return fmt.Errorf("link key result: %w", err)
			}
		}
	}

	return tx.Commit(ctx)
}

// DraftObjective is one objective and its key results, as written by the
// drafting agent or edited by the user on the review screen.
type DraftObjective struct {
	Description string
	KeyResults  []DraftKeyResult
}

// DraftKeyResult is one key result in a draft.
type DraftKeyResult struct {
	Description string
	TargetUnits string
	TargetDate  *time.Time
}

// GetOnboardingState gathers everything the setup ribbon needs for a project.
//
// Every field but OKRsApproved is derived from rows that already exist, so the
// ribbon cannot claim a stage the project has not actually reached.
func (db *DB) GetOnboardingState(ctx context.Context, projectID uuid.UUID) (domain.OnboardingState, error) {
	var st domain.OnboardingState

	err := db.Pool.QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM strategies s WHERE s.project_id = $1),
			EXISTS (SELECT 1 FROM objectives o
			        JOIN strategies s ON s.id = o.strategy_id
			        WHERE s.project_id = $1 AND o.deleted_at IS NULL),
			EXISTS (SELECT 1 FROM strategies s
			        WHERE s.project_id = $1 AND s.okrs_approved_at IS NOT NULL),
			EXISTS (SELECT 1 FROM input_requests ir
			        WHERE ir.project_id = $1 AND ir.kind = 'roadmap_review'
			          AND ir.status <> 'resolved'),
			EXISTS (SELECT 1 FROM hops h
			        JOIN strategies s ON s.id = h.strategy_id
			        WHERE s.project_id = $1),
			EXISTS (SELECT 1 FROM variations v
			        JOIN hops h ON h.id = v.hop_id
			        JOIN strategies s ON s.id = h.strategy_id
			        WHERE s.project_id = $1),
			EXISTS (SELECT 1 FROM repositories r
			        WHERE r.project_id = $1
			          AND COALESCE(r.url, '') <> ''
			          AND COALESCE(r.config->>'auth_token', '') <> '')
	`, projectID).Scan(&st.HasStrategy, &st.HasDraftOKRs, &st.OKRsApproved,
		&st.RoadmapPending, &st.HasHops, &st.HasVariations, &st.RepoConnected)
	if err != nil {
		return st, fmt.Errorf("load onboarding state: %w", err)
	}
	return st, nil
}

// FindOpenInputRequestByKind returns the oldest unresolved input request of a
// given kind for a project, or nil when there is none.
func (db *DB) FindOpenInputRequestByKind(ctx context.Context, projectID uuid.UUID, kind domain.InputRequestKind) (*domain.InputRequest, error) {
	var id uuid.UUID
	err := db.Pool.QueryRow(ctx, `
		SELECT id FROM input_requests
		WHERE project_id = $1 AND kind = $2 AND status <> 'resolved'
		ORDER BY created_at
		LIMIT 1
	`, projectID, kind).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return db.GetInputRequest(ctx, id)
}
