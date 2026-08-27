// Package cost turns the raw usage that providers report into dollars, and
// keeps the prices it uses in the database rather than in Go source.
//
// Two rules shape this package. First, USD is the unit of account: token counts
// are evidence, not currency. Second, every dollar figure must be reproducible
// from the counts and a dated rate card, so a human reviewing a number in the
// UI can check the arithmetic rather than trust it.
package cost

import (
	"fmt"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
)

// tokensPerMillion is the denominator for USD-per-million-token rates.
const tokensPerMillion = 1_000_000.0

// PriceModelUsage converts token counts to USD under the given rate card.
//
// Cache reads and writes are priced as multiples of the input rate: a read is a
// fraction of an input token, a write carries a premium over one. Both are
// counted separately from InputTokens, which the API reports as the uncached
// remainder only.
func PriceModelUsage(card *domain.ModelRateCard, t domain.TokenCounts, batch bool) float64 {
	if card == nil {
		return 0
	}

	inputUnits := float64(t.InputTokens) +
		float64(t.CacheReadTokens)*card.CacheReadMultiplier +
		float64(t.CacheWriteTokens)*card.CacheWriteMultiplier

	usd := (inputUnits*card.InputUSDPerMTok +
		float64(t.OutputTokens)*card.OutputUSDPerMTok) / tokensPerMillion

	if batch {
		usd *= card.BatchMultiplier
	}
	return usd
}

// PriceHostingUsage converts a deployment's wall-clock lifetime to USD.
//
// Platforms that scale to zero are only charged for the time they actually
// serve traffic, which Mendel cannot observe; for those, wall-clock would
// overstate spend badly, so this reports zero rather than a confident wrong
// number. Callers should present the result as an estimate either way.
func PriceHostingUsage(card *domain.HostingRateCard, d time.Duration) float64 {
	if card == nil || d <= 0 || !card.BillsWhenIdle {
		return 0
	}
	return card.USDPerHour * d.Hours()
}

// Explain renders the arithmetic behind a model charge, for display next to the
// figure so a reviewer can verify it instead of taking it on faith.
func Explain(card *domain.ModelRateCard, t domain.TokenCounts, batch bool) string {
	if card == nil {
		return "no rate card"
	}
	s := fmt.Sprintf(
		"%s: %d in @ $%.2f/M, %d out @ $%.2f/M, %d cache-read @ %.2fx, %d cache-write @ %.2fx",
		card.Model,
		t.InputTokens, card.InputUSDPerMTok,
		t.OutputTokens, card.OutputUSDPerMTok,
		t.CacheReadTokens, card.CacheReadMultiplier,
		t.CacheWriteTokens, card.CacheWriteMultiplier,
	)
	if batch {
		s += fmt.Sprintf(", batch %.2fx", card.BatchMultiplier)
	}
	return s
}
