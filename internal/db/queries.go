package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
				INSERT INTO key_results (id, strategy_id, description,
				                         target_comparator, target_value, target_unit,
				                         target_date, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
				ON CONFLICT (id) DO UPDATE SET
					description = EXCLUDED.description,
					target_comparator = EXCLUDED.target_comparator,
					target_value = EXCLUDED.target_value,
					target_unit = EXCLUDED.target_unit,
					target_date = EXCLUDED.target_date,
					updated_at = $8
			`, krID, strategyID, kr.Description,
				kr.TargetComparator, kr.TargetValue, kr.TargetUnit, targetDate, now)
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
		fundID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("funding:%s:%s", strategyID, fund.Name)))
		_, err = tx.Exec(ctx, `
			INSERT INTO funding_sources (id, strategy_id, name, amount_usd, period_start, period_end, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
			ON CONFLICT (id) DO UPDATE SET
				name = EXCLUDED.name,
				amount_usd = EXCLUDED.amount_usd,
				period_start = EXCLUDED.period_start,
				period_end = EXCLUDED.period_end,
				updated_at = $7
		`, fundID, strategyID, fund.Name, fund.AmountUSD,
			parseInputDate(fund.PeriodStart), parseInputDate(fund.PeriodEnd), now)
		if err != nil {
			return uuid.Nil, fmt.Errorf("upsert funding source %s: %w", fund.Name, err)
		}

		// Tie the budget to the key results it is meant to move. Those key
		// results carry target dates, which is what makes a budget answerable
		// to a milestone rather than only to a calendar.
		for _, krRef := range fund.KeyResultIDs {
			krID, err := uuid.Parse(krRef)
			if err != nil {
				krID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("keyresult:"+krRef))
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO funding_success_criteria (id, funding_source_id, key_result_id, weight, created_at)
				VALUES ($1, $2, $3, 1.0, $4)
				ON CONFLICT (funding_source_id, key_result_id) DO NOTHING
			`, uuid.New(), fundID, krID, now)
			if err != nil {
				return uuid.Nil, fmt.Errorf("link funding %s to key result %s: %w", fund.Name, krRef, err)
			}
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
		SELECT id, name, config, brief, created_at, updated_at
		FROM projects WHERE id = $1
	`, id).Scan(&p.ID, &p.Name, &p.Config, &p.Brief, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProjectByName retrieves a project by name.
func (db *DB) GetProjectByName(ctx context.Context, name string) (*domain.Project, error) {
	var p domain.Project
	err := db.Pool.QueryRow(ctx, `
		SELECT id, name, config, brief, created_at, updated_at
		FROM projects WHERE name = $1
	`, name).Scan(&p.ID, &p.Name, &p.Config, &p.Brief, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProjectReadiness checks if a project has required settings configured.
func (db *DB) GetProjectReadiness(ctx context.Context, projectID uuid.UUID) (domain.ProjectReadiness, error) {
	var readiness domain.ProjectReadiness

	var repoURL, authToken *string
	err := db.Pool.QueryRow(ctx, `
		SELECT r.url, r.config->>'auth_token'
		FROM repositories r
		WHERE r.project_id = $1
		LIMIT 1
	`, projectID).Scan(&repoURL, &authToken)

	if err == nil {
		readiness.HasRepoURL = repoURL != nil && *repoURL != ""
		readiness.HasAuthToken = authToken != nil && *authToken != ""
	}

	return readiness, nil
}

// GetStrategiesByProject retrieves all strategies for a project.
func (db *DB) GetStrategiesByProject(ctx context.Context, projectID uuid.UUID) ([]domain.Strategy, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, project_id, parent_id, name, okrs_approved_at, draft_notes,
		       measurements_asked_at, draft_status, draft_error, draft_started_at, created_at, updated_at
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
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.ParentID, &s.Name, &s.OKRsApprovedAt,
			&s.DraftNotes, &s.MeasurementsAskedAt, &s.DraftStatus, &s.DraftError, &s.DraftStartedAt,
			&s.CreatedAt, &s.UpdatedAt); err != nil {
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
		SELECT kr.id, kr.strategy_id, kr.description, kr.target_comparator, kr.target_value, kr.target_unit, kr.target_date,
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
		if err := rows.Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetComparator, &kr.TargetValue, &kr.TargetUnit, &kr.TargetDate,
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
		SELECT id, strategy_id, name, amount_usd, period_start, period_end, created_at, updated_at
		FROM funding_sources WHERE strategy_id = $1
		ORDER BY created_at
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []domain.FundingSource
	for rows.Next() {
		var f domain.FundingSource
		if err := rows.Scan(&f.ID, &f.StrategyID, &f.Name, &f.AmountUSD,
			&f.PeriodStart, &f.PeriodEnd, &f.CreatedAt, &f.UpdatedAt); err != nil {
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
		SELECT id, project_id, parent_id, name, okrs_approved_at, draft_notes,
		       measurements_asked_at, draft_status, draft_error, draft_started_at, created_at, updated_at
		FROM strategies WHERE id = $1
	`, id).Scan(&s.ID, &s.ProjectID, &s.ParentID, &s.Name, &s.OKRsApprovedAt,
		&s.DraftNotes, &s.MeasurementsAskedAt, &s.DraftStatus, &s.DraftError, &s.DraftStartedAt,
		&s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateInputRequest creates a new decision/input request.
func (db *DB) CreateInputRequest(ctx context.Context, d *domain.InputRequest) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO input_requests (id, project_id, kind, title, details, instructions, link, required_capabilities,
		                            objectivity_score, importance_score, status, subject_type, subject_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)
	`, d.ID, d.ProjectID, d.Kind, d.Title, d.Details, d.Instructions, d.Link, d.RequiredCapabilities,
		d.ObjectivityScore, d.ImportanceScore, d.Status, d.SubjectType, d.SubjectID, d.CreatedAt)
	return err
}

// GetInputRequest retrieves a decision/input request by ID.
func (db *DB) GetInputRequest(ctx context.Context, id uuid.UUID) (*domain.InputRequest, error) {
	var d domain.InputRequest
	err := db.Pool.QueryRow(ctx, `
		SELECT id, project_id, kind, title, details, instructions, link, required_capabilities,
		       objectivity_score, importance_score, status,
			   assigned_to, assigned_at, accepted_by, accepted_at,
			   resolved_by, resolved_at, resolution, rationale,
			   subject_type, subject_id, created_at, updated_at
		FROM input_requests WHERE id = $1
	`, id).Scan(
		&d.ID, &d.ProjectID, &d.Kind, &d.Title, &d.Details, &d.Instructions, &d.Link, &d.RequiredCapabilities,
		&d.ObjectivityScore, &d.ImportanceScore, &d.Status,
		&d.AssignedTo, &d.AssignedAt, &d.AcceptedBy, &d.AcceptedAt,
		&d.ResolvedBy, &d.ResolvedAt, &d.Resolution, &d.Rationale,
		&d.SubjectType, &d.SubjectID, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetInputRequestsBySubject retrieves all decisions for a subject.
func (db *DB) GetInputRequestsBySubject(ctx context.Context, subjectType string, subjectID uuid.UUID) ([]domain.InputRequest, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, project_id, kind, title, details, objectivity_score, importance_score, status,
			   assigned_to, assigned_at, accepted_by, accepted_at,
			   resolved_by, resolved_at, resolution, rationale,
			   subject_type, subject_id, created_at, updated_at
		FROM input_requests
		WHERE subject_type = $1 AND subject_id = $2
		ORDER BY created_at DESC
	`, subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var decisions []domain.InputRequest
	for rows.Next() {
		var d domain.InputRequest
		if err := rows.Scan(
			&d.ID, &d.ProjectID, &d.Kind, &d.Title, &d.Details, &d.ObjectivityScore, &d.ImportanceScore, &d.Status,
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

// GetInputRequestsByProject retrieves all decisions related to a project.
func (db *DB) GetInputRequestsByProject(ctx context.Context, projectID uuid.UUID) ([]domain.InputRequest, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, project_id, kind, title, details, objectivity_score, importance_score, status,
			   assigned_to, assigned_at, accepted_by, accepted_at,
			   resolved_by, resolved_at, resolution, rationale,
			   subject_type, subject_id, created_at, updated_at
		FROM input_requests
		WHERE project_id = $1
		ORDER BY created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var decisions []domain.InputRequest
	for rows.Next() {
		var d domain.InputRequest
		if err := rows.Scan(
			&d.ID, &d.ProjectID, &d.Kind, &d.Title, &d.Details, &d.ObjectivityScore, &d.ImportanceScore, &d.Status,
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

// CountOpenInputRequestsByProject counts unresolved input requests for a project.
func (db *DB) CountOpenInputRequestsByProject(ctx context.Context, projectID uuid.UUID) (int, error) {
	var count int
	err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM input_requests
		WHERE project_id = $1 AND status != 'resolved'
	`, projectID).Scan(&count)
	return count, err
}

// UpdateInputRequest updates a decision.
func (db *DB) UpdateInputRequest(ctx context.Context, d *domain.InputRequest) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE input_requests SET
			title = $2, details = $3, status = $4,
			assigned_to = $5, assigned_at = $6,
			accepted_by = $7, accepted_at = $8,
			resolved_by = $9, resolved_at = $10,
			resolution = $11, rationale = $12,
			importance_score = $13,
			updated_at = NOW()
		WHERE id = $1
	`, d.ID, d.Title, d.Details, d.Status,
		d.AssignedTo, d.AssignedAt,
		d.AcceptedBy, d.AcceptedAt,
		d.ResolvedBy, d.ResolvedAt,
		d.Resolution, d.Rationale, d.ImportanceScore)
	return err
}

// CreateInputRequestMessage creates a new decision message.
func (db *DB) CreateInputRequestMessage(ctx context.Context, m *domain.InputRequestMessage) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO input_request_messages (id, input_request_id, role, content, tokens_used, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, m.ID, m.InputRequestID, m.Role, m.Content, m.TokensUsed, m.CreatedAt)
	return err
}

// GetInputRequestMessages retrieves all messages for a decision.
func (db *DB) GetInputRequestMessages(ctx context.Context, inputRequestID uuid.UUID) ([]domain.InputRequestMessage, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, input_request_id, role, content, tokens_used, created_at
		FROM input_request_messages
		WHERE input_request_id = $1
		ORDER BY created_at ASC
	`, inputRequestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []domain.InputRequestMessage
	for rows.Next() {
		var m domain.InputRequestMessage
		if err := rows.Scan(&m.ID, &m.InputRequestID, &m.Role, &m.Content, &m.TokensUsed, &m.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, nil
}

// CreateHop creates a new hop.
func (db *DB) CreateHop(ctx context.Context, h *domain.Hop) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO hops (id, strategy_id, name, commentary, params, evaluation_criteria,
		                  requires_demo, requires_production, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
	`, h.ID, h.StrategyID, h.Name, h.Commentary, h.Params, h.EvaluationCriteria,
		h.RequiresDemo, h.RequiresProduction, h.Status, h.CreatedAt)
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

// CreateBudgetAllocation grants a Hop a USD spend ceiling from a funding source.
func (db *DB) CreateBudgetAllocation(ctx context.Context, hopID, fundingSourceID uuid.UUID, limitUSD float64) error {
	id := uuid.New()
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO budget_allocations (id, hop_id, funding_source_id, limit_usd, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
	`, id, hopID, fundingSourceID, limitUSD)
	return err
}

// GetPrimaryFundingSource returns the strategy's largest funding source, which
// is what Hop allocations draw against when no source is named explicitly.
func (db *DB) GetPrimaryFundingSource(ctx context.Context, strategyID uuid.UUID) (*domain.FundingSource, error) {
	var f domain.FundingSource
	err := db.Pool.QueryRow(ctx, `
		SELECT id, strategy_id, name, amount_usd, period_start, period_end, created_at, updated_at
		FROM funding_sources
		WHERE strategy_id = $1
		ORDER BY amount_usd DESC, created_at
		LIMIT 1
	`, strategyID).Scan(&f.ID, &f.StrategyID, &f.Name, &f.AmountUSD,
		&f.PeriodStart, &f.PeriodEnd, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// GetHopsByStrategy retrieves all hops for a strategy.
func (db *DB) GetHopsByStrategy(ctx context.Context, strategyID uuid.UUID) ([]domain.Hop, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, name, commentary, params, evaluation_criteria,
		       requires_demo, requires_production, status, created_at, updated_at
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
		if err := rows.Scan(&h.ID, &h.StrategyID, &h.Name, &h.Commentary, &h.Params, &h.EvaluationCriteria,
			&h.RequiresDemo, &h.RequiresProduction, &h.Status, &h.CreatedAt, &h.UpdatedAt); err != nil {
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
		SELECT id, strategy_id, name, commentary, params, evaluation_criteria,
		       requires_demo, requires_production, status, created_at, updated_at
		FROM hops WHERE id = $1
	`, id).Scan(&h.ID, &h.StrategyID, &h.Name, &h.Commentary, &h.Params, &h.EvaluationCriteria,
		&h.RequiresDemo, &h.RequiresProduction, &h.Status, &h.CreatedAt, &h.UpdatedAt)
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

// UpdateHopComparisonRequirements updates the demo/production requirements for a hop.
func (db *DB) UpdateHopComparisonRequirements(ctx context.Context, hopID uuid.UUID, requiresDemo, requiresProduction bool) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE hops SET requires_demo = $1, requires_production = $2, updated_at = NOW() WHERE id = $3
	`, requiresDemo, requiresProduction, hopID)
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
		SELECT id, hop_id, name, approach, repository_id, commit_ref, ecosystem_id, deployment_ref,
		       diff_files_changed, diff_additions, diff_deletions, evaluation_scores,
		       budget_paused_usd, budget_ceiling_usd,
		       status, created_at, updated_at
		FROM variations WHERE id = $1
	`, id).Scan(&v.ID, &v.HopID, &v.Name, &v.Approach, &v.RepositoryID, &v.CommitRef, &v.EcosystemID, &v.DeploymentRef,
		&v.DiffFilesChanged, &v.DiffAdditions, &v.DiffDeletions, &v.EvaluationScores,
		&v.BudgetPausedUSD, &v.BudgetCeilingUSD,
		&v.Status, &v.CreatedAt, &v.UpdatedAt)
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

// AtomicUpdateVariationStatus atomically updates a variation's status only if it's in the expected current state.
// Returns true if the update succeeded, false if the current status didn't match (meaning someone else changed it).
func (db *DB) AtomicUpdateVariationStatus(ctx context.Context, variationID uuid.UUID, fromStatus, toStatus domain.VariationStatus) (bool, error) {
	result, err := db.Pool.Exec(ctx, `
		UPDATE variations SET status = $3, updated_at = NOW()
		WHERE id = $1 AND status = $2
	`, variationID, fromStatus, toStatus)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

// UpdateVariationDiffStats updates the diff stats for a variation.
func (db *DB) UpdateVariationDiffStats(ctx context.Context, variationID uuid.UUID, filesChanged, additions, deletions int) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE variations SET
			diff_files_changed = $2, diff_additions = $3, diff_deletions = $4, updated_at = NOW()
		WHERE id = $1
	`, variationID, filesChanged, additions, deletions)
	return err
}

// UpdateVariationEvaluationScores updates the cached evaluation scores for a variation.
func (db *DB) UpdateVariationEvaluationScores(ctx context.Context, variationID uuid.UUID, scores json.RawMessage) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE variations SET evaluation_scores = $2, updated_at = NOW() WHERE id = $1
	`, variationID, scores)
	return err
}

// GetVariationsByHop retrieves all variations for a hop.
func (db *DB) GetVariationsByHop(ctx context.Context, hopID uuid.UUID) ([]domain.Variation, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, hop_id, name, approach, repository_id, commit_ref, ecosystem_id, deployment_ref,
		       diff_files_changed, diff_additions, diff_deletions, evaluation_scores,
		       budget_paused_usd, budget_ceiling_usd,
		       status, created_at, updated_at
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
		if err := rows.Scan(&v.ID, &v.HopID, &v.Name, &v.Approach, &v.RepositoryID, &v.CommitRef, &v.EcosystemID, &v.DeploymentRef,
			&v.DiffFilesChanged, &v.DiffAdditions, &v.DiffDeletions, &v.EvaluationScores,
			&v.BudgetPausedUSD, &v.BudgetCeilingUSD,
			&v.Status, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		variations = append(variations, v)
	}
	return variations, nil
}

// GetHopsWithCreatingVariations returns hops that have variations in "creating" status.
func (db *DB) GetHopsWithCreatingVariations(ctx context.Context) ([]domain.Hop, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT h.id, h.strategy_id, h.name, h.commentary, h.params, h.evaluation_criteria,
		       h.requires_demo, h.requires_production, h.status, h.created_at, h.updated_at
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
		if err := rows.Scan(&h.ID, &h.StrategyID, &h.Name, &h.Commentary, &h.Params, &h.EvaluationCriteria,
			&h.RequiresDemo, &h.RequiresProduction, &h.Status, &h.CreatedAt, &h.UpdatedAt); err != nil {
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

// GetBudgetAllocationsByHop retrieves all budget allocations for a hop.
func (db *DB) GetBudgetAllocationsByHop(ctx context.Context, hopID uuid.UUID) ([]domain.BudgetAllocation, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, hop_id, funding_source_id, limit_usd, created_at, updated_at
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
		if err := rows.Scan(&a.ID, &a.HopID, &a.FundingSourceID, &a.LimitUSD, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		allocations = append(allocations, a)
	}
	return allocations, nil
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

// CreateVariationLog creates a new log entry for a variation (defaults to codegen source).
func (db *DB) CreateVariationLog(ctx context.Context, variationID uuid.UUID, level domain.LogLevel, message string) error {
	return db.CreateVariationLogWithSource(ctx, variationID, level, message, domain.SourceTypeCodegen, nil)
}

// CreateVariationLogWithSource creates a log entry with explicit source tracking.
func (db *DB) CreateVariationLogWithSource(ctx context.Context, variationID uuid.UUID, level domain.LogLevel, message string, sourceType domain.SourceType, sourceID *uuid.UUID) error {
	id := uuid.New()
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO variation_logs (id, variation_id, logged_at, level, message, source_type, source_id)
		VALUES ($1, $2, NOW(), $3, $4, $5, $6)
	`, id, variationID, string(level), message, string(sourceType), sourceID)
	return err
}

// GetVariationLogs retrieves logs for a variation.
// Returns the most recent `limit` logs in chronological order (oldest first for display).
func (db *DB) GetVariationLogs(ctx context.Context, variationID uuid.UUID, limit int) ([]domain.VariationLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT id, variation_id, logged_at, level, message, source_type, source_id
		FROM (
			SELECT id, variation_id, logged_at, level, message, source_type, source_id
			FROM variation_logs
			WHERE variation_id = $1
			ORDER BY logged_at DESC
			LIMIT $2
		) recent
		ORDER BY logged_at ASC
	`, variationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []domain.VariationLog
	for rows.Next() {
		var l domain.VariationLog
		if err := rows.Scan(&l.ID, &l.VariationID, &l.LoggedAt, &l.Level, &l.Message, &l.SourceType, &l.SourceID); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, nil
}

// GetVariationLogsBySource retrieves logs for a specific source (e.g., a demo instance).
func (db *DB) GetVariationLogsBySource(ctx context.Context, sourceType domain.SourceType, sourceID uuid.UUID, limit int) ([]domain.VariationLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT id, variation_id, logged_at, level, message, source_type, source_id
		FROM (
			SELECT id, variation_id, logged_at, level, message, source_type, source_id
			FROM variation_logs
			WHERE source_type = $1 AND source_id = $2
			ORDER BY logged_at DESC
			LIMIT $3
		) recent
		ORDER BY logged_at ASC
	`, string(sourceType), sourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []domain.VariationLog
	for rows.Next() {
		var l domain.VariationLog
		if err := rows.Scan(&l.ID, &l.VariationID, &l.LoggedAt, &l.Level, &l.Message, &l.SourceType, &l.SourceID); err != nil {
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

// GetVariationLogsByType retrieves logs for a variation filtered by source type.
// Returns the most recent `limit` logs in chronological order (oldest first for display).
func (db *DB) GetVariationLogsByType(ctx context.Context, variationID uuid.UUID, sourceType domain.SourceType, limit int) ([]domain.VariationLog, error) {
	if limit <= 0 {
		limit = 100
	}
	// Subquery gets most recent N logs, outer query orders them chronologically
	rows, err := db.Pool.Query(ctx, `
		SELECT id, variation_id, logged_at, level, message, source_type, source_id
		FROM (
			SELECT id, variation_id, logged_at, level, message, source_type, source_id
			FROM variation_logs
			WHERE variation_id = $1 AND source_type = $2
			ORDER BY logged_at DESC
			LIMIT $3
		) recent
		ORDER BY logged_at ASC
	`, variationID, string(sourceType), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []domain.VariationLog
	for rows.Next() {
		var l domain.VariationLog
		if err := rows.Scan(&l.ID, &l.VariationID, &l.LoggedAt, &l.Level, &l.Message, &l.SourceType, &l.SourceID); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, nil
}

// GetInputRequestBySubjectAndKind retrieves a decision by subject and kind.
func (db *DB) GetInputRequestBySubjectAndKind(ctx context.Context, subjectType string, subjectID uuid.UUID, kind domain.InputRequestKind) (*domain.InputRequest, error) {
	var d domain.InputRequest
	err := db.Pool.QueryRow(ctx, `
		SELECT id, kind, title, details, objectivity_score, importance_score, status,
			   assigned_to, assigned_at, accepted_by, accepted_at,
			   resolved_by, resolved_at, resolution, rationale,
			   subject_type, subject_id, created_at, updated_at
		FROM input_requests
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

// GetHopsNeedingSelectionInputRequest returns hops with at least one pending variation
// but no unresolved variation_selection input request. Includes both 'active' and 'selecting'
// hops to handle cases where status was updated but input request wasn't created.
// Also excludes hops that have an unresolved variation_review input request (user is still
// proposing/reviewing additional variations).
func (db *DB) GetHopsNeedingSelectionInputRequest(ctx context.Context) ([]domain.Hop, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT h.id, h.strategy_id, h.name, h.commentary, h.params, h.evaluation_criteria,
		       h.requires_demo, h.requires_production, h.status, h.created_at, h.updated_at
		FROM hops h
		JOIN variations v ON v.hop_id = h.id
		WHERE h.status IN ('active', 'selecting')
		  AND v.status = 'pending'
		  AND NOT EXISTS (
			SELECT 1 FROM input_requests d
			WHERE d.subject_type = 'hop'
			  AND d.subject_id = h.id
			  AND d.kind = 'variation_selection'
			  AND d.status != 'resolved'
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM input_requests d
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
		if err := rows.Scan(&h.ID, &h.StrategyID, &h.Name, &h.Commentary, &h.Params, &h.EvaluationCriteria,
			&h.RequiresDemo, &h.RequiresProduction, &h.Status, &h.CreatedAt, &h.UpdatedAt); err != nil {
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
		SELECT h.id, h.strategy_id, h.name, h.commentary, h.params, h.evaluation_criteria,
		       h.requires_demo, h.requires_production, h.status, h.created_at, h.updated_at
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
		if err := rows.Scan(&h.ID, &h.StrategyID, &h.Name, &h.Commentary, &h.Params, &h.EvaluationCriteria,
			&h.RequiresDemo, &h.RequiresProduction, &h.Status, &h.CreatedAt, &h.UpdatedAt); err != nil {
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

// CompletedDependencyInfo holds info about a completed dependency hop and its selected variation.
type CompletedDependencyInfo struct {
	HopID             uuid.UUID
	HopName           string
	HopCommentary     string
	VariationID       uuid.UUID
	VariationName     string
	VariationApproach string
}

// GetCompletedTransitiveDependencies returns all completed dependency hops (transitive closure)
// with their selected/merged variations. This is used to provide context when proposing new variations.
func (db *DB) GetCompletedTransitiveDependencies(ctx context.Context, hopID uuid.UUID) ([]CompletedDependencyInfo, error) {
	rows, err := db.Pool.Query(ctx, `
		WITH RECURSIVE transitive_deps AS (
			-- Base case: direct dependencies
			SELECT depends_on_hop_id as hop_id, 1 as depth
			FROM hop_dependencies
			WHERE hop_id = $1

			UNION

			-- Recursive case: dependencies of dependencies
			SELECT hd.depends_on_hop_id, td.depth + 1
			FROM hop_dependencies hd
			JOIN transitive_deps td ON hd.hop_id = td.hop_id
			WHERE td.depth < 100  -- Safety limit to prevent infinite recursion
		)
		SELECT DISTINCT
			h.id as hop_id,
			h.name as hop_name,
			h.commentary as hop_commentary,
			v.id as variation_id,
			v.name as variation_name,
			v.approach as variation_approach
		FROM transitive_deps td
		JOIN hops h ON h.id = td.hop_id
		JOIN variations v ON v.hop_id = h.id
		WHERE h.status = 'completed'
		  AND v.status IN ('merged', 'selected')
		ORDER BY h.name
	`, hopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deps []CompletedDependencyInfo
	for rows.Next() {
		var d CompletedDependencyInfo
		if err := rows.Scan(&d.HopID, &d.HopName, &d.HopCommentary, &d.VariationID, &d.VariationName, &d.VariationApproach); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// GetHopsNeedingVariationProposal returns active hops that have no variations
// and no existing variation_review input request (pending or resolved).
func (db *DB) GetHopsNeedingVariationProposal(ctx context.Context) ([]domain.Hop, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT h.id, h.strategy_id, h.name, h.commentary, h.params, h.evaluation_criteria, h.status, h.created_at, h.updated_at
		FROM hops h
		WHERE h.status = 'active'
		  AND NOT EXISTS (
			SELECT 1 FROM variations v WHERE v.hop_id = h.id
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM input_requests d
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
		SELECT id, strategy_id, description, target_comparator, target_value, target_unit, target_date, tune_score, tune_feedback, deleted_at, created_at, updated_at
		FROM key_results WHERE id = $1
	`, id).Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetComparator, &kr.TargetValue, &kr.TargetUnit, &kr.TargetDate, &kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt, &kr.CreatedAt, &kr.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &kr, nil
}

// GetAllKeyResultsForStrategy retrieves all non-deleted key results for a strategy.
func (db *DB) GetAllKeyResultsForStrategy(ctx context.Context, strategyID uuid.UUID) ([]domain.KeyResult, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, description, target_comparator, target_value, target_unit, target_date, tune_score, tune_feedback, deleted_at, created_at, updated_at
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
		if err := rows.Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetComparator, &kr.TargetValue, &kr.TargetUnit, &kr.TargetDate, &kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt, &kr.CreatedAt, &kr.UpdatedAt); err != nil {
			return nil, err
		}
		keyResults = append(keyResults, kr)
	}
	return keyResults, nil
}

// GetUnlinkedKeyResults retrieves key results not linked to any objective.
func (db *DB) GetUnlinkedKeyResults(ctx context.Context, strategyID uuid.UUID) ([]domain.KeyResult, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT kr.id, kr.strategy_id, kr.description, kr.target_comparator, kr.target_value, kr.target_unit, kr.target_date,
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
		if err := rows.Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetComparator, &kr.TargetValue, &kr.TargetUnit, &kr.TargetDate, &kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt, &kr.CreatedAt, &kr.UpdatedAt); err != nil {
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
		INSERT INTO key_results (id, strategy_id, description,
		                         target_comparator, target_value, target_unit,
		                         target_date, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
	`, kr.ID, kr.StrategyID, kr.Description,
		kr.TargetComparator, kr.TargetValue, kr.TargetUnit, kr.TargetDate, now)
	return err
}

// UpdateKeyResult updates a key result and clears tuning.
func (db *DB) UpdateKeyResult(ctx context.Context, kr *domain.KeyResult) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE key_results SET
			description = $2,
			target_comparator = $3,
			target_value = $4,
			target_unit = $5,
			target_date = $6,
			tune_score = NULL,
			tune_feedback = NULL,
			updated_at = NOW()
		WHERE id = $1
	`, kr.ID, kr.Description,
		kr.TargetComparator, kr.TargetValue, kr.TargetUnit, kr.TargetDate)
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
		SELECT kr.id, kr.strategy_id, kr.description, kr.target_comparator, kr.target_value, kr.target_unit, kr.target_date,
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
		if err := rows.Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetComparator, &kr.TargetValue, &kr.TargetUnit, &kr.TargetDate, &kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt, &kr.CreatedAt, &kr.UpdatedAt); err != nil {
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
		SELECT id, strategy_id, description, target_comparator, target_value, target_unit, target_date, tune_score, tune_feedback, deleted_at, created_at, updated_at
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
		if err := rows.Scan(&kr.ID, &kr.StrategyID, &kr.Description, &kr.TargetComparator, &kr.TargetValue, &kr.TargetUnit, &kr.TargetDate, &kr.TuneScore, &kr.TuneFeedback, &kr.DeletedAt, &kr.CreatedAt, &kr.UpdatedAt); err != nil {
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

// ActivateRootHops marks pending hops that have no dependencies as active.
// Call this after creating a roadmap to start root hops.
func (db *DB) ActivateRootHops(ctx context.Context, strategyID uuid.UUID) (int, error) {
	result, err := db.Pool.Exec(ctx, `
		UPDATE hops
		SET status = 'active', updated_at = NOW()
		WHERE strategy_id = $1
		  AND status = 'pending'
		  AND NOT EXISTS (
			SELECT 1 FROM hop_dependencies hd WHERE hd.hop_id = hops.id
		  )
	`, strategyID)
	if err != nil {
		return 0, err
	}
	return int(result.RowsAffected()), nil
}

// =====================================================
// Demo Instance Queries (added in 008)
// =====================================================

// CreateDemoInstance creates a new demo instance.
func (db *DB) CreateDemoInstance(ctx context.Context, di *domain.DemoInstance) error {
	now := time.Now()
	if di.ID == uuid.Nil {
		di.ID = uuid.New()
	}
	di.StartedAt = now
	di.CreatedAt = now

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO demo_instances (id, variation_id, url, teardown_instructions, started_at, status, process_info, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, di.ID, di.VariationID, di.URL, di.TeardownInstructions, di.StartedAt, di.Status, di.ProcessInfo, di.CreatedAt)
	return err
}

// GetDemoInstance retrieves a demo instance by ID.
func (db *DB) GetDemoInstance(ctx context.Context, id uuid.UUID) (*domain.DemoInstance, error) {
	var di domain.DemoInstance
	err := db.Pool.QueryRow(ctx, `
		SELECT id, variation_id, url, teardown_instructions, started_at, stopped_at, status, process_info, error_message, suggested_fix, created_at
		FROM demo_instances WHERE id = $1
	`, id).Scan(&di.ID, &di.VariationID, &di.URL, &di.TeardownInstructions, &di.StartedAt, &di.StoppedAt, &di.Status, &di.ProcessInfo, &di.ErrorMessage, &di.SuggestedFix, &di.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &di, nil
}

// GetRunningDemoByVariation retrieves the running or starting demo instance for a variation (if any).
func (db *DB) GetRunningDemoByVariation(ctx context.Context, variationID uuid.UUID) (*domain.DemoInstance, error) {
	var di domain.DemoInstance
	err := db.Pool.QueryRow(ctx, `
		SELECT id, variation_id, url, teardown_instructions, started_at, stopped_at, status, process_info, error_message, suggested_fix, created_at
		FROM demo_instances
		WHERE variation_id = $1 AND status IN ('starting', 'running')
		ORDER BY started_at DESC
		LIMIT 1
	`, variationID).Scan(&di.ID, &di.VariationID, &di.URL, &di.TeardownInstructions, &di.StartedAt, &di.StoppedAt, &di.Status, &di.ProcessInfo, &di.ErrorMessage, &di.SuggestedFix, &di.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &di, nil
}

// GetLatestDemoByVariation retrieves the most recent demo instance for a variation (any status).
func (db *DB) GetLatestDemoByVariation(ctx context.Context, variationID uuid.UUID) (*domain.DemoInstance, error) {
	var di domain.DemoInstance
	err := db.Pool.QueryRow(ctx, `
		SELECT id, variation_id, url, teardown_instructions, started_at, stopped_at, status, process_info, error_message, suggested_fix, created_at
		FROM demo_instances
		WHERE variation_id = $1
		ORDER BY started_at DESC
		LIMIT 1
	`, variationID).Scan(&di.ID, &di.VariationID, &di.URL, &di.TeardownInstructions, &di.StartedAt, &di.StoppedAt, &di.Status, &di.ProcessInfo, &di.ErrorMessage, &di.SuggestedFix, &di.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &di, nil
}

// GetAllRunningDemos retrieves all running or starting demo instances (for cleanup on startup).
func (db *DB) GetAllRunningDemos(ctx context.Context) ([]domain.DemoInstance, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, variation_id, url, teardown_instructions, started_at, stopped_at, status, process_info, error_message, suggested_fix, created_at
		FROM demo_instances
		WHERE status IN ('starting', 'running')
		ORDER BY started_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var demos []domain.DemoInstance
	for rows.Next() {
		var di domain.DemoInstance
		if err := rows.Scan(&di.ID, &di.VariationID, &di.URL, &di.TeardownInstructions, &di.StartedAt, &di.StoppedAt, &di.Status, &di.ProcessInfo, &di.ErrorMessage, &di.SuggestedFix, &di.CreatedAt); err != nil {
			return nil, err
		}
		demos = append(demos, di)
	}
	return demos, nil
}

// UpdateDemoInstanceStatus updates a demo instance's status.
func (db *DB) UpdateDemoInstanceStatus(ctx context.Context, id uuid.UUID, status domain.DemoInstanceStatus, errorMessage *string) error {
	var stoppedAt *time.Time
	if status == domain.DemoInstanceStatusStopped || status == domain.DemoInstanceStatusError {
		now := time.Now()
		stoppedAt = &now
	}
	_, err := db.Pool.Exec(ctx, `
		UPDATE demo_instances SET status = $2, stopped_at = $3, error_message = $4 WHERE id = $1
	`, id, status, stoppedAt, errorMessage)
	return err
}

// UpdateDemoInstanceWithSuggestedFix updates a demo instance with error status and a suggested fix.
func (db *DB) UpdateDemoInstanceWithSuggestedFix(ctx context.Context, id uuid.UUID, errorMessage, suggestedFix string) error {
	now := time.Now()
	_, err := db.Pool.Exec(ctx, `
		UPDATE demo_instances SET status = $2, stopped_at = $3, error_message = $4, suggested_fix = $5 WHERE id = $1
	`, id, domain.DemoInstanceStatusError, now, errorMessage, suggestedFix)
	return err
}

// =====================================================
// Variation Migration Queries (updated in 009)
// =====================================================

// CreateVariationMigration creates a new variation migration record.
func (db *DB) CreateVariationMigration(ctx context.Context, m *domain.VariationMigration) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO variation_migrations (id, variation_id, up_instructions, down_instructions, notes, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, m.ID, m.VariationID, m.UpInstructions, m.DownInstructions, m.Notes, m.CreatedAt)
	return err
}

// GetVariationMigration retrieves the migration for a variation (if any).
func (db *DB) GetVariationMigration(ctx context.Context, variationID uuid.UUID) (*domain.VariationMigration, error) {
	var m domain.VariationMigration
	err := db.Pool.QueryRow(ctx, `
		SELECT id, variation_id, up_instructions, down_instructions, notes, applied_at, reverted_at, created_at
		FROM variation_migrations WHERE variation_id = $1
	`, variationID).Scan(&m.ID, &m.VariationID, &m.UpInstructions, &m.DownInstructions, &m.Notes, &m.AppliedAt, &m.RevertedAt, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// GetVariationMigrationsForHop retrieves all variation migrations for variations belonging to a hop.
// Returns a map from variation ID to migration for easy lookup.
func (db *DB) GetVariationMigrationsForHop(ctx context.Context, hopID uuid.UUID) (map[string]*domain.VariationMigration, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT vm.id, vm.variation_id, vm.up_instructions, vm.down_instructions, vm.notes, vm.applied_at, vm.reverted_at, vm.created_at
		FROM variation_migrations vm
		JOIN variations v ON v.id = vm.variation_id
		WHERE v.hop_id = $1
	`, hopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*domain.VariationMigration)
	for rows.Next() {
		var m domain.VariationMigration
		if err := rows.Scan(&m.ID, &m.VariationID, &m.UpInstructions, &m.DownInstructions, &m.Notes, &m.AppliedAt, &m.RevertedAt, &m.CreatedAt); err != nil {
			return nil, err
		}
		result[m.VariationID.String()] = &m
	}
	return result, rows.Err()
}

// MarkVariationMigrationApplied marks a migration as applied.
// Returns error if already applied (applied_at is not null).
func (db *DB) MarkVariationMigrationApplied(ctx context.Context, id uuid.UUID) error {
	result, err := db.Pool.Exec(ctx, `
		UPDATE variation_migrations SET applied_at = NOW()
		WHERE id = $1 AND applied_at IS NULL
	`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("migration already applied or not found")
	}
	return nil
}

// MarkVariationMigrationReverted marks a migration as reverted.
// Returns error if not applied or already reverted.
func (db *DB) MarkVariationMigrationReverted(ctx context.Context, id uuid.UUID) error {
	result, err := db.Pool.Exec(ctx, `
		UPDATE variation_migrations SET reverted_at = NOW()
		WHERE id = $1 AND applied_at IS NOT NULL AND reverted_at IS NULL
	`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("migration not applied, already reverted, or not found")
	}
	return nil
}

// =====================================================
// Variation Revision Queries (added in 025)
// =====================================================

// CreateVariationRevision creates a new revision request for a variation.
func (db *DB) CreateVariationRevision(ctx context.Context, r *domain.VariationRevision) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO variation_revisions (id, variation_id, feedback, status, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, r.ID, r.VariationID, r.Feedback, r.Status, r.CreatedAt)
	return err
}

// GetVariationRevision retrieves a revision by ID.
func (db *DB) GetVariationRevision(ctx context.Context, id uuid.UUID) (*domain.VariationRevision, error) {
	var r domain.VariationRevision
	err := db.Pool.QueryRow(ctx, `
		SELECT id, variation_id, feedback, status, error_message, created_at, started_at, completed_at
		FROM variation_revisions WHERE id = $1
	`, id).Scan(&r.ID, &r.VariationID, &r.Feedback, &r.Status, &r.ErrorMessage, &r.CreatedAt, &r.StartedAt, &r.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetVariationRevisions retrieves all revisions for a variation, newest first.
func (db *DB) GetVariationRevisions(ctx context.Context, variationID uuid.UUID) ([]domain.VariationRevision, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, variation_id, feedback, status, error_message, created_at, started_at, completed_at
		FROM variation_revisions
		WHERE variation_id = $1
		ORDER BY created_at DESC
	`, variationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var revisions []domain.VariationRevision
	for rows.Next() {
		var r domain.VariationRevision
		if err := rows.Scan(&r.ID, &r.VariationID, &r.Feedback, &r.Status, &r.ErrorMessage, &r.CreatedAt, &r.StartedAt, &r.CompletedAt); err != nil {
			return nil, err
		}
		revisions = append(revisions, r)
	}
	return revisions, rows.Err()
}

// GetPendingVariationRevision gets the oldest pending revision for a variation.
func (db *DB) GetPendingVariationRevision(ctx context.Context, variationID uuid.UUID) (*domain.VariationRevision, error) {
	var r domain.VariationRevision
	err := db.Pool.QueryRow(ctx, `
		SELECT id, variation_id, feedback, status, error_message, created_at, started_at, completed_at
		FROM variation_revisions
		WHERE variation_id = $1 AND status = 'pending'
		ORDER BY created_at ASC
		LIMIT 1
	`, variationID).Scan(&r.ID, &r.VariationID, &r.Feedback, &r.Status, &r.ErrorMessage, &r.CreatedAt, &r.StartedAt, &r.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// UpdateVariationRevisionStatus updates the status of a revision.
func (db *DB) UpdateVariationRevisionStatus(ctx context.Context, id uuid.UUID, status domain.VariationRevisionStatus, errorMsg *string) error {
	var completedAt interface{}
	var startedAt interface{}

	if status == domain.VariationRevisionStatusInProgress {
		startedAt = time.Now()
	}
	if status == domain.VariationRevisionStatusCompleted || status == domain.VariationRevisionStatusFailed {
		completedAt = time.Now()
	}

	_, err := db.Pool.Exec(ctx, `
		UPDATE variation_revisions
		SET status = $2, error_message = $3, started_at = COALESCE($4, started_at), completed_at = COALESCE($5, completed_at)
		WHERE id = $1
	`, id, status, errorMsg, startedAt, completedAt)
	return err
}

// GetInputRequestCache retrieves the cache JSON for a decision.
func (db *DB) GetInputRequestCache(ctx context.Context, inputRequestID uuid.UUID) (json.RawMessage, error) {
	var cache json.RawMessage
	err := db.Pool.QueryRow(ctx, `
		SELECT cache FROM input_requests WHERE id = $1
	`, inputRequestID).Scan(&cache)
	if err != nil {
		return nil, err
	}
	return cache, nil
}

// SetInputRequestCache updates the cache JSON for a decision.
func (db *DB) SetInputRequestCache(ctx context.Context, inputRequestID uuid.UUID, cache json.RawMessage) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE input_requests SET cache = $2, updated_at = NOW() WHERE id = $1
	`, inputRequestID, cache)
	return err
}

// ClearInputRequestCache sets the cache to NULL for a decision.
func (db *DB) ClearInputRequestCache(ctx context.Context, inputRequestID uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE input_requests SET cache = NULL, updated_at = NOW() WHERE id = $1
	`, inputRequestID)
	return err
}

// ClearInputRequestCacheBySubject clears cache for all decisions related to a subject.
// Used when evaluation criteria change for a hop.
func (db *DB) ClearInputRequestCacheBySubject(ctx context.Context, subjectType string, subjectID uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE input_requests SET cache = NULL, updated_at = NOW()
		WHERE subject_type = $1 AND subject_id = $2
	`, subjectType, subjectID)
	return err
}

// =====================================================
// Project Credential Queries (added in 015)
// =====================================================

// CreateProjectCredential creates a new encrypted credential.
func (db *DB) CreateProjectCredential(ctx context.Context, cred *domain.ProjectCredential) error {
	now := time.Now()
	if cred.ID == uuid.Nil {
		cred.ID = uuid.New()
	}
	cred.CreatedAt = now
	cred.UpdatedAt = now

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO project_credentials (id, project_id, name, encrypted_value, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, cred.ID, cred.ProjectID, cred.Name, cred.EncryptedValue, cred.CreatedAt, cred.UpdatedAt)
	return err
}

// GetProjectCredential retrieves a credential by project and name.
func (db *DB) GetProjectCredential(ctx context.Context, projectID uuid.UUID, name string) (*domain.ProjectCredential, error) {
	var cred domain.ProjectCredential
	err := db.Pool.QueryRow(ctx, `
		SELECT id, project_id, name, encrypted_value, created_at, updated_at
		FROM project_credentials
		WHERE project_id = $1 AND name = $2
	`, projectID, name).Scan(&cred.ID, &cred.ProjectID, &cred.Name, &cred.EncryptedValue, &cred.CreatedAt, &cred.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &cred, nil
}

// GetProjectCredentialByID retrieves a credential by ID.
func (db *DB) GetProjectCredentialByID(ctx context.Context, id uuid.UUID) (*domain.ProjectCredential, error) {
	var cred domain.ProjectCredential
	err := db.Pool.QueryRow(ctx, `
		SELECT id, project_id, name, encrypted_value, created_at, updated_at
		FROM project_credentials
		WHERE id = $1
	`, id).Scan(&cred.ID, &cred.ProjectID, &cred.Name, &cred.EncryptedValue, &cred.CreatedAt, &cred.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &cred, nil
}

// ListProjectCredentials lists all credentials for a project (names only, not values).
func (db *DB) ListProjectCredentials(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectCredential, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, project_id, name, created_at, updated_at
		FROM project_credentials
		WHERE project_id = $1
		ORDER BY name
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []domain.ProjectCredential
	for rows.Next() {
		var cred domain.ProjectCredential
		if err := rows.Scan(&cred.ID, &cred.ProjectID, &cred.Name, &cred.CreatedAt, &cred.UpdatedAt); err != nil {
			return nil, err
		}
		creds = append(creds, cred)
	}
	return creds, nil
}

// UpdateProjectCredential updates an existing credential's encrypted value.
func (db *DB) UpdateProjectCredential(ctx context.Context, id uuid.UUID, encryptedValue []byte) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE project_credentials
		SET encrypted_value = $2, updated_at = NOW()
		WHERE id = $1
	`, id, encryptedValue)
	return err
}

// DeleteProjectCredential deletes a credential.
func (db *DB) DeleteProjectCredential(ctx context.Context, id uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		DELETE FROM project_credentials WHERE id = $1
	`, id)
	return err
}

// GetUnresolvedCredentialRequests returns all unresolved credential_request InputRequests for a project.
func (db *DB) GetUnresolvedCredentialRequests(ctx context.Context, projectID uuid.UUID) ([]domain.InputRequest, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT ir.id, ir.kind, ir.title, ir.details, ir.instructions, ir.link, ir.required_capabilities,
		       ir.objectivity_score, ir.importance_score, ir.status,
		       ir.assigned_to, ir.assigned_at, ir.accepted_by, ir.accepted_at,
		       ir.resolved_by, ir.resolved_at, ir.resolution, ir.rationale,
		       ir.subject_type, ir.subject_id, ir.created_at, ir.updated_at
		FROM input_requests ir
		JOIN variations v ON ir.subject_type = 'variation' AND ir.subject_id = v.id
		JOIN hops h ON v.hop_id = h.id
		JOIN strategies s ON h.strategy_id = s.id
		WHERE s.project_id = $1
		  AND ir.kind = 'credential_request'
		  AND ir.status != 'resolved'
		ORDER BY ir.created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reqs []domain.InputRequest
	for rows.Next() {
		var r domain.InputRequest
		if err := rows.Scan(
			&r.ID, &r.Kind, &r.Title, &r.Details, &r.Instructions, &r.Link, &r.RequiredCapabilities,
			&r.ObjectivityScore, &r.ImportanceScore, &r.Status,
			&r.AssignedTo, &r.AssignedAt, &r.AcceptedBy, &r.AcceptedAt,
			&r.ResolvedBy, &r.ResolvedAt, &r.Resolution, &r.Rationale,
			&r.SubjectType, &r.SubjectID, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		reqs = append(reqs, r)
	}
	return reqs, nil
}

// ResolveCredentialRequestsByName resolves all credential_request InputRequests matching a credential name.
// Called when a credential is added via Settings to auto-resolve matching requests.
func (db *DB) ResolveCredentialRequestsByName(ctx context.Context, projectID uuid.UUID, credentialName string) error {
	now := time.Now()
	_, err := db.Pool.Exec(ctx, `
		UPDATE input_requests ir
		SET status = 'resolved',
		    resolution = 'credential_provided',
		    resolved_at = $3,
		    updated_at = $3
		FROM variations v
		JOIN hops h ON v.hop_id = h.id
		JOIN strategies s ON h.strategy_id = s.id
		WHERE ir.subject_type = 'variation'
		  AND ir.subject_id = v.id
		  AND s.project_id = $1
		  AND ir.kind = 'credential_request'
		  AND ir.title LIKE '%' || $2 || '%'
		  AND ir.status != 'resolved'
	`, projectID, credentialName, now)
	return err
}

// GetBlockedVariationsWithResolvedRequests returns blocked variations whose InputRequests are now resolved.
func (db *DB) GetBlockedVariationsWithResolvedRequests(ctx context.Context) ([]domain.Variation, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT v.id, v.hop_id, v.name, v.approach, v.repository_id, v.commit_ref, v.ecosystem_id, v.deployment_ref,
		       v.diff_files_changed, v.diff_additions, v.diff_deletions, v.evaluation_scores, v.status, v.created_at, v.updated_at
		FROM variations v
		WHERE v.status = 'blocked'
		  AND NOT EXISTS (
		      SELECT 1 FROM input_requests ir
		      WHERE ir.subject_type = 'variation'
		        AND ir.subject_id = v.id
		        AND ir.status != 'resolved'
		  )
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var variations []domain.Variation
	for rows.Next() {
		var v domain.Variation
		if err := rows.Scan(&v.ID, &v.HopID, &v.Name, &v.Approach, &v.RepositoryID, &v.CommitRef, &v.EcosystemID, &v.DeploymentRef,
			&v.DiffFilesChanged, &v.DiffAdditions, &v.DiffDeletions, &v.EvaluationScores, &v.Status, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		variations = append(variations, v)
	}
	return variations, nil
}

// GetCredentialRequestForVariation returns an unresolved credential request for a variation, if any.
func (db *DB) GetCredentialRequestForVariation(ctx context.Context, variationID uuid.UUID, credentialName string) (*domain.InputRequest, error) {
	var r domain.InputRequest
	err := db.Pool.QueryRow(ctx, `
		SELECT id, kind, title, details, instructions, link, required_capabilities,
		       objectivity_score, importance_score, status,
		       assigned_to, assigned_at, accepted_by, accepted_at,
		       resolved_by, resolved_at, resolution, rationale,
		       subject_type, subject_id, created_at, updated_at
		FROM input_requests
		WHERE subject_type = 'variation'
		  AND subject_id = $1
		  AND kind = 'credential_request'
		  AND title LIKE '%' || $2 || '%'
		  AND status != 'resolved'
		LIMIT 1
	`, variationID, credentialName).Scan(
		&r.ID, &r.Kind, &r.Title, &r.Details, &r.Instructions, &r.Link, &r.RequiredCapabilities,
		&r.ObjectivityScore, &r.ImportanceScore, &r.Status,
		&r.AssignedTo, &r.AssignedAt, &r.AcceptedBy, &r.AcceptedAt,
		&r.ResolvedBy, &r.ResolvedAt, &r.Resolution, &r.Rationale,
		&r.SubjectType, &r.SubjectID, &r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// =====================================================
// Hosting Deployment Queries (added in 029)
// =====================================================

const hostingDeploymentCols = `id, project_id, channel_id, kind, variation_id, commit_sha,
	app_name, url, teardown_instructions, status, error_message,
	started_at, finished_at, created_at, updated_at`

func scanHostingDeployment(row interface {
	Scan(dest ...any) error
}) (*domain.HostingDeployment, error) {
	var d domain.HostingDeployment
	err := row.Scan(
		&d.ID, &d.ProjectID, &d.ChannelID, &d.Kind, &d.VariationID, &d.CommitSHA,
		&d.AppName, &d.URL, &d.TeardownInstructions, &d.Status, &d.ErrorMessage,
		&d.StartedAt, &d.FinishedAt, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// CreateHostingDeployment inserts a new deployment row in the "deploying" state.
func (db *DB) CreateHostingDeployment(ctx context.Context, d *domain.HostingDeployment) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	if d.Status == "" {
		d.Status = domain.HostingDeploymentStatusDeploying
	}
	return db.Pool.QueryRow(ctx, `
		INSERT INTO hosting_deployments
			(id, project_id, channel_id, kind, variation_id, commit_sha, app_name, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING started_at, created_at, updated_at
	`, d.ID, d.ProjectID, d.ChannelID, d.Kind, d.VariationID, d.CommitSHA, d.AppName, d.Status).
		Scan(&d.StartedAt, &d.CreatedAt, &d.UpdatedAt)
}

// GetHostingDeployment retrieves a deployment by ID.
func (db *DB) GetHostingDeployment(ctx context.Context, id uuid.UUID) (*domain.HostingDeployment, error) {
	return scanHostingDeployment(db.Pool.QueryRow(ctx,
		`SELECT `+hostingDeploymentCols+` FROM hosting_deployments WHERE id = $1`, id))
}

// SetHostingDeploymentCommit records which commit a deployment is shipping.
func (db *DB) SetHostingDeploymentCommit(ctx context.Context, id uuid.UUID, commitSHA string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE hosting_deployments SET commit_sha = $2, updated_at = now() WHERE id = $1
	`, id, commitSHA)
	return err
}

// CompleteHostingDeployment marks a deployment running and records where it landed.
func (db *DB) CompleteHostingDeployment(ctx context.Context, id uuid.UUID, url, teardownInstructions string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE hosting_deployments
		SET status = 'running', url = $2, teardown_instructions = $3,
			finished_at = now(), updated_at = now()
		WHERE id = $1
	`, id, url, teardownInstructions)
	return err
}

// FailHostingDeployment marks a deployment failed with an error message.
func (db *DB) FailHostingDeployment(ctx context.Context, id uuid.UUID, errMsg string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE hosting_deployments
		SET status = 'failed', error_message = $2, finished_at = now(), updated_at = now()
		WHERE id = $1
	`, id, errMsg)
	return err
}

// UpdateHostingDeploymentStatus sets the status of a deployment.
func (db *DB) UpdateHostingDeploymentStatus(ctx context.Context, id uuid.UUID, status domain.HostingDeploymentStatus) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE hosting_deployments SET status = $2, updated_at = now() WHERE id = $1
	`, id, status)
	return err
}

// GetCurrentProdDeployment returns the most recent successful production
// deployment for a project, or nil if the project has never deployed.
func (db *DB) GetCurrentProdDeployment(ctx context.Context, projectID uuid.UUID) (*domain.HostingDeployment, error) {
	d, err := scanHostingDeployment(db.Pool.QueryRow(ctx,
		`SELECT `+hostingDeploymentCols+` FROM hosting_deployments
		 WHERE project_id = $1 AND kind = 'prod' AND status = 'running'
		 ORDER BY started_at DESC LIMIT 1`, projectID))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return d, err
}

// GetLatestProdDeployment returns the most recent production deployment for a
// project regardless of outcome, so callers can surface in-progress and failed
// deploys. Returns nil if the project has never deployed.
func (db *DB) GetLatestProdDeployment(ctx context.Context, projectID uuid.UUID) (*domain.HostingDeployment, error) {
	d, err := scanHostingDeployment(db.Pool.QueryRow(ctx,
		`SELECT `+hostingDeploymentCols+` FROM hosting_deployments
		 WHERE project_id = $1 AND kind = 'prod'
		 ORDER BY started_at DESC LIMIT 1`, projectID))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return d, err
}

// ListHostingDeployments returns deployments for a project, newest first.
func (db *DB) ListHostingDeployments(ctx context.Context, projectID uuid.UUID, kind domain.HostingDeploymentKind, limit int) ([]domain.HostingDeployment, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT `+hostingDeploymentCols+` FROM hosting_deployments
		 WHERE project_id = $1 AND kind = $2
		 ORDER BY started_at DESC LIMIT $3`, projectID, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.HostingDeployment
	for rows.Next() {
		d, err := scanHostingDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// AppendHostingDeploymentLog records a single log line for a deployment.
func (db *DB) AppendHostingDeploymentLog(ctx context.Context, deploymentID uuid.UUID, level domain.LogLevel, message string) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO hosting_deployment_logs (deployment_id, level, message)
		VALUES ($1, $2, $3)
	`, deploymentID, level, message)
	return err
}

// GetHostingDeploymentLogs returns the log lines for a deployment, oldest first.
func (db *DB) GetHostingDeploymentLogs(ctx context.Context, deploymentID uuid.UUID) ([]domain.HostingDeploymentLog, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, deployment_id, logged_at, level, message
		FROM hosting_deployment_logs
		WHERE deployment_id = $1
		ORDER BY logged_at, id
	`, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.HostingDeploymentLog
	for rows.Next() {
		var l domain.HostingDeploymentLog
		if err := rows.Scan(&l.ID, &l.DeploymentID, &l.LoggedAt, &l.Level, &l.Message); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}


// ============================================================================
// User and Session Queries
// ============================================================================

// CreateUser creates a new user.
func (db *DB) CreateUser(ctx context.Context, user *domain.User) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, email, name, picture_url, google_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, user.ID, user.Email, user.Name, user.PictureURL, user.GoogleID, user.CreatedAt, user.UpdatedAt)
	return err
}

// GetUser gets a user by ID.
func (db *DB) GetUser(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	var user domain.User
	err := db.Pool.QueryRow(ctx, `
		SELECT id, email, name, picture_url, google_id, created_at, updated_at
		FROM users WHERE id = $1
	`, id).Scan(&user.ID, &user.Email, &user.Name, &user.PictureURL, &user.GoogleID, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByEmail gets a user by email address.
func (db *DB) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	var user domain.User
	err := db.Pool.QueryRow(ctx, `
		SELECT id, email, name, picture_url, google_id, created_at, updated_at
		FROM users WHERE email = $1
	`, email).Scan(&user.ID, &user.Email, &user.Name, &user.PictureURL, &user.GoogleID, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByGoogleID gets a user by Google ID.
func (db *DB) GetUserByGoogleID(ctx context.Context, googleID string) (*domain.User, error) {
	var user domain.User
	err := db.Pool.QueryRow(ctx, `
		SELECT id, email, name, picture_url, google_id, created_at, updated_at
		FROM users WHERE google_id = $1
	`, googleID).Scan(&user.ID, &user.Email, &user.Name, &user.PictureURL, &user.GoogleID, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// UpdateUserGoogleID updates a user's Google ID.
func (db *DB) UpdateUserGoogleID(ctx context.Context, userID uuid.UUID, googleID string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE users SET google_id = $1, updated_at = NOW() WHERE id = $2
	`, googleID, userID)
	return err
}

// CreateSession creates a new session.
func (db *DB) CreateSession(ctx context.Context, session *domain.Session) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, session.ID, session.UserID, session.TokenHash, session.ExpiresAt, session.CreatedAt)
	return err
}

// GetSessionByTokenHash gets a session by token hash.
func (db *DB) GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (*domain.Session, error) {
	var session domain.Session
	err := db.Pool.QueryRow(ctx, `
		SELECT id, user_id, token_hash, expires_at, created_at
		FROM sessions WHERE token_hash = $1
	`, tokenHash).Scan(&session.ID, &session.UserID, &session.TokenHash, &session.ExpiresAt, &session.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// DeleteSession deletes a session by ID.
func (db *DB) DeleteSession(ctx context.Context, id uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

// DeleteExpiredSessions deletes all expired sessions.
func (db *DB) DeleteExpiredSessions(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < NOW()`)
	return err
}

// ============================================================================
// Project Membership Queries
// ============================================================================

// AddProjectMember adds a user to a project with a role.
func (db *DB) AddProjectMember(ctx context.Context, projectID, userID uuid.UUID, role domain.ProjectMemberRole) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO project_members (id, project_id, user_id, role, created_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (project_id, user_id) DO UPDATE SET role = $4
	`, uuid.New(), projectID, userID, role)
	return err
}

// RemoveProjectMember removes a user from a project.
func (db *DB) RemoveProjectMember(ctx context.Context, projectID, userID uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		DELETE FROM project_members WHERE project_id = $1 AND user_id = $2
	`, projectID, userID)
	return err
}

// IsProjectMember checks if a user is a member of a project.
func (db *DB) IsProjectMember(ctx context.Context, projectID, userID uuid.UUID) (bool, error) {
	var exists bool
	err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2)
	`, projectID, userID).Scan(&exists)
	return exists, err
}

// GetProjectMemberRole gets a user's role in a project.
func (db *DB) GetProjectMemberRole(ctx context.Context, projectID, userID uuid.UUID) (domain.ProjectMemberRole, error) {
	var role domain.ProjectMemberRole
	err := db.Pool.QueryRow(ctx, `
		SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2
	`, projectID, userID).Scan(&role)
	return role, err
}

// GetProjectMembers gets all members of a project.
func (db *DB) GetProjectMembers(ctx context.Context, projectID uuid.UUID) ([]struct {
	User domain.User
	Role domain.ProjectMemberRole
}, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT u.id, u.email, u.name, u.picture_url, u.google_id, u.created_at, u.updated_at, pm.role
		FROM project_members pm
		JOIN users u ON pm.user_id = u.id
		WHERE pm.project_id = $1
		ORDER BY pm.created_at
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []struct {
		User domain.User
		Role domain.ProjectMemberRole
	}
	for rows.Next() {
		var m struct {
			User domain.User
			Role domain.ProjectMemberRole
		}
		if err := rows.Scan(&m.User.ID, &m.User.Email, &m.User.Name, &m.User.PictureURL, &m.User.GoogleID, &m.User.CreatedAt, &m.User.UpdatedAt, &m.Role); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// GetUserProjects gets all projects a user is a member of.
func (db *DB) GetUserProjects(ctx context.Context, userID uuid.UUID) ([]struct {
	Project domain.Project
	Role    domain.ProjectMemberRole
}, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT p.id, p.name, p.config, p.brief, p.created_at, p.updated_at, pm.role
		FROM project_members pm
		JOIN projects p ON pm.project_id = p.id
		WHERE pm.user_id = $1
		ORDER BY p.name
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []struct {
		Project domain.Project
		Role    domain.ProjectMemberRole
	}
	for rows.Next() {
		var p struct {
			Project domain.Project
			Role    domain.ProjectMemberRole
		}
		if err := rows.Scan(&p.Project.ID, &p.Project.Name, &p.Project.Config, &p.Project.Brief,
			&p.Project.CreatedAt, &p.Project.UpdatedAt, &p.Role); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// AssignOwnerToUnownedProjects assigns a user as owner to all projects that have no owner.
func (db *DB) AssignOwnerToUnownedProjects(ctx context.Context, userID uuid.UUID) (int, error) {
	result, err := db.Pool.Exec(ctx, `
		INSERT INTO project_members (id, project_id, user_id, role, created_at)
		SELECT gen_random_uuid(), p.id, $1, 'owner', NOW()
		FROM projects p
		WHERE NOT EXISTS (
			SELECT 1 FROM project_members pm
			WHERE pm.project_id = p.id AND pm.role = 'owner'
		)
	`, userID)
	if err != nil {
		return 0, err
	}
	return int(result.RowsAffected()), nil
}

// --- Hosting Platforms ---

// ListHostingPlatforms returns all available hosting platforms.
func (db *DB) ListHostingPlatforms(ctx context.Context) ([]domain.HostingPlatform, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, slug, name, deployer_image, instructions, setup_script, created_at, updated_at
		FROM hosting_platforms
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var platforms []domain.HostingPlatform
	for rows.Next() {
		var p domain.HostingPlatform
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.DeployerImage, &p.Instructions, &p.SetupScript, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		platforms = append(platforms, p)
	}
	return platforms, rows.Err()
}

// GetHostingPlatformBySlug retrieves a hosting platform by its slug.
func (db *DB) GetHostingPlatformBySlug(ctx context.Context, slug string) (*domain.HostingPlatform, error) {
	var p domain.HostingPlatform
	err := db.Pool.QueryRow(ctx, `
		SELECT id, slug, name, deployer_image, instructions, setup_script, created_at, updated_at
		FROM hosting_platforms
		WHERE slug = $1
	`, slug).Scan(&p.ID, &p.Slug, &p.Name, &p.DeployerImage, &p.Instructions, &p.SetupScript, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpsertHostingPlatform creates or updates a hosting platform by slug.
func (db *DB) UpsertHostingPlatform(ctx context.Context, p *domain.HostingPlatform) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO hosting_platforms (id, slug, name, deployer_image, instructions, setup_script, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, NOW(), NOW())
		ON CONFLICT (slug) DO UPDATE SET
			name = EXCLUDED.name,
			deployer_image = EXCLUDED.deployer_image,
			instructions = EXCLUDED.instructions,
			setup_script = EXCLUDED.setup_script,
			updated_at = NOW()
	`, p.Slug, p.Name, p.DeployerImage, p.Instructions, p.SetupScript)
	return err
}

// DeleteHostingPlatform removes a hosting platform by slug.
func (db *DB) DeleteHostingPlatform(ctx context.Context, slug string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM hosting_platforms WHERE slug = $1`, slug)
	return err
}

// CountHostingPlatforms returns the number of hosting platforms.
func (db *DB) CountHostingPlatforms(ctx context.Context) (int, error) {
	var count int
	err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM hosting_platforms`).Scan(&count)
	return count, err
}

// --- Deployment Channels ---

// ListSupportedDeploymentCombos returns all supported (artifact_kind, hosting_platform) combinations.
func (db *DB) ListSupportedDeploymentCombos(ctx context.Context) ([]domain.SupportedDeploymentCombo, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT sdc.id, sdc.artifact_kind, sdc.hosting_platform_id, sdc.notes, sdc.guidance, sdc.created_at,
			   hp.id, hp.slug, hp.name, hp.deployer_image, hp.instructions, hp.setup_script, hp.created_at, hp.updated_at
		FROM supported_deployment_combos sdc
		JOIN hosting_platforms hp ON hp.id = sdc.hosting_platform_id
		ORDER BY sdc.artifact_kind, hp.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var combos []domain.SupportedDeploymentCombo
	for rows.Next() {
		var c domain.SupportedDeploymentCombo
		var hp domain.HostingPlatform
		if err := rows.Scan(
			&c.ID, &c.ArtifactKind, &c.HostingPlatformID, &c.Notes, &c.Guidance, &c.CreatedAt,
			&hp.ID, &hp.Slug, &hp.Name, &hp.DeployerImage, &hp.Instructions, &hp.SetupScript, &hp.CreatedAt, &hp.UpdatedAt,
		); err != nil {
			return nil, err
		}
		c.HostingPlatform = &hp
		combos = append(combos, c)
	}
	return combos, rows.Err()
}

// GetSupportedDeploymentCombo returns a specific combo by artifact kind and platform.
func (db *DB) GetSupportedDeploymentCombo(ctx context.Context, artifactKind domain.DeployArtifactKind, platformID uuid.UUID) (*domain.SupportedDeploymentCombo, error) {
	var c domain.SupportedDeploymentCombo
	var hp domain.HostingPlatform
	err := db.Pool.QueryRow(ctx, `
		SELECT sdc.id, sdc.artifact_kind, sdc.hosting_platform_id, sdc.notes, sdc.guidance, sdc.created_at,
			   hp.id, hp.slug, hp.name, hp.deployer_image, hp.instructions, hp.setup_script, hp.created_at, hp.updated_at
		FROM supported_deployment_combos sdc
		JOIN hosting_platforms hp ON hp.id = sdc.hosting_platform_id
		WHERE sdc.artifact_kind = $1 AND sdc.hosting_platform_id = $2
	`, artifactKind, platformID).Scan(
		&c.ID, &c.ArtifactKind, &c.HostingPlatformID, &c.Notes, &c.Guidance, &c.CreatedAt,
		&hp.ID, &hp.Slug, &hp.Name, &hp.DeployerImage, &hp.Instructions, &hp.SetupScript, &hp.CreatedAt, &hp.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	c.HostingPlatform = &hp
	return &c, nil
}

// CreateSupportedDeploymentCombo adds a new supported combo.
func (db *DB) CreateSupportedDeploymentCombo(ctx context.Context, c *domain.SupportedDeploymentCombo) error {
	return db.Pool.QueryRow(ctx, `
		INSERT INTO supported_deployment_combos (artifact_kind, hosting_platform_id, notes, guidance)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at
	`, c.ArtifactKind, c.HostingPlatformID, c.Notes, c.Guidance).Scan(&c.ID, &c.CreatedAt)
}

// UpsertSupportedDeploymentCombo writes a combo, updating the existing row when
// the (artifact_kind, platform) pair is already present.
func (db *DB) UpsertSupportedDeploymentCombo(ctx context.Context, c *domain.SupportedDeploymentCombo) error {
	return db.Pool.QueryRow(ctx, `
		INSERT INTO supported_deployment_combos (artifact_kind, hosting_platform_id, notes, guidance)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (artifact_kind, hosting_platform_id) DO UPDATE
		SET notes = EXCLUDED.notes, guidance = EXCLUDED.guidance
		RETURNING id, created_at
	`, c.ArtifactKind, c.HostingPlatformID, c.Notes, c.Guidance).Scan(&c.ID, &c.CreatedAt)
}

// CountSupportedDeploymentCombos returns the number of supported combos.
func (db *DB) CountSupportedDeploymentCombos(ctx context.Context) (int, error) {
	var count int
	err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM supported_deployment_combos`).Scan(&count)
	return count, err
}

// GetActiveProjectDeploymentChannel returns the current active channel for a project.
// Uses LEFT JOIN so channels with missing/deleted platforms are still returned.
func (db *DB) GetActiveProjectDeploymentChannel(ctx context.Context, projectID uuid.UUID) (*domain.ProjectDeploymentChannel, error) {
	var c domain.ProjectDeploymentChannel
	var hpID, hpSlug, hpName, hpDeployerImage, hpInstructions, hpSetupScript *string
	var hpCreatedAt, hpUpdatedAt *time.Time
	err := db.Pool.QueryRow(ctx, `
		SELECT pdc.id, pdc.project_id, pdc.artifact_kind, pdc.hosting_platform_id,
			   pdc.demo_validated_at, pdc.demo_validating_at, pdc.demo_validation_error,
			   pdc.prod_validated_at, pdc.prod_validating_at, pdc.prod_validation_error,
			   pdc.disabled_at, pdc.created_at, pdc.updated_at,
			   hp.id, hp.slug, hp.name, hp.deployer_image, hp.instructions, hp.setup_script, hp.created_at, hp.updated_at
		FROM project_deployment_channels pdc
		LEFT JOIN hosting_platforms hp ON hp.id = pdc.hosting_platform_id
		WHERE pdc.project_id = $1 AND pdc.disabled_at IS NULL
	`, projectID).Scan(
		&c.ID, &c.ProjectID, &c.ArtifactKind, &c.HostingPlatformID,
		&c.DemoValidatedAt, &c.DemoValidatingAt, &c.DemoValidationError,
		&c.ProdValidatedAt, &c.ProdValidatingAt, &c.ProdValidationError,
		&c.DisabledAt, &c.CreatedAt, &c.UpdatedAt,
		&hpID, &hpSlug, &hpName, &hpDeployerImage, &hpInstructions, &hpSetupScript, &hpCreatedAt, &hpUpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	// Only populate HostingPlatform if the join found a match
	if hpID != nil {
		hp := domain.HostingPlatform{
			Slug: *hpSlug,
			Name: *hpName,
		}
		hp.ID, _ = uuid.Parse(*hpID)
		if hpDeployerImage != nil {
			hp.DeployerImage = *hpDeployerImage
		}
		if hpInstructions != nil {
			hp.Instructions = *hpInstructions
		}
		if hpSetupScript != nil {
			hp.SetupScript = *hpSetupScript
		}
		if hpCreatedAt != nil {
			hp.CreatedAt = *hpCreatedAt
		}
		if hpUpdatedAt != nil {
			hp.UpdatedAt = *hpUpdatedAt
		}
		c.HostingPlatform = &hp
	}
	return &c, nil
}

// ListProjectDeploymentChannels returns all channels for a project (including disabled).
func (db *DB) ListProjectDeploymentChannels(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectDeploymentChannel, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT pdc.id, pdc.project_id, pdc.artifact_kind, pdc.hosting_platform_id,
			   pdc.demo_validated_at, pdc.demo_validating_at, pdc.demo_validation_error,
			   pdc.prod_validated_at, pdc.prod_validating_at, pdc.prod_validation_error,
			   pdc.disabled_at, pdc.created_at, pdc.updated_at,
			   hp.id, hp.slug, hp.name, hp.deployer_image, hp.instructions, hp.setup_script, hp.created_at, hp.updated_at
		FROM project_deployment_channels pdc
		JOIN hosting_platforms hp ON hp.id = pdc.hosting_platform_id
		WHERE pdc.project_id = $1
		ORDER BY pdc.disabled_at NULLS FIRST, pdc.created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []domain.ProjectDeploymentChannel
	for rows.Next() {
		var c domain.ProjectDeploymentChannel
		var hp domain.HostingPlatform
		if err := rows.Scan(
			&c.ID, &c.ProjectID, &c.ArtifactKind, &c.HostingPlatformID,
			&c.DemoValidatedAt, &c.DemoValidatingAt, &c.DemoValidationError,
			&c.ProdValidatedAt, &c.ProdValidatingAt, &c.ProdValidationError,
				&c.DisabledAt, &c.CreatedAt, &c.UpdatedAt,
			&hp.ID, &hp.Slug, &hp.Name, &hp.DeployerImage, &hp.Instructions, &hp.SetupScript, &hp.CreatedAt, &hp.UpdatedAt,
		); err != nil {
			return nil, err
		}
		c.HostingPlatform = &hp
		channels = append(channels, c)
	}
	return channels, rows.Err()
}

// CreateProjectDeploymentChannel creates a new channel (disabling any existing active one).
func (db *DB) CreateProjectDeploymentChannel(ctx context.Context, c *domain.ProjectDeploymentChannel) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Disable any existing active channel
	_, err = tx.Exec(ctx, `
		UPDATE project_deployment_channels
		SET disabled_at = NOW(), updated_at = NOW()
		WHERE project_id = $1 AND disabled_at IS NULL
	`, c.ProjectID)
	if err != nil {
		return err
	}

	// Create the new channel
	err = tx.QueryRow(ctx, `
		INSERT INTO project_deployment_channels (project_id, artifact_kind, hosting_platform_id)
		VALUES ($1, $2, $3)
		RETURNING id, created_at, updated_at
	`, c.ProjectID, c.ArtifactKind, c.HostingPlatformID).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// StartDemoValidation marks demo validation as in progress.
func (db *DB) StartDemoValidation(ctx context.Context, channelID uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE project_deployment_channels
		SET demo_validating_at = NOW(), demo_validation_error = NULL, updated_at = NOW()
		WHERE id = $1
	`, channelID)
	return err
}

// CompleteDemoValidation marks demo as validated (success).
func (db *DB) CompleteDemoValidation(ctx context.Context, channelID uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE project_deployment_channels
		SET demo_validated_at = NOW(), demo_validating_at = NULL, demo_validation_error = NULL, updated_at = NOW()
		WHERE id = $1
	`, channelID)
	return err
}

// FailDemoValidation marks demo validation as failed.
func (db *DB) FailDemoValidation(ctx context.Context, channelID uuid.UUID, errMsg string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE project_deployment_channels
		SET demo_validating_at = NULL, demo_validation_error = $2, updated_at = NOW()
		WHERE id = $1
	`, channelID, errMsg)
	return err
}

// UpdateProjectDeploymentChannelDemoValidation marks demo as validated (legacy, use CompleteDemoValidation).
func (db *DB) UpdateProjectDeploymentChannelDemoValidation(ctx context.Context, channelID uuid.UUID) error {
	return db.CompleteDemoValidation(ctx, channelID)
}

// StartProdValidation marks prod validation as in progress.
func (db *DB) StartProdValidation(ctx context.Context, channelID uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE project_deployment_channels
		SET prod_validating_at = NOW(), prod_validation_error = NULL, updated_at = NOW()
		WHERE id = $1
	`, channelID)
	return err
}

// CompleteProdValidation marks prod as validated (success).
func (db *DB) CompleteProdValidation(ctx context.Context, channelID uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE project_deployment_channels
		SET prod_validated_at = NOW(), prod_validating_at = NULL, prod_validation_error = NULL, updated_at = NOW()
		WHERE id = $1
	`, channelID)
	return err
}

// FailProdValidation marks prod validation as failed.
func (db *DB) FailProdValidation(ctx context.Context, channelID uuid.UUID, errMsg string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE project_deployment_channels
		SET prod_validating_at = NULL, prod_validation_error = $2, updated_at = NOW()
		WHERE id = $1
	`, channelID, errMsg)
	return err
}

// UpdateProjectDeploymentChannelProdValidation marks prod as validated (legacy, use CompleteProdValidation).
func (db *DB) UpdateProjectDeploymentChannelProdValidation(ctx context.Context, channelID uuid.UUID) error {
	return db.CompleteProdValidation(ctx, channelID)
}




// parseInputDate accepts either a full RFC3339 timestamp or a bare ISO date
// from a strategy input file, and returns nil for anything it cannot read.
func parseInputDate(v *string) *time.Time {
	if v == nil || *v == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, *v); err == nil {
		return &t
	}
	if t, err := time.Parse("2006-01-02", *v); err == nil {
		return &t
	}
	return nil
}
