package codegen

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/codegen/executor"
	"github.com/bhs/mendelbuild/internal/cost"
	"github.com/bhs/mendelbuild/internal/domain"
)

const (
	// spendCeilingMultiple is how far past a Hop's estimate a single run may go
	// before pausing for a human decision. Generous, because an estimate made
	// with little history is itself uncertain, and a pause costs someone's
	// attention -- but bounded, because the alternative is an open-ended spend.
	spendCeilingMultiple = 5.0

	// flatSpendCeilingUSD applies when a Hop has no estimate to multiply, which
	// is every Hop until the current model accrues history. A bound that does
	// not depend on a number Mendel may not have.
	flatSpendCeilingUSD = 5.0

	// unpricedRoundCap bounds a run that cannot be priced at all -- no rate card
	// for the model. Rounds are a poor proxy for cost, but with no pricing they
	// are the only bound left, so it is set conservatively.
	unpricedRoundCap = 50
)

// runBudget is how a single generation run is bounded.
type runBudget struct {
	// LimitUSD is the ceiling; zero means the run could not be priced.
	LimitUSD float64

	// Price converts usage to dollars, or nil when no rate card was found.
	Price func(domain.TokenCounts) float64

	// FromEstimate records whether the ceiling came from a Hop estimate or the
	// flat fallback, so logs can say which.
	FromEstimate bool
}

// Apply configures an executor with this budget, or with the conservative round
// cap when the run cannot be priced.
func (b runBudget) Apply(e *executor.Executor) *executor.Executor {
	if b.Price == nil || b.LimitUSD <= 0 {
		return e.WithMaxRounds(unpricedRoundCap)
	}
	return e.WithSpendLimit(b.LimitUSD, b.Price)
}

// Describe renders the bound for the run log.
func (b runBudget) Describe() string {
	if b.Price == nil || b.LimitUSD <= 0 {
		return fmt.Sprintf("no rate card for the model, so this run is bounded at %d rounds instead of by cost", unpricedRoundCap)
	}
	basis := fmt.Sprintf("%.0fx the hop's estimate", spendCeilingMultiple)
	if !b.FromEstimate {
		basis = "flat cap; this hop has no cost estimate yet"
	}
	return fmt.Sprintf("spend ceiling $%.2f (%s)", b.LimitUSD, basis)
}

// budgetDB is the persistence this needs.
type budgetDB interface {
	GetLatestHopEstimateUSD(ctx context.Context, hopID uuid.UUID) (float64, error)
	GetModelRateCard(ctx context.Context, model string, at time.Time) (*domain.ModelRateCard, error)
}

// budgetForRun sizes the ceiling for one generation run.
//
// Per run, not per Hop cumulatively: a human approving a paused run is granting
// another run's worth of spend, and each further run needs its own approval.
// That keeps the total bounded by decisions rather than by a number chosen up
// front, which is the point -- the previous fixed round cap threw work away
// without ever asking.
func budgetForRun(ctx context.Context, database budgetDB, hopID uuid.UUID, model string) runBudget {
	var b runBudget

	if card, err := database.GetModelRateCard(ctx, model, time.Now()); err == nil && card != nil {
		b.Price = func(t domain.TokenCounts) float64 {
			return cost.PriceModelUsage(card, t, false)
		}
	}

	estimate, err := database.GetLatestHopEstimateUSD(ctx, hopID)
	if err == nil && estimate > 0 {
		b.LimitUSD = estimate * spendCeilingMultiple
		b.FromEstimate = true
	} else {
		b.LimitUSD = flatSpendCeilingUSD
	}

	return b
}
