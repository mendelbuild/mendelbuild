package experiment_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhs/mendelbuild/internal/codegen"
	"github.com/bhs/mendelbuild/internal/experiment"
	"github.com/bhs/mendelbuild/internal/experiment/pgstore"
	"github.com/bhs/mendelbuild/internal/testdb"
	"github.com/bhs/mendelbuild/internal/testrepo"
)

// Admission, end to end, against a repository rather than against strings.
//
// This is what the fixtures are for. Every other test of this machinery supplies
// its own migration and its own schema, which checks the machinery against what
// the test's author expected a project to look like. Here the migration comes
// out of a repository's `.mendel/experiment.json` and the schema out of that
// repository's `schema.sql`, so the two have to agree with each other and with
// the code — and nothing here depends on a real project that someone might
// redeploy underneath it.

// fixtureStores gives a fixture's schema two ways: the live datastore admission
// reads, and a disposable copy it verifies against.
//
// Two schemas rather than two databases, which is the same isolation for this
// purpose and needs no privilege to create. What matters is that they start
// identical, since admission declines when the copy has drifted from what it is
// vouching for.
func fixtureStores(t *testing.T, name string) (live, verify experiment.Datastore) {
	t.Helper()

	schema := testrepo.Schema(t, name)
	if strings.TrimSpace(schema) == "" {
		t.Fatalf("%s has no schema; admission has nothing to be about", name)
	}

	url := testdb.Require(t)
	open := func(role string) experiment.Datastore {
		space := "fx_" + role + "_" + strings.ReplaceAll(uuid.New().String()[:8], "-", "")

		admin, err := pgxpool.New(t.Context(), url)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer admin.Close()
		if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+space); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		t.Cleanup(func() {
			cleanup, err := pgxpool.New(t.Context(), url)
			if err != nil {
				return
			}
			defer cleanup.Close()
			cleanup.Exec(t.Context(), "DROP SCHEMA IF EXISTS "+space+" CASCADE")
		})

		cfg, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = space
		pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
		if err != nil {
			t.Fatalf("connect to %s: %v", space, err)
		}
		t.Cleanup(pool.Close)

		if _, err := pool.Exec(t.Context(), schema); err != nil {
			t.Fatalf("apply %s's schema: %v", name, err)
		}
		if role == "verify" {
			return pgstore.NewScratch(pool)
		}
		return pgstore.New(pool)
	}

	return open("live"), open("verify")
}

// The whole path: a repository declares an experiment, and the migration it
// declared is admitted against the schema that repository actually has.
func TestAFixtureRepositorysDeclaredMigrationIsAdmitted(t *testing.T) {
	decl, err := codegen.ReadDeclaredExperiment(testrepo.Path(t, testrepo.Ledger))
	if err != nil {
		t.Fatalf("the ledger declares an experiment and it should parse: %v", err)
	}
	if decl.Migration == nil {
		t.Fatal("the ledger's declaration has lost its migration")
	}

	live, verify := fixtureStores(t, testrepo.Ledger)
	applier := &experiment.Applier{Store: live, Verify: verify, Lock: noLock{}}

	admission, err := applier.Admit(t.Context(), experiment.Migration{
		Up:   decl.Migration.Up,
		Down: decl.Migration.Down,
	})
	if err != nil {
		t.Fatalf("the ledger's own migration was refused against the ledger's own schema: %v", err)
	}

	if !admission.Delta.PurelyAdditive() {
		t.Errorf("the declared migration is additive and was read as %s", admission.Delta.Describe())
	}
	named := false
	for _, o := range admission.Delta.Added {
		if o.Collection == "entries" && strings.HasPrefix(o.Name, "mendel_exp_") {
			named = true
		}
	}
	if !named {
		t.Errorf("admission did not name what the declaration adds to entries: %v", admission.Delta.Added)
	}
	if _, ok := admission.Shapes["entries"]; !ok {
		t.Error("admission recorded no shape for the collection it touched, so drift could not be checked later")
	}
}

// And the refusal that keeps an experiment from writing where it could never be
// archived from, against a table a real repository would actually have.
func TestAMigrationTouchingAnUnarchivableTableIsRefused(t *testing.T) {
	live, verify := fixtureStores(t, testrepo.Ledger)
	applier := &experiment.Applier{Store: live, Verify: verify, Lock: noLock{}}

	_, err := applier.Admit(t.Context(), experiment.Migration{
		Up:   "ALTER TABLE audit_log ADD COLUMN mendel_exp_reviewed BOOLEAN;",
		Down: "ALTER TABLE audit_log DROP COLUMN mendel_exp_reviewed;",
	})
	if err == nil {
		t.Fatal("audit_log has no primary key, so anything written to it could be captured and never put back")
	}
	if !strings.Contains(err.Error(), "primary key") {
		t.Errorf("the refusal should name what is missing, got %v", err)
	}
}
