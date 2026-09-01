package cost

import (
	"context"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/codegen/executor"
	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
)

// StrategyContextDB is the persistence surface needed to assemble the context
// handed to the estimating agents.
type StrategyContextDB interface {
	CalibrationDB
	GetObjectivesByStrategy(ctx context.Context, strategyID uuid.UUID) ([]domain.Objective, error)
	GetKeyResultsByObjective(ctx context.Context, objectiveID uuid.UUID) ([]domain.KeyResult, error)
	GetFundingSourcesByStrategy(ctx context.Context, strategyID uuid.UUID) ([]domain.FundingSource, error)
	GetStrategyCostSummary(ctx context.Context, strategyID uuid.UUID) (db.CostSummary, error)
}

// BuildStrategyContext assembles everything an estimating agent needs about a
// Strategy: its objectives, its budget and time window, what has been spent so
// far, and the project's observed cost history.
//
// This is deliberately one function rather than something each caller
// assembles. When the roadmap proposer, the reviser, and the CLI each built
// their own context, the budget was passed as a bare token figure and no
// calibration was passed at all, which is why estimates were untethered from
// what anything had actually cost.
func BuildStrategyContext(ctx context.Context, database StrategyContextDB, strategy *domain.Strategy) (agent.StrategyContext, error) {
	sc := agent.StrategyContext{
		ID:   strategy.ID.String(),
		Name: strategy.Name,
	}

	objectives, err := database.GetObjectivesByStrategy(ctx, strategy.ID)
	if err != nil {
		return sc, err
	}
	for _, obj := range objectives {
		krs, err := database.GetKeyResultsByObjective(ctx, obj.ID)
		if err != nil {
			return sc, err
		}
		var krInfos []agent.KeyResultInfo
		for _, kr := range krs {
			info := agent.KeyResultInfo{
				ID:          kr.ID.String(),
				Description: kr.Description,
				Target:      kr.Target(),
			}
			if kr.TargetDate != nil {
				d := kr.TargetDate.Format("2006-01-02")
				info.TargetDate = &d
			}
			krInfos = append(krInfos, info)
		}
		sc.Objectives = append(sc.Objectives, agent.ObjectiveInfo{
			ID:          obj.ID.String(),
			Description: obj.Description,
			KeyResults:  krInfos,
		})
	}

	// Budgets sum across funding sources; the widest period any of them covers
	// is the window the roadmap has to fit into.
	funding, err := database.GetFundingSourcesByStrategy(ctx, strategy.ID)
	if err != nil {
		return sc, err
	}
	for _, f := range funding {
		sc.BudgetUSD += f.AmountUSD
		if f.PeriodStart != nil {
			if d := f.PeriodStart.Format("2006-01-02"); sc.BudgetStart == nil || d < *sc.BudgetStart {
				sc.BudgetStart = &d
			}
		}
		if f.PeriodEnd != nil {
			if d := f.PeriodEnd.Format("2006-01-02"); sc.BudgetEnd == nil || d > *sc.BudgetEnd {
				sc.BudgetEnd = &d
			}
		}
	}

	if summary, err := database.GetStrategyCostSummary(ctx, strategy.ID); err == nil {
		sc.SpentUSD = round2(summary.AmountUSD)
	}

	// A nil calibration is meaningful: it tells the agent there is no history,
	// which should lower its confidence rather than be silently ignored.
	cal, err := BuildCalibration(ctx, database, strategy.ProjectID, executor.DefaultModel)
	if err != nil {
		return sc, err
	}
	sc.Calibration = cal

	return sc, nil
}

// RemainingUSD is the budget left for a strategy after spend to date.
func RemainingUSD(sc agent.StrategyContext) float64 {
	r := sc.BudgetUSD - sc.SpentUSD
	if r < 0 {
		return 0
	}
	return r
}
