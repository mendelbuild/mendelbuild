package experiment_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/experiment"
)

// Why the affirmative judgment is empirical rather than textual: this migration
// only creates a table, and both the deny-list and any reading of the SQL pass
// it — but applying it changes how mainline's own DELETEs behave. Reading SQL
// cannot see that. Reading the catalogue can.
func TestVerifySeesWhatTextCannot(t *testing.T) {
	ctx := context.Background()
	pool, applier := targetDB(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE orders (id serial PRIMARY KEY, user_id uuid REFERENCES users(id))`); err != nil {
		t.Fatalf("seed orders: %v", err)
	}

	up := `CREATE TABLE mendel_exp_receipts (
		id      serial PRIMARY KEY,
		user_id uuid REFERENCES users(id) ON DELETE CASCADE
	)`

	if reasons := applier.Store.Forbidden(up); len(reasons) != 0 {
		t.Fatalf("the deny-list should not object to a CREATE TABLE: %v", reasons)
	}

	delta, err := applier.Store.VerifySpeculatively(ctx, up)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	var sawConstraint bool
	for _, o := range delta.Added {
		if o.Kind == experiment.ObjectConstraint && o.Collection == "mendel_exp_receipts" {
			sawConstraint = true
		}
	}
	if !sawConstraint {
		t.Errorf("the cascading foreign key should appear in the delta, got %v", delta.Added)
	}
}

// Verification runs the migration to find out what it does, and undoes
// whatever that was.
func TestVerifyLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	pool, applier := targetDB(t)

	if _, err := applier.Store.VerifySpeculatively(ctx, armMigration("probe").Up); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if columnExists(t, pool, "users", experiment.NamespacePrefix+"probe_choice") {
		t.Error("verification committed its column")
	}
	if tableExists(t, pool, experiment.NamespacePrefix+"probe_events") {
		t.Error("verification committed its table")
	}
}

// The layer that has to hold when a statement slips past the deny-list: a
// change is judged by what it did, not by what it looked like.
func TestVerifyRefusesRemovalAndChange(t *testing.T) {
	ctx := context.Background()
	pool, applier := targetDB(t)

	if _, err := pool.Exec(ctx, `ALTER TABLE users ADD COLUMN nickname text`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("removal", func(t *testing.T) {
		// Leading whitespace so the deny-list's ^DROP anchor does not match —
		// exactly the kind of gap the empirical layer exists to close.
		delta, err := applier.Store.VerifySpeculatively(ctx, "\n\t\tALTER TABLE users DROP nickname")
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if delta.PurelyAdditive() {
			t.Error("dropping a column is not additive")
		}
		if !strings.Contains(delta.Describe(), "nickname") {
			t.Errorf("the description should name the column, got %q", delta.Describe())
		}
	})

	t.Run("type change", func(t *testing.T) {
		delta, err := applier.Store.VerifySpeculatively(ctx, `ALTER TABLE users ALTER COLUMN nickname TYPE varchar(10)`)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if delta.PurelyAdditive() {
			t.Error("retyping a column is not additive")
		}
		if !strings.Contains(delta.Describe(), "changes") {
			t.Errorf("the description should say what changed, got %q", delta.Describe())
		}
	})
}

// CREATE INDEX CONCURRENTLY cannot run inside a transaction, so the adapter
// strips it for verification. The same index results, which is what makes the
// substitution honest.
func TestVerifyHandlesConcurrentIndexes(t *testing.T) {
	ctx := context.Background()
	_, applier := targetDB(t)

	delta, err := applier.Store.VerifySpeculatively(ctx,
		`CREATE INDEX CONCURRENTLY mendel_exp_users_name ON users (name)`)
	if err != nil {
		t.Fatalf("a concurrent index build should verify: %v", err)
	}
	if !delta.PurelyAdditive() || len(delta.Added) != 1 {
		t.Fatalf("expected exactly one addition, got added=%v removed=%v", delta.Added, delta.Removed)
	}
	if delta.Added[0].Name != "mendel_exp_users_name" {
		t.Errorf("the index should be the addition, got %v", delta.Added)
	}
}

// A migration that does not apply is reported here rather than at deploy time,
// when it would take a live table with it.
func TestVerifyReportsMigrationsThatDoNotApply(t *testing.T) {
	ctx := context.Background()
	_, applier := targetDB(t)

	_, err := applier.Store.VerifySpeculatively(ctx, `ALTER TABLE users ADD COLUMN mendel_exp_x nonexistent_type`)
	if err == nil {
		t.Fatal("expected the broken migration to be reported")
	}
	if !strings.Contains(err.Error(), "does not apply") {
		t.Errorf("error should say the migration does not apply, got: %v", err)
	}
}

// Admission runs the empirical check, so a change that is textually innocuous
// and behaviourally destructive is refused there too.
func TestAdmitRefusesOnEmpiricalEvidence(t *testing.T) {
	ctx := context.Background()
	_, applier := targetDB(t)

	_, err := applier.Admit(ctx, experiment.Migration{
		ArmID: "sneaky",
		Up:    "\n\t\tALTER TABLE users DROP name",
		Down:  `ALTER TABLE users ADD COLUMN name text`,
	})
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "not additive") {
		t.Errorf("error should say it is not additive, got: %v", err)
	}
}

// A migration that changes nothing has nothing to withdraw, and an experiment
// built on it would be measuring the control against itself.
func TestAdmitRefusesEmptyMigrations(t *testing.T) {
	ctx := context.Background()
	_, applier := targetDB(t)

	_, err := applier.Admit(ctx, experiment.Migration{ArmID: "noop", Up: `SELECT 1`, Down: `SELECT 1`})
	if err == nil || !strings.Contains(err.Error(), "changes nothing") {
		t.Errorf("expected a refusal naming the empty change, got: %v", err)
	}
}

// Checked at admission, when refusing is cheap, rather than at rollback, when
// the data is already on its way out.
func TestAdmitRefusesCollectionWithoutIdentity(t *testing.T) {
	ctx := context.Background()
	pool, applier := targetDB(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE events (kind text, at timestamptz)`); err != nil {
		t.Fatalf("seed keyless table: %v", err)
	}

	_, err := applier.Admit(ctx, experiment.Migration{
		ArmID: "keyless",
		Up:    `ALTER TABLE events ADD COLUMN mendel_exp_tag text`,
		Down:  `ALTER TABLE events DROP COLUMN mendel_exp_tag`,
	})
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "no primary key") || !strings.Contains(err.Error(), "restorable") {
		t.Errorf("the reason should explain that the data could not be put back, got: %v", err)
	}
}

// An object outside the experiment namespace is caught from the catalogue
// rather than from parsing, so it is caught however the migration was phrased.
func TestAdmitRefusesUnnamespacedObjects(t *testing.T) {
	ctx := context.Background()
	_, applier := targetDB(t)

	_, err := applier.Admit(ctx, experiment.Migration{
		ArmID: "bare",
		Up:    `ALTER TABLE users ADD COLUMN preferred_theme text`,
		Down:  `ALTER TABLE users DROP COLUMN preferred_theme`,
	})
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), experiment.NamespacePrefix) {
		t.Errorf("the reason should name the required prefix, got: %v", err)
	}
}

// unsupportedStore stands for a datastore that cannot verify speculatively —
// MySQL's position, since it commits DDL immediately.
type unsupportedStore struct{ experiment.Datastore }

func (unsupportedStore) Kind() string { return "mysql" }
func (unsupportedStore) Capabilities() experiment.Capabilities {
	return experiment.Capabilities{SpeculativeApply: false, StructuralDiff: true}
}

// Mendel runs on Postgres; its users need not. A datastore that cannot support
// an experiment safely is declined by name, rather than having something
// Postgres-shaped done to it.
func TestUnsupportedDatastoreIsDeclinedByName(t *testing.T) {
	applier := &experiment.Applier{Store: unsupportedStore{}}

	_, err := applier.Admit(context.Background(), experiment.Migration{
		ArmID: "x", Up: `ALTER TABLE users ADD COLUMN mendel_exp_a text`, Down: `x`})
	if err == nil {
		t.Fatal("expected the unsupported datastore to be declined")
	}
	if !strings.Contains(err.Error(), "mysql") {
		t.Errorf("the refusal should name the datastore, got: %v", err)
	}
	if !strings.Contains(err.Error(), "undo it without a trace") {
		t.Errorf("the refusal should explain what is missing, got: %v", err)
	}
}
