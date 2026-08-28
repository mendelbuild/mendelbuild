package executor

import (
	"testing"

	"github.com/bhs/mendelbuild/internal/domain"
)

// sonnetish prices roughly like a real card, so the numbers below read as money
// rather than as arbitrary units.
func sonnetish(t domain.TokenCounts) float64 {
	return (float64(t.InputTokens)*2 +
		float64(t.OutputTokens)*10 +
		float64(t.CacheReadTokens)*0.2) / 1_000_000
}

func TestSpendLimitTripsOnCostNotRounds(t *testing.T) {
	e := New("key", "/tmp").WithSpendLimit(1.00, sonnetish)

	// Many cheap rounds stay well inside the ceiling. Under the old fixed round
	// cap this run would have been cut off long before spending anything.
	e.stats = Stats{APIRounds: 120, InputTokens: 50_000, OutputTokens: 5_000}
	if e.overSpendLimit() {
		t.Errorf("120 cheap rounds costing $%.4f should not trip a $1.00 ceiling", e.SpendUSD())
	}

	// A handful of expensive rounds does trip it, which is the point: cost,
	// not turn count, is what the ceiling bounds.
	e.stats = Stats{APIRounds: 6, InputTokens: 200_000, OutputTokens: 80_000}
	if !e.overSpendLimit() {
		t.Errorf("6 expensive rounds costing $%.4f should trip a $1.00 ceiling", e.SpendUSD())
	}
}

func TestSpendLimitIsInertWithoutConfiguration(t *testing.T) {
	e := New("key", "/tmp")
	e.stats = Stats{InputTokens: 100_000_000}
	if e.overSpendLimit() {
		t.Error("an executor with no spend limit must never stop for spend")
	}
	if got := e.SpendUSD(); got != 0 {
		t.Errorf("SpendUSD without a pricing function = %v, want 0", got)
	}

	// A limit with no way to price it is not a limit.
	e = New("key", "/tmp").WithSpendLimit(0.01, nil)
	e.stats = Stats{InputTokens: 100_000_000}
	if e.overSpendLimit() {
		t.Error("a limit with no pricing function must not trip")
	}
}

// The backstop is lowered when a run cannot be priced, so an unpriceable run is
// still bounded by something.
func TestMaxRoundsOverrideOnlyLowersDeliberately(t *testing.T) {
	if got := New("key", "/tmp").WithMaxRounds(50).maxRounds; got != 50 {
		t.Errorf("maxRounds = %d, want 50", got)
	}
	// A nonsense value must not silently disable the backstop.
	if got := New("key", "/tmp").WithMaxRounds(0).maxRounds; got != 0 {
		t.Errorf("WithMaxRounds(0) should be ignored, leaving the default; got %d", got)
	}
}
