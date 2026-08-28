package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

//------------------------------------------------------------------------------
// Rate cards
//------------------------------------------------------------------------------

// CountModelRateCards reports how many model rate cards exist.
func (db *DB) CountModelRateCards(ctx context.Context) (int, error) {
	var n int
	err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM model_rate_cards`).Scan(&n)
	return n, err
}

// UpsertModelRateCard writes a rate card, replacing any card for the same model
// with the same effective_from. Cards with earlier effective dates are left
// alone: ledger entries point at the card that priced them, so rewriting an old
// card would make settled figures unverifiable.
func (db *DB) UpsertModelRateCard(ctx context.Context, c *domain.ModelRateCard) error {
	if c.EffectiveFrom.IsZero() {
		c.EffectiveFrom = time.Now()
	}
	return db.Pool.QueryRow(ctx, `
		INSERT INTO model_rate_cards (
			model, input_usd_per_mtok, output_usd_per_mtok,
			cache_read_multiplier, cache_write_multiplier, batch_multiplier,
			effective_from, source
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (model, effective_from) DO UPDATE SET
			input_usd_per_mtok = EXCLUDED.input_usd_per_mtok,
			output_usd_per_mtok = EXCLUDED.output_usd_per_mtok,
			cache_read_multiplier = EXCLUDED.cache_read_multiplier,
			cache_write_multiplier = EXCLUDED.cache_write_multiplier,
			batch_multiplier = EXCLUDED.batch_multiplier,
			source = EXCLUDED.source
		RETURNING id
	`, c.Model, c.InputUSDPerMTok, c.OutputUSDPerMTok,
		c.CacheReadMultiplier, c.CacheWriteMultiplier, c.BatchMultiplier,
		c.EffectiveFrom, c.Source).Scan(&c.ID)
}

// GetModelRateCard returns the card in force for a model at a given time.
// Returns nil (not an error) when the model has no card, so an unpriced model
// shows up as a zero-dollar entry with its tokens still recorded, rather than
// failing the run that used it.
func (db *DB) GetModelRateCard(ctx context.Context, model string, at time.Time) (*domain.ModelRateCard, error) {
	var c domain.ModelRateCard
	err := db.Pool.QueryRow(ctx, `
		SELECT id, model, input_usd_per_mtok, output_usd_per_mtok,
		       cache_read_multiplier, cache_write_multiplier, batch_multiplier,
		       effective_from, source, created_at
		FROM model_rate_cards
		WHERE model = $1 AND effective_from <= $2
		ORDER BY effective_from DESC
		LIMIT 1
	`, model, at).Scan(&c.ID, &c.Model, &c.InputUSDPerMTok, &c.OutputUSDPerMTok,
		&c.CacheReadMultiplier, &c.CacheWriteMultiplier, &c.BatchMultiplier,
		&c.EffectiveFrom, &c.Source, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListModelRateCards returns the currently effective card for each model.
func (db *DB) ListModelRateCards(ctx context.Context) ([]domain.ModelRateCard, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT ON (model)
		       id, model, input_usd_per_mtok, output_usd_per_mtok,
		       cache_read_multiplier, cache_write_multiplier, batch_multiplier,
		       effective_from, source, created_at
		FROM model_rate_cards
		WHERE effective_from <= NOW()
		ORDER BY model, effective_from DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cards []domain.ModelRateCard
	for rows.Next() {
		var c domain.ModelRateCard
		if err := rows.Scan(&c.ID, &c.Model, &c.InputUSDPerMTok, &c.OutputUSDPerMTok,
			&c.CacheReadMultiplier, &c.CacheWriteMultiplier, &c.BatchMultiplier,
			&c.EffectiveFrom, &c.Source, &c.CreatedAt); err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

// CountHostingRateCards reports how many hosting rate cards exist.
func (db *DB) CountHostingRateCards(ctx context.Context) (int, error) {
	var n int
	err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM hosting_rate_cards`).Scan(&n)
	return n, err
}

// UpsertHostingRateCard writes a hosting rate card.
func (db *DB) UpsertHostingRateCard(ctx context.Context, c *domain.HostingRateCard) error {
	if c.EffectiveFrom.IsZero() {
		c.EffectiveFrom = time.Now()
	}
	return db.Pool.QueryRow(ctx, `
		INSERT INTO hosting_rate_cards (
			platform_slug, machine_shape, usd_per_hour, bills_when_idle,
			effective_from, source
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (platform_slug, machine_shape, effective_from) DO UPDATE SET
			usd_per_hour = EXCLUDED.usd_per_hour,
			bills_when_idle = EXCLUDED.bills_when_idle,
			source = EXCLUDED.source
		RETURNING id
	`, c.PlatformSlug, c.MachineShape, c.USDPerHour, c.BillsWhenIdle,
		c.EffectiveFrom, c.Source).Scan(&c.ID)
}

// GetHostingRateCard returns the card in force for a machine shape at a time,
// falling back to the platform's "default" shape when the exact shape is
// unknown. Returns nil when neither exists.
func (db *DB) GetHostingRateCard(ctx context.Context, platformSlug, shape string, at time.Time) (*domain.HostingRateCard, error) {
	var c domain.HostingRateCard
	err := db.Pool.QueryRow(ctx, `
		SELECT id, platform_slug, machine_shape, usd_per_hour, bills_when_idle,
		       effective_from, source, created_at
		FROM hosting_rate_cards
		WHERE platform_slug = $1 AND machine_shape IN ($2, 'default') AND effective_from <= $3
		ORDER BY (machine_shape = $2) DESC, effective_from DESC
		LIMIT 1
	`, platformSlug, shape, at).Scan(&c.ID, &c.PlatformSlug, &c.MachineShape,
		&c.USDPerHour, &c.BillsWhenIdle, &c.EffectiveFrom, &c.Source, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

//------------------------------------------------------------------------------
// Cost ledger
//------------------------------------------------------------------------------

// RecordCostEntry appends one line to the actuals ledger.
func (db *DB) RecordCostEntry(ctx context.Context, e *domain.CostEntry) error {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now()
	}
	return db.Pool.QueryRow(ctx, `
		INSERT INTO cost_entries (
			project_id, strategy_id, hop_id, variation_id,
			kind, component, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			deployment_id, machine_shape, duration_seconds,
			amount_usd, model_rate_card_id, hosting_rate_card_id, occurred_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		RETURNING id
	`, e.ProjectID, e.StrategyID, e.HopID, e.VariationID,
		e.Kind, e.Component, e.Model,
		e.Tokens.InputTokens, e.Tokens.OutputTokens,
		e.Tokens.CacheReadTokens, e.Tokens.CacheWriteTokens,
		e.DeploymentID, e.MachineShape, e.DurationSeconds,
		e.AmountUSD, e.ModelRateCardID, e.HostingRateCardID, e.OccurredAt).Scan(&e.ID)
}

// CostSummary aggregates ledger rows. Tokens are kept alongside the dollars
// because they are the evidence the dollars were derived from.
type CostSummary struct {
	domain.TokenCounts
	AmountUSD float64
	Entries   int
}

const costSummarySelect = `
	SELECT COALESCE(SUM(COALESCE(reconciled_amount_usd, amount_usd)), 0),
	       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
	       COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0),
	       COUNT(*)
	FROM cost_entries`

func scanSummary(row interface{ Scan(...any) error }) (CostSummary, error) {
	var s CostSummary
	err := row.Scan(&s.AmountUSD, &s.InputTokens, &s.OutputTokens,
		&s.CacheReadTokens, &s.CacheWriteTokens, &s.Entries)
	return s, err
}

// GetHopCostSummary totals everything spent against one Hop.
func (db *DB) GetHopCostSummary(ctx context.Context, hopID uuid.UUID) (CostSummary, error) {
	return scanSummary(db.Pool.QueryRow(ctx, costSummarySelect+` WHERE hop_id = $1`, hopID))
}

// GetVariationCostSummary totals everything spent on one Variation.
func (db *DB) GetVariationCostSummary(ctx context.Context, variationID uuid.UUID) (CostSummary, error) {
	return scanSummary(db.Pool.QueryRow(ctx, costSummarySelect+` WHERE variation_id = $1`, variationID))
}

// GetStrategyCostSummary totals everything spent against one Strategy.
func (db *DB) GetStrategyCostSummary(ctx context.Context, strategyID uuid.UUID) (CostSummary, error) {
	return scanSummary(db.Pool.QueryRow(ctx, costSummarySelect+` WHERE strategy_id = $1`, strategyID))
}

// GetProjectCostSummary totals everything spent across a Project.
func (db *DB) GetProjectCostSummary(ctx context.Context, projectID uuid.UUID) (CostSummary, error) {
	return scanSummary(db.Pool.QueryRow(ctx, costSummarySelect+` WHERE project_id = $1`, projectID))
}

// GetHopCostSummaries returns per-Hop totals for a whole Strategy in one query,
// so the roadmap view does not fan out into a query per Hop.
func (db *DB) GetHopCostSummaries(ctx context.Context, strategyID uuid.UUID) (map[uuid.UUID]CostSummary, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT hop_id,
		       COALESCE(SUM(COALESCE(reconciled_amount_usd, amount_usd)), 0),
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0),
		       COUNT(*)
		FROM cost_entries
		WHERE strategy_id = $1 AND hop_id IS NOT NULL
		GROUP BY hop_id
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[uuid.UUID]CostSummary)
	for rows.Next() {
		var id uuid.UUID
		var s CostSummary
		if err := rows.Scan(&id, &s.AmountUSD, &s.InputTokens, &s.OutputTokens,
			&s.CacheReadTokens, &s.CacheWriteTokens, &s.Entries); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}

// ComponentCost is spend attributed to one part of Mendel.
type ComponentCost struct {
	Component string
	Kind      domain.CostKind
	AmountUSD float64
	Entries   int
}

// GetCostByComponent breaks a Strategy's spend down by what produced it, which
// is what turns "this cost $40" into something a user can act on.
func (db *DB) GetCostByComponent(ctx context.Context, strategyID uuid.UUID) ([]ComponentCost, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT component, kind,
		       COALESCE(SUM(COALESCE(reconciled_amount_usd, amount_usd)), 0), COUNT(*)
		FROM cost_entries
		WHERE strategy_id = $1
		GROUP BY component, kind
		ORDER BY 3 DESC
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ComponentCost
	for rows.Next() {
		var c ComponentCost
		if err := rows.Scan(&c.Component, &c.Kind, &c.AmountUSD, &c.Entries); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SpendPoint is cumulative spend as of a day, for burn-down against a budget.
type SpendPoint struct {
	Day       time.Time
	AmountUSD float64
}

// GetStrategySpendSeries returns daily cumulative spend for a Strategy.
func (db *DB) GetStrategySpendSeries(ctx context.Context, strategyID uuid.UUID) ([]SpendPoint, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT day, SUM(daily) OVER (ORDER BY day) FROM (
			SELECT date_trunc('day', occurred_at) AS day,
			       SUM(COALESCE(reconciled_amount_usd, amount_usd)) AS daily
			FROM cost_entries
			WHERE strategy_id = $1
			GROUP BY 1
		) t ORDER BY day
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SpendPoint
	for rows.Next() {
		var p SpendPoint
		if err := rows.Scan(&p.Day, &p.AmountUSD); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

//------------------------------------------------------------------------------
// Hop cost estimates
//------------------------------------------------------------------------------

// CreateHopCostEstimate appends an estimate to a Hop's history.
func (db *DB) CreateHopCostEstimate(ctx context.Context, e *domain.HopCostEstimate) error {
	return db.Pool.QueryRow(ctx, `
		INSERT INTO hop_cost_estimates (
			hop_id, amount_usd, estimator, confidence, basis, calibrated_from_hops
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at
	`, e.HopID, e.AmountUSD, e.Estimator, e.Confidence, e.Basis,
		e.CalibratedFromHops).Scan(&e.ID, &e.CreatedAt)
}

const hopEstimateSelect = `
	SELECT id, hop_id, amount_usd, estimator, confidence, basis,
	       calibrated_from_hops, created_at
	FROM hop_cost_estimates`

func scanHopEstimate(row interface{ Scan(...any) error }) (*domain.HopCostEstimate, error) {
	var e domain.HopCostEstimate
	err := row.Scan(&e.ID, &e.HopID, &e.AmountUSD, &e.Estimator, &e.Confidence,
		&e.Basis, &e.CalibratedFromHops, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// GetLatestHopCostEstimate returns a Hop's most recent estimate, or nil if it
// has never been estimated.
func (db *DB) GetLatestHopCostEstimate(ctx context.Context, hopID uuid.UUID) (*domain.HopCostEstimate, error) {
	e, err := scanHopEstimate(db.Pool.QueryRow(ctx,
		hopEstimateSelect+` WHERE hop_id = $1 ORDER BY created_at DESC LIMIT 1`, hopID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// GetLatestHopCostEstimates returns the newest estimate per Hop for a Strategy.
func (db *DB) GetLatestHopCostEstimates(ctx context.Context, strategyID uuid.UUID) (map[uuid.UUID]domain.HopCostEstimate, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT ON (e.hop_id)
		       e.id, e.hop_id, e.amount_usd, e.estimator, e.confidence, e.basis,
		       e.calibrated_from_hops, e.created_at
		FROM hop_cost_estimates e
		JOIN hops h ON h.id = e.hop_id
		WHERE h.strategy_id = $1
		ORDER BY e.hop_id, e.created_at DESC
	`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[uuid.UUID]domain.HopCostEstimate)
	for rows.Next() {
		var e domain.HopCostEstimate
		if err := rows.Scan(&e.ID, &e.HopID, &e.AmountUSD, &e.Estimator, &e.Confidence,
			&e.Basis, &e.CalibratedFromHops, &e.CreatedAt); err != nil {
			return nil, err
		}
		out[e.HopID] = e
	}
	return out, rows.Err()
}

// GetHopCostEstimateHistory returns every estimate made for a Hop, newest first.
func (db *DB) GetHopCostEstimateHistory(ctx context.Context, hopID uuid.UUID) ([]domain.HopCostEstimate, error) {
	rows, err := db.Pool.Query(ctx,
		hopEstimateSelect+` WHERE hop_id = $1 ORDER BY created_at DESC`, hopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.HopCostEstimate
	for rows.Next() {
		var e domain.HopCostEstimate
		if err := rows.Scan(&e.ID, &e.HopID, &e.AmountUSD, &e.Estimator, &e.Confidence,
			&e.Basis, &e.CalibratedFromHops, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

//------------------------------------------------------------------------------
// Calibration
//------------------------------------------------------------------------------

// HopOutcome pairs what a completed Hop was predicted to cost with what it
// actually cost. This is the raw material for calibration: without it, every
// estimate is an unfalsifiable guess.
type HopOutcome struct {
	HopID       uuid.UUID
	Name        string
	Commentary  string
	EstimateUSD *float64
	ActualUSD   float64
	Variations  int
	CompletedAt time.Time
}

// Ratio is actual over estimate. The bool is false when the Hop was never
// estimated or was estimated at zero, in which case there is nothing to compare.
func (h HopOutcome) Ratio() (float64, bool) {
	if h.EstimateUSD == nil || *h.EstimateUSD <= 0 {
		return 0, false
	}
	return h.ActualUSD / *h.EstimateUSD, true
}

// GetCompletedHopOutcomes returns estimate-vs-actual for every completed Hop in
// a project, newest first. Hops from all of a project's strategies count: a
// project's own history is the best available predictor of its next Hop.
//
// When model is non-empty, only Hops whose spend was mostly on that model are
// returned. What a Hop cost is a fact about the model that built it: prices
// differ several-fold across models, and enabling prompt caching moved the
// figure again by roughly a factor of six. History from a superseded setup
// would anchor new estimates to a world that no longer exists.
func (db *DB) GetCompletedHopOutcomes(ctx context.Context, projectID uuid.UUID, model string, limit int) ([]HopOutcome, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT h.id, h.name, h.commentary, h.updated_at,
		       (SELECT e.amount_usd FROM hop_cost_estimates e
		         WHERE e.hop_id = h.id ORDER BY e.created_at LIMIT 1),
		       COALESCE((SELECT SUM(COALESCE(c.reconciled_amount_usd, c.amount_usd))
		                   FROM cost_entries c WHERE c.hop_id = h.id), 0),
		       (SELECT COUNT(*) FROM variations v WHERE v.hop_id = h.id)
		FROM hops h
		JOIN strategies s ON h.strategy_id = s.id
		WHERE s.project_id = $1 AND h.status = 'completed'
		  AND ($2 = '' OR $2 = (
		      SELECT c.model FROM cost_entries c
		      WHERE c.hop_id = h.id AND c.kind = 'model' AND c.model IS NOT NULL
		      GROUP BY c.model
		      ORDER BY SUM(COALESCE(c.reconciled_amount_usd, c.amount_usd)) DESC
		      LIMIT 1
		  ))
		ORDER BY h.updated_at DESC
		LIMIT $3
	`, projectID, model, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []HopOutcome
	for rows.Next() {
		var o HopOutcome
		if err := rows.Scan(&o.HopID, &o.Name, &o.Commentary, &o.CompletedAt,
			&o.EstimateUSD, &o.ActualUSD, &o.Variations); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

//------------------------------------------------------------------------------
// Funding success criteria (budget -> OKR link)
//------------------------------------------------------------------------------

// LinkFundingToKeyResult records that a budget is being spent to move a Key
// Result. Re-linking the same pair is a no-op.
func (db *DB) LinkFundingToKeyResult(ctx context.Context, fundingSourceID, keyResultID uuid.UUID, weight float64) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO funding_success_criteria (id, funding_source_id, key_result_id, weight)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (funding_source_id, key_result_id) DO UPDATE SET weight = EXCLUDED.weight
	`, uuid.New(), fundingSourceID, keyResultID, weight)
	return err
}

// UnlinkFundingFromKeyResult removes a budget-to-Key-Result link.
func (db *DB) UnlinkFundingFromKeyResult(ctx context.Context, fundingSourceID, keyResultID uuid.UUID) error {
	_, err := db.Pool.Exec(ctx, `
		DELETE FROM funding_success_criteria
		WHERE funding_source_id = $1 AND key_result_id = $2
	`, fundingSourceID, keyResultID)
	return err
}

// FundedKeyResult is a Key Result a budget is meant to move, with the target
// date that makes it a milestone.
type FundedKeyResult struct {
	KeyResultID uuid.UUID
	Description string
	TargetUnits string
	TargetDate  *time.Time
	Weight      float64
}

// GetFundedKeyResults returns the Key Results a funding source is spent against.
func (db *DB) GetFundedKeyResults(ctx context.Context, fundingSourceID uuid.UUID) ([]FundedKeyResult, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT kr.id, kr.description, kr.target_units, kr.target_date, fsc.weight
		FROM funding_success_criteria fsc
		JOIN key_results kr ON kr.id = fsc.key_result_id
		WHERE fsc.funding_source_id = $1 AND kr.deleted_at IS NULL
		ORDER BY kr.target_date NULLS LAST, kr.created_at
	`, fundingSourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FundedKeyResult
	for rows.Next() {
		var k FundedKeyResult
		if err := rows.Scan(&k.KeyResultID, &k.Description, &k.TargetUnits,
			&k.TargetDate, &k.Weight); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// CreateFundingSource adds a USD budget to a Strategy.
func (db *DB) CreateFundingSource(ctx context.Context, f *domain.FundingSource) error {
	if f.ID == uuid.Nil {
		f.ID = uuid.New()
	}
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO funding_sources (id, strategy_id, name, amount_usd, period_start, period_end, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
	`, f.ID, f.StrategyID, f.Name, f.AmountUSD, f.PeriodStart, f.PeriodEnd)
	return err
}

// ResolveHopAttribution returns the project and strategy a Hop belongs to, so a
// charge incurred deep in codegen can still be filed against the right budget.
func (db *DB) ResolveHopAttribution(ctx context.Context, hopID uuid.UUID) (projectID, strategyID uuid.UUID, err error) {
	err = db.Pool.QueryRow(ctx, `
		SELECT s.project_id, h.strategy_id
		FROM hops h JOIN strategies s ON s.id = h.strategy_id
		WHERE h.id = $1
	`, hopID).Scan(&projectID, &strategyID)
	return projectID, strategyID, err
}

// DeploymentMeterRow is a deployment that may have accrued hosting cost since
// it was last metered.
type DeploymentMeterRow struct {
	DeploymentID uuid.UUID
	ProjectID    uuid.UUID
	StrategyID   *uuid.UUID
	HopID        *uuid.UUID
	VariationID  *uuid.UUID
	PlatformSlug string
	StartedAt    time.Time
	FinishedAt   *time.Time
	Status       string

	// MeteredThrough is when this deployment was last billed to the ledger.
	// Nil means it has never been metered, so billing starts at StartedAt.
	MeteredThrough *time.Time
}

// BillableThrough is the point up to which this deployment should be charged:
// when it stopped, or now while it is still running.
func (d DeploymentMeterRow) BillableThrough(now time.Time) time.Time {
	if d.FinishedAt != nil && d.FinishedAt.Before(now) {
		return *d.FinishedAt
	}
	return now
}

// UnbilledSince is the start of the period not yet charged for.
func (d DeploymentMeterRow) UnbilledSince() time.Time {
	if d.MeteredThrough != nil && d.MeteredThrough.After(d.StartedAt) {
		return *d.MeteredThrough
	}
	return d.StartedAt
}

// GetHostingDeploymentsToMeter returns deployments whose hosting cost may have
// accrued since they were last billed to the ledger.
//
// Failed deployments are excluded: they never served anything, and their
// wall-clock is deploy time, not runtime.
func (db *DB) GetHostingDeploymentsToMeter(ctx context.Context) ([]DeploymentMeterRow, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT d.id, d.project_id, h.strategy_id, v.hop_id, d.variation_id,
		       p.slug, d.started_at, d.finished_at, d.status,
		       (SELECT MAX(c.occurred_at) FROM cost_entries c
		         WHERE c.deployment_id = d.id AND c.kind = 'hosting')
		FROM hosting_deployments d
		JOIN project_deployment_channels ch ON ch.id = d.channel_id
		JOIN hosting_platforms p ON p.id = ch.hosting_platform_id
		LEFT JOIN variations v ON v.id = d.variation_id
		LEFT JOIN hops h ON h.id = v.hop_id
		WHERE d.status IN ('running', 'terminated')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DeploymentMeterRow
	for rows.Next() {
		var d DeploymentMeterRow
		if err := rows.Scan(&d.DeploymentID, &d.ProjectID, &d.StrategyID, &d.HopID,
			&d.VariationID, &d.PlatformSlug, &d.StartedAt, &d.FinishedAt,
			&d.Status, &d.MeteredThrough); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetInputRequestCostAudit stores the cost auditor's verdict on a proposed
// roadmap, so the review page can show it without re-running the audit.
func (db *DB) SetInputRequestCostAudit(ctx context.Context, inputRequestID uuid.UUID, audit []byte) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE input_requests SET cost_audit = $2, updated_at = now() WHERE id = $1
	`, inputRequestID, audit)
	return err
}

// GetInputRequestCostAudit returns the stored audit, or nil if none was made.
func (db *DB) GetInputRequestCostAudit(ctx context.Context, inputRequestID uuid.UUID) ([]byte, error) {
	var raw []byte
	err := db.Pool.QueryRow(ctx,
		`SELECT cost_audit FROM input_requests WHERE id = $1`, inputRequestID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

//------------------------------------------------------------------------------
// Per-model usage
//------------------------------------------------------------------------------

// ModelUsage is what one model cost and how well it did.
//
// Cost alone cannot say whether a model is the right choice: a cheap model that
// fails half its variations is the expensive one. Pairing spend with outcomes is
// what makes model choice answerable from evidence.
type ModelUsage struct {
	Model  string
	Tokens domain.TokenCounts

	AmountUSD float64
	Entries   int

	// Variations this model did the majority of the spend on. A variation
	// worked on by several models is attributed to whichever spent most, so
	// each variation counts exactly once across the whole table.
	Variations int
	Succeeded  int // selected or merged
	Failed     int // errored or terminated

	// MedianVariationUSD is the median total spend on the variations attributed
	// to this model. The median rather than the mean so one runaway variation
	// does not redefine what the model normally costs.
	MedianVariationUSD float64
}

// Finished is the number of variations that reached a terminal outcome.
func (m ModelUsage) Finished() int { return m.Succeeded + m.Failed }

// HasFinished reports whether any variation has finished, which is when a
// success rate starts to mean anything.
//
// Split from SuccessRate rather than returned alongside it because html/template
// only accepts a second return value when it is an error.
func (m ModelUsage) HasFinished() bool { return m.Finished() > 0 }

// SuccessRate is the share of finished variations that produced a winner.
// Zero when none have finished; guard with HasFinished.
func (m ModelUsage) SuccessRate() float64 {
	if !m.HasFinished() {
		return 0
	}
	return float64(m.Succeeded) / float64(m.Finished())
}

// HasSuccess reports whether this model has landed a winner yet.
func (m ModelUsage) HasSuccess() bool { return m.Succeeded > 0 }

// USDPerSuccess is spend divided by successful variations -- the figure that
// actually compares models, since a cheaper model that retries more can cost
// more per result. Zero with no successes; guard with HasSuccess.
func (m ModelUsage) USDPerSuccess() float64 {
	if !m.HasSuccess() {
		return 0
	}
	return m.AmountUSD / float64(m.Succeeded)
}

// GetModelUsage reports spend and outcomes per model across a project.
func (db *DB) GetModelUsage(ctx context.Context, projectID uuid.UUID) ([]ModelUsage, error) {
	rows, err := db.Pool.Query(ctx, `
		WITH model_spend AS (
			SELECT c.model, c.variation_id, SUM(COALESCE(c.reconciled_amount_usd, c.amount_usd)) AS spend
			FROM cost_entries c
			WHERE c.project_id = $1 AND c.kind = 'model' AND c.variation_id IS NOT NULL
			GROUP BY c.model, c.variation_id
		),
		-- Attribute each variation to the model that spent most on it, so a
		-- variation fixed on a second model is not counted twice.
		dominant AS (
			SELECT DISTINCT ON (variation_id) variation_id, model, spend
			FROM model_spend
			ORDER BY variation_id, spend DESC
		),
		outcomes AS (
			SELECT d.model,
			       COUNT(*) AS variations,
			       COUNT(*) FILTER (WHERE v.status IN ('selected', 'merged')) AS succeeded,
			       COUNT(*) FILTER (WHERE v.status IN ('error', 'terminated')) AS failed,
			       COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY d.spend), 0) AS median_usd
			FROM dominant d
			JOIN variations v ON v.id = d.variation_id
			GROUP BY d.model
		),
		totals AS (
			SELECT model,
			       COALESCE(SUM(COALESCE(reconciled_amount_usd, amount_usd)), 0) AS amount_usd,
			       COALESCE(SUM(input_tokens), 0) AS in_tok,
			       COALESCE(SUM(output_tokens), 0) AS out_tok,
			       COALESCE(SUM(cache_read_tokens), 0) AS cr_tok,
			       COALESCE(SUM(cache_write_tokens), 0) AS cw_tok,
			       COUNT(*) AS entries
			FROM cost_entries
			WHERE project_id = $1 AND kind = 'model'
			GROUP BY model
		)
		SELECT t.model, t.amount_usd, t.in_tok, t.out_tok, t.cr_tok, t.cw_tok, t.entries,
		       COALESCE(o.variations, 0), COALESCE(o.succeeded, 0), COALESCE(o.failed, 0),
		       COALESCE(o.median_usd, 0)
		FROM totals t
		LEFT JOIN outcomes o ON o.model = t.model
		ORDER BY t.amount_usd DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ModelUsage
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Model, &m.AmountUSD,
			&m.Tokens.InputTokens, &m.Tokens.OutputTokens,
			&m.Tokens.CacheReadTokens, &m.Tokens.CacheWriteTokens, &m.Entries,
			&m.Variations, &m.Succeeded, &m.Failed, &m.MedianVariationUSD); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetUnpricedModels returns models that appear in the ledger with no rate card.
//
// Such entries record their tokens but price to zero, so a project's cost
// silently understates itself. That is the right behaviour at write time -- far
// better than failing a completed run -- but it has to be visible somewhere.
func (db *DB) GetUnpricedModels(ctx context.Context) ([]string, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT DISTINCT c.model
		FROM cost_entries c
		WHERE c.kind = 'model' AND c.model IS NOT NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM model_rate_cards r
		      WHERE r.model = c.model AND r.effective_from <= c.occurred_at
		  )
		ORDER BY 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
