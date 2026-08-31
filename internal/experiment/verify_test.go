package experiment

import (
	"context"
	"strings"
	"testing"
)

// The reason the affirmative judgment is empirical rather than textual: this
// migration only adds a table, and the deny-list and the patterns both pass it,
// but applying it changes how mainline's own DELETEs behave. Reading SQL cannot
// see that. Reading the catalogue can.
func TestVerifyCatchesBehaviourTextLooksAdditive(t *testing.T) {
	ctx := context.Background()
	pool, applier := targetDB(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE orders (id serial PRIMARY KEY, user_id uuid REFERENCES users(id))`); err != nil {
		t.Fatalf("seed orders: %v", err)
	}

	m := Migration{
		ArmID: "cascade",
		Up: `CREATE TABLE mendel_exp_receipts (
			id      serial PRIMARY KEY,
			user_id uuid REFERENCES users(id) ON DELETE CASCADE
		)`,
		Down: `DROP TABLE mendel_exp_receipts`,
	}

	// Nothing textual objects: it creates one namespaced table.
	if reasons := Forbidden(m.Up); len(reasons) != 0 {
		t.Fatalf("deny-list should not object to a CREATE TABLE: %v", reasons)
	}
	v := Classify(m.Up)
	if !v.Additive {
		t.Fatalf("patterns should not object either: %v", v.Reasons)
	}

	// And the catalogue records the new constraint, cascading deletes from a
	// table mainline owns. It is additive in the catalogue's terms, which is
	// the honest answer — so what this test pins is that the constraint is
	// *seen*, and therefore available to the reviewing agent and the user.
	delta, err := applier.VerifyAdditive(ctx, m)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	var sawCascade bool
	for _, added := range delta.Added {
		if strings.Contains(added, "constraint") && strings.Contains(added, "mendel_exp_receipts") {
			sawCascade = true
		}
	}
	if !sawCascade {
		t.Errorf("the new foreign key should appear in the delta, got %v", delta.Added)
	}
}

// Verification must leave nothing behind: it runs the migration to find out
// what it does, and rolls back whatever that was.
func TestVerifyLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	pool, applier := targetDB(t)

	m := armMigration("probe")
	if _, err := applier.VerifyAdditive(ctx, m); err != nil {
		t.Fatalf("verify: %v", err)
	}

	if columnExists(t, pool, "users", NamespacePrefix+"probe_choice") {
		t.Error("verification committed its column")
	}
	if tableExists(t, pool, NamespacePrefix+"probe_events") {
		t.Error("verification committed its table")
	}
}

// A migration that removes something must be caught by what it did, not by
// what it looked like — this is the layer that has to hold when a statement
// slips past the deny-list.
func TestVerifyRefusesRemovalAndChange(t *testing.T) {
	ctx := context.Background()
	pool, applier := targetDB(t)

	if _, err := pool.Exec(ctx, `ALTER TABLE users ADD COLUMN nickname text`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("removal", func(t *testing.T) {
		// Phrased so the deny-list's ^DROP anchor does not match, which is
		// exactly the kind of gap the empirical layer exists to close.
		delta, err := applier.VerifyAdditive(ctx, Migration{
			Up: "\n\t\t\tALTER TABLE users DROP nickname"})
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
		delta, err := applier.VerifyAdditive(ctx, Migration{
			Up: `ALTER TABLE users ALTER COLUMN nickname TYPE varchar(10)`})
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

// CREATE INDEX CONCURRENTLY cannot run inside a transaction, so verification
// strips it. The index that results is the same one, which is what makes that
// substitution honest.
func TestVerifyHandlesConcurrentIndexes(t *testing.T) {
	ctx := context.Background()
	_, applier := targetDB(t)

	delta, err := applier.VerifyAdditive(ctx, Migration{
		Up: `CREATE INDEX CONCURRENTLY mendel_exp_users_name ON users (name)`})
	if err != nil {
		t.Fatalf("a concurrent index build should verify: %v", err)
	}
	if !delta.PurelyAdditive() || len(delta.Added) != 1 {
		t.Fatalf("expected exactly one addition, got added=%v removed=%v", delta.Added, delta.Removed)
	}
	if !strings.Contains(delta.Added[0], "mendel_exp_users_name") {
		t.Errorf("the index should be the addition, got %v", delta.Added)
	}
}

// A migration that does not apply is refused here rather than at deploy time,
// when it would take a live table with it.
func TestVerifyReportsMigrationsThatDoNotApply(t *testing.T) {
	ctx := context.Background()
	_, applier := targetDB(t)

	_, err := applier.VerifyAdditive(ctx, Migration{
		Up: `ALTER TABLE users ADD COLUMN mendel_exp_x nonexistent_type`})
	if err == nil {
		t.Fatal("expected the broken migration to be reported")
	}
	if !strings.Contains(err.Error(), "does not apply") {
		t.Errorf("error should say the migration does not apply, got: %v", err)
	}
}

// Admission runs the empirical check, so a migration that is textually
// innocuous and behaviourally destructive is refused there too.
func TestAdmitRefusesOnEmpiricalEvidence(t *testing.T) {
	ctx := context.Background()
	_, applier := targetDB(t)

	_, err := applier.Admit(ctx, Migration{
		ArmID: "sneaky",
		Up:    "\n\t\t\tALTER TABLE users DROP name",
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

	_, err := applier.Admit(ctx, Migration{
		ArmID: "noop",
		Up:    `SELECT 1`,
		Down:  `SELECT 1`,
	})
	if err == nil || !strings.Contains(err.Error(), "changes nothing") {
		t.Errorf("expected a refusal naming the empty change, got: %v", err)
	}
}

// The primary key requirement is checked at admission, when refusing is cheap,
// rather than at rollback, when the data is already on its way out.
func TestAdmitRefusesTableWithoutPrimaryKey(t *testing.T) {
	ctx := context.Background()
	pool, applier := targetDB(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE events (kind text, at timestamptz)`); err != nil {
		t.Fatalf("seed keyless table: %v", err)
	}

	_, err := applier.Admit(ctx, Migration{
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
