package cost

import (
	"math"
	"testing"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
)

// sonnet46 mirrors a seeded rate card: $3/M in, $15/M out, cache read at a
// tenth of input, cache write at a 1.25x premium over it.
func sonnet46() *domain.ModelRateCard {
	return &domain.ModelRateCard{
		Model:                "claude-sonnet-4-6",
		InputUSDPerMTok:      3,
		OutputUSDPerMTok:     15,
		CacheReadMultiplier:  0.1,
		CacheWriteMultiplier: 1.25,
		BatchMultiplier:      0.5,
	}
}

func closeTo(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %.10f, want %.10f", got, want)
	}
}

func TestPriceModelUsageChargesEachTokenKindAtItsOwnRate(t *testing.T) {
	// 1M plain input, 1M output, 1M cache reads, 1M cache writes.
	// $3 + $15 + $0.30 + $3.75 = $22.05
	closeTo(t, PriceModelUsage(sonnet46(), domain.TokenCounts{
		InputTokens:      1_000_000,
		OutputTokens:     1_000_000,
		CacheReadTokens:  1_000_000,
		CacheWriteTokens: 1_000_000,
	}, false), 22.05)
}

// The bug this guards: input_tokens from the API is the uncached remainder
// only, so pricing it alone silently drops most of a long agentic run's prompt.
func TestPriceModelUsageDoesNotIgnoreCacheTokens(t *testing.T) {
	// A realistic cache-heavy run: a small uncached remainder, a large cached
	// prefix re-read every round.
	heavy := domain.TokenCounts{InputTokens: 20_000, OutputTokens: 40_000, CacheReadTokens: 4_000_000}

	full := PriceModelUsage(sonnet46(), heavy, false)
	inputOutputOnly := PriceModelUsage(sonnet46(), domain.TokenCounts{
		InputTokens: heavy.InputTokens, OutputTokens: heavy.OutputTokens,
	}, false)

	// $0.06 + $0.60 + $1.20 = $1.86 against $0.66 -- the old accounting would
	// have reported barely a third of the real charge.
	closeTo(t, full, 1.86)
	if full <= inputOutputOnly*2 {
		t.Errorf("cache reads should dominate this run: full %.4f vs input/output-only %.4f",
			full, inputOutputOnly)
	}
}

func TestPriceModelUsageAppliesBatchDiscount(t *testing.T) {
	tokens := domain.TokenCounts{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	closeTo(t, PriceModelUsage(sonnet46(), tokens, true), 9.0)
}

// An unpriced model must not fail the run that used it; it reports zero, and
// the tokens are still recorded by the caller.
func TestPriceModelUsageWithoutRateCardIsZero(t *testing.T) {
	closeTo(t, PriceModelUsage(nil, domain.TokenCounts{InputTokens: 1_000_000}, false), 0)
}

func TestPriceHostingUsageChargesByWallClock(t *testing.T) {
	card := &domain.HostingRateCard{USDPerHour: 0.02, BillsWhenIdle: true}
	closeTo(t, PriceHostingUsage(card, 90*time.Minute), 0.03)
}

// Scale-to-zero platforms bill per request. Mendel can see how long a
// deployment existed but not how long it served traffic, so wall-clock pricing
// there would overstate spend badly; reporting zero is the honest answer.
func TestPriceHostingUsageSkipsScaleToZeroPlatforms(t *testing.T) {
	card := &domain.HostingRateCard{USDPerHour: 0.02, BillsWhenIdle: false}
	closeTo(t, PriceHostingUsage(card, 100*time.Hour), 0)
}

func TestMedianIgnoresOutliers(t *testing.T) {
	// One runaway Hop must not redefine what a normal Hop costs.
	if got := median([]float64{1, 2, 3, 4, 1000}); got != 3 {
		t.Errorf("median = %v, want 3", got)
	}
	if got := median(nil); got != 0 {
		t.Errorf("median of nothing = %v, want 0", got)
	}
}

func TestTokenCountsPromptIncludesCache(t *testing.T) {
	tc := domain.TokenCounts{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 100, CacheWriteTokens: 20}
	if got := tc.PromptTokens(); got != 130 {
		t.Errorf("PromptTokens = %d, want 130", got)
	}
	if got := tc.Total(); got != 135 {
		t.Errorf("Total = %d, want 135", got)
	}
}
