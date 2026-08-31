package experiment

import (
	"strings"
	"testing"
)

// The classifier is default-deny, and these are the cases where saying yes
// would corrupt production data that no rollback recovers.
func TestClassifyRefusesWhatCannotBeUndone(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		why  string // a fragment the reason must contain
	}{
		{"drop table", `DROP TABLE orders`, "drops an object"},
		{"drop column", `ALTER TABLE orders DROP COLUMN status`, "drops"},
		{"rename", `ALTER TABLE orders RENAME COLUMN status TO state`, "renames"},
		{"type change", `ALTER TABLE orders ALTER COLUMN total TYPE bigint`, "type"},
		{"set not null", `ALTER TABLE orders ALTER COLUMN status SET NOT NULL`, "constrains"},
		{"update rows", `UPDATE orders SET status = 'new' WHERE status IS NULL`, "rewrites existing rows"},
		{"delete rows", `DELETE FROM orders WHERE created_at < now()`, "removes existing rows"},
		{"insert rows", `INSERT INTO orders (id) VALUES (1)`, "seeds rows"},
		{"truncate", `TRUNCATE orders`, "empties"},
		{"grant", `GRANT SELECT ON orders TO someone`, "privileges"},
		{"not null without default", `ALTER TABLE orders ADD COLUMN mendel_exp_x text NOT NULL`, "cannot apply to rows that already exist"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Classify(tc.sql)
			if v.Additive {
				t.Fatalf("expected refusal, got additive; adds=%v", v.Adds)
			}
			joined := strings.Join(v.Reasons, " | ")
			if !strings.Contains(joined, tc.why) {
				t.Errorf("reason should explain %q, got %q", tc.why, joined)
			}
		})
	}
}

func TestClassifyAcceptsAdditive(t *testing.T) {
	sql := `
		-- A variation that needs somewhere to record a preference.
		ALTER TABLE users ADD COLUMN mendel_exp_theme text;
		CREATE TABLE mendel_exp_theme_events (
			id uuid PRIMARY KEY,
			user_id uuid NOT NULL,
			chosen text
		);
		CREATE INDEX CONCURRENTLY mendel_exp_theme_events_user ON mendel_exp_theme_events (user_id);
	`
	v := Classify(sql)
	if !v.Additive {
		t.Fatalf("expected additive, refused because: %v", v.Reasons)
	}
	if len(v.Adds) != 3 {
		t.Fatalf("expected 3 added objects, got %d: %v", len(v.Adds), v.Adds)
	}
	if len(v.Hazards) != 0 {
		t.Errorf("nothing here should be hazardous, got %v", v.Hazards)
	}
	if bad := CheckNamespace(v.Adds); len(bad) != 0 {
		t.Errorf("all objects are namespaced, got complaints: %v", bad)
	}
}

// Hazards are additive but disturb production. They inform rather than refuse,
// so the migration must still classify as additive.
func TestClassifyFlagsHazardsWithoutRefusing(t *testing.T) {
	t.Run("index without CONCURRENTLY", func(t *testing.T) {
		v := Classify(`CREATE INDEX mendel_exp_i ON orders (customer_id)`)
		if !v.Additive {
			t.Fatalf("an index build is additive; refused: %v", v.Reasons)
		}
		if len(v.Hazards) != 1 || !strings.Contains(v.Hazards[0], "CONCURRENTLY") {
			t.Errorf("expected a lock hazard, got %v", v.Hazards)
		}
	})

	t.Run("volatile default rewrites the table", func(t *testing.T) {
		v := Classify(`ALTER TABLE users ADD COLUMN mendel_exp_seen_at timestamptz DEFAULT now()`)
		if !v.Additive {
			t.Fatalf("refused: %v", v.Reasons)
		}
		if len(v.Hazards) != 1 || !strings.Contains(v.Hazards[0], "rewrites every row") {
			t.Errorf("expected a rewrite hazard, got %v", v.Hazards)
		}
	})

	t.Run("constant default is free", func(t *testing.T) {
		v := Classify(`ALTER TABLE users ADD COLUMN mendel_exp_n integer DEFAULT 0`)
		if !v.Additive || len(v.Hazards) != 0 {
			t.Errorf("a constant default is metadata-only; additive=%v hazards=%v", v.Additive, v.Hazards)
		}
	})
}

// Namespacing is what makes concurrent Arms commutative, so an un-namespaced
// object has to be caught by name.
func TestCheckNamespace(t *testing.T) {
	v := Classify(`
		ALTER TABLE users ADD COLUMN preferred_theme text;
		ALTER TABLE users ADD COLUMN mendel_exp_ok text;
	`)
	if !v.Additive {
		t.Fatalf("both statements are additive; refused: %v", v.Reasons)
	}
	bad := CheckNamespace(v.Adds)
	if len(bad) != 1 || bad[0].Name != "preferred_theme" {
		t.Fatalf("expected preferred_theme flagged, got %v", bad)
	}
}

// Drift detection re-reads the tables a migration alters. A table the migration
// creates cannot have drifted, so it is not in that set.
func TestTouchedTablesExcludesWhatItCreates(t *testing.T) {
	v := Classify(`
		ALTER TABLE users ADD COLUMN mendel_exp_theme text;
		CREATE TABLE mendel_exp_events (id uuid PRIMARY KEY);
		CREATE INDEX CONCURRENTLY mendel_exp_events_i ON mendel_exp_events (id);
	`)
	got := TouchedTables(v.Adds)
	if len(got) != 1 || got[0] != "users" {
		t.Fatalf("only users pre-existed, got %v", got)
	}
}

// Statement splitting has to survive semicolons that are not statement
// boundaries, or a refusable statement could hide inside a string literal.
func TestSplitStatements(t *testing.T) {
	got := SplitStatements(`
		-- DROP TABLE orders;  (a comment, not a statement)
		ALTER TABLE users ADD COLUMN mendel_exp_note text DEFAULT 'a;b';
		/* DELETE FROM users; */
		CREATE TABLE "mendel_exp_odd;name" (id int);
	`)
	if len(got) != 2 {
		t.Fatalf("expected 2 statements, got %d: %q", len(got), got)
	}
	if !strings.Contains(got[0], "'a;b'") {
		t.Errorf("semicolon inside a literal split the statement: %q", got[0])
	}
	if !strings.Contains(got[1], `"mendel_exp_odd;name"`) {
		t.Errorf("semicolon inside a quoted identifier split the statement: %q", got[1])
	}
}

// A commented-out drop must not be read as a drop, and a real one after a
// comment must still be caught.
func TestCommentsDoNotChangeTheVerdict(t *testing.T) {
	if v := Classify(`-- DROP TABLE orders
		ALTER TABLE users ADD COLUMN mendel_exp_x text;`); !v.Additive {
		t.Errorf("a commented drop is not a drop; refused: %v", v.Reasons)
	}
	if v := Classify(`/* harmless */ DROP TABLE orders;`); v.Additive {
		t.Error("a real drop behind a comment must still be refused")
	}
}

// An unrecognised statement is a gap in what the patterns can name, not a
// verdict. Refusing it here would reject legitimate additive SQL — CREATE TYPE,
// ADD CONSTRAINT NOT VALID, partitions — that the empirical check in Admit
// handles correctly. It is recorded so the archive knows its inventory may be
// incomplete.
func TestUnrecognisedStatementsAreRecordedNotRefused(t *testing.T) {
	v := Classify(`CREATE TYPE mendel_exp_mood AS ENUM ('ok', 'good')`)
	if !v.Additive {
		t.Errorf("pattern matching does not get to refuse what it cannot read: %v", v.Reasons)
	}
	if len(v.Unrecognised) != 1 {
		t.Errorf("the statement should be recorded as unread, got %v", v.Unrecognised)
	}
}

// The deny-list is the backstop that runs before anything is executed, so it
// must catch the categorically destructive on its own, without help.
func TestForbiddenCatchesTheCatastrophicAlone(t *testing.T) {
	for _, sql := range []string{
		`DROP TABLE orders`,
		`ALTER TABLE orders DROP COLUMN status`,
		`UPDATE orders SET status = 'x'`,
		`DELETE FROM orders`,
		`TRUNCATE orders`,
		`GRANT SELECT ON orders TO nobody`,
		`ALTER TABLE orders RENAME COLUMN a TO b`,
	} {
		if reasons := Forbidden(sql); len(reasons) == 0 {
			t.Errorf("deny-list let this through: %s", sql)
		}
	}

	// It must not refuse ordinary additions, or it becomes the allow-list again.
	for _, sql := range []string{
		`ALTER TABLE users ADD COLUMN mendel_exp_x text`,
		`CREATE TABLE mendel_exp_t (id int PRIMARY KEY)`,
		`CREATE INDEX CONCURRENTLY mendel_exp_i ON users (id)`,
		`CREATE TYPE mendel_exp_mood AS ENUM ('ok')`,
	} {
		if reasons := Forbidden(sql); len(reasons) != 0 {
			t.Errorf("deny-list refused an addition: %s -> %v", sql, reasons)
		}
	}
}
