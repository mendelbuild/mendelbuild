package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// The drafting state machine lives in SQL, deliberately: the timestamp columns
// are `timestamp without time zone`, and pgx writes a time.Time as its local
// wall clock but reads one back labelled UTC, so the same comparison done in Go
// is wrong by the machine's UTC offset. That bug shipped once — a draft thirty
// seconds old read as hours stale on a machine seven hours off UTC — and it is
// invisible to any test that does not run real SQL against a real clock.

// TestBeginStrategyDraftIsExclusive: a second draft must not start against rows
// a first one is still rewriting. Two concurrent agent calls replacing the same
// objectives would interleave their deletes and inserts.
func TestBeginStrategyDraftIsExclusive(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	strategyID := onlyStrategy(t, db, projectID)

	started, err := db.BeginStrategyDraft(ctx, strategyID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if !started {
		t.Fatal("the first draft should start")
	}

	again, err := db.BeginStrategyDraft(ctx, strategyID)
	if err != nil {
		t.Fatalf("begin again: %v", err)
	}
	if again {
		t.Error("a second draft must not start while one is running")
	}

	// Once the first finishes, a retry is allowed again.
	if err := db.FinishStrategyDraft(ctx, strategyID, nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	retry, err := db.BeginStrategyDraft(ctx, strategyID)
	if err != nil {
		t.Fatalf("begin retry: %v", err)
	}
	if !retry {
		t.Error("a draft should be startable once the previous one has finished")
	}
}

// TestBeginStrategyDraftReclaimsStale: a draft whose process died leaves a row
// claiming to be running. Without reclaiming it, the retry button does nothing
// and the project is stuck forever.
func TestBeginStrategyDraftReclaimsStale(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	strategyID := onlyStrategy(t, db, projectID)

	if _, err := db.BeginStrategyDraft(ctx, strategyID); err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Age it past the stale window using the database's own clock.
	if _, err := db.Pool.Exec(ctx, `
		UPDATE strategies SET draft_started_at = NOW() - INTERVAL '1 hour' WHERE id = $1
	`, strategyID); err != nil {
		t.Fatalf("age the draft: %v", err)
	}

	reclaimed, err := db.BeginStrategyDraft(ctx, strategyID)
	if err != nil {
		t.Fatalf("begin over stale: %v", err)
	}
	if !reclaimed {
		t.Error("a draft abandoned an hour ago should be reclaimable")
	}

	// A 'drafting' row that never recorded a start time is the same situation.
	if _, err := db.Pool.Exec(ctx, `
		UPDATE strategies SET draft_status = 'drafting', draft_started_at = NULL WHERE id = $1
	`, strategyID); err != nil {
		t.Fatalf("clear start time: %v", err)
	}
	reclaimed, err = db.BeginStrategyDraft(ctx, strategyID)
	if err != nil {
		t.Fatalf("begin over null start: %v", err)
	}
	if !reclaimed {
		t.Error("a drafting row with no start time should be reclaimable")
	}
}

// TestOnboardingStateReadsDraft is the regression test for the timezone bug: a
// draft that has just started must read as running, not stale, whatever offset
// the machine running the tests happens to be at.
func TestOnboardingStateReadsDraft(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	strategyID := onlyStrategy(t, db, projectID)

	if _, err := db.BeginStrategyDraft(ctx, strategyID); err != nil {
		t.Fatalf("begin: %v", err)
	}

	st, err := db.GetOnboardingState(ctx, projectID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !st.Drafting {
		t.Error("a draft that just started must read as running")
	}
	if st.DraftFailed {
		t.Error("a draft that just started must not read as failed")
	}

	// Aged out: now it is the user's move, not Mendel's.
	if _, err := db.Pool.Exec(ctx, `
		UPDATE strategies SET draft_started_at = NOW() - INTERVAL '1 hour' WHERE id = $1
	`, strategyID); err != nil {
		t.Fatalf("age the draft: %v", err)
	}
	st, err = db.GetOnboardingState(ctx, projectID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.Drafting {
		t.Error("an abandoned draft must stop reading as running, or the page polls forever")
	}
	if !st.DraftFailed {
		t.Error("an abandoned draft should surface as failed so the user can retry")
	}

	// A recorded failure reads the same way.
	if err := db.FinishStrategyDraft(ctx, strategyID, context.DeadlineExceeded); err != nil {
		t.Fatalf("finish with error: %v", err)
	}
	st, err = db.GetOnboardingState(ctx, projectID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.Drafting || !st.DraftFailed {
		t.Error("a recorded failure should read as failed and not running")
	}

	// And a successful finish clears both.
	if err := db.FinishStrategyDraft(ctx, strategyID, nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	st, err = db.GetOnboardingState(ctx, projectID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.Drafting || st.DraftFailed {
		t.Error("a finished draft is neither running nor failed")
	}
}

// TestCreateProjectWithStrategyStartsDrafting: the row must already say
// 'drafting' when it is created, or the review screen the browser is redirected
// to can render "no objectives yet" in the gap before the goroutine starts.
func TestCreateProjectWithStrategyStartsDrafting(t *testing.T) {
	db, _ := testDB(t)
	ctx := context.Background()

	projectID, strategyID, err := db.CreateProjectWithStrategy(ctx, "trailkit", "a brief", "Initial strategy", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	st, err := db.GetOnboardingState(ctx, projectID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !st.Drafting {
		t.Error("a newly created project must read as drafting from its first render")
	}
	if st.DraftFailed {
		t.Error("a newly created project must not read as failed")
	}

	strategy, err := db.GetStrategy(ctx, strategyID)
	if err != nil {
		t.Fatalf("get strategy: %v", err)
	}
	if strategy.DraftStartedAt == nil {
		t.Error("creation should record when the draft started")
	}
}

// onlyStrategy returns the single strategy the test harness seeds.
func onlyStrategy(t *testing.T, db *DB, projectID uuid.UUID) uuid.UUID {
	t.Helper()
	strategies, err := db.GetStrategiesByProject(context.Background(), projectID)
	if err != nil || len(strategies) != 1 {
		t.Fatalf("expected one seeded strategy, got %d (err %v)", len(strategies), err)
	}
	return strategies[0].ID
}
