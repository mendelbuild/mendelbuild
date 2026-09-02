package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/experiment"
)

// --- Experiments ---

// CreateExperiment records a Hop's intention to take live traffic.
func (db *DB) CreateExperiment(ctx context.Context, e *domain.Experiment) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.Status == "" {
		e.Status = domain.ExperimentDraft
	}
	return db.Pool.QueryRow(ctx, `
		INSERT INTO experiments (
			id, project_id, hop_id, assignment_unit, assignment_key_source,
			assignment_key_name, status, minimum_detectable_effect, stopping_rule,
			planned_duration_hours, dissonance_description, dissonance_phrase
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING created_at, updated_at
	`, e.ID, e.ProjectID, e.HopID, e.AssignmentUnit, e.AssignmentKeySource,
		e.AssignmentKeyName, e.Status, e.MinimumDetectableEffect, e.StoppingRule,
		e.PlannedDurationHours, e.DissonanceDescription, e.DissonancePhrase,
	).Scan(&e.CreatedAt, &e.UpdatedAt)
}

const experimentColumns = `
	id, project_id, hop_id, assignment_unit, assignment_key_source,
	assignment_key_name, status, minimum_detectable_effect, stopping_rule,
	planned_duration_hours, dissonance_description, dissonance_phrase,
	acknowledged_by, acknowledged_at, started_at, stopped_at, created_at, updated_at`

func scanExperiment(row pgx.Row) (*domain.Experiment, error) {
	var e domain.Experiment
	err := row.Scan(&e.ID, &e.ProjectID, &e.HopID, &e.AssignmentUnit, &e.AssignmentKeySource,
		&e.AssignmentKeyName, &e.Status, &e.MinimumDetectableEffect, &e.StoppingRule,
		&e.PlannedDurationHours, &e.DissonanceDescription, &e.DissonancePhrase,
		&e.AcknowledgedBy, &e.AcknowledgedAt, &e.StartedAt, &e.StoppedAt,
		&e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// GetExperiment returns an experiment with its Arms, or nil when there is none.
func (db *DB) GetExperiment(ctx context.Context, id uuid.UUID) (*domain.Experiment, error) {
	e, err := scanExperiment(db.Pool.QueryRow(ctx,
		`SELECT `+experimentColumns+` FROM experiments WHERE id = $1`, id))
	if err != nil || e == nil {
		return nil, err
	}
	// Loaded together because an experiment without its Arms cannot answer the
	// only question anyone asks of it -- who is being compared with whom.
	e.Arms, err = db.GetExperimentArms(ctx, e.ID)
	return e, err
}

// GetExperimentForHop returns the experiment attached to a Hop, if any.
func (db *DB) GetExperimentForHop(ctx context.Context, hopID uuid.UUID) (*domain.Experiment, error) {
	e, err := scanExperiment(db.Pool.QueryRow(ctx,
		`SELECT `+experimentColumns+` FROM experiments WHERE hop_id = $1
		 ORDER BY created_at DESC LIMIT 1`, hopID))
	if err != nil || e == nil {
		return nil, err
	}
	e.Arms, err = db.GetExperimentArms(ctx, e.ID)
	return e, err
}

// SetExperimentStatus moves an experiment, stamping the timing that goes with
// the move so the two cannot disagree.
func (db *DB) SetExperimentStatus(ctx context.Context, id uuid.UUID, status domain.ExperimentStatus) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE experiments SET
			status = $2,
			started_at = CASE WHEN $2 = 'running' AND started_at IS NULL THEN NOW() ELSE started_at END,
			stopped_at = CASE WHEN $2 IN ('stopped','promoted') THEN NOW() ELSE stopped_at END,
			updated_at = NOW()
		WHERE id = $1
	`, id, status)
	return err
}

// AcknowledgeDissonance records the phrase the Mendel user typed.
//
// Refuses a phrase that does not match, rather than recording an approximation:
// the row is keyed by the exact string confirmed, and a near-miss stored as an
// acknowledgement is worse than no acknowledgement at all.
func (db *DB) AcknowledgeDissonance(ctx context.Context, id uuid.UUID, typed string, by uuid.UUID) error {
	e, err := db.GetExperiment(ctx, id)
	if err != nil {
		return err
	}
	if e == nil {
		return fmt.Errorf("no experiment %s", id)
	}
	if !e.Acknowledged(typed) {
		return fmt.Errorf("the typed phrase does not match %q", e.DissonancePhrase)
	}
	_, err = db.Pool.Exec(ctx, `
		UPDATE experiments SET acknowledged_by = $2, acknowledged_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, id, by)
	return err
}

// --- Arms ---

// CreateExperimentArm adds one side of the comparison.
func (db *DB) CreateExperimentArm(ctx context.Context, a *domain.ExperimentArm) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return db.Pool.QueryRow(ctx, `
		INSERT INTO experiment_arms (id, experiment_id, variation_id, slug, allocation_weight,
		                             deployment_name, declared_migration_up, declared_migration_down)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING created_at, updated_at
	`, a.ID, a.ExperimentID, a.VariationID, a.Slug, a.AllocationWeight, a.DeploymentName,
		a.DeclaredMigrationUp, a.DeclaredMigrationDown,
	).Scan(&a.CreatedAt, &a.UpdatedAt)
}

// UpsertExperimentArm creates an Arm or updates the Variation's existing one.
//
// A Variation is generated more than once -- a revision re-runs the whole thing
// -- and the second run must replace what it declared rather than fail on a
// unique constraint or leave the first run's migration in place.
func (db *DB) UpsertExperimentArm(ctx context.Context, a *domain.ExperimentArm) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return db.Pool.QueryRow(ctx, `
		INSERT INTO experiment_arms (id, experiment_id, variation_id, slug, allocation_weight,
		                             deployment_name, declared_migration_up, declared_migration_down)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (experiment_id, slug) DO UPDATE SET
			declared_migration_up = EXCLUDED.declared_migration_up,
			declared_migration_down = EXCLUDED.declared_migration_down,
			updated_at = NOW()
		RETURNING id, created_at, updated_at
	`, a.ID, a.ExperimentID, a.VariationID, a.Slug, a.AllocationWeight, a.DeploymentName,
		a.DeclaredMigrationUp, a.DeclaredMigrationDown,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
}

// SetExperimentDissonance records what withdrawal will feel like, and the phrase
// the Mendel user will have to type to acknowledge it.
func (db *DB) SetExperimentDissonance(ctx context.Context, id uuid.UUID, description, phrase string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE experiments SET dissonance_description = $2, dissonance_phrase = $3, updated_at = NOW()
		WHERE id = $1
	`, id, description, phrase)
	return err
}

// GetExperimentArms returns an experiment's Arms, mainline first.
func (db *DB) GetExperimentArms(ctx context.Context, experimentID uuid.UUID) ([]domain.ExperimentArm, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, experiment_id, variation_id, slug, allocation_weight, deployment_name,
		       declared_migration_up, declared_migration_down, created_at, updated_at
		FROM experiment_arms WHERE experiment_id = $1
		ORDER BY variation_id IS NOT NULL, slug
	`, experimentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.ExperimentArm
	for rows.Next() {
		var a domain.ExperimentArm
		if err := rows.Scan(&a.ID, &a.ExperimentID, &a.VariationID, &a.Slug,
			&a.AllocationWeight, &a.DeploymentName, &a.DeclaredMigrationUp,
			&a.DeclaredMigrationDown, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetArmAllocation changes how traffic is shared, for every Arm at once.
//
// One statement, because a partial write leaves weights that do not total 100 --
// a state the allocation is meaningless in and which nothing downstream checks
// again.
func (db *DB) SetArmAllocation(ctx context.Context, weights map[uuid.UUID]int) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for armID, weight := range weights {
		if _, err := tx.Exec(ctx,
			`UPDATE experiment_arms SET allocation_weight = $2, updated_at = NOW() WHERE id = $1`,
			armID, weight); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SetArmDeployment records what was deployed for an Arm.
func (db *DB) SetArmDeployment(ctx context.Context, armID uuid.UUID, deploymentName string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE experiment_arms SET deployment_name = $2, updated_at = NOW() WHERE id = $1`,
		armID, deploymentName)
	return err
}

// --- Admissions ---

// RecordAdmission stores what internal/experiment decided about an Arm.
//
// This is the seam that made the package reachable: it could already admit,
// apply, archive and roll back, and had nowhere to write any of it down.
//
// Delta and Shapes are stored as the package's own JSON rather than reshaped, so
// the record does not drift as those types gain fields -- and a verdict acted on
// months ago stays readable exactly as it was made.
func (db *DB) RecordAdmission(ctx context.Context, armID uuid.UUID,
	adm *experiment.Admission, verdict, reason string) (*domain.ArmAdmission, error) {

	rec := &domain.ArmAdmission{
		ID:      uuid.New(),
		ArmID:   armID,
		Verdict: verdict,
		Reason:  reason,
	}

	// A declined admission has no Admission to record -- that is what declining
	// means -- so the reason carries the whole story.
	if adm != nil {
		rec.MigrationUp = adm.Migration.Up
		rec.MigrationDown = adm.Migration.Down

		delta, err := json.Marshal(adm.Delta)
		if err != nil {
			return nil, fmt.Errorf("recording the delta: %w", err)
		}
		shapes, err := json.Marshal(adm.Shapes)
		if err != nil {
			return nil, fmt.Errorf("recording the shapes: %w", err)
		}
		rec.Delta, rec.Shapes = delta, shapes
	} else {
		rec.Delta, rec.Shapes = []byte("null"), []byte("null")
	}

	err := db.Pool.QueryRow(ctx, `
		INSERT INTO arm_admissions (id, arm_id, migration_up, migration_down, delta, shapes, verdict, reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING admitted_at
	`, rec.ID, rec.ArmID, rec.MigrationUp, rec.MigrationDown, rec.Delta, rec.Shapes,
		rec.Verdict, rec.Reason).Scan(&rec.AdmittedAt)
	return rec, err
}

// GetArmAdmissions returns every verdict reached about an Arm, newest first.
//
// Plural because the table is append-only: a re-admission after schema drift is
// a new row, and the sequence is often the thing worth reading.
func (db *DB) GetArmAdmissions(ctx context.Context, armID uuid.UUID) ([]domain.ArmAdmission, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, arm_id, migration_up, migration_down, delta, shapes, verdict, reason, admitted_at
		FROM arm_admissions WHERE arm_id = $1 ORDER BY admitted_at DESC
	`, armID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.ArmAdmission
	for rows.Next() {
		var a domain.ArmAdmission
		if err := rows.Scan(&a.ID, &a.ArmID, &a.MigrationUp, &a.MigrationDown,
			&a.Delta, &a.Shapes, &a.Verdict, &a.Reason, &a.AdmittedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- Archives ---

// RecordArchive notes where a rolled-back Arm's data went.
func (db *DB) RecordArchive(ctx context.Context, a *domain.ArmArchive) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return db.Pool.QueryRow(ctx, `
		INSERT INTO arm_archives (id, arm_id, location, size_bytes, expires_at)
		VALUES ($1,$2,$3,$4,$5) RETURNING created_at
	`, a.ID, a.ArmID, a.Location, a.SizeBytes, a.ExpiresAt).Scan(&a.CreatedAt)
}

// --- Events ---

// RecordExperimentEvent notes something that happened while the experiment ran.
func (db *DB) RecordExperimentEvent(ctx context.Context, e *domain.ExperimentEvent) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	return db.Pool.QueryRow(ctx, `
		INSERT INTO experiment_events (id, experiment_id, arm_id, kind, detail, data)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING occurred_at
	`, e.ID, e.ExperimentID, e.ArmID, e.Kind, e.Detail, e.Data).Scan(&e.OccurredAt)
}

// GetExperimentEvents returns what happened, oldest first, because the sequence
// is the point.
func (db *DB) GetExperimentEvents(ctx context.Context, experimentID uuid.UUID) ([]domain.ExperimentEvent, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, experiment_id, arm_id, kind, detail, data, occurred_at
		FROM experiment_events WHERE experiment_id = $1 ORDER BY occurred_at
	`, experimentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.ExperimentEvent
	for rows.Next() {
		var e domain.ExperimentEvent
		if err := rows.Scan(&e.ID, &e.ExperimentID, &e.ArmID, &e.Kind, &e.Detail,
			&e.Data, &e.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
