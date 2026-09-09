package db

import (
	"context"
	"fmt"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ProjectDeletionBlockers is what is still live in a project, and so what has
// to be stopped before it can be retired.
//
// Three things qualify, and the test for all of them is the same: Mendel
// believes it is running, Mendel can stop it, and leaving it running would cost
// money against a project nothing in the app still lists.
//
// A production deployment used to fail the middle test and was said as a
// warning instead. It no longer does. Nothing moved a hosting deployment off
// 'running' when that was written, so the status was not worth gating on, and
// there was no route that took production down even if it had been. Both are
// now true -- see TerminateHostingDeployment and handleTeardownProd -- so
// production is a gate like the others, with a page that satisfies it.
func (db *DB) ProjectDeletionBlockers(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectDeletionBlocker, error) {
	var out []domain.ProjectDeletionBlocker

	// Demos, named by the Variation they are demonstrating. Read straight off
	// hosting_deployments, which since 053 is where a demo lives; the variation
	// join is only for the name, and project_id is on the deployment itself.
	rows, err := db.Pool.Query(ctx, `
		SELECT d.variation_id, COALESCE(v.name, '')
		FROM hosting_deployments d
		JOIN variations v ON v.id = d.variation_id
		WHERE d.project_id = $1 AND d.kind = 'demo'
		  AND d.status IN ('deploying', 'running')
		ORDER BY d.started_at
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var variationID uuid.UUID
		var name string
		if err := rows.Scan(&variationID, &name); err != nil {
			return nil, err
		}
		if name == "" {
			name = "an unnamed Variation"
		}
		out = append(out, domain.ProjectDeletionBlocker{
			Name:    fmt.Sprintf("A demo of %s is running", name),
			Missing: "Stop the demo, so the hosting it is using is released.",
			Path:    fmt.Sprintf("/p/%s/variations/%s", projectID, variationID),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	prodRows, err := db.Pool.Query(ctx, `
		SELECT app_name FROM hosting_deployments
		WHERE project_id = $1 AND kind = 'prod'
		  AND status IN ('deploying', 'running')
		ORDER BY started_at
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer prodRows.Close()
	for prodRows.Next() {
		var appName string
		if err := prodRows.Scan(&appName); err != nil {
			return nil, err
		}
		out = append(out, domain.ProjectDeletionBlocker{
			Name:    fmt.Sprintf("Production is serving as %s", appName),
			Missing: "Take production down, so it stops serving and stops billing.",
			Path:    fmt.Sprintf("/p/%s/deployment", projectID),
		})
	}
	if err := prodRows.Err(); err != nil {
		return nil, err
	}

	expRows, err := db.Pool.Query(ctx, `
		SELECT id, status FROM experiments
		WHERE project_id = $1 AND status IN ('starting', 'running', 'stopping')
		ORDER BY created_at
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer expRows.Close()
	for expRows.Next() {
		var id uuid.UUID
		var status string
		if err := expRows.Scan(&id, &status); err != nil {
			return nil, err
		}
		out = append(out, domain.ProjectDeletionBlocker{
			Name:    fmt.Sprintf("A live experiment is %s", status),
			Missing: "Stop the experiment, so real traffic stops being split between its Arms.",
			Path:    fmt.Sprintf("/p/%s/experiments", projectID),
		})
	}
	return out, expRows.Err()
}

// SoftDeleteProject retires a project, or declines and says what is still live.
//
// The check and the write are one statement so that a demo started between
// reading the settings page and pressing the button cannot slip through: the
// UPDATE only lands if the project is live and nothing is running under it.
// A nil return with no blockers means it was already retired, which is the
// right answer to pressing delete twice.
func (db *DB) SoftDeleteProject(ctx context.Context, projectID uuid.UUID, by *uuid.UUID) ([]domain.ProjectDeletionBlocker, error) {
	blockers, err := db.ProjectDeletionBlockers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(blockers) > 0 {
		return blockers, nil
	}

	tag, err := db.Pool.Exec(ctx, `
		UPDATE projects
		SET deleted_at = NOW(), deleted_by = $2, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM hosting_deployments d
			WHERE d.project_id = $1 AND d.status IN ('deploying', 'running')
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM experiments e
			WHERE e.project_id = $1 AND e.status IN ('starting', 'running', 'stopping')
		  )
	`, projectID, by)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		// Either it was already retired, or something started in the gap. Ask
		// again rather than guessing which.
		return db.ProjectDeletionBlockers(ctx, projectID)
	}
	return nil, nil
}

// ProjectIsLive reports whether a project exists and has not been retired.
//
// Separate from GetProject, which filters retired projects out and so cannot
// tell "no such project" from "retired" -- a distinction the middleware needs
// in order to say which one happened.
func (db *DB) ProjectIsLive(ctx context.Context, projectID uuid.UUID) (live, exists bool, err error) {
	var deletedAt *time.Time
	err = db.Pool.QueryRow(ctx,
		`SELECT deleted_at FROM projects WHERE id = $1`, projectID).Scan(&deletedAt)
	if err == pgx.ErrNoRows {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return deletedAt == nil, true, nil
}
