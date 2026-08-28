package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhs/mendelbuild/internal/testdb"

	"github.com/bhs/mendelbuild/internal/domain"
)

// testDB brings up an isolated Postgres schema loaded from full.sql and
// returns a DB pointed at it. Like the schema test, this requires a real
// database rather than skipping: the behaviour under test here — what a
// re-declaration keeps and what it drops — exists only in SQL.
func testDB(t *testing.T) (*DB, uuid.UUID) {
	t.Helper()

	connString := testdb.ConnString()

	ctx := context.Background()
	schemaName := "test_req_" + uuid.New().String()[:8]

	admin, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(context.Background(), connString)
		if err != nil {
			return
		}
		defer cleanup.Close()
		cleanup.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE")
	})

	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	t.Cleanup(pool.Close)

	full, err := os.ReadFile(filepath.Join("..", "..", "schema", "full.sql"))
	if err != nil {
		t.Fatalf("read full.sql: %v", err)
	}
	if _, err := pool.Exec(ctx, string(full)); err != nil {
		t.Fatalf("load full.sql: %v", err)
	}

	db := &DB{Pool: pool}
	projectID := seedProject(t, db)
	return db, projectID
}

// seedProject creates the project/strategy/hop chain a variation hangs from.
func seedProject(t *testing.T, db *DB) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	projectID, strategyID := uuid.New(), uuid.New()
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO projects (id, name) VALUES ($1, 'test')`, []any{projectID}},
		{`INSERT INTO strategies (id, project_id, name) VALUES ($1, $2, 'main')`, []any{strategyID, projectID}},
	}
	for _, s := range stmts {
		if _, err := db.Pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return projectID
}

// seedVariation creates a variation in the given status and returns its ID.
func seedVariation(t *testing.T, db *DB, projectID uuid.UUID, status string) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var strategyID uuid.UUID
	if err := db.Pool.QueryRow(ctx,
		`SELECT id FROM strategies WHERE project_id = $1`, projectID).Scan(&strategyID); err != nil {
		t.Fatalf("find strategy: %v", err)
	}

	hopID, variationID := uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO hops (id, strategy_id, name, commentary) VALUES ($1, $2, $3, 'why')`,
		hopID, strategyID, "hop-"+hopID.String()[:6]); err != nil {
		t.Fatalf("seed hop: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO variations (id, hop_id, name, status) VALUES ($1, $2, 'v', $3)`,
		variationID, hopID, status); err != nil {
		t.Fatalf("seed variation: %v", err)
	}
	return variationID
}

func secretReq(name string) domain.VariationRequirement {
	return domain.VariationRequirement{Kind: domain.RequirementKindSecret, Name: name}
}

// Re-running code generation must not make the user re-register a redirect URI
// that is still required and still correct, so a requirement that survives a
// re-declaration keeps its ID — and with it its acknowledgements.
func TestReplaceVariationRequirementsPreservesSurvivors(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	variationID := seedVariation(t, db, projectID, "pending")

	instructions := "Register " + domain.DeployURLPlaceholder
	first := []domain.VariationRequirement{
		secretReq("GOOGLE_CLIENT_SECRET"),
		{Kind: domain.RequirementKindAcknowledgement, Name: "redirect-uri", Instructions: &instructions},
	}
	if err := db.ReplaceVariationRequirements(ctx, variationID, first); err != nil {
		t.Fatalf("first declaration: %v", err)
	}

	stored, err := db.ListVariationRequirements(ctx, variationID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected 2 requirements, got %d", len(stored))
	}

	var ackID uuid.UUID
	for _, r := range stored {
		if r.Kind == domain.RequirementKindAcknowledgement {
			ackID = r.ID
		}
	}
	if err := db.AcknowledgeRequirement(ctx, ackID, "Register https://demo.fly.dev", nil); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	// A revision re-declares the same two, and adds one.
	second := append(append([]domain.VariationRequirement{}, first...), secretReq("GOOGLE_CLIENT_ID"))
	if err := db.ReplaceVariationRequirements(ctx, variationID, second); err != nil {
		t.Fatalf("second declaration: %v", err)
	}

	after, err := db.ListVariationRequirements(ctx, variationID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(after) != 3 {
		t.Fatalf("expected 3 requirements after re-declaration, got %d", len(after))
	}

	acked, err := db.ListAcknowledgements(ctx, []uuid.UUID{ackID})
	if err != nil {
		t.Fatalf("list acknowledgements: %v", err)
	}
	if !acked[ackID]["Register https://demo.fly.dev"] {
		t.Error("re-declaring an unchanged requirement discarded its acknowledgement")
	}
}

// A revision that drops the OAuth flow must not leave the demo blocked on a
// secret nothing reads any more.
func TestReplaceVariationRequirementsDropsWhatIsGone(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	variationID := seedVariation(t, db, projectID, "pending")

	if err := db.ReplaceVariationRequirements(ctx, variationID,
		[]domain.VariationRequirement{secretReq("GOOGLE_CLIENT_SECRET"), secretReq("STRIPE_KEY")}); err != nil {
		t.Fatalf("declare: %v", err)
	}

	// Only one is still needed.
	if err := db.ReplaceVariationRequirements(ctx, variationID,
		[]domain.VariationRequirement{secretReq("STRIPE_KEY")}); err != nil {
		t.Fatalf("re-declare: %v", err)
	}
	after, _ := db.ListVariationRequirements(ctx, variationID)
	if len(after) != 1 || after[0].Name != "STRIPE_KEY" {
		t.Fatalf("expected only STRIPE_KEY, got %v", names(after))
	}

	// And an empty declaration clears the lot — this is the path taken when
	// codegen writes no requirements file at all.
	if err := db.ReplaceVariationRequirements(ctx, variationID, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	after, _ = db.ListVariationRequirements(ctx, variationID)
	if len(after) != 0 {
		t.Fatalf("expected no requirements, got %v", names(after))
	}
}

// Production runs the merged code, so it needs what the merged variations
// needed — and nothing from variations that were rejected or are still in
// flight.
func TestListMergedVariationRequirements(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	merged := seedVariation(t, db, projectID, "merged")
	rejected := seedVariation(t, db, projectID, "rejected")
	inFlight := seedVariation(t, db, projectID, "pending")

	if err := db.ReplaceVariationRequirements(ctx, merged,
		[]domain.VariationRequirement{secretReq("GOOGLE_CLIENT_SECRET")}); err != nil {
		t.Fatalf("declare merged: %v", err)
	}
	if err := db.ReplaceVariationRequirements(ctx, rejected,
		[]domain.VariationRequirement{secretReq("ABANDONED_KEY")}); err != nil {
		t.Fatalf("declare rejected: %v", err)
	}
	if err := db.ReplaceVariationRequirements(ctx, inFlight,
		[]domain.VariationRequirement{secretReq("NOT_YET_KEY")}); err != nil {
		t.Fatalf("declare in-flight: %v", err)
	}

	got, err := db.ListMergedVariationRequirements(ctx, projectID)
	if err != nil {
		t.Fatalf("list merged: %v", err)
	}
	if len(got) != 1 || got[0].Name != "GOOGLE_CLIENT_SECRET" {
		t.Fatalf("production should require only the merged variation's secret, got %v", names(got))
	}
}

// Two variations that both wire up Google sign-in describe the same
// requirement; production should ask for it once.
func TestMergedRequirementsDeduplicate(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		v := seedVariation(t, db, projectID, "merged")
		if err := db.ReplaceVariationRequirements(ctx, v,
			[]domain.VariationRequirement{secretReq("GOOGLE_CLIENT_SECRET")}); err != nil {
			t.Fatalf("declare: %v", err)
		}
	}

	got, err := db.ListMergedVariationRequirements(ctx, projectID)
	if err != nil {
		t.Fatalf("list merged: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one deduplicated requirement, got %v", names(got))
	}
}

// A requirement ID from another project must not be usable to write a value
// into this one.
func TestGetProjectIDForVariation(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	variationID := seedVariation(t, db, projectID, "pending")

	got, err := db.GetProjectIDForVariation(ctx, variationID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got != projectID {
		t.Errorf("project = %s, want %s", got, projectID)
	}

	if _, err := db.GetProjectIDForVariation(ctx, uuid.New()); err == nil {
		t.Error("an unknown variation should not resolve to a project")
	}
}

func names(reqs []domain.VariationRequirement) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, fmt.Sprintf("%s/%s", r.Kind, r.Name))
	}
	return out
}
