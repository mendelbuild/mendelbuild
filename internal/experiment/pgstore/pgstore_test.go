package pgstore

import (
	"strings"
	"testing"
)

// The deny-list is the backstop that runs before anything is executed, so it
// has to catch the categorically destructive on its own, without help from the
// empirical check that follows it.
func TestForbiddenCatchesTheCatastrophicAlone(t *testing.T) {
	s := &Store{}

	for _, sql := range []string{
		`DROP TABLE orders`,
		`ALTER TABLE orders DROP COLUMN status`,
		`ALTER TABLE orders RENAME COLUMN a TO b`,
		`ALTER TABLE orders ALTER COLUMN total TYPE bigint`,
		`ALTER TABLE orders ALTER COLUMN status SET NOT NULL`,
		`UPDATE orders SET status = 'x'`,
		`DELETE FROM orders`,
		`INSERT INTO orders (id) VALUES (1)`,
		`TRUNCATE orders`,
		`GRANT SELECT ON orders TO nobody`,
	} {
		if reasons := s.Forbidden(sql); len(reasons) == 0 {
			t.Errorf("deny-list let this through: %s", sql)
		}
	}
}

// It must not refuse ordinary additions, or it becomes the allow-list this
// design deliberately moved away from — including additive SQL no pattern here
// recognises, which the empirical check judges instead.
func TestForbiddenLeavesAdditionsAlone(t *testing.T) {
	s := &Store{}

	for _, sql := range []string{
		`ALTER TABLE users ADD COLUMN mendel_exp_x text`,
		`CREATE TABLE mendel_exp_t (id int PRIMARY KEY)`,
		`CREATE INDEX CONCURRENTLY mendel_exp_i ON users (id)`,
		`CREATE TYPE mendel_exp_mood AS ENUM ('ok')`,
		`ALTER TABLE users ADD CONSTRAINT mendel_exp_c CHECK (id IS NOT NULL) NOT VALID`,
	} {
		if reasons := s.Forbidden(sql); len(reasons) != 0 {
			t.Errorf("deny-list refused an addition: %s -> %v", sql, reasons)
		}
	}
}

// A refusal names the statement and why, because a user told "not additive"
// learns nothing while a user told which line to change can act.
func TestForbiddenExplainsItself(t *testing.T) {
	s := &Store{}
	reasons := s.Forbidden(`ALTER TABLE orders DROP COLUMN status`)
	if len(reasons) != 1 {
		t.Fatalf("expected one reason, got %v", reasons)
	}
	if !strings.Contains(reasons[0], "DROP COLUMN status") {
		t.Errorf("the reason should quote the statement, got %q", reasons[0])
	}
	if !strings.Contains(reasons[0], "mainline may still be writing") {
		t.Errorf("the reason should explain the risk, got %q", reasons[0])
	}
}

// Statement splitting has to survive semicolons that are not boundaries, or a
// destructive statement could hide inside a literal and escape the deny-list.
func TestSplitIgnoresSemicolonsThatAreNotBoundaries(t *testing.T) {
	s := &Store{}
	got := s.Split(`
		-- DROP TABLE orders;  (a comment, not a statement)
		ALTER TABLE users ADD COLUMN mendel_exp_note text DEFAULT 'a;b';
		/* DELETE FROM users; */
		CREATE TABLE "mendel_exp_odd;name" (id int);
	`)
	if len(got) != 2 {
		t.Fatalf("expected 2 statements, got %d: %q", len(got), got)
	}
	if !strings.Contains(got[0], "'a;b'") {
		t.Errorf("a semicolon inside a literal split the statement: %q", got[0])
	}
	if !strings.Contains(got[1], `"mendel_exp_odd;name"`) {
		t.Errorf("a semicolon inside a quoted identifier split the statement: %q", got[1])
	}
}

// A commented-out drop is not a drop; a real one after a comment still is.
func TestCommentsDoNotChangeTheVerdict(t *testing.T) {
	s := &Store{}
	if reasons := s.Forbidden("-- DROP TABLE orders\nALTER TABLE users ADD COLUMN mendel_exp_x text"); len(reasons) != 0 {
		t.Errorf("a commented drop is not a drop: %v", reasons)
	}
	if reasons := s.Forbidden(`/* harmless */ DROP TABLE orders;`); len(reasons) == 0 {
		t.Error("a real drop behind a comment must still be caught")
	}
}

// Postgres can do both things the experiment machinery needs. The value of
// saying so explicitly is that an adapter which cannot must say that instead
// of approximating.
func TestCapabilities(t *testing.T) {
	c := (&Store{}).Capabilities()
	if !c.SpeculativeApply || !c.StructuralDiff {
		t.Errorf("postgres supports both; got %+v", c)
	}
	if kind := (&Store{}).Kind(); kind != "postgres" {
		t.Errorf("Kind() = %q", kind)
	}
}
