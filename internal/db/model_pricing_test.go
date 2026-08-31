package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// modelIDCases covers the shapes a model identifier takes. Used by both the
// unit test and the Go-vs-SQL agreement test below.
var modelIDCases = []struct {
	in   string
	want string
}{
	{"claude-haiku-4-5-20251001", "claude-haiku-4-5"}, // the one that was mispriced
	{"claude-sonnet-5-20260401", "claude-sonnet-5"},
	{"claude-haiku-4-5", "claude-haiku-4-5"},   // already a model line
	{"claude-sonnet-5", "claude-sonnet-5"},     // digits in the name, no date
	{"claude-opus-4-6", "claude-opus-4-6"},     // trailing digits are not a date
	{"gpt-4-1234567", "gpt-4-1234567"},         // seven digits: not a date suffix
	{"model-123456789", "model-123456789"},     // nine digits: not a date suffix
	{"", ""},                                   // degenerate, must not panic
	{"-20251001", ""},                          // pathological, but well defined
}

// TestBaseModelID pins the rule that keeps a dated snapshot priced against its
// model line.
func TestBaseModelID(t *testing.T) {
	for _, c := range modelIDCases {
		if got := domain.BaseModelID(c.in); got != c.want {
			t.Errorf("BaseModelID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBaseModelIDMatchesSQL is the guard against the two halves of this rule
// drifting apart. It is implemented twice — in Go for the rate-card lookup, and
// in SQL for the unpriced-models query — and a difference between them would
// mean the UI reports a model as unpriced while the recorder prices it, or the
// reverse. Neither failure is visible without comparing the two directly.
func TestBaseModelIDMatchesSQL(t *testing.T) {
	db, _ := testDB(t)
	ctx := context.Background()

	for _, c := range modelIDCases {
		var fromSQL string
		// The identical expression used in GetUnpricedModels.
		if err := db.Pool.QueryRow(ctx,
			`SELECT regexp_replace($1::text, '-[0-9]{8}$', '')`, c.in).Scan(&fromSQL); err != nil {
			t.Fatalf("sql for %q: %v", c.in, err)
		}
		if fromGo := domain.BaseModelID(c.in); fromGo != fromSQL {
			t.Errorf("Go and SQL disagree on %q: Go says %q, SQL says %q.\n"+
				"These two implementations of the same rule must stay identical.",
				c.in, fromGo, fromSQL)
		}
	}
}

// TestDatedSnapshotPricesAgainstItsModelLine is the bug itself: the OKR tuner
// asked for claude-haiku-4-5, the API answered as claude-haiku-4-5-20251001,
// no card matched, and every one of its calls was billed at nothing.
func TestDatedSnapshotPricesAgainstItsModelLine(t *testing.T) {
	db, _ := testDB(t)
	ctx := context.Background()
	seedCard(t, db, "claude-haiku-4-5", 1.0, 5.0, time.Now().Add(-24*time.Hour))

	card, err := db.GetModelRateCard(ctx, "claude-haiku-4-5-20251001", time.Now())
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if card == nil {
		t.Fatal("a dated snapshot found no rate card; its spend would be priced at zero")
	}
	if card.Model != "claude-haiku-4-5" {
		t.Errorf("priced against %q, want the model line claude-haiku-4-5", card.Model)
	}
}

// TestExactCardBeatsModelLine: falling back to the model line must not override
// a card someone added deliberately for one snapshot.
func TestExactCardBeatsModelLine(t *testing.T) {
	db, _ := testDB(t)
	ctx := context.Background()
	yesterday := time.Now().Add(-24 * time.Hour)
	seedCard(t, db, "claude-haiku-4-5", 1.0, 5.0, yesterday)
	seedCard(t, db, "claude-haiku-4-5-20251001", 2.0, 9.0, yesterday)

	card, err := db.GetModelRateCard(ctx, "claude-haiku-4-5-20251001", time.Now())
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if card == nil || card.Model != "claude-haiku-4-5-20251001" {
		t.Fatalf("a card for the exact snapshot should win; got %v", card)
	}
	if card.InputUSDPerMTok != 2.0 {
		t.Errorf("priced at %.2f, want the snapshot's own 2.00", card.InputUSDPerMTok)
	}
}

// TestUnpricedModelsIgnoresDatedSnapshots: with the fallback in place, a dated
// snapshot whose line has a card is priced, so warning about it would send
// someone to add a rate card that is not missing.
func TestUnpricedModelsIgnoresDatedSnapshots(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	seedCard(t, db, "claude-haiku-4-5", 1.0, 5.0, time.Now().Add(-24*time.Hour))

	seedModelEntry(t, db, projectID, "claude-haiku-4-5-20251001")
	seedModelEntry(t, db, projectID, "some-model-nobody-priced")

	unpriced, err := db.GetUnpricedModels(ctx)
	if err != nil {
		t.Fatalf("unpriced: %v", err)
	}
	for _, m := range unpriced {
		if m == "claude-haiku-4-5-20251001" {
			t.Error("a dated snapshot of a priced model line was reported as unpriced")
		}
	}
	var sawGenuine bool
	for _, m := range unpriced {
		if m == "some-model-nobody-priced" {
			sawGenuine = true
		}
	}
	if !sawGenuine {
		t.Error("a model with no card at all should still be reported")
	}
}

// TestPriceExistingEntryOnlyFillsGaps: repricing may fill in a charge that was
// never priced, and must never revise one that was. A figure computed from a
// card stays verifiable against that card.
func TestPriceExistingEntryOnlyFillsGaps(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	cardID := seedCard(t, db, "claude-haiku-4-5", 1.0, 5.0, time.Now().Add(-24*time.Hour))

	gapID := seedModelEntry(t, db, projectID, "claude-haiku-4-5-20251001")
	if err := db.PriceExistingEntry(ctx, gapID, cardID, 0.25); err != nil {
		t.Fatalf("price gap: %v", err)
	}
	if got := entryAmount(t, db, gapID); got != 0.25 {
		t.Errorf("unpriced entry should have been filled in; amount = %v", got)
	}

	// Now that it is priced, a second pass must leave it alone.
	if err := db.PriceExistingEntry(ctx, gapID, cardID, 99.0); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if got := entryAmount(t, db, gapID); got != 0.25 {
		t.Errorf("an already-priced entry was rewritten to %v; ledger figures must stay verifiable", got)
	}
}

func seedCard(t *testing.T, db *DB, model string, in, out float64, from time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Pool.Exec(context.Background(), `
		INSERT INTO model_rate_cards (id, model, input_usd_per_mtok, output_usd_per_mtok,
		    cache_read_multiplier, cache_write_multiplier, batch_multiplier, effective_from, source)
		VALUES ($1, $2, $3, $4, 0.1, 1.25, 0.5, $5, 'test')
	`, id, model, in, out, from)
	if err != nil {
		t.Fatalf("seed card %s: %v", model, err)
	}
	return id
}

func seedModelEntry(t *testing.T, db *DB, projectID uuid.UUID, model string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Pool.Exec(context.Background(), `
		INSERT INTO cost_entries (id, project_id, kind, component, model,
		    input_tokens, output_tokens, amount_usd, occurred_at)
		VALUES ($1, $2, 'model', 'okr_tuner', $3, 1000, 500, 0, NOW())
	`, id, projectID, model)
	if err != nil {
		t.Fatalf("seed entry %s: %v", model, err)
	}
	return id
}

func entryAmount(t *testing.T, db *DB, id uuid.UUID) float64 {
	t.Helper()
	var amount float64
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT amount_usd FROM cost_entries WHERE id = $1`, id).Scan(&amount); err != nil {
		t.Fatalf("read amount: %v", err)
	}
	return amount
}
