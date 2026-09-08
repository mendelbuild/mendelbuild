package pgstore_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhs/mendelbuild/internal/experiment"
	"github.com/bhs/mendelbuild/internal/experiment/conformance"
	"github.com/bhs/mendelbuild/internal/experiment/pgstore"
	"github.com/bhs/mendelbuild/internal/testdb"
)

// pgstore is one implementation of experiment.Datastore, and this is where it
// proves it. Everything about what the interface *means* lives in the
// conformance package; what is here is Postgres's dialect and nothing else.
//
// The division is the point. A second adapter is written against the suite
// rather than against this file, so it inherits the contract without inheriting
// Postgres's assumptions -- which is what reading pgstore and copying it would
// have done.
func TestPostgresConforms(t *testing.T) {
	url := testdb.Require(t)

	conformance.Run(t, conformance.Fixture{
		NewStore: func(t *testing.T) experiment.Datastore {
			return pgstore.NewScratch(newScratchSchema(t, url))
		},
		// The same data through a store that is not disposable, which is where
		// "learning what a change does must not change anything" is actually
		// checkable.
		NewLiveStore: func(t *testing.T) experiment.Datastore {
			return pgstore.New(newScratchSchema(t, url))
		},
		Collection: "conformance_orders",
		// Nullable and with no default, which is the additive change that takes
		// no rewrite and no long lock -- the shape an experiment is allowed to
		// make.
		AdditiveChange: "ALTER TABLE conformance_orders ADD COLUMN mendel_exp_score INT;",
		AddedField:     "mendel_exp_score",
		DestructiveChanges: []string{
			"DROP TABLE conformance_orders",
			"ALTER TABLE conformance_orders DROP COLUMN total",
			"ALTER TABLE conformance_orders ALTER COLUMN total TYPE bigint",
			"DELETE FROM conformance_orders",
			"TRUNCATE conformance_orders",
		},
		TwoStatements: "ALTER TABLE conformance_orders ADD COLUMN mendel_exp_a INT; " +
			"ALTER TABLE conformance_orders ADD COLUMN mendel_exp_b INT;",
		CollectionWithoutIdentity: "conformance_events",

		CompositeIdentityCollection: "conformance_memberships",
		CompositeIdentityFields:     []string{"org_id", "user_id"},

		// Relaxing NOT NULL modifies a column mainline shares. The deny-list
		// catches SET NOT NULL and not this, which is what makes it the right
		// probe: nothing but VerifySpeculatively stands between it and being
		// admitted as additive.
		NonAdditiveChange: "ALTER TABLE conformance_orders ALTER COLUMN total DROP NOT NULL;",

		CreateCollectionChange: "CREATE TABLE mendel_exp_reviews (id SERIAL PRIMARY KEY, note TEXT);",
		CreatedCollection:      "mendel_exp_reviews",

		AddIndexChange: "CREATE INDEX mendel_exp_orders_total ON conformance_orders (total);",
		AddedIndex:     "mendel_exp_orders_total",

		// The first statement is valid and the second names a column that does
		// not exist, so a partial apply would leave the first behind.
		PartiallyFailingChange: "ALTER TABLE conformance_orders ADD COLUMN mendel_exp_first INT; " +
			"ALTER TABLE conformance_orders ADD COLUMN mendel_exp_second INT REFERENCES nothing_at_all(id);",
		PartiallyFailingField: "mendel_exp_first",
	})
}

// newScratchSchema gives each run its own Postgres schema, so runs from separate
// worktrees on the shared test database cannot collide -- the same arrangement
// go test ./schema/... already relies on.
func newScratchSchema(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(t.Context(), url)
	if err != nil {
		t.Fatalf("connecting to the test database: %v", err)
	}
	// A failure rather than a skip, which is what this used to do and was the
	// odd one out. A conformance suite that quietly does not run is an adapter
	// nobody is checking, reported as a pass. -short is where opting out lives.
	if err := pool.Ping(t.Context()); err != nil {
		pool.Close()
		t.Fatalf("no test database at %s: %v", url, err)
	}

	schema := fmt.Sprintf("mendel_conf_%d", os.Getpid())
	exec := func(sql string) {
		if _, err := pool.Exec(t.Context(), sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	exec(`CREATE SCHEMA ` + schema)
	exec(`SET search_path TO ` + schema)

	// A collection with an identity, and one without. The second is what proves
	// Identity reports absence rather than inventing a key: admission refuses an
	// experiment that writes somewhere it could not archive from.
	exec(`CREATE TABLE ` + schema + `.conformance_orders (id SERIAL PRIMARY KEY, total INT NOT NULL)`)
	exec(`INSERT INTO ` + schema + `.conformance_orders (total) VALUES (1), (2)`)
	exec(`CREATE TABLE ` + schema + `.conformance_events (at TIMESTAMPTZ, note TEXT)`)

	// A composite key, because reporting one field of two is worse than
	// reporting none: admission would accept, and the archive would restore
	// rows to the wrong place.
	exec(`CREATE TABLE ` + schema + `.conformance_memberships (
		org_id INT, user_id INT, role TEXT, PRIMARY KEY (org_id, user_id))`)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		pool.Close()
	})

	// Every connection in the pool has to see the scratch schema, not just the
	// one that created it.
	scoped, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parsing %s: %v", url, err)
	}
	scoped.ConnConfig.RuntimeParams["search_path"] = schema
	scopedPool, err := pgxpool.NewWithConfig(t.Context(), scoped)
	if err != nil {
		t.Fatalf("opening a pool scoped to %s: %v", schema, err)
	}
	t.Cleanup(scopedPool.Close)

	return scopedPool
}
