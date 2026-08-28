package cost

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/db"
)

// calibrationSampleSize caps how much history is summarised. Recent Hops are
// better evidence than old ones: the codebase, the models, and the prices all
// move, and a year-old Hop says little about the next one.
const calibrationSampleSize = 25

// CalibrationDB is the persistence surface calibration needs.
type CalibrationDB interface {
	GetCompletedHopOutcomes(ctx context.Context, projectID uuid.UUID, model string, limit int) ([]db.HopOutcome, error)
}

// BuildCalibration summarises a project's completed Hops into the evidence an
// estimating agent needs, restricted to Hops built by the given model.
//
// The model filter is not a refinement, it is the point. What a Hop costs is a
// fact about the model that built it -- prices differ several-fold across
// models, and turning on prompt caching moved the figure again by roughly a
// factor of six. Averaging across that would anchor every new estimate to a
// setup that has been retired.
//
// Returns nil when the model has no completed Hops here. That nil is
// meaningful: it tells the agents there is no history, which should make them
// say so and keep their confidence low rather than inventing precision. A
// model switch therefore resets calibration to an honest "we do not know yet"
// instead of quietly carrying the old model's numbers forward.
func BuildCalibration(ctx context.Context, database CalibrationDB, projectID uuid.UUID, model string) (*agent.CostCalibration, error) {
	outcomes, err := database.GetCompletedHopOutcomes(ctx, projectID, model, calibrationSampleSize)
	if err != nil {
		return nil, err
	}
	if len(outcomes) == 0 {
		return nil, nil
	}

	cal := &agent.CostCalibration{Model: model, SampleSize: len(outcomes)}

	var actuals, perVariation, ratios []float64
	for _, o := range outcomes {
		// A completed Hop that cost nothing recorded no spend -- usually work
		// done before the ledger existed. Including it would drag the median
		// toward zero and make every future estimate too low.
		if o.ActualUSD > 0 {
			actuals = append(actuals, o.ActualUSD)
			if o.Variations > 0 {
				perVariation = append(perVariation, o.ActualUSD/float64(o.Variations))
			}
		}
		if r, ok := o.Ratio(); ok {
			ratios = append(ratios, r)
		}

		hop := agent.CompletedHopCost{
			Name:       o.Name,
			Commentary: o.Commentary,
			ActualUSD:  round2(o.ActualUSD),
			Variations: o.Variations,
		}
		if o.EstimateUSD != nil {
			hop.EstimatedUSD = round2(*o.EstimateUSD)
		}
		cal.CompletedHops = append(cal.CompletedHops, hop)
	}

	cal.MedianHopUSD = round2(median(actuals))
	cal.MedianVariationUSD = round2(median(perVariation))
	cal.EstimateBiasRatio = round2(median(ratios))

	return cal, nil
}

// median returns the middle value, or 0 for an empty set. The median rather
// than the mean because one runaway Hop should not redefine what normal costs.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}
