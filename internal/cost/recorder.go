package cost

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// LedgerDB is the persistence surface the Recorder needs.
type LedgerDB interface {
	GetModelRateCard(ctx context.Context, model string, at time.Time) (*domain.ModelRateCard, error)
	GetHostingRateCard(ctx context.Context, platformSlug, shape string, at time.Time) (*domain.HostingRateCard, error)
	RecordCostEntry(ctx context.Context, e *domain.CostEntry) error
}

// Attribution says which parts of a project a charge belongs to. Only ProjectID
// is required; spend that has no Hop (OKR tuning, roadmap proposal) still needs
// to land somewhere, or the project total quietly understates itself.
type Attribution struct {
	ProjectID   uuid.UUID
	StrategyID  *uuid.UUID
	HopID       *uuid.UUID
	VariationID *uuid.UUID
}

// Recorder prices usage and appends it to the ledger. Every charge in Mendel
// goes through here, so there is one place where tokens become dollars.
type Recorder struct {
	db LedgerDB
}

// NewRecorder creates a Recorder over the given store.
func NewRecorder(db LedgerDB) *Recorder { return &Recorder{db: db} }

// RecordModelUsage prices token counts and appends a ledger entry.
//
// Usage with no tokens is dropped rather than written as a zero row. An unknown
// model is still recorded, priced at zero: losing the token counts would be
// worse than showing a charge Mendel cannot yet price, and the missing rate
// card is visible on the rate-card settings page.
func (r *Recorder) RecordModelUsage(
	ctx context.Context,
	attr Attribution,
	component, model string,
	tokens domain.TokenCounts,
) (*domain.CostEntry, error) {
	if tokens.IsZero() {
		return nil, nil
	}

	now := time.Now()
	card, err := r.db.GetModelRateCard(ctx, model, now)
	if err != nil {
		return nil, err
	}

	entry := &domain.CostEntry{
		ProjectID:   attr.ProjectID,
		StrategyID:  attr.StrategyID,
		HopID:       attr.HopID,
		VariationID: attr.VariationID,
		Kind:        domain.CostKindModel,
		Component:   component,
		Model:       &model,
		Tokens:      tokens,
		AmountUSD:   PriceModelUsage(card, tokens, false),
		OccurredAt:  now,
	}
	if card != nil {
		entry.ModelRateCardID = &card.ID
	}

	if err := r.db.RecordCostEntry(ctx, entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// RecordDeployment prices a deployment's wall-clock lifetime and appends a
// ledger entry.
//
// This is an estimate from machine shape x elapsed time against a list-price
// rate card, not a provider invoice. On scale-to-zero platforms the rate card
// says so and the charge comes out zero, leaving the deployment tracked but
// unpriced rather than confidently wrong.
func (r *Recorder) RecordDeployment(
	ctx context.Context,
	attr Attribution,
	deploymentID uuid.UUID,
	platformSlug, machineShape string,
	d time.Duration,
) (*domain.CostEntry, error) {
	if d <= 0 {
		return nil, nil
	}

	now := time.Now()
	card, err := r.db.GetHostingRateCard(ctx, platformSlug, machineShape, now)
	if err != nil {
		return nil, err
	}

	seconds := d.Seconds()
	entry := &domain.CostEntry{
		ProjectID:       attr.ProjectID,
		StrategyID:      attr.StrategyID,
		HopID:           attr.HopID,
		VariationID:     attr.VariationID,
		Kind:            domain.CostKindHosting,
		Component:       "deploy",
		DeploymentID:    &deploymentID,
		MachineShape:    &machineShape,
		DurationSeconds: &seconds,
		AmountUSD:       PriceHostingUsage(card, d),
		OccurredAt:      now,
	}
	if card != nil {
		entry.HostingRateCardID = &card.ID
	}

	if err := r.db.RecordCostEntry(ctx, entry); err != nil {
		return nil, err
	}
	return entry, nil
}
