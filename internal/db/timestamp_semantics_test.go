package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhs/mendelbuild/internal/testdb"
)

// Timestamps must survive a round trip through the database unchanged.
//
// They did not, before migration 035. Columns were `timestamp without time
// zone`, which stores the digits of a clock face and forgets which clock: pgx
// writes a time.Time as its *local* wall clock and reads one back labelled
// *UTC*, so a value written and read on a host seven hours off UTC came back
// seven hours wrong, with nothing to warn you. It expired sessions late and
// made a thirty-second-old draft read as hours stale.
//
// These tests run in whatever zone the machine is in. On a UTC host they cannot
// fail, which is exactly how the bug survived review and staging — so run them
// under TZ=America/Los_Angeles when touching timestamp handling.

// TestTimestampsRoundTripUnchanged is the property the whole migration exists
// for: what you store is what you read back, as an instant.
func TestTimestampsRoundTripUnchanged(t *testing.T) {
	pool, table := tsTestTable(t, "instant TIMESTAMPTZ NOT NULL")
	ctx := context.Background()

	// Sub-second precision matters: these feed duration and staleness maths.
	written := time.Now()
	if _, err := pool.Exec(ctx, "INSERT INTO "+table+" (instant) VALUES ($1)", written); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var read time.Time
	if err := pool.QueryRow(ctx, "SELECT instant FROM "+table).Scan(&read); err != nil {
		t.Fatalf("select: %v", err)
	}

	if drift := read.Sub(written); drift != 0 {
		t.Errorf("a stored instant drifted by %v on a host at %s.\n"+
			"written: %s\nread:    %s\n"+
			"This is the `timestamp without time zone` bug returning: the column "+
			"stores a wall-clock reading rather than a point in time.",
			drift, time.Now().Format("-07:00"),
			written.Format(time.RFC3339Nano), read.Format(time.RFC3339Nano))
	}
}

// TestTimestampComparisonsAreCorrectInGo: the failures this caused were all
// comparisons against time.Now() — an expiry check that let sessions live past
// their expiry, and a staleness check that declared a running draft dead.
func TestTimestampComparisonsAreCorrectInGo(t *testing.T) {
	pool, table := tsTestTable(t, "instant TIMESTAMPTZ NOT NULL")
	ctx := context.Background()

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	if _, err := pool.Exec(ctx, "INSERT INTO "+table+" (instant) VALUES ($1), ($2)", past, future); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := pool.Query(ctx, "SELECT instant FROM "+table+" ORDER BY instant")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()

	var got []time.Time
	for rows.Next() {
		var v time.Time
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}

	now := time.Now()
	if !got[0].Before(now) {
		t.Errorf("an instant an hour in the past read as %s, which is not before now (%s)",
			got[0].Format(time.RFC3339), now.Format(time.RFC3339))
	}
	if !got[1].After(now) {
		t.Errorf("an instant an hour in the future read as %s, which is not after now (%s)",
			got[1].Format(time.RFC3339), now.Format(time.RFC3339))
	}
}

// TestTargetDateKeepsItsCalendarDay is the other half of the rule, and the
// reason the conversion was not blanket.
//
// A key result's target date names a day: "100 signups by 1 November". Stored
// as an instant it becomes midnight UTC, which renders as 31 October to any
// reader west of UTC — an off-by-one on the very date the key result is about.
// DATE has no such ambiguity.
func TestTargetDateKeepsItsCalendarDay(t *testing.T) {
	pool, table := tsTestTable(t, "target_date DATE")
	ctx := context.Background()

	const want = "2026-11-01"
	parsed, err := time.Parse("2006-01-02", want) // how the handlers parse form input
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+table+" (target_date) VALUES ($1)", parsed); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var read time.Time
	if err := pool.QueryRow(ctx, "SELECT target_date FROM "+table).Scan(&read); err != nil {
		t.Fatalf("select: %v", err)
	}

	// Rendered exactly as the templates render it.
	if got := read.Format("2006-01-02"); got != want {
		t.Errorf("target date rendered as %s, want %s (host at %s).\n"+
			"A date stored as an instant shifts by the reader's UTC offset; "+
			"this column must stay DATE.",
			got, want, time.Now().Format("-07:00"))
	}
}

// TestSchemaHasNoNaiveTimestamps guards the rule itself. A new column declared
// TIMESTAMP would reintroduce the bug silently, and only on non-UTC hosts.
func TestSchemaHasNoNaiveTimestamps(t *testing.T) {
	db, _ := testDB(t)

	rows, err := db.Pool.Query(context.Background(), `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND data_type = 'timestamp without time zone'
		ORDER BY table_name, column_name
	`)
	if err != nil {
		t.Fatalf("query columns: %v", err)
	}
	defer rows.Close()

	var offenders []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("scan: %v", err)
		}
		offenders = append(offenders, table+"."+column)
	}

	if len(offenders) > 0 {
		t.Errorf("these columns are `timestamp without time zone`: %v\n"+
			"Store instants as TIMESTAMPTZ, and calendar dates as DATE. A naive "+
			"timestamp records a clock reading without saying whose clock, and "+
			"reads back wrong by the host's UTC offset. See migration 035.",
			offenders)
	}
}

// tsTestTable makes a throwaway table in its own schema, so these can run
// alongside the rest of the suite and against a shared database.
func tsTestTable(t *testing.T, columns string) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testdb.ConnString())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	table := "ts_probe_" + uuid.New().String()[:8]
	if _, err := pool.Exec(ctx, "CREATE TABLE "+table+" ("+columns+")"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(context.Background(), testdb.ConnString())
		if err != nil {
			return
		}
		defer cleanup.Close()
		cleanup.Exec(context.Background(), "DROP TABLE IF EXISTS "+table)
	})
	return pool, table
}
