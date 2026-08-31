package experiment_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhs/mendelbuild/internal/experiment"
	"github.com/bhs/mendelbuild/internal/experiment/pgstore"
	"github.com/bhs/mendelbuild/internal/testdb"
)

// targetDB stands in for the user's application database: a schema of its own,
// with a users table mainline owns and writes.
//
// Real Postgres rather than a fake, because everything under test here is
// SQL — what a migration does to a live table, what a concurrent one does to
// the same table, and whether a dump can be put back. A mock would only
// confirm that the code calls the functions it calls.
func targetDB(t *testing.T) (*pgxpool.Pool, *experiment.Applier) {
	t.Helper()
	ctx := context.Background()
	conn := testdb.ConnString()

	schema := "exp_" + uuid.New().String()[:8]
	admin, err := pgxpool.New(ctx, conn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		p, err := pgxpool.New(context.Background(), conn)
		if err != nil {
			return
		}
		defer p.Close()
		p.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	cfg, err := pgxpool.ParseConfig(conn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `
		CREATE TABLE users (
			id   uuid PRIMARY KEY,
			name text NOT NULL
		);
		INSERT INTO users (id, name) VALUES
			(gen_random_uuid(), 'ana'),
			(gen_random_uuid(), 'ben'),
			(gen_random_uuid(), 'cy');
	`); err != nil {
		t.Fatalf("seed mainline table: %v", err)
	}

	// Mendel's own database holds the lock. Here it is the same server on a
	// different pool, which is what matters: the lock is not in the target.
	lockPool, err := pgxpool.New(ctx, conn)
	if err != nil {
		t.Fatalf("connect lock pool: %v", err)
	}
	t.Cleanup(lockPool.Close)

	return pool, &experiment.Applier{
		Store:   pgstore.New(pool),
		Lock:    experiment.PGLocker{Pool: lockPool},
		LockKey: schema,
	}
}

// armMigration builds an additive, namespaced migration for one Arm.
func armMigration(arm string) experiment.Migration {
	col := experiment.NamespacePrefix + arm + "_choice"
	tbl := experiment.NamespacePrefix + arm + "_events"
	return experiment.Migration{
		ArmID: arm,
		Up: fmt.Sprintf(`
			ALTER TABLE users ADD COLUMN %s text;
			CREATE TABLE %s (
				id      serial PRIMARY KEY,
				user_id uuid NOT NULL,
				note    text
			);
		`, col, tbl),
		Down: fmt.Sprintf(`
			DROP TABLE %s;
			ALTER TABLE users DROP COLUMN %s;
		`, tbl, col),
	}
}

func columnExists(t *testing.T, pool *pgxpool.Pool, table, col string) bool {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = ANY(current_schemas(false)) AND table_name = $1 AND column_name = $2
	`, table, col).Scan(&n)
	if err != nil {
		t.Fatalf("check column: %v", err)
	}
	return n > 0
}

func tableExists(t *testing.T, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = ANY(current_schemas(false)) AND table_name = $1
	`, table).Scan(&n)
	if err != nil {
		t.Fatalf("check table: %v", err)
	}
	return n > 0
}

// This is the claim the whole design rests on: several Variations of one Hop
// can hold schema changes against one production database at the same time
// without disturbing each other or mainline, and any one of them can be
// withdrawn while the others carry on.
//
// If this does not hold, nothing above it is worth building.
func TestThreeArmsCoexistAndOneRollsBack(t *testing.T) {
	ctx := context.Background()
	pool, applier := targetDB(t)

	arms := []string{"a", "b", "c"}
	admitted := map[string]*experiment.Admission{}

	for _, arm := range arms {
		adm, err := applier.Admit(ctx, armMigration(arm))
		if err != nil {
			t.Fatalf("arm %s should be admissible: %v", arm, err)
		}
		if err := applier.Apply(ctx, adm); err != nil {
			t.Fatalf("apply arm %s: %v", arm, err)
		}
		admitted[arm] = adm
	}

	// All three arms' objects are present, side by side.
	for _, arm := range arms {
		if !columnExists(t, pool, "users", experiment.NamespacePrefix+arm+"_choice") {
			t.Errorf("arm %s: its column is missing after applying", arm)
		}
		if !tableExists(t, pool, experiment.NamespacePrefix+arm+"_events") {
			t.Errorf("arm %s: its table is missing after applying", arm)
		}
	}

	// Mainline is untouched: same columns, same rows.
	mainline, err := applier.Store.Shape(ctx, "users")
	if err != nil {
		t.Fatalf("read users: %v", err)
	}
	for _, col := range []string{"id", "name"} {
		if _, ok := mainline[col]; !ok {
			t.Errorf("mainline lost users.%s", col)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 3 {
		t.Errorf("mainline rows changed: want 3, got %d", count)
	}

	// Each arm writes through its own column, as it would while serving.
	for _, arm := range arms {
		col := experiment.NamespacePrefix + arm + "_choice"
		if _, err := pool.Exec(ctx,
			fmt.Sprintf(`UPDATE users SET %s = $1 WHERE name = 'ana'`, quoted(col)), "chose-"+arm); err != nil {
			t.Fatalf("arm %s write: %v", arm, err)
		}
		if _, err := pool.Exec(ctx,
			fmt.Sprintf(`INSERT INTO %s (user_id, note) SELECT id, $1 FROM users WHERE name = 'ana'`,
				quoted(experiment.NamespacePrefix+arm+"_events")), "seen-"+arm); err != nil {
			t.Fatalf("arm %s event: %v", arm, err)
		}
	}

	// Arm b loses and is withdrawn.
	archive, err := applier.Rollback(ctx, admitted["b"])
	if err != nil {
		t.Fatalf("rollback arm b: %v", err)
	}

	// Its objects are gone.
	if columnExists(t, pool, "users", experiment.NamespacePrefix+"b_choice") {
		t.Error("arm b's column survived rollback")
	}
	if tableExists(t, pool, experiment.NamespacePrefix+"b_events") {
		t.Error("arm b's table survived rollback")
	}

	// The other arms and mainline are untouched by that rollback — the
	// property that makes concurrent experiments possible at all.
	for _, arm := range []string{"a", "c"} {
		if !columnExists(t, pool, "users", experiment.NamespacePrefix+arm+"_choice") {
			t.Errorf("rolling back arm b removed arm %s's column", arm)
		}
		if !tableExists(t, pool, experiment.NamespacePrefix+arm+"_events") {
			t.Errorf("rolling back arm b removed arm %s's table", arm)
		}
		var v string
		if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM users WHERE name = 'ana'`,
			quoted(experiment.NamespacePrefix+arm+"_choice"))).Scan(&v); err != nil {
			t.Fatalf("read arm %s value: %v", arm, err)
		}
		if v != "chose-"+arm {
			t.Errorf("arm %s value changed to %q", arm, v)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 3 {
		t.Errorf("rollback disturbed mainline rows: want 3, got %d", count)
	}

	// The withdrawn arm's data was captured on the way out, not discarded.
	if archive.ArmID != "b" {
		t.Errorf("archive names the wrong arm: %q", archive.ArmID)
	}
	if rows := archive.Fields["users"]; len(rows) != 1 {
		t.Fatalf("expected one archived row for the participating user, got %d", len(rows))
	} else if rows[0][experiment.NamespacePrefix+"b_choice"] != "chose-b" {
		t.Errorf("archived the wrong value: %v", rows[0])
	}
	if rows := archive.Collections[experiment.NamespacePrefix+"b_events"]; len(rows) != 1 {
		t.Errorf("expected one archived event row, got %d", len(rows))
	}
	if _, err := archive.JSON(); err != nil {
		t.Errorf("archive should serialise: %v", err)
	}
}

// The archive is only a backup if it goes back. Exercising the round trip is
// what turns "we took a dump" into something known to work.
func TestArchiveRestoresWhatItCaptured(t *testing.T) {
	ctx := context.Background()
	pool, applier := targetDB(t)

	adm, err := applier.Admit(ctx, armMigration("solo"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := applier.Apply(ctx, adm); err != nil {
		t.Fatalf("apply: %v", err)
	}

	col := experiment.NamespacePrefix + "solo_choice"
	tbl := experiment.NamespacePrefix + "solo_events"
	if _, err := pool.Exec(ctx, fmt.Sprintf(`UPDATE users SET %s = 'kept' WHERE name = 'ben'`, quoted(col))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (user_id, note) SELECT id, 'note' FROM users WHERE name = 'ben'`,
		quoted(tbl))); err != nil {
		t.Fatalf("event: %v", err)
	}

	archive, err := applier.Rollback(ctx, adm)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Re-apply and put the data back, as someone would when reconsidering a
	// rejected Variation.
	if err := applier.Apply(ctx, adm); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if err := applier.Restore(ctx, archive); err != nil {
		t.Fatalf("restore: %v", err)
	}

	var got string
	if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM users WHERE name = 'ben'`, quoted(col))).Scan(&got); err != nil {
		t.Fatalf("read restored column: %v", err)
	}
	if got != "kept" {
		t.Errorf("restored value = %q, want %q", got, "kept")
	}

	var notes int
	if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, quoted(tbl))).Scan(&notes); err != nil {
		t.Fatalf("count restored rows: %v", err)
	}
	if notes != 1 {
		t.Errorf("restored table has %d rows, want 1", notes)
	}
}

// Mendel is not the only writer of this database. A verdict reached against
// one shape of a table must not be applied against a different one.
func TestApplyRefusesAfterSchemaDrift(t *testing.T) {
	ctx := context.Background()
	pool, applier := targetDB(t)

	adm, err := applier.Admit(ctx, armMigration("drift"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	// Someone else alters the table between admission and application.
	if _, err := pool.Exec(ctx, `ALTER TABLE users ADD COLUMN email text`); err != nil {
		t.Fatalf("external change: %v", err)
	}

	err = applier.Apply(ctx, adm)
	if err == nil {
		t.Fatal("expected the stale classification to be refused")
	}
	if !strings.Contains(err.Error(), "re-classified") || !strings.Contains(err.Error(), "email") {
		t.Errorf("the error should name what changed, got: %v", err)
	}
}

// quoted is the test's own identifier quoting; the package's is not exported,
// and these are names the test itself wrote.
func quoted(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
