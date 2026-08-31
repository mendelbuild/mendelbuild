package experiment

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Catalogue is what a migration can change about a schema: columns and their
// types, indexes, and constraints. Constraints matter as much as columns — a
// new table whose foreign key carries ON DELETE CASCADE alters what mainline's
// own deletes do, while adding no column anywhere.
//
// Known gap: types, functions and triggers are not inventoried. A statement
// creating one is neither namespace-checked nor archived, and the patterns
// cannot name it either, so it is recorded in Verdict.Unrecognised and left for
// the reviewing agent to judge. Widening the catalogue is the better fix and is
// not hard; it is simply not done yet.
type Catalogue struct {
	Columns     map[string]string // "table.column" -> declared type
	Indexes     map[string]string // index name -> definition
	Constraints map[string]string // "table.constraint" -> definition
}

// Delta is the difference between two Catalogues.
type Delta struct {
	Added   []string
	Removed []string
	Changed []string
}

// PurelyAdditive reports whether the migration only added.
func (d Delta) PurelyAdditive() bool { return len(d.Removed) == 0 && len(d.Changed) == 0 }

// Describe renders the non-additive part of a delta for a person to read.
func (d Delta) Describe() string {
	var parts []string
	if len(d.Removed) > 0 {
		parts = append(parts, "removes "+strings.Join(d.Removed, ", "))
	}
	if len(d.Changed) > 0 {
		parts = append(parts, "changes "+strings.Join(d.Changed, ", "))
	}
	return strings.Join(parts, "; ")
}

// concurrentIndex matches CONCURRENTLY, which cannot run inside a transaction.
// Removing it does not change which index results, only how it is built, so a
// verification run can strip it and still be reading the truth.
var concurrentIndex = regexp.MustCompile(`(?is)(CREATE\s+(?:UNIQUE\s+)?INDEX\s+)CONCURRENTLY(\s+)`)

// VerifyAdditive answers whether a migration only adds by running it and
// reading the catalogue, rather than by reading its text.
//
// This is the affirmative safety judgment. Pattern matching can say what a
// statement looks like; only executing it can say what it does — and the
// difference is where the dangerous cases live. A CREATE TABLE whose foreign
// key adds ON DELETE CASCADE to an existing table is textually an addition and
// behaviourally a change to how mainline deletes rows. The catalogue shows it.
//
// The migration runs against the real schema inside a transaction that is
// always rolled back, so the answer is about the schema as it actually is,
// with no scratch copy to drift from it and nothing left behind. Postgres makes
// DDL transactional, which is what makes this possible at all.
//
// Call Forbidden first. This takes brief locks on live tables, and a
// categorically destructive migration should be refused before it gets that
// far.
func (a *Applier) VerifyAdditive(ctx context.Context, m Migration) (Delta, error) {
	var delta Delta

	err := pgx.BeginFunc(ctx, a.Target, func(tx pgx.Tx) error {
		before, err := readCatalogue(ctx, tx)
		if err != nil {
			return fmt.Errorf("read catalogue before: %w", err)
		}

		for _, stmt := range SplitStatements(m.Up) {
			stmt = concurrentIndex.ReplaceAllString(stmt, "$1$2")
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("migration does not apply: %q: %w", firstLine(stmt), err)
			}
		}

		after, err := readCatalogue(ctx, tx)
		if err != nil {
			return fmt.Errorf("read catalogue after: %w", err)
		}
		delta = diffCatalogues(before, after)

		// Always roll back: this ran to find out what it does, not to do it.
		return errVerificationDone
	})

	if err != nil && err != errVerificationDone {
		return Delta{}, err
	}
	return delta, nil
}

// errVerificationDone unwinds the verification transaction. BeginFunc commits
// on a nil error, and there is nothing here that should ever be committed.
var errVerificationDone = fmt.Errorf("verification complete, rolling back")

func readCatalogue(ctx context.Context, tx pgx.Tx) (Catalogue, error) {
	c := Catalogue{
		Columns:     map[string]string{},
		Indexes:     map[string]string{},
		Constraints: map[string]string{},
	}

	rows, err := tx.Query(ctx, `
		SELECT table_name, column_name,
		       data_type || coalesce('(' || character_maximum_length || ')', '') ||
		       CASE WHEN is_nullable = 'NO' THEN ' NOT NULL' ELSE '' END ||
		       coalesce(' DEFAULT ' || column_default, '')
		FROM information_schema.columns
		WHERE table_schema = ANY(current_schemas(false))
	`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var table, col, typ string
		if err := rows.Scan(&table, &col, &typ); err != nil {
			rows.Close()
			return c, err
		}
		c.Columns[table+"."+col] = typ
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}

	rows, err = tx.Query(ctx, `
		SELECT indexname, indexdef FROM pg_indexes
		WHERE schemaname = ANY(current_schemas(false))
	`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			rows.Close()
			return c, err
		}
		c.Indexes[name] = def
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}

	// Constraints are read from pg_constraint rather than information_schema
	// because pg_get_constraintdef renders the whole rule — including the
	// referential actions that make a foreign key change mainline's behaviour.
	rows, err = tx.Query(ctx, `
		SELECT rel.relname, con.conname, pg_get_constraintdef(con.oid)
		FROM pg_constraint con
		JOIN pg_class rel ON rel.oid = con.conrelid
		JOIN pg_namespace ns ON ns.oid = rel.relnamespace
		WHERE ns.nspname = ANY(current_schemas(false))
	`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var table, name, def string
		if err := rows.Scan(&table, &name, &def); err != nil {
			rows.Close()
			return c, err
		}
		c.Constraints[table+"."+name] = def
	}
	rows.Close()
	return c, rows.Err()
}

func diffCatalogues(before, after Catalogue) Delta {
	var d Delta

	compare := func(kind string, was, now map[string]string) {
		for k, v := range now {
			old, ok := was[k]
			switch {
			case !ok:
				d.Added = append(d.Added, kind+" "+k)
			case old != v:
				d.Changed = append(d.Changed, fmt.Sprintf("%s %s (%s to %s)", kind, k, old, v))
			}
		}
		for k := range was {
			if _, ok := now[k]; !ok {
				d.Removed = append(d.Removed, kind+" "+k)
			}
		}
	}

	compare("column", before.Columns, after.Columns)
	compare("index", before.Indexes, after.Indexes)
	compare("constraint", before.Constraints, after.Constraints)

	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Changed)
	return d
}
