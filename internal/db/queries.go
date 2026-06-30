package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/google/uuid"
)

// LoadStrategy loads a strategy from the input definition, upserting as needed.
// Returns the project ID.
func (db *DB) LoadStrategy(ctx context.Context, input *domain.StrategyInput) (uuid.UUID, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	now := time.Now()

	// Prepare project config JSON from credentials
	var projectConfig json.RawMessage
	if input.Credentials.AnthropicAPIKey != "" {
		configBytes, _ := json.Marshal(domain.ProjectConfig{
			AnthropicAPIKey: input.Credentials.AnthropicAPIKey,
		})
		projectConfig = configBytes
	}

	// Upsert project (check if exists first since name isn't unique-constrained)
	var projectID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM projects WHERE name = $1`, input.Project).Scan(&projectID)
	if err != nil {
		// Doesn't exist, create it
		projectID = uuid.New()
		_, err = tx.Exec(ctx, `
			INSERT INTO projects (id, name, config, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)
		`, projectID, input.Project, projectConfig, now)
	} else {
		// Exists, update timestamp and config if provided
		if projectConfig != nil {
			_, err = tx.Exec(ctx, `UPDATE projects SET config = $1, updated_at = $2 WHERE id = $3`, projectConfig, now, projectID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE projects SET updated_at = $1 WHERE id = $2`, now, projectID)
		}
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert project: %w", err)
	}

	// Upsert strategy
	var strategyID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM strategies WHERE project_id = $1 AND name = $2`, projectID, input.Strategy.Name).Scan(&strategyID)
	if err != nil {
		strategyID = uuid.New()
		_, err = tx.Exec(ctx, `
			INSERT INTO strategies (id, project_id, name, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)
		`, strategyID, projectID, input.Strategy.Name, now)
		if err != nil {
			return uuid.Nil, fmt.Errorf("insert strategy: %w", err)
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE strategies SET updated_at = $1 WHERE id = $2`, now, strategyID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("update strategy: %w", err)
		}
	}

	// Track existing objectives and KRs for orphan detection
	existingObjectives := make(map[string]bool)
	existingKRs := make(map[string]bool)

	rows, err := tx.Query(ctx, `
		SELECT id FROM objectives WHERE strategy_id = $1
	`, strategyID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("query existing objectives: %w", err)
	}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		existingObjectives[id] = true
	}
	rows.Close()

	// Upsert objectives and key results
	for _, obj := range input.Strategy.Objectives {
		objID, err := uuid.Parse(obj.ID)
		if err != nil {
			// If not a valid UUID, create a deterministic one from the string ID
			objID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("objective:"+obj.ID))
		}
		delete(existingObjectives, objID.String())

		_, err = tx.Exec(ctx, `
			INSERT INTO objectives (id, strategy_id, description, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (id) DO UPDATE SET
				description = EXCLUDED.description,
				updated_at = $4
		`, objID, strategyID, obj.Description, now)
		if err != nil {
			return uuid.Nil, fmt.Errorf("upsert objective %s: %w", obj.ID, err)
		}

		for _, kr := range obj.KeyResults {
			krID, err := uuid.Parse(kr.ID)
			if err != nil {
				krID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("keyresult:"+kr.ID))
			}
			delete(existingKRs, krID.String())

			var targetDate *time.Time
			if kr.TargetDate != nil {
				t, err := time.Parse(time.RFC3339, *kr.TargetDate)
				if err != nil {
					t, err = time.Parse("2006-01-02", *kr.TargetDate)
				}
				if err == nil {
					targetDate = &t
				}
			}

			// Insert key_result with strategy_id (new schema)
			_, err = tx.Exec(ctx, `
				INSERT INTO key_results (id, strategy_id, description, target_units, target_date, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $6)
				ON CONFLICT (id) DO UPDATE SET
					description = EXCLUDED.description,
					target_units = EXCLUDED.target_units,
					target_date = EXCLUDED.target_date,
					updated_at = $6
			`, krID, strategyID, kr.Description, kr.TargetUnits, targetDate, now)
			if err != nil {
				return uuid.Nil, fmt.Errorf("upsert key result %s: %w", kr.ID, err)
			}

			// Link key_result to objective via junction table
			_, err = tx.Exec(ctx, `
				INSERT INTO objective_key_result_pairs (objective_id, key_result_id, created_at)
				VALUES ($1, $2, $3)
				ON CONFLICT (objective_id, key_result_id) DO NOTHING
			`, objID, krID, now)
			if err != nil {
				return uuid.Nil, fmt.Errorf("link key result %s to objective: %w", kr.ID, err)
			}
		}
	}

	// Warn about orphaned objectives (don't delete automatically)
	for id := range existingObjectives {
		fmt.Printf("Warning: Objective %s exists in DB but not in input file\n", id)
	}

	// Upsert funding sources
	for _, fund := range input.Strategy.Funding {
		fundID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("funding:%s:%s", strategyID, fund.ResourceType)))
		_, err = tx.Exec(ctx, `
			INSERT INTO funding_sources (id, strategy_id, resource_type, amount, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5)
			ON CONFLICT (id) DO UPDATE SET
				amount = EXCLUDED.amount,
				updated_at = $5
		`, fundID, strategyID, fund.ResourceType, fund.Amount, now)
		if err != nil {
			return uuid.Nil, fmt.Errorf("upsert funding source %s: %w", fund.ResourceType, err)
		}
	}

	// Upsert repository
	repoID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("repo:"+input.Repository.URL))
	configJSON, _ := json.Marshal(map[string]interface{}{
		"main_branch": input.Repository.MainBranch,
	})
	if input.Repository.Config != nil {
		// Merge with user-provided config
		var userConfig map[string]interface{}
		json.Unmarshal(input.Repository.Config, &userConfig)
		var baseConfig map[string]interface{}
		json.Unmarshal(configJSON, &baseConfig)
		for k, v := range userConfig {
			baseConfig[k] = v
		}
		configJSON, _ = json.Marshal(baseConfig)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO repositories (id, project_id, name, repo_type, url, config, created_at, updated_at)
		VALUES ($1, $2, $3, 'git', $4, $5, $6, $6)
		ON CONFLICT (id) DO UPDATE SET
			config = EXCLUDED.config,
			updated_at = $6
	`, repoID, projectID, input.Project, input.Repository.URL, configJSON, now)
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert repository: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit transaction: %w", err)
	}

	return projectID, nil
}

// GetProject retrieves a project by ID.
func (db *DB) GetProject(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
	var p domain.Project
	err := db.Pool.QueryRow(ctx, `
		SELECT id, name, config, created_at, updated_at
		FROM projects WHERE id = $1
	`, id).Scan(&p.ID, &p.Name, &p.Config, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProjectByName retrieves a project by name.
func (db *DB) GetProjectByName(ctx context.Context, name string) (*domain.Project, error) {
	var p domain.Project
	err := db.Pool.QueryRow(ctx, `
		SELECT id, name, config, created_at, updated_at
		FROM projects WHERE name = $1
	`, name).Scan(&p.ID, &p.Name, &p.Config, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetStrategiesByProject retrieves all strategies for a project.
func (db *DB) GetStrategiesByProject(ctx context.Context, projectID uuid.UUID) ([]domain.Strategy, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, project_id, parent_id, name, created_at, updated_at
		FROM strategies WHERE project_id = $1
		ORDER BY name
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var strategies []domain.Strategy
	for rows.Next() {
		var s domain.Strategy
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.ParentID, &s.Name, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		strategies = append(strategies, s)
	}
	return strategies, nil
}

// GetObjectivesByStrategy retrieves all non-deleted objectives for a strategy.
func (db *DB) GetObjectivesByStrategy(ctx context.Context, strategyID uuid.UUID) ([]domain.Objective, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, parent_id, description, tune_score, tune_feedback, deleted_at, created_at, updated_at
		FROM objectives WHERE strategy_id = $1 AND deleted_at IS NULL
		ORDER BY created_at
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var objectives []domain.Objective
	for rows.Next() {
		var o domain.Objective
		if err := rows.Scan(&o.ID, &o.StrategyID, &o.ParentID, &o.Description, &o.TuneScore, &o.TuneFeedback, &o.DeletedAt, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		objectives = append(objectives, o)
	}
	return objectives, nil
}

// GetKeyResultsByObjective retrieves all non-deleted key results linked to an objective via junction table.
func (db *DB) GetKeyResultsByObjective(ctx context.Context, objectiveID uuid.UUID) ([]domain.KeyResult, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT kr.id, kr.strategy_id, kr.description, kr.target_units, kr.target_date,
		       kr.tune_score, kr.tune_feedback, kr.deleted_at, kr.created_at, kr.updated_at
		FROM key_results kr
		JOIN objective_key_result_pairs okrp ON okrp.key_result_id = kr.id
		WHERE okrp.objective_id = $1 AND kr.deleted_at IS NULL
		ORDER BY kr.created_at
	`, objectiveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keyResults []domain.KeyResult
	for rows.Next() {
		var kr domain.KeyResult
		if err := rows.Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetUnits, &kr.TargetDate,
			&kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt, &kr.CreatedAt, &kr.UpdatedAt); err != nil {
			return nil, err
		}
		keyResults = append(keyResults, kr)
	}
	return keyResults, nil
}

// GetFundingSourcesByStrategy retrieves all funding sources for a strategy.
func (db *DB) GetFundingSourcesByStrategy(ctx context.Context, strategyID uuid.UUID) ([]domain.FundingSource, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, resource_type, amount, created_at, updated_at
		FROM funding_sources WHERE strategy_id = $1
		ORDER BY resource_type
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []domain.FundingSource
	for rows.Next() {
		var f domain.FundingSource
		if err := rows.Scan(&f.ID, &f.StrategyID, &f.ResourceType, &f.Amount, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		sources = append(sources, f)
	}
	return sources, nil
}

// GetStrategy retrieves a strategy by ID.
func (db *DB) GetStrategy(ctx context.Context, id uuid.UUID) (*domain.Strategy, error) {
	var s domain.Strategy
	err := db.Pool.QueryRow(ctx, `
		SELECT id, project_id, parent_id, name, created_at, updated_at
		FROM strategies WHERE id = $1
	`, id).Scan(&s.ID, &s.ProjectID, &s.ParentID, &s.Name, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateDecision creates a new decision.
func (db *DB) CreateDecision(ctx context.Context, d *domain.Decision) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO decisions (id, kind, title, details, objectivity_score, importance_score, status, subject_type, subject_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
	`, d.ID, d.Kind, d.Title, d.Details, d.ObjectivityScore, d.ImportanceScore, d.Status, d.SubjectType, d.SubjectID, d.CreatedAt)
	return err
}

// GetDecision retrieves a decision by ID.
func (db *DB) GetDecision(ctx context.Context, id uuid.UUID) (*domain.Decision, error) {
	var d domain.Decision
	err := db.Pool.QueryRow(ctx, `
		SELECT id, kind, title, details, objectivity_score, importance_score, status,
			   assigned_to, assigned_at, accepted_by, accepted_at,
			   resolved_by, resolved_at, resolution, rationale,
			   subject_type, subject_id, created_at, updated_at
		FROM decisions WHERE id = $1
	`, id).Scan(
		&d.ID, &d.Kind, &d.Title, &d.Details, &d.ObjectivityScore, &d.ImportanceScore, &d.Status,
		&d.AssignedTo, &d.AssignedAt, &d.AcceptedBy, &d.AcceptedAt,
		&d.ResolvedBy, &d.ResolvedAt, &d.Resolution, &d.Rationale,
		&d.SubjectType, &d.SubjectID, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDecisionsBySubject retrieves all decisions for a subject.
func (db *DB) GetDecisionsBySubject(ctx context.Context, subjectType string, subjectID uuid.UUID) ([]domain.Decision, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, kind, title, details, objectivity_score, importance_score, status,
			   assigned_to, assigned_at, accepted_by, accepted_at,
			   resolved_by, resolved_at, resolution, rationale,
			   subject_type, subject_id, created_at, updated_at
		FROM decisions
		WHERE subject_type = $1 AND subject_id = $2
		ORDER BY created_at DESC
	`, subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var decisions []domain.Decision
	for rows.Next() {
		var d domain.Decision
		if err := rows.Scan(
			&d.ID, &d.Kind, &d.Title, &d.Details, &d.ObjectivityScore, &d.ImportanceScore, &d.Status,
			&d.AssignedTo, &d.AssignedAt, &d.AcceptedBy, &d.AcceptedAt,
			&d.ResolvedBy, &d.ResolvedAt, &d.Resolution, &d.Rationale,
			&d.SubjectType, &d.SubjectID, &d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, err
		}
		decisions = append(decisions, d)
	}
	return decisions, nil
}

// GetDecisionsByProject retrieves all decisions related to a project.
// Includes decisions for strategies, hops, and variations belonging to the project.
func (db *DB) GetDecisionsByProject(ctx context.Context, projectID uuid.UUID) ([]domain.Decision, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT d.id, d.kind, d.title, d.details, d.objectivity_score, d.importance_score, d.status,
			   d.assigned_to, d.assigned_at, d.accepted_by, d.accepted_at,
			   d.resolved_by, d.resolved_at, d.resolution, d.rationale,
			   d.subject_type, d.subject_id, d.created_at, d.updated_at
		FROM decisions d
		WHERE
			-- Decisions about strategies in this project
			(d.subject_type = 'strategy' AND d.subject_id IN (
				SELECT id FROM strategies WHERE project_id = $1
			))
			OR
			-- Decisions about hops in strategies in this project
			(d.subject_type = 'hop' AND d.subject_id IN (
				SELECT h.id FROM hops h
				JOIN strategies s ON h.strategy_id = s.id
				WHERE s.project_id = $1
			))
			OR
			-- Decisions about variations in hops in strategies in this project
			(d.subject_type = 'variation' AND d.subject_id IN (
				SELECT v.id FROM variations v
				JOIN hops h ON v.hop_id = h.id
				JOIN strategies s ON h.strategy_id = s.id
				WHERE s.project_id = $1
			))
		ORDER BY d.created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var decisions []domain.Decision
	for rows.Next() {
		var d domain.Decision
		if err := rows.Scan(
			&d.ID, &d.Kind, &d.Title, &d.Details, &d.ObjectivityScore, &d.ImportanceScore, &d.Status,
			&d.AssignedTo, &d.AssignedAt, &d.AcceptedBy, &d.AcceptedAt,
			&d.ResolvedBy, &d.ResolvedAt, &d.Resolution, &d.Rationale,
			&d.SubjectType, &d.SubjectID, &d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, err
		}
		decisions = append(decisions, d)
	}
	return decisions, nil
}

// UpdateDecision updates a decision.
func (db *DB) UpdateDecision(ctx context.Context, d *domain.Decision) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE decisions SET
			title = $2, details = $3, status = $4,
			assigned_to = $5, assigned_at = $6,
			accepted_by = $7, accepted_at = $8,
			resolved_by = $9, resolved_at = $10,
			resolution = $11, rationale = $12,
			updated_at = NOW()
		WHERE id = $1
	`, d.ID, d.Title, d.Details, d.Status,
		d.AssignedTo, d.AssignedAt,
		d.AcceptedBy, d.AcceptedAt,
		d.ResolvedBy, d.ResolvedAt,
		d.Resolution, d.Rationale)
	return err
}

// CreateDecisionMessage creates a new decision message.
func (db *DB) CreateDecisionMessage(ctx context.Context, m *domain.DecisionMessage) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO decision_messages (id, decision_id, role, content, tokens_used, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, m.ID, m.DecisionID, m.Role, m.Content, m.TokensUsed, m.CreatedAt)
	return err
}

// GetDecisionMessages retrieves all messages for a decision.
func (db *DB) GetDecisionMessages(ctx context.Context, decisionID uuid.UUID) ([]domain.DecisionMessage, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, decision_id, role, content, tokens_used, created_at
		FROM decision_messages
		WHERE decision_id = $1
		ORDER BY created_at ASC
	`, decisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []domain.DecisionMessage
	for rows.Next() {
		var m domain.DecisionMessage
		if err := rows.Scan(&m.ID, &m.DecisionID, &m.Role, &m.Content, &m.TokensUsed, &m.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, nil
}

// CreateHop creates a new hop.
func (db *DB) CreateHop(ctx context.Context, h *domain.Hop) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO hops (id, strategy_id, name, commentary, params, evaluation_criteria, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
	`, h.ID, h.StrategyID, h.Name, h.Commentary, h.Params, h.EvaluationCriteria, h.Status, h.CreatedAt)
	return err
}

// CreateHopDependency creates a hop dependency.
func (db *DB) CreateHopDependency(ctx context.Context, hopID, dependsOnHopID uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO hop_dependencies (hop_id, depends_on_hop_id)
		VALUES ($1, $2)
	`, hopID, dependsOnHopID)
	return err
}

// CreateBudgetAllocation creates a budget allocation for a hop.
func (db *DB) CreateBudgetAllocation(ctx context.Context, hopID, fundingSourceID uuid.UUID, limitAmount float64) error {
	id := uuid.New()
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO budget_allocations (id, hop_id, funding_source_id, limit_amount, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
	`, id, hopID, fundingSourceID, limitAmount)
	return err
}

// GetFundingSourceByType retrieves a funding source by strategy and resource type.
func (db *DB) GetFundingSourceByType(ctx context.Context, strategyID uuid.UUID, resourceType string) (*domain.FundingSource, error) {
	var f domain.FundingSource
	err := db.Pool.QueryRow(ctx, `
		SELECT id, strategy_id, resource_type, amount, created_at, updated_at
		FROM funding_sources
		WHERE strategy_id = $1 AND resource_type = $2
	`, strategyID, resourceType).Scan(&f.ID, &f.StrategyID, &f.ResourceType, &f.Amount, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// GetHopsByStrategy retrieves all hops for a strategy.
func (db *DB) GetHopsByStrategy(ctx context.Context, strategyID uuid.UUID) ([]domain.Hop, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, name, commentary, params, evaluation_criteria, status, created_at, updated_at
		FROM hops
		WHERE strategy_id = $1
		ORDER BY created_at ASC
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hops []domain.Hop
	for rows.Next() {
		var h domain.Hop
		if err := rows.Scan(&h.ID, &h.StrategyID, &h.Name, &h.Commentary, &h.Params, &h.EvaluationCriteria, &h.Status, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, err
		}
		hops = append(hops, h)
	}
	return hops, nil
}

// GetHop retrieves a hop by ID.
func (db *DB) GetHop(ctx context.Context, id uuid.UUID) (*domain.Hop, error) {
	var h domain.Hop
	err := db.Pool.QueryRow(ctx, `
		SELECT id, strategy_id, name, commentary, params, evaluation_criteria, status, created_at, updated_at
		FROM hops WHERE id = $1
	`, id).Scan(&h.ID, &h.StrategyID, &h.Name, &h.Commentary, &h.Params, &h.EvaluationCriteria, &h.Status, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// UpdateHopStatus updates the status of a hop.
func (db *DB) UpdateHopStatus(ctx context.Context, hopID uuid.UUID, status domain.HopStatus) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE hops SET status = $1, updated_at = NOW() WHERE id = $2
	`, status, hopID)
	return err
}

// UpdateHopEvaluationCriteria updates the evaluation criteria for a hop.
func (db *DB) UpdateHopEvaluationCriteria(ctx context.Context, hopID uuid.UUID, criteria json.RawMessage) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE hops SET evaluation_criteria = $1, updated_at = NOW() WHERE id = $2
	`, criteria, hopID)
	return err
}

// CreateVariation creates a new variation.
func (db *DB) CreateVariation(ctx context.Context, v *domain.Variation) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO variations (id, hop_id, name, approach, repository_id, commit_ref, ecosystem_id, deployment_ref, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
	`, v.ID, v.HopID, v.Name, v.Approach, v.RepositoryID, v.CommitRef, v.EcosystemID, v.DeploymentRef, v.Status, v.CreatedAt)
	return err
}

// GetVariation retrieves a variation by ID.
func (db *DB) GetVariation(ctx context.Context, id uuid.UUID) (*domain.Variation, error) {
	var v domain.Variation
	err := db.Pool.QueryRow(ctx, `
		SELECT id, hop_id, name, approach, repository_id, commit_ref, ecosystem_id, deployment_ref, status, created_at, updated_at
		FROM variations WHERE id = $1
	`, id).Scan(&v.ID, &v.HopID, &v.Name, &v.Approach, &v.RepositoryID, &v.CommitRef, &v.EcosystemID, &v.DeploymentRef, &v.Status, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// UpdateVariation updates a variation.
func (db *DB) UpdateVariation(ctx context.Context, v *domain.Variation) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE variations SET
			name = $2, approach = $3, repository_id = $4, commit_ref = $5,
			ecosystem_id = $6, deployment_ref = $7, status = $8, updated_at = NOW()
		WHERE id = $1
	`, v.ID, v.Name, v.Approach, v.RepositoryID, v.CommitRef, v.EcosystemID, v.DeploymentRef, v.Status)
	return err
}

// GetVariationsByHop retrieves all variations for a hop.
func (db *DB) GetVariationsByHop(ctx context.Context, hopID uuid.UUID) ([]domain.Variation, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, hop_id, name, approach, repository_id, commit_ref, ecosystem_id, deployment_ref, status, created_at, updated_at
		FROM variations
		WHERE hop_id = $1
		ORDER BY created_at ASC
	`, hopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var variations []domain.Variation
	for rows.Next() {
		var v domain.Variation
		if err := rows.Scan(&v.ID, &v.HopID, &v.Name, &v.Approach, &v.RepositoryID, &v.CommitRef, &v.EcosystemID, &v.DeploymentRef, &v.Status, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		variations = append(variations, v)
	}
	return variations, nil
}

// GetHopsWithCreatingVariations returns hops that have variations in "creating" status.
func (db *DB) GetHopsWithCreatingVariations(ctx context.Context) ([]domain.Hop, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT h.id, h.strategy_id, h.name, h.commentary, h.params, h.evaluation_criteria, h.status, h.created_at, h.updated_at
		FROM hops h
		JOIN variations v ON v.hop_id = h.id
		WHERE v.status = 'creating'
		ORDER BY h.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hops []domain.Hop
	for rows.Next() {
		var h domain.Hop
		if err := rows.Scan(&h.ID, &h.StrategyID, &h.Name, &h.Commentary, &h.Params, &h.EvaluationCriteria, &h.Status, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, err
		}
		hops = append(hops, h)
	}
	return hops, nil
}

// CreateVariationStateTransition records a state transition for a variation.
func (db *DB) CreateVariationStateTransition(ctx context.Context, variationID uuid.UUID, fromStatus, toStatus, reason string) error {
	id := uuid.New()
	var fromPtr *string
	if fromStatus != "" {
		fromPtr = &fromStatus
	}
	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO variation_state_history (id, variation_id, from_status, to_status, transitioned_at, reason)
		VALUES ($1, $2, $3, $4, NOW(), $5)
	`, id, variationID, fromPtr, toStatus, reasonPtr)
	return err
}

// GetRepositoryByProject retrieves the repository for a project.
func (db *DB) GetRepositoryByProject(ctx context.Context, projectID uuid.UUID) (*domain.Repository, error) {
	var r domain.Repository
	err := db.Pool.QueryRow(ctx, `
		SELECT id, project_id, name, repo_type, url, config, created_at, updated_at
		FROM repositories WHERE project_id = $1 LIMIT 1
	`, projectID).Scan(&r.ID, &r.ProjectID, &r.Name, &r.RepoType, &r.URL, &r.Config, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// LogBudgetSpend logs a budget spend entry.
func (db *DB) LogBudgetSpend(ctx context.Context, allocationID uuid.UUID, amount float64, description string) error {
	id := uuid.New()
	var descPtr *string
	if description != "" {
		descPtr = &description
	}
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO budget_spend_log (id, budget_allocation_id, amount, recorded_at, description)
		VALUES ($1, $2, $3, NOW(), $4)
	`, id, allocationID, amount, descPtr)
	return err
}

// GetBudgetAllocationsByHop retrieves all budget allocations for a hop.
func (db *DB) GetBudgetAllocationsByHop(ctx context.Context, hopID uuid.UUID) ([]domain.BudgetAllocation, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, hop_id, funding_source_id, limit_amount, created_at, updated_at
		FROM budget_allocations
		WHERE hop_id = $1
	`, hopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var allocations []domain.BudgetAllocation
	for rows.Next() {
		var a domain.BudgetAllocation
		if err := rows.Scan(&a.ID, &a.HopID, &a.FundingSourceID, &a.LimitAmount, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		allocations = append(allocations, a)
	}
	return allocations, nil
}

// GetBudgetSpendByAllocation retrieves total spend for a budget allocation.
func (db *DB) GetBudgetSpendByAllocation(ctx context.Context, allocationID uuid.UUID) (float64, error) {
	var total float64
	err := db.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0) FROM budget_spend_log WHERE budget_allocation_id = $1
	`, allocationID).Scan(&total)
	return total, err
}

// UpsertRepository creates or updates the repository for a project.
func (db *DB) UpsertRepository(ctx context.Context, projectID uuid.UUID, url string, config json.RawMessage) error {
	// Check if repository exists
	var repoID uuid.UUID
	err := db.Pool.QueryRow(ctx, `SELECT id FROM repositories WHERE project_id = $1 LIMIT 1`, projectID).Scan(&repoID)
	if err != nil {
		// Create new repository
		repoID = uuid.New()
		_, err = db.Pool.Exec(ctx, `
			INSERT INTO repositories (id, project_id, name, repo_type, url, config, created_at, updated_at)
			VALUES ($1, $2, 'main', 'git', $3, $4, NOW(), NOW())
		`, repoID, projectID, url, config)
	} else {
		// Update existing repository
		_, err = db.Pool.Exec(ctx, `
			UPDATE repositories SET url = $1, config = $2, updated_at = NOW() WHERE id = $3
		`, url, config, repoID)
	}
	return err
}

// UpdateProjectConfig updates the config JSONB field for a project.
func (db *DB) UpdateProjectConfig(ctx context.Context, projectID uuid.UUID, config json.RawMessage) error {
	_, err := db.Pool.Exec(ctx, `UPDATE projects SET config = $1, updated_at = NOW() WHERE id = $2`, config, projectID)
	return err
}

// CreateVariationLog creates a new log entry for a variation.
func (db *DB) CreateVariationLog(ctx context.Context, variationID uuid.UUID, level domain.LogLevel, message string) error {
	id := uuid.New()
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO variation_logs (id, variation_id, logged_at, level, message)
		VALUES ($1, $2, NOW(), $3, $4)
	`, id, variationID, string(level), message)
	return err
}

// GetVariationLogs retrieves logs for a variation, most recent first.
func (db *DB) GetVariationLogs(ctx context.Context, variationID uuid.UUID, limit int) ([]domain.VariationLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT id, variation_id, logged_at, level, message
		FROM variation_logs
		WHERE variation_id = $1
		ORDER BY logged_at DESC
		LIMIT $2
	`, variationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []domain.VariationLog
	for rows.Next() {
		var l domain.VariationLog
		if err := rows.Scan(&l.ID, &l.VariationID, &l.LoggedAt, &l.Level, &l.Message); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, nil
}

// GetRecentVariationLogs retrieves the most recent N logs for a variation.
func (db *DB) GetRecentVariationLogs(ctx context.Context, variationID uuid.UUID, limit int) ([]domain.VariationLog, error) {
	return db.GetVariationLogs(ctx, variationID, limit)
}

// GetDecisionBySubjectAndKind retrieves a decision by subject and kind.
func (db *DB) GetDecisionBySubjectAndKind(ctx context.Context, subjectType string, subjectID uuid.UUID, kind domain.DecisionKind) (*domain.Decision, error) {
	var d domain.Decision
	err := db.Pool.QueryRow(ctx, `
		SELECT id, kind, title, details, objectivity_score, importance_score, status,
			   assigned_to, assigned_at, accepted_by, accepted_at,
			   resolved_by, resolved_at, resolution, rationale,
			   subject_type, subject_id, created_at, updated_at
		FROM decisions
		WHERE subject_type = $1 AND subject_id = $2 AND kind = $3
		ORDER BY created_at DESC
		LIMIT 1
	`, subjectType, subjectID, kind).Scan(
		&d.ID, &d.Kind, &d.Title, &d.Details, &d.ObjectivityScore, &d.ImportanceScore, &d.Status,
		&d.AssignedTo, &d.AssignedAt, &d.AcceptedBy, &d.AcceptedAt,
		&d.ResolvedBy, &d.ResolvedAt, &d.Resolution, &d.Rationale,
		&d.SubjectType, &d.SubjectID, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetHopsNeedingSelectionDecision returns hops with at least one pending variation
// but no unresolved variation_selection Decision. Includes both 'active' and 'selecting'
// hops to handle cases where status was updated but Decision wasn't created.
// Also excludes hops that have an unresolved variation_review Decision (user is still
// proposing/reviewing additional variations).
func (db *DB) GetHopsNeedingSelectionDecision(ctx context.Context) ([]domain.Hop, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT h.id, h.strategy_id, h.name, h.commentary, h.params, h.evaluation_criteria, h.status, h.created_at, h.updated_at
		FROM hops h
		JOIN variations v ON v.hop_id = h.id
		WHERE h.status IN ('active', 'selecting')
		  AND v.status = 'pending'
		  AND NOT EXISTS (
			SELECT 1 FROM decisions d
			WHERE d.subject_type = 'hop'
			  AND d.subject_id = h.id
			  AND d.kind = 'variation_selection'
			  AND d.status != 'resolved'
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM decisions d
			WHERE d.subject_type = 'hop'
			  AND d.subject_id = h.id
			  AND d.kind = 'variation_review'
			  AND d.status != 'resolved'
		  )
		ORDER BY h.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hops []domain.Hop
	for rows.Next() {
		var h domain.Hop
		if err := rows.Scan(&h.ID, &h.StrategyID, &h.Name, &h.Commentary, &h.Params, &h.EvaluationCriteria, &h.Status, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, err
		}
		hops = append(hops, h)
	}
	return hops, nil
}

// GetHopsReadyForSelection returns active hops where all variations are done
// (no variations in 'creating' status) and at least one is 'pending'.
func (db *DB) GetHopsReadyForSelection(ctx context.Context) ([]domain.Hop, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT h.id, h.strategy_id, h.name, h.commentary, h.params, h.evaluation_criteria, h.status, h.created_at, h.updated_at
		FROM hops h
		WHERE h.status = 'active'
		  AND EXISTS (
			SELECT 1 FROM variations v WHERE v.hop_id = h.id AND v.status = 'pending'
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM variations v WHERE v.hop_id = h.id AND v.status = 'creating'
		  )
		ORDER BY h.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hops []domain.Hop
	for rows.Next() {
		var h domain.Hop
		if err := rows.Scan(&h.ID, &h.StrategyID, &h.Name, &h.Commentary, &h.Params, &h.EvaluationCriteria, &h.Status, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, err
		}
		hops = append(hops, h)
	}
	return hops, nil
}

// GetHopDependencies retrieves all hops that depend on the given hop.
func (db *DB) GetHopDependencies(ctx context.Context, hopID uuid.UUID) ([]domain.HopDependency, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT hop_id, depends_on_hop_id
		FROM hop_dependencies
		WHERE depends_on_hop_id = $1
	`, hopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deps []domain.HopDependency
	for rows.Next() {
		var d domain.HopDependency
		if err := rows.Scan(&d.HopID, &d.DependsOnHopID); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// GetHopDependsOn retrieves all hops that the given hop depends on.
func (db *DB) GetHopDependsOn(ctx context.Context, hopID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT depends_on_hop_id
		FROM hop_dependencies
		WHERE hop_id = $1
	`, hopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deps []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		deps = append(deps, id)
	}
	return deps, nil
}

// GetHopsNeedingVariationProposal returns active hops that have no variations
// and no existing variation_review Decision (pending or resolved).
func (db *DB) GetHopsNeedingVariationProposal(ctx context.Context) ([]domain.Hop, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT h.id, h.strategy_id, h.name, h.commentary, h.params, h.evaluation_criteria, h.status, h.created_at, h.updated_at
		FROM hops h
		WHERE h.status = 'active'
		  AND NOT EXISTS (
			SELECT 1 FROM variations v WHERE v.hop_id = h.id
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM decisions d
			WHERE d.subject_type = 'hop'
			  AND d.subject_id = h.id
			  AND d.kind = 'variation_review'
		  )
		ORDER BY h.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hops []domain.Hop
	for rows.Next() {
		var h domain.Hop
		if err := rows.Scan(&h.ID, &h.StrategyID, &h.Name, &h.Commentary, &h.Params, &h.EvaluationCriteria, &h.Status, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, err
		}
		hops = append(hops, h)
	}
	return hops, nil
}

// GetHopDependenciesByStrategy retrieves all hop dependencies for hops in a strategy.
func (db *DB) GetHopDependenciesByStrategy(ctx context.Context, strategyID uuid.UUID) ([]domain.HopDependency, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT hd.hop_id, hd.depends_on_hop_id
		FROM hop_dependencies hd
		JOIN hops h ON hd.hop_id = h.id
		WHERE h.strategy_id = $1
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deps []domain.HopDependency
	for rows.Next() {
		var d domain.HopDependency
		if err := rows.Scan(&d.HopID, &d.DependsOnHopID); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// =====================================================
// OKR Management Queries (added in 007)
// =====================================================

// GetObjective retrieves an objective by ID.
func (db *DB) GetObjective(ctx context.Context, id uuid.UUID) (*domain.Objective, error) {
	var o domain.Objective
	err := db.Pool.QueryRow(ctx, `
		SELECT id, strategy_id, parent_id, description, tune_score, tune_feedback, deleted_at, created_at, updated_at
		FROM objectives WHERE id = $1
	`, id).Scan(&o.ID, &o.StrategyID, &o.ParentID, &o.Description, &o.TuneScore, &o.TuneFeedback, &o.DeletedAt, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// GetRootObjectives retrieves top-level (no parent) non-deleted objectives for a strategy.
func (db *DB) GetRootObjectives(ctx context.Context, strategyID uuid.UUID) ([]domain.Objective, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, parent_id, description, tune_score, tune_feedback, deleted_at, created_at, updated_at
		FROM objectives WHERE strategy_id = $1 AND parent_id IS NULL AND deleted_at IS NULL
		ORDER BY created_at
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var objectives []domain.Objective
	for rows.Next() {
		var o domain.Objective
		if err := rows.Scan(&o.ID, &o.StrategyID, &o.ParentID, &o.Description, &o.TuneScore, &o.TuneFeedback, &o.DeletedAt, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		objectives = append(objectives, o)
	}
	return objectives, nil
}

// GetObjectivesByParent retrieves non-deleted child objectives for a parent objective.
func (db *DB) GetObjectivesByParent(ctx context.Context, parentID uuid.UUID) ([]domain.Objective, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, parent_id, description, tune_score, tune_feedback, deleted_at, created_at, updated_at
		FROM objectives WHERE parent_id = $1 AND deleted_at IS NULL
		ORDER BY created_at
	`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var objectives []domain.Objective
	for rows.Next() {
		var o domain.Objective
		if err := rows.Scan(&o.ID, &o.StrategyID, &o.ParentID, &o.Description, &o.TuneScore, &o.TuneFeedback, &o.DeletedAt, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		objectives = append(objectives, o)
	}
	return objectives, nil
}

// CreateObjective creates a new objective.
func (db *DB) CreateObjective(ctx context.Context, obj *domain.Objective) error {
	now := time.Now()
	if obj.ID == uuid.Nil {
		obj.ID = uuid.New()
	}
	obj.CreatedAt = now
	obj.UpdatedAt = now

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO objectives (id, strategy_id, parent_id, description, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)
	`, obj.ID, obj.StrategyID, obj.ParentID, obj.Description, now)
	return err
}

// UpdateObjective updates an objective's description and clears tuning.
func (db *DB) UpdateObjective(ctx context.Context, obj *domain.Objective) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE objectives SET
			description = $2,
			parent_id = $3,
			tune_score = NULL,
			tune_feedback = NULL,
			updated_at = NOW()
		WHERE id = $1
	`, obj.ID, obj.Description, obj.ParentID)
	return err
}

// SoftDeleteObjective soft-deletes an objective by setting deleted_at.
func (db *DB) SoftDeleteObjective(ctx context.Context, id uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE objectives SET deleted_at = NOW(), updated_at = NOW() WHERE id = $1
	`, id)
	return err
}

// GetKeyResult retrieves a key result by ID.
func (db *DB) GetKeyResult(ctx context.Context, id uuid.UUID) (*domain.KeyResult, error) {
	var kr domain.KeyResult
	err := db.Pool.QueryRow(ctx, `
		SELECT id, strategy_id, description, target_units, target_date, tune_score, tune_feedback, deleted_at, created_at, updated_at
		FROM key_results WHERE id = $1
	`, id).Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetUnits, &kr.TargetDate, &kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt, &kr.CreatedAt, &kr.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &kr, nil
}

// GetAllKeyResultsForStrategy retrieves all non-deleted key results for a strategy.
func (db *DB) GetAllKeyResultsForStrategy(ctx context.Context, strategyID uuid.UUID) ([]domain.KeyResult, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, description, target_units, target_date, tune_score, tune_feedback, deleted_at, created_at, updated_at
		FROM key_results WHERE strategy_id = $1 AND deleted_at IS NULL
		ORDER BY created_at
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keyResults []domain.KeyResult
	for rows.Next() {
		var kr domain.KeyResult
		if err := rows.Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetUnits, &kr.TargetDate, &kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt, &kr.CreatedAt, &kr.UpdatedAt); err != nil {
			return nil, err
		}
		keyResults = append(keyResults, kr)
	}
	return keyResults, nil
}

// GetUnlinkedKeyResults retrieves key results not linked to any objective.
func (db *DB) GetUnlinkedKeyResults(ctx context.Context, strategyID uuid.UUID) ([]domain.KeyResult, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT kr.id, kr.strategy_id, kr.description, kr.target_units, kr.target_date,
		       kr.tune_score, kr.tune_feedback, kr.deleted_at, kr.created_at, kr.updated_at
		FROM key_results kr
		WHERE kr.strategy_id = $1 AND kr.deleted_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM objective_key_result_pairs okrp WHERE okrp.key_result_id = kr.id
		  )
		ORDER BY kr.created_at
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keyResults []domain.KeyResult
	for rows.Next() {
		var kr domain.KeyResult
		if err := rows.Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetUnits, &kr.TargetDate, &kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt, &kr.CreatedAt, &kr.UpdatedAt); err != nil {
			return nil, err
		}
		keyResults = append(keyResults, kr)
	}
	return keyResults, nil
}

// CreateKeyResult creates a new key result.
func (db *DB) CreateKeyResult(ctx context.Context, kr *domain.KeyResult) error {
	now := time.Now()
	if kr.ID == uuid.Nil {
		kr.ID = uuid.New()
	}
	kr.CreatedAt = now
	kr.UpdatedAt = now

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO key_results (id, strategy_id, description, target_units, target_date, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
	`, kr.ID, kr.StrategyID, kr.Description, kr.TargetUnits, kr.TargetDate, now)
	return err
}

// UpdateKeyResult updates a key result and clears tuning.
func (db *DB) UpdateKeyResult(ctx context.Context, kr *domain.KeyResult) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE key_results SET
			description = $2,
			target_units = $3,
			target_date = $4,
			tune_score = NULL,
			tune_feedback = NULL,
			updated_at = NOW()
		WHERE id = $1
	`, kr.ID, kr.Description, kr.TargetUnits, kr.TargetDate)
	return err
}

// SoftDeleteKeyResult soft-deletes a key result by setting deleted_at.
func (db *DB) SoftDeleteKeyResult(ctx context.Context, id uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE key_results SET deleted_at = NOW(), updated_at = NOW() WHERE id = $1
	`, id)
	return err
}

// LinkKeyResultToObjective creates a junction table entry linking a KR to an objective.
func (db *DB) LinkKeyResultToObjective(ctx context.Context, objectiveID, keyResultID uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO objective_key_result_pairs (objective_id, key_result_id, created_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (objective_id, key_result_id) DO NOTHING
	`, objectiveID, keyResultID)
	return err
}

// UnlinkKeyResultFromObjective removes a junction table entry. If the KR becomes orphaned, it is soft-deleted.
func (db *DB) UnlinkKeyResultFromObjective(ctx context.Context, objectiveID, keyResultID uuid.UUID) error {
	// Delete the link
	_, err := db.Pool.Exec(ctx, `
		DELETE FROM objective_key_result_pairs
		WHERE objective_id = $1 AND key_result_id = $2
	`, objectiveID, keyResultID)
	if err != nil {
		return err
	}

	// Check if KR is now orphaned and soft-delete it if so
	var count int
	err = db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM objective_key_result_pairs WHERE key_result_id = $1
	`, keyResultID).Scan(&count)
	if err != nil {
		return err
	}

	if count == 0 {
		return db.SoftDeleteKeyResult(ctx, keyResultID)
	}
	return nil
}

// GetObjectiveIDsForKeyResult retrieves all objective IDs linked to a key result.
func (db *DB) GetObjectiveIDsForKeyResult(ctx context.Context, keyResultID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT objective_id FROM objective_key_result_pairs WHERE key_result_id = $1
	`, keyResultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// GetAvailableKeyResultsForObjective returns KRs that can be linked to an objective
// (same strategy, not already linked, not deleted).
func (db *DB) GetAvailableKeyResultsForObjective(ctx context.Context, objectiveID uuid.UUID) ([]domain.KeyResult, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT kr.id, kr.strategy_id, kr.description, kr.target_units, kr.target_date,
		       kr.tune_score, kr.tune_feedback, kr.deleted_at, kr.created_at, kr.updated_at
		FROM key_results kr
		JOIN objectives o ON o.strategy_id = kr.strategy_id
		WHERE o.id = $1 AND kr.deleted_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM objective_key_result_pairs okrp
			WHERE okrp.objective_id = $1 AND okrp.key_result_id = kr.id
		  )
		ORDER BY kr.created_at
	`, objectiveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keyResults []domain.KeyResult
	for rows.Next() {
		var kr domain.KeyResult
		if err := rows.Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetUnits, &kr.TargetDate, &kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt, &kr.CreatedAt, &kr.UpdatedAt); err != nil {
			return nil, err
		}
		keyResults = append(keyResults, kr)
	}
	return keyResults, nil
}

// =====================================================
// OKR Tuning Queries
// =====================================================

// GetUntunedObjectives retrieves objectives without tuning scores for a strategy.
func (db *DB) GetUntunedObjectives(ctx context.Context, strategyID uuid.UUID) ([]domain.Objective, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, parent_id, description, tune_score, tune_feedback, deleted_at, created_at, updated_at
		FROM objectives WHERE strategy_id = $1 AND deleted_at IS NULL AND tune_score IS NULL
		ORDER BY created_at
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var objectives []domain.Objective
	for rows.Next() {
		var o domain.Objective
		if err := rows.Scan(&o.ID, &o.StrategyID, &o.ParentID, &o.Description, &o.TuneScore, &o.TuneFeedback, &o.DeletedAt, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		objectives = append(objectives, o)
	}
	return objectives, nil
}

// GetUntunedKeyResults retrieves key results without tuning scores for a strategy.
func (db *DB) GetUntunedKeyResults(ctx context.Context, strategyID uuid.UUID) ([]domain.KeyResult, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, description, target_units, target_date, tune_score, tune_feedback, deleted_at, created_at, updated_at
		FROM key_results WHERE strategy_id = $1 AND deleted_at IS NULL AND tune_score IS NULL
		ORDER BY created_at
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keyResults []domain.KeyResult
	for rows.Next() {
		var kr domain.KeyResult
		if err := rows.Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetUnits, &kr.TargetDate, &kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt, &kr.CreatedAt, &kr.UpdatedAt); err != nil {
			return nil, err
		}
		keyResults = append(keyResults, kr)
	}
	return keyResults, nil
}

// UpdateObjectiveTuning updates the tuning score and feedback for an objective.
func (db *DB) UpdateObjectiveTuning(ctx context.Context, id uuid.UUID, score float64, feedback string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE objectives SET tune_score = $2, tune_feedback = $3, updated_at = NOW() WHERE id = $1
	`, id, score, feedback)
	return err
}

// UpdateKeyResultTuning updates the tuning score and feedback for a key result.
func (db *DB) UpdateKeyResultTuning(ctx context.Context, id uuid.UUID, score float64, feedback string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE key_results SET tune_score = $2, tune_feedback = $3, updated_at = NOW() WHERE id = $1
	`, id, score, feedback)
	return err
}

// ClearObjectiveTuning clears the tuning score and feedback for an objective.
func (db *DB) ClearObjectiveTuning(ctx context.Context, id uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE objectives SET tune_score = NULL, tune_feedback = NULL, updated_at = NOW() WHERE id = $1
	`, id)
	return err
}

// ClearKeyResultTuning clears the tuning score and feedback for a key result.
func (db *DB) ClearKeyResultTuning(ctx context.Context, id uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE key_results SET tune_score = NULL, tune_feedback = NULL, updated_at = NOW() WHERE id = $1
	`, id)
	return err
}

// GetObjectiveAncestors retrieves the ancestor chain for breadcrumb navigation.
// Returns ancestors in order from root to parent (excluding the objective itself).
func (db *DB) GetObjectiveAncestors(ctx context.Context, objectiveID uuid.UUID) ([]domain.Objective, error) {
	rows, err := db.Pool.Query(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT id, strategy_id, parent_id, description, tune_score, tune_feedback, deleted_at, created_at, updated_at, 0 as depth
			FROM objectives WHERE id = $1
			UNION ALL
			SELECT o.id, o.strategy_id, o.parent_id, o.description, o.tune_score, o.tune_feedback, o.deleted_at, o.created_at, o.updated_at, a.depth + 1
			FROM objectives o
			JOIN ancestors a ON o.id = a.parent_id
		)
		SELECT id, strategy_id, parent_id, description, tune_score, tune_feedback, deleted_at, created_at, updated_at
		FROM ancestors
		WHERE depth > 0
		ORDER BY depth DESC
	`, objectiveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var objectives []domain.Objective
	for rows.Next() {
		var o domain.Objective
		if err := rows.Scan(&o.ID, &o.StrategyID, &o.ParentID, &o.Description, &o.TuneScore, &o.TuneFeedback, &o.DeletedAt, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		objectives = append(objectives, o)
	}
	return objectives, nil
}

// ActivateDependentHops marks hops that depend on completedHopID as active
// if all their dependencies are now completed.
func (db *DB) ActivateDependentHops(ctx context.Context, completedHopID uuid.UUID) (int, error) {
	result, err := db.Pool.Exec(ctx, `
		UPDATE hops
		SET status = 'active', updated_at = NOW()
		WHERE status = 'pending'
		  AND id IN (
			SELECT hd.hop_id
			FROM hop_dependencies hd
			WHERE hd.depends_on_hop_id = $1
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM hop_dependencies hd2
			JOIN hops dep ON dep.id = hd2.depends_on_hop_id
			WHERE hd2.hop_id = hops.id
			  AND dep.status != 'completed'
		  )
	`, completedHopID)
	if err != nil {
		return 0, err
	}
	return int(result.RowsAffected()), nil
}
