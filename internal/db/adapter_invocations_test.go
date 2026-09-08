package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
)

// A report arrives from a job in the user's own environment, over the public
// internet, knowing only what Mendel gave it. So the token is the whole of the
// authentication, and these are about what it does and does not open.

func TestATokenIsNeverStoredAndOpensOneInvocation(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	inv, token, err := db.CreateAdapterInvocation(ctx, projectID, "probe", []byte(`{"phase":"probe"}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if token == "" {
		t.Fatal("an invocation with no token cannot be reported against")
	}

	// The token itself must not be recoverable from the database. Reading the
	// row is not supposed to let anyone forge a report.
	var stored []byte
	if err := db.Pool.QueryRow(ctx,
		`SELECT token_hash FROM adapter_invocations WHERE id = $1`, inv.ID).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(stored) == token {
		t.Error("the token was stored in the clear")
	}

	found, err := db.AdapterInvocationByToken(ctx, token)
	if err != nil || found == nil {
		t.Fatalf("the minted token should find its invocation: %v", err)
	}
	if found.ID != inv.ID || found.ProjectID != projectID {
		t.Errorf("found the wrong invocation: %+v", found)
	}

	if _, err := db.AdapterInvocationByToken(ctx, token+"x"); err == nil {
		t.Error("a token that was never minted must open nothing")
	}
}

// Two invocations must not be reachable by one token, or a job could report
// findings about a phase nobody asked it for.
func TestATokenOpensOnlyItsOwnInvocation(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	first, firstToken, err := db.CreateAdapterInvocation(ctx, projectID, "probe", []byte(`{}`))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, secondToken, err := db.CreateAdapterInvocation(ctx, projectID, "admit", []byte(`{}`))
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	got, err := db.AdapterInvocationByToken(ctx, firstToken)
	if err != nil {
		t.Fatalf("lookup first: %v", err)
	}
	if got.ID != first.ID {
		t.Errorf("the first token found %v", got)
	}
	got, err = db.AdapterInvocationByToken(ctx, secondToken)
	if err != nil || got.ID != second.ID {
		t.Errorf("the second token found %v", got)
	}
}

// An expired token is answered the same way an unknown one is, so presenting one
// cannot be used to learn whether it was ever real.
func TestAnExpiredTokenOpensNothing(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	_, token, err := db.CreateAdapterInvocation(ctx, projectID, "probe", []byte(`{}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`UPDATE adapter_invocations SET expires_at = NOW() - INTERVAL '1 minute'`); err != nil {
		t.Fatalf("expire: %v", err)
	}

	if _, err := db.AdapterInvocationByToken(ctx, token); err == nil {
		t.Error("an expired token must open nothing; it is a standing credential otherwise")
	}
}

// One invocation reports once. A second report is a retrying job or a replay,
// and it must not overwrite what the first said.
func TestAnInvocationReportsOnce(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	inv, _, _ := db.CreateAdapterInvocation(ctx, projectID, "probe", []byte(`{}`))

	if err := db.RecordAdapterResult(ctx, inv.ID, domain.AdapterOutcomeCompleted, []byte(`{"first":true}`)); err != nil {
		t.Fatalf("first report: %v", err)
	}
	if err := db.RecordAdapterResult(ctx, inv.ID, domain.AdapterOutcomeFailed, []byte(`{"second":true}`)); err == nil {
		t.Error("a second report must be refused rather than overwrite the first")
	}

	back, err := db.LatestAdapterInvocation(ctx, projectID, "probe")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	// Compared as JSON rather than as bytes: JSONB normalises key order and
	// whitespace on the way in, so these are the same JSON and not the same
	// bytes.
	var stood struct{ First bool }
	if err := json.Unmarshal(back.Result, &stood); err != nil {
		t.Fatalf("unmarshal what was stored: %v", err)
	}
	if !stood.First {
		t.Errorf("the first report should stand, got %s", back.Result)
	}
}

// Never asked is a third answer, and a caller has to be able to tell it from
// asked-and-told-no. A page that renders them alike sends someone to fix
// something nobody has looked at.
func TestNeverAskedIsNotAnAnswer(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	got, err := db.LatestAdapterInvocation(ctx, projectID, "probe")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if got != nil {
		t.Errorf("a project that has never been probed returned %+v", got)
	}
	if got.Answered() {
		t.Error("nothing is not an answer")
	}
	if got.State(time.Now()) != domain.AdapterAbandoned {
		t.Errorf("state of nothing = %q", got.State(time.Now()))
	}
}

// The three states a run can be in, told apart. Running, answered and failed are
// different things, and so is a run nobody heard from inside its window.
func TestARunIsRunningAnsweredFailedOrAbandoned(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	now := time.Now()

	running, _, _ := db.CreateAdapterInvocation(ctx, projectID, "probe", []byte(`{}`))
	if got := running.State(now); got != domain.AdapterRunning {
		t.Errorf("a fresh invocation is %q, want running", got)
	}

	if err := db.RecordAdapterResult(ctx, running.ID, domain.AdapterOutcomeFailed, []byte(`{}`)); err != nil {
		t.Fatalf("report: %v", err)
	}
	failed, _ := db.LatestAdapterInvocation(ctx, projectID, "probe")
	if got := failed.State(now); got != domain.AdapterFailed {
		t.Errorf("a run that could not conclude is %q, want failed", got)
	}
	if failed.Answered() {
		t.Error("a failed run has not answered")
	}

	stale, _, _ := db.CreateAdapterInvocation(ctx, projectID, "admit", []byte(`{}`))
	if got := stale.State(stale.ExpiresAt.Add(time.Minute)); got != domain.AdapterAbandoned {
		t.Errorf("a run past its window with no report is %q, want abandoned", got)
	}
}
