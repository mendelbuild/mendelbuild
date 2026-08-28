package codegen

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

type fakeBudgetDB struct {
	estimate float64
	card     *domain.ModelRateCard
}

func (f fakeBudgetDB) GetLatestHopEstimateUSD(context.Context, uuid.UUID) (float64, error) {
	return f.estimate, nil
}
func (f fakeBudgetDB) GetModelRateCard(context.Context, string, time.Time) (*domain.ModelRateCard, error) {
	return f.card, nil
}

func card() *domain.ModelRateCard {
	return &domain.ModelRateCard{
		Model: "claude-sonnet-5", InputUSDPerMTok: 2, OutputUSDPerMTok: 10,
		CacheReadMultiplier: 0.1, CacheWriteMultiplier: 1.25, BatchMultiplier: 0.5,
	}
}

func TestBudgetFromEstimate(t *testing.T) {
	b := budgetForRun(context.Background(), fakeBudgetDB{estimate: 3, card: card()}, uuid.New(), "claude-sonnet-5")

	if b.LimitUSD != 15 {
		t.Errorf("ceiling = %v, want 5x the $3 estimate", b.LimitUSD)
	}
	if !b.FromEstimate {
		t.Error("should be marked as derived from the estimate")
	}
	if b.Price == nil {
		t.Fatal("expected a pricing function")
	}
	// The pricing function must actually price, or the ceiling never trips.
	if got := b.Price(domain.TokenCounts{InputTokens: 1_000_000}); got != 2 {
		t.Errorf("1M input tokens priced at %v, want 2", got)
	}
}

// Every Hop lacks an estimate until the current model accrues history, so the
// fallback is the common case rather than an edge one.
func TestBudgetFallsBackToFlatCapWithoutAnEstimate(t *testing.T) {
	b := budgetForRun(context.Background(), fakeBudgetDB{estimate: 0, card: card()}, uuid.New(), "claude-sonnet-5")

	if b.LimitUSD != flatSpendCeilingUSD {
		t.Errorf("ceiling = %v, want the flat cap %v", b.LimitUSD, flatSpendCeilingUSD)
	}
	if b.FromEstimate {
		t.Error("should not claim to be derived from an estimate")
	}
	if !strings.Contains(b.Describe(), "no cost estimate yet") {
		t.Errorf("description should say why it is a flat cap: %q", b.Describe())
	}
}

// With no rate card the run cannot be priced, so it must fall back to a
// conservative round cap rather than running unbounded.
func TestBudgetWithoutARateCardBoundsByRounds(t *testing.T) {
	b := budgetForRun(context.Background(), fakeBudgetDB{estimate: 3, card: nil}, uuid.New(), "unpriced-model")

	if b.Price != nil {
		t.Error("expected no pricing function when there is no rate card")
	}
	if !strings.Contains(b.Describe(), "bounded at 50 rounds") {
		t.Errorf("description should say the run is round-bounded: %q", b.Describe())
	}
}
