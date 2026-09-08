package experiment_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhs/mendelbuild/internal/experiment"
	"github.com/bhs/mendelbuild/internal/experiment/pgstore"
	"github.com/bhs/mendelbuild/internal/testdb"
)

// scratchDB is a non-production copy: a schema of its own, holding what
// production holds. Mendel may change it and reset it, which is the whole point.
func scratchDB(t *testing.T, mirroring string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	conn := testdb.Require(t)

	schema := "scratch_" + uuid.New().String()[:8]
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

	cfg, _ := pgxpool.ParseConfig(conn)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to scratch: %v", err)
	}
	t.Cleanup(pool.Close)

	// The same structure as production, with none of its data. Structure is all
	// verification reads.
	if _, err := pool.Exec(ctx, mirroring); err != nil {
		t.Fatalf("build scratch schema: %v", err)
	}
	return pool
}

const usersTable = `
	CREATE TABLE users (
		id   uuid PRIMARY KEY,
		name text NOT NULL
	);`

// The arrangement itself: the migration is run somewhere it does not matter, and
// production is only read.
//
// Verification against production is not free even rolled back -- ADD COLUMN
// holds an exclusive lock until the rollback, and a concurrent index build
// cannot be verified as written at all -- so the judgment is made on a copy and
// checked against production rather than performed on it.
func TestVerificationHappensOnTheCopyAndProductionIsOnlyRead(t *testing.T) {
	ctx := context.Background()
	prod, applier := targetDB(t)
	applier.Verify = pgstore.NewScratch(scratchDB(t, usersTable))

	m := armMigration("a")
	adm, err := applier.Admit(ctx, m)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	// Production is untouched: the column exists only where it was verified.
	var onProd int
	if err := prod.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'users' AND column_name LIKE 'mendel_exp_%'
	`).Scan(&onProd); err != nil {
		t.Fatalf("inspect production: %v", err)
	}
	if onProd != 0 {
		t.Errorf("verification left %d experiment columns on production", onProd)
	}

	// And the recorded shapes describe production, since that is what Apply
	// re-reads and refuses on.
	if _, ok := adm.Shapes["users"]["mendel_exp_a_choice"]; ok {
		t.Error("recorded shape includes a column production does not have")
	}

	if err := applier.Apply(ctx, adm); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := prod.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'users' AND column_name = 'mendel_exp_a_choice'
	`).Scan(&onProd); err != nil {
		t.Fatalf("inspect production: %v", err)
	}
	if onProd != 1 {
		t.Error("Apply did not reach production")
	}
}

// A copy that has drifted gives a confident answer about the wrong schema, which
// is worse than no answer at all -- so the difference is named and the migration
// declined.
func TestDriftedCopyIsRefusedRatherThanTrusted(t *testing.T) {
	ctx := context.Background()
	_, applier := targetDB(t)

	// Production's users table has grown a column the copy does not have.
	applier.Verify = pgstore.NewScratch(scratchDB(t, `
		CREATE TABLE users (
			id uuid PRIMARY KEY
		);`))

	_, err := applier.Admit(ctx, armMigration("a"))
	if err == nil {
		t.Fatal("a migration was admitted on evidence from a schema that is not production's")
	}
	if !strings.Contains(err.Error(), "does not match production") {
		t.Errorf("the refusal should say the copy is the problem: %v", err)
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("the refusal should name the column that differs: %v", err)
	}
}

// CREATE INDEX CONCURRENTLY is written precisely to avoid locking a live table.
// It cannot run inside a transaction, so the speculative path has to rewrite it
// into the locking form -- meaning what gets verified is not what will run. On a
// copy there is no transaction and no rewrite.
func TestConcurrentIndexIsVerifiedAsWritten(t *testing.T) {
	ctx := context.Background()
	_, applier := targetDB(t)
	applier.Verify = pgstore.NewScratch(scratchDB(t, usersTable))

	idx := experiment.NamespacePrefix + "a_by_name"
	adm, err := applier.Admit(ctx, experiment.Migration{
		ArmID: "a",
		Up:    "CREATE INDEX CONCURRENTLY " + idx + " ON users (name);",
		Down:  "DROP INDEX IF EXISTS " + idx + ";",
	})
	if err != nil {
		t.Fatalf("a concurrent index build should be admissible: %v", err)
	}
	if len(adm.Delta.Added) != 1 || adm.Delta.Added[0].Name != idx {
		t.Errorf("delta did not record the index: %+v", adm.Delta.Added)
	}
}

// A datastore whose DDL commits immediately cannot be probed in place -- the
// probe would be the change. On a copy that is about to be discarded, it can.
// This is what admits MySQL and anything else without transactional DDL.
func TestDatastoreWithoutTransactionalDDLIsUsableAsACopy(t *testing.T) {
	live := fakeStore{kind: "mysql", caps: experiment.Capabilities{StructuralDiff: true}}
	inPlace := fakeStore{kind: "mysql", caps: experiment.Capabilities{StructuralDiff: true}}

	if err := experiment.RequireForExperiments(inPlace, live); err == nil {
		t.Error("a datastore that cannot undo a probe was accepted as its own verification target")
	}

	copy := fakeStore{kind: "mysql", caps: experiment.Capabilities{StructuralDiff: true, Disposable: true}}
	if err := experiment.RequireForExperiments(copy, live); err != nil {
		t.Errorf("a disposable copy needs no transactional DDL: %v", err)
	}

	// And the live datastore must not be the throwaway one, or the migration is
	// verified and applied against the same copy and never reaches production.
	if err := experiment.RequireForExperiments(copy, copy); err == nil {
		t.Error("a disposable datastore was accepted as the live one")
	}
}

// fakeStore reports capabilities and nothing else; only RequireForExperiments
// reads it, and only for its answers to those two questions.
type fakeStore struct {
	kind string
	caps experiment.Capabilities
}

func (f fakeStore) Kind() string                          { return f.kind }
func (f fakeStore) Capabilities() experiment.Capabilities { return f.caps }
func (f fakeStore) Forbidden(string) []string             { return nil }
func (f fakeStore) Split(string) []string                 { return nil }
func (f fakeStore) VerifySpeculatively(context.Context, string) (experiment.Delta, error) {
	return experiment.Delta{}, nil
}
func (f fakeStore) Exec(context.Context, string) error { return nil }
func (f fakeStore) Shape(context.Context, string) (experiment.TableSchema, error) {
	return nil, nil
}
func (f fakeStore) Identity(context.Context, string) ([]string, error) { return nil, nil }
func (f fakeStore) Dump(context.Context, experiment.DumpQuery) ([]map[string]any, error) {
	return nil, nil
}
func (f fakeStore) Load(context.Context, string, []string, []map[string]any) error { return nil }

// --- What reaches production is compared against what was admitted ---

// noLock stands in for the Mendel-side lock, which orders Mendel against itself
// and has nothing to do with what these check.
type noLock struct{}

func (noLock) Acquire(context.Context, string) (func(), error) { return func() {}, nil }

// A migration is verified against a copy and then handed to the adapter against
// production. Until Apply checked, the second half rested entirely on the
// adapter doing the same thing twice -- which is the one part of this machinery
// Mendel may not have written.
type lyingStore struct {
	experiment.Datastore

	// applied is what the store pretends the collection looks like after the
	// migration, regardless of what the migration said.
	applied experiment.TableSchema
	before  experiment.TableSchema
	execs   int
}

func (l *lyingStore) Kind() string { return "lying" }
func (l *lyingStore) Capabilities() experiment.Capabilities {
	return experiment.Capabilities{StructuralDiff: true, SpeculativeApply: true}
}
func (l *lyingStore) Split(change string) []string { return []string{change} }
func (l *lyingStore) Exec(context.Context, string) error {
	l.execs++
	return nil
}
func (l *lyingStore) Shape(_ context.Context, _ string) (experiment.TableSchema, error) {
	if l.execs == 0 {
		return l.before, nil
	}
	return l.applied, nil
}
func (l *lyingStore) Identity(context.Context, string) ([]string, error) {
	return []string{"id"}, nil
}

func admissionFor(added []experiment.Object, before experiment.TableSchema) *experiment.Admission {
	return &experiment.Admission{
		Migration: experiment.Migration{Up: "ALTER TABLE orders ADD COLUMN mendel_exp_x INT", Down: "x"},
		Delta:     experiment.Delta{Added: added},
		Shapes:    map[string]experiment.TableSchema{"orders": before},
	}
}

// An adapter that does something other than the admitted migration is caught,
// and what it did is undone.
func TestApplyRefusesWhatWasNotAdmitted(t *testing.T) {
	before := experiment.TableSchema{"id": "integer", "total": "integer"}
	store := &lyingStore{
		before: before,
		// The admitted column, and one nobody asked for.
		applied: experiment.TableSchema{
			"id": "integer", "total": "integer",
			"mendel_exp_x": "integer", "surprise": "text",
		},
	}
	a := &experiment.Applier{Store: store, Lock: noLock{}}

	err := a.Apply(t.Context(), admissionFor(
		[]experiment.Object{{Kind: experiment.ObjectField, Collection: "orders", Name: "mendel_exp_x"}},
		before,
	))
	if err == nil {
		t.Fatal("applying something other than what was admitted must be refused")
	}
	if !strings.Contains(err.Error(), "not do what was admitted") {
		t.Errorf("the error should say the migration did not do what was admitted, got %v", err)
	}
}

// And one that silently does less is caught too: an experiment would otherwise
// run against a schema it does not have.
func TestApplyRefusesAMigrationThatDidNothing(t *testing.T) {
	before := experiment.TableSchema{"id": "integer", "total": "integer"}
	store := &lyingStore{before: before, applied: before}
	a := &experiment.Applier{Store: store, Lock: noLock{}}

	err := a.Apply(t.Context(), admissionFor(
		[]experiment.Object{{Kind: experiment.ObjectField, Collection: "orders", Name: "mendel_exp_x"}},
		before,
	))
	if err == nil {
		t.Fatal("a migration that added nothing it was admitted to add must be refused")
	}
	if !strings.Contains(err.Error(), "did not add") {
		t.Errorf("the error should name what is missing, got %v", err)
	}
}

// The honest case still works, or the check is worse than no check.
func TestApplyAcceptsWhatWasAdmitted(t *testing.T) {
	before := experiment.TableSchema{"id": "integer", "total": "integer"}
	store := &lyingStore{
		before:  before,
		applied: experiment.TableSchema{"id": "integer", "total": "integer", "mendel_exp_x": "integer"},
	}
	a := &experiment.Applier{Store: store, Lock: noLock{}}

	if err := a.Apply(t.Context(), admissionFor(
		[]experiment.Object{{Kind: experiment.ObjectField, Collection: "orders", Name: "mendel_exp_x"}},
		before,
	)); err != nil {
		t.Fatalf("a migration that did exactly what it was admitted to do was refused: %v", err)
	}
}
