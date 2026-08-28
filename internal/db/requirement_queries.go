package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bhs/mendelbuild/internal/domain"
)

//------------------------------------------------------------------------------
// Variation requirements (added in 031)
//------------------------------------------------------------------------------

const variationRequirementColumns = `id, variation_id, kind, name, description, instructions, console_url, created_at`

func scanVariationRequirement(row pgx.Row) (*domain.VariationRequirement, error) {
	var req domain.VariationRequirement
	err := row.Scan(&req.ID, &req.VariationID, &req.Kind, &req.Name,
		&req.Description, &req.Instructions, &req.ConsoleURL, &req.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// ReplaceVariationRequirements makes the given set the variation's complete
// requirements. Code generation declares requirements from the code it just
// wrote, and it runs again on every revision, so a re-declaration supersedes
// the previous one rather than adding to it.
//
// Requirements that survive unchanged keep their ID, and with it their
// acknowledgements — re-running codegen must not make the user re-register a
// redirect URI that is still required and still correct.
func (db *DB) ReplaceVariationRequirements(ctx context.Context, variationID uuid.UUID, reqs []domain.VariationRequirement) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	keepKinds := make([]string, 0, len(reqs))
	keepNames := make([]string, 0, len(reqs))
	for _, req := range reqs {
		keepKinds = append(keepKinds, string(req.Kind))
		keepNames = append(keepNames, req.Name)
	}

	// Drop requirements the variation no longer declares. Their
	// acknowledgements go with them, which is correct: nothing requires that
	// string any more. Empty arrays mean nothing is declared, and delete the
	// lot.
	if _, err := tx.Exec(ctx, `
		DELETE FROM variation_requirements
		WHERE variation_id = $1
		  AND (kind, name) NOT IN (SELECT * FROM unnest($2::text[], $3::text[]))
	`, variationID, keepKinds, keepNames); err != nil {
		return err
	}

	for _, req := range reqs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO variation_requirements
				(id, variation_id, kind, name, description, instructions, console_url)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
			ON CONFLICT (variation_id, kind, name) DO UPDATE SET
				description = EXCLUDED.description,
				instructions = EXCLUDED.instructions,
				console_url = EXCLUDED.console_url
		`, variationID, req.Kind, req.Name, req.Description, req.Instructions, req.ConsoleURL); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// ListVariationRequirements returns everything a variation needs in order to run.
func (db *DB) ListVariationRequirements(ctx context.Context, variationID uuid.UUID) ([]domain.VariationRequirement, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT `+variationRequirementColumns+`
		FROM variation_requirements
		WHERE variation_id = $1
		ORDER BY kind, name
	`, variationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reqs []domain.VariationRequirement
	for rows.Next() {
		req, err := scanVariationRequirement(rows)
		if err != nil {
			return nil, err
		}
		reqs = append(reqs, *req)
	}
	return reqs, rows.Err()
}

// GetVariationRequirement retrieves one requirement by ID.
func (db *DB) GetVariationRequirement(ctx context.Context, id uuid.UUID) (*domain.VariationRequirement, error) {
	return scanVariationRequirement(db.Pool.QueryRow(ctx, `
		SELECT `+variationRequirementColumns+`
		FROM variation_requirements WHERE id = $1
	`, id))
}

// ListMergedVariationRequirements returns the requirements of every variation
// whose code reached main. Production runs that merged code, so it needs
// whatever those variations needed — deduplicated by (kind, name), keeping the
// most recently declared, since two variations that both wired up Google
// sign-in describe the same requirement.
func (db *DB) ListMergedVariationRequirements(ctx context.Context, projectID uuid.UUID) ([]domain.VariationRequirement, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT ON (vr.kind, vr.name)
		       vr.id, vr.variation_id, vr.kind, vr.name, vr.description,
		       vr.instructions, vr.console_url, vr.created_at
		FROM variation_requirements vr
		JOIN variations v ON vr.variation_id = v.id
		JOIN hops h ON v.hop_id = h.id
		JOIN strategies s ON h.strategy_id = s.id
		WHERE s.project_id = $1
		  AND v.status IN ('merged', 'selected')
		ORDER BY vr.kind, vr.name, vr.created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reqs []domain.VariationRequirement
	for rows.Next() {
		req, err := scanVariationRequirement(rows)
		if err != nil {
			return nil, err
		}
		reqs = append(reqs, *req)
	}
	return reqs, rows.Err()
}

//------------------------------------------------------------------------------
// Project env vars (added in 031)
//------------------------------------------------------------------------------

// UpsertProjectEnvVar stores the value of a 'secret' requirement. Values are
// project-scoped, so entering GOOGLE_CLIENT_SECRET once serves every variation
// and production.
func (db *DB) UpsertProjectEnvVar(ctx context.Context, projectID uuid.UUID, name string, encryptedValue []byte) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO project_env_vars (id, project_id, name, encrypted_value)
		VALUES (gen_random_uuid(), $1, $2, $3)
		ON CONFLICT (project_id, name) DO UPDATE SET
			encrypted_value = EXCLUDED.encrypted_value,
			updated_at = NOW()
	`, projectID, name, encryptedValue)
	return err
}

// GetProjectEnvVar retrieves one stored value, including its ciphertext.
func (db *DB) GetProjectEnvVar(ctx context.Context, projectID uuid.UUID, name string) (*domain.ProjectEnvVar, error) {
	var v domain.ProjectEnvVar
	err := db.Pool.QueryRow(ctx, `
		SELECT id, project_id, name, encrypted_value, created_at, updated_at
		FROM project_env_vars
		WHERE project_id = $1 AND name = $2
	`, projectID, name).Scan(&v.ID, &v.ProjectID, &v.Name, &v.EncryptedValue, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ListProjectEnvVarNames returns the names the project has values for, without
// the values. This is what deciding whether a requirement is met needs; the
// ciphertext is fetched only at deploy time.
func (db *DB) ListProjectEnvVarNames(ctx context.Context, projectID uuid.UUID) (map[string]bool, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT name FROM project_env_vars WHERE project_id = $1
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	names := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[name] = true
	}
	return names, rows.Err()
}

// DeleteProjectEnvVar removes a stored value.
func (db *DB) DeleteProjectEnvVar(ctx context.Context, projectID uuid.UUID, name string) error {
	_, err := db.Pool.Exec(ctx, `
		DELETE FROM project_env_vars WHERE project_id = $1 AND name = $2
	`, projectID, name)
	return err
}

//------------------------------------------------------------------------------
// Requirement acknowledgements (added in 031)
//------------------------------------------------------------------------------

// AcknowledgeRequirement records that the user carried out an acknowledgement
// for one particular resolved string. Confirming the same string twice is not
// an error; it simply remains confirmed.
func (db *DB) AcknowledgeRequirement(ctx context.Context, requirementID uuid.UUID, resolvedValue string, by *uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO requirement_acknowledgements
			(id, requirement_id, resolved_value, acknowledged_by)
		VALUES (gen_random_uuid(), $1, $2, $3)
		ON CONFLICT (requirement_id, resolved_value) DO NOTHING
	`, requirementID, resolvedValue, by)
	return err
}

// RetractAcknowledgement removes a confirmation, for when the user discovers
// they had not in fact done the thing.
func (db *DB) RetractAcknowledgement(ctx context.Context, requirementID uuid.UUID, resolvedValue string) error {
	_, err := db.Pool.Exec(ctx, `
		DELETE FROM requirement_acknowledgements
		WHERE requirement_id = $1 AND resolved_value = $2
	`, requirementID, resolvedValue)
	return err
}

// ListAcknowledgements returns, per requirement, the exact strings confirmed.
func (db *DB) ListAcknowledgements(ctx context.Context, requirementIDs []uuid.UUID) (map[uuid.UUID]map[string]bool, error) {
	acked := make(map[uuid.UUID]map[string]bool)
	if len(requirementIDs) == 0 {
		return acked, nil
	}

	rows, err := db.Pool.Query(ctx, `
		SELECT requirement_id, resolved_value
		FROM requirement_acknowledgements
		WHERE requirement_id = ANY($1)
	`, requirementIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		var value string
		if err := rows.Scan(&id, &value); err != nil {
			return nil, err
		}
		if acked[id] == nil {
			acked[id] = make(map[string]bool)
		}
		acked[id][value] = true
	}
	return acked, rows.Err()
}

// RequirementEvidenceFor gathers what the project has stored and confirmed, so
// a set of requirements can be judged against a deployment.
func (db *DB) RequirementEvidenceFor(ctx context.Context, projectID uuid.UUID, reqs []domain.VariationRequirement) (domain.RequirementEvidence, error) {
	names, err := db.ListProjectEnvVarNames(ctx, projectID)
	if err != nil {
		return domain.RequirementEvidence{}, err
	}

	ids := make([]uuid.UUID, 0, len(reqs))
	for _, req := range reqs {
		if req.Kind == domain.RequirementKindAcknowledgement {
			ids = append(ids, req.ID)
		}
	}
	acked, err := db.ListAcknowledgements(ctx, ids)
	if err != nil {
		return domain.RequirementEvidence{}, err
	}

	return domain.RequirementEvidence{EnvVarNames: names, Acknowledged: acked}, nil
}

// GetProjectIDForVariation returns the project a variation belongs to, for
// confirming that an ID from a request refers to something in the project the
// request is scoped to.
func (db *DB) GetProjectIDForVariation(ctx context.Context, variationID uuid.UUID) (uuid.UUID, error) {
	var projectID uuid.UUID
	err := db.Pool.QueryRow(ctx, `
		SELECT s.project_id
		FROM variations v
		JOIN hops h ON v.hop_id = h.id
		JOIN strategies s ON h.strategy_id = s.id
		WHERE v.id = $1
	`, variationID).Scan(&projectID)
	return projectID, err
}
