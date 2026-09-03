// Package pgstore adapts Postgres to the experiment machinery.
//
// It is one implementation of experiment.Datastore, not the shape the rest of
// Mendel assumes. Everything Postgres-specific lives here: transactional DDL,
// information_schema and pg_catalog, and the SQL dialect the deny-list reads.
// A second adapter is an addition rather than a rewrite, which is the point —
// Mendel runs on Postgres and its users need not.
package pgstore

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhs/mendelbuild/internal/experiment"
)

// Store is a Postgres database an experiment runs against.
type Store struct {
	Pool *pgxpool.Pool

	// disposable marks a scratch datastore Mendel may change and reset, as
	// opposed to the user's live one.
	disposable bool
}

// New returns a Store over the given pool.
func New(pool *pgxpool.Pool) *Store { return &Store{Pool: pool} }

// NewScratch is a datastore that exists to be experimented on and thrown away.
//
// Worth being a separate constructor rather than a flag on New: the difference
// decides whether a migration may be applied for real, and a caller that has to
// remember to set a boolean will eventually forget on the connection where it
// matters.
func NewScratch(pool *pgxpool.Pool) *Store { return &Store{Pool: pool, disposable: true} }

func (s *Store) Kind() string { return "postgres" }

// Capabilities: Postgres can do both of the things the experiment machinery
// needs. Transactional DDL is what makes speculative application possible, and
// it is a Postgres property rather than a general one — MySQL commits DDL
// immediately, so an adapter for it would answer false here and experiments
// with migrations would be refused rather than approximated.
func (s *Store) Capabilities() experiment.Capabilities {
	return experiment.Capabilities{SpeculativeApply: true, StructuralDiff: true, Disposable: s.disposable}
}

// Statements that are never safe against a shared production database.
//
// A deny-list, deliberately short. It runs before anything is executed, so an
// obviously destructive statement never takes even the brief lock that
// speculative application needs; and it is the backstop that a confidently
// wrong judge, or SQL written to mislead one, cannot talk past. Being small is
// what makes it auditable — the affirmative judgment of whether a change only
// adds belongs to VerifySpeculatively, not here.
var refusals = []struct {
	pattern *regexp.Regexp
	because string
}{
	{regexp.MustCompile(`(?is)^DROP\s+`), "drops an object, which cannot be undone for data mainline is still writing"},
	{regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+.*\s+DROP\s+COLUMN`), "drops a column mainline may still be writing"},
	{regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+.*\s+RENAME`), "renames an object, which changes what mainline's queries mean"},
	{regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+.*\s+ALTER\s+COLUMN.*TYPE`), "changes a column's type, reinterpreting data mainline shares"},
	{regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+.*\s+ALTER\s+COLUMN.*SET\s+NOT\s+NULL`), "constrains an existing column, which can reject mainline's writes"},
	{regexp.MustCompile(`(?is)^UPDATE\s+`), "rewrites existing rows, which is not reversible once mainline writes over them"},
	{regexp.MustCompile(`(?is)^DELETE\s+`), "removes existing rows"},
	{regexp.MustCompile(`(?is)^INSERT\s+`), "seeds rows that mainline would then read as its own"},
	{regexp.MustCompile(`(?is)^TRUNCATE\s+`), "empties a table"},
	{regexp.MustCompile(`(?is)^GRANT\s+|^REVOKE\s+`), "changes privileges, which is Mendel's to control, not the migration's"},
}

// Forbidden reports statements that are categorically destructive. An empty
// result does not mean the change is additive — VerifySpeculatively answers
// that — only that nothing here is unconditionally ruinous.
func (s *Store) Forbidden(change string) []string {
	var reasons []string
	for _, stmt := range s.Split(change) {
		for _, r := range refusals {
			if r.pattern.MatchString(stmt) {
				reasons = append(reasons, fmt.Sprintf("%s: %s", firstLine(stmt), r.because))
				break
			}
		}
	}
	return reasons
}

// concurrentIndex matches CONCURRENTLY, which cannot run inside a transaction.
// Removing it does not change which index results, only how it is built, so a
// speculative run can strip it and still be reading the truth.
var concurrentIndex = regexp.MustCompile(`(?is)(CREATE\s+(?:UNIQUE\s+)?INDEX\s+)CONCURRENTLY(\s+)`)

// VerifySpeculatively answers what a change does by running it and reading the
// catalogue, rather than by reading its text.
//
// Pattern matching can say what a statement looks like; only executing it can
// say what it does, and the difference is where the dangerous cases live. A
// CREATE TABLE whose foreign key adds ON DELETE CASCADE is textually one
// addition and behaviourally a change to how mainline deletes rows. The
// catalogue shows it.
//
// On a scratch datastore the change is applied for real and left there. That
// is the better answer wherever one is available: a transaction that must be
// rolled back cannot contain CREATE INDEX CONCURRENTLY, so the statement written
// precisely to avoid locking a live table has to be rewritten into the locking
// form to be verified at all -- and then what was verified is not what will run.
// On a copy, it runs as written.
//
// Against a live datastore it runs inside a transaction that is always rolled
// back. Correct, and not free: the locks are real for as long as it takes, which
// is why a project is asked for somewhere else to do this.
func (s *Store) VerifySpeculatively(ctx context.Context, change string) (experiment.Delta, error) {
	if s.disposable {
		return s.verifyByApplying(ctx, change)
	}

	var delta experiment.Delta

	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		before, err := readCatalogue(ctx, tx)
		if err != nil {
			return fmt.Errorf("read catalogue before: %w", err)
		}
		for _, stmt := range s.Split(change) {
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
		return errRollBack
	})
	if err != nil && err != errRollBack {
		return experiment.Delta{}, err
	}
	return delta, nil
}

// verifyByApplying runs the change where it does not matter and reads what it
// did, with no transaction and nothing rewritten.
//
// The caller is responsible for resetting the datastore afterwards. That is the
// bargain a disposable datastore makes, and it buys the two things the
// transactional path cannot give: statements verified exactly as written, and no
// requirement that the engine have transactional DDL at all.
func (s *Store) verifyByApplying(ctx context.Context, change string) (experiment.Delta, error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return experiment.Delta{}, err
	}
	defer conn.Release()

	before, err := readCatalogue(ctx, conn)
	if err != nil {
		return experiment.Delta{}, fmt.Errorf("read catalogue before: %w", err)
	}
	for _, stmt := range s.Split(change) {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return experiment.Delta{}, fmt.Errorf("migration does not apply: %q: %w", firstLine(stmt), err)
		}
	}
	after, err := readCatalogue(ctx, conn)
	if err != nil {
		return experiment.Delta{}, fmt.Errorf("read catalogue after: %w", err)
	}
	return diffCatalogues(before, after), nil
}

// errRollBack unwinds the speculative transaction. BeginFunc commits on a nil
// error, and nothing here should ever be committed.
var errRollBack = fmt.Errorf("speculative apply complete, rolling back")

func (s *Store) Exec(ctx context.Context, stmt string) error {
	_, err := s.Pool.Exec(ctx, stmt)
	return err
}

func (s *Store) Shape(ctx context.Context, collection string) (experiment.TableSchema, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = ANY(current_schemas(false)) AND table_name = $1
	`, collection)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := experiment.TableSchema{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		out[name] = typ
	}
	return out, rows.Err()
}

func (s *Store) Identity(ctx context.Context, collection string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass AND i.indisprimary
		ORDER BY a.attnum
	`, collection)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) Dump(ctx context.Context, q experiment.DumpQuery) ([]map[string]any, error) {
	var sql string
	if q.Whole {
		sql = fmt.Sprintf(`SELECT * FROM %s`, quoteIdent(q.Collection))
	} else {
		names := append(append([]string{}, q.Identity...), q.Fields...)
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = quoteIdent(n)
		}
		var present []string
		for _, f := range q.Fields {
			present = append(present, fmt.Sprintf("%s IS NOT NULL", quoteIdent(f)))
		}
		sql = fmt.Sprintf(`SELECT %s FROM %s WHERE %s`,
			strings.Join(quoted, ", "), quoteIdent(q.Collection), strings.Join(present, " OR "))
	}

	rows, err := s.Pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, len(vals))
		for i, fd := range rows.FieldDescriptions() {
			m[string(fd.Name)] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Load puts archived records back. With no identity the records are inserted
// whole, which is right for a collection the experiment created; with one they
// update the row they came from.
func (s *Store) Load(ctx context.Context, collection string, identity []string, records []map[string]any) error {
	for _, rec := range records {
		var err error
		if len(identity) == 0 {
			err = s.insertRow(ctx, collection, rec)
		} else {
			err = s.updateRow(ctx, collection, identity, rec)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) insertRow(ctx context.Context, table string, row map[string]any) error {
	cols := sortedKeys(row)
	quoted := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = row[c]
	}
	_, err := s.Pool.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		quoteIdent(table), strings.Join(quoted, ", "), strings.Join(placeholders, ", ")), args...)
	return err
}

func (s *Store) updateRow(ctx context.Context, table string, identity []string, row map[string]any) error {
	isID := map[string]bool{}
	for _, k := range identity {
		isID[k] = true
	}

	var sets, wheres []string
	var args []any
	for _, c := range sortedKeys(row) {
		if isID[c] {
			continue
		}
		args = append(args, row[c])
		sets = append(sets, fmt.Sprintf("%s = $%d", quoteIdent(c), len(args)))
	}
	if len(sets) == 0 {
		return nil
	}
	for _, k := range identity {
		args = append(args, row[k])
		wheres = append(wheres, fmt.Sprintf("%s = $%d", quoteIdent(k), len(args)))
	}
	_, err := s.Pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s WHERE %s`,
		quoteIdent(table), strings.Join(sets, ", "), strings.Join(wheres, " AND ")), args...)
	return err
}

// catalogue is what a change can alter about a schema: columns, indexes and
// constraints. Constraints matter as much as columns — a foreign key's
// referential actions change what mainline's own deletes do while adding no
// column anywhere.
//
// Known gap: types, functions and triggers are not inventoried, so a statement
// creating one is neither namespace-checked nor archived. Widening this is the
// fix and is not hard; it is simply not done yet.
type catalogue struct {
	columns     map[string]string
	indexes     map[string]string
	constraints map[string]string
}

// querier is whatever can run a query: the speculative path has a transaction,
// the disposable path has a plain connection and deliberately no transaction.
type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func readCatalogue(ctx context.Context, tx querier) (catalogue, error) {
	c := catalogue{
		columns:     map[string]string{},
		indexes:     map[string]string{},
		constraints: map[string]string{},
	}

	read := func(sql string, into map[string]string, key func(a, b string) string) error {
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a, b, v string
			if err := rows.Scan(&a, &b, &v); err != nil {
				return err
			}
			into[key(a, b)] = v
		}
		return rows.Err()
	}

	joined := func(a, b string) string { return a + "." + b }

	if err := read(`
		SELECT table_name, column_name,
		       data_type || coalesce('(' || character_maximum_length || ')', '') ||
		       CASE WHEN is_nullable = 'NO' THEN ' NOT NULL' ELSE '' END ||
		       coalesce(' DEFAULT ' || column_default, '')
		FROM information_schema.columns
		WHERE table_schema = ANY(current_schemas(false))
	`, c.columns, joined); err != nil {
		return c, err
	}

	if err := read(`
		SELECT tablename, indexname, indexdef FROM pg_indexes
		WHERE schemaname = ANY(current_schemas(false))
	`, c.indexes, joined); err != nil {
		return c, err
	}

	// pg_get_constraintdef rather than information_schema, because it renders
	// the whole rule including the referential actions.
	if err := read(`
		SELECT rel.relname, con.conname, pg_get_constraintdef(con.oid)
		FROM pg_constraint con
		JOIN pg_class rel ON rel.oid = con.conrelid
		JOIN pg_namespace ns ON ns.oid = rel.relnamespace
		WHERE ns.nspname = ANY(current_schemas(false))
	`, c.constraints, joined); err != nil {
		return c, err
	}
	return c, nil
}

func diffCatalogues(before, after catalogue) experiment.Delta {
	var d experiment.Delta

	compare := func(kind experiment.ObjectKind, label string, was, now map[string]string) {
		for k, v := range now {
			collection, name, _ := strings.Cut(k, ".")
			old, ok := was[k]
			switch {
			case !ok:
				d.Added = append(d.Added, experiment.Object{Kind: kind, Collection: collection, Name: name})
			case old != v:
				d.Changed = append(d.Changed, fmt.Sprintf("%s %s (%s to %s)", label, k, old, v))
			}
		}
		for k := range was {
			if _, ok := now[k]; !ok {
				d.Removed = append(d.Removed, label+" "+k)
			}
		}
	}

	compare(experiment.ObjectField, "column", before.columns, after.columns)
	compare(experiment.ObjectIndex, "index", before.indexes, after.indexes)
	compare(experiment.ObjectConstraint, "constraint", before.constraints, after.constraints)

	// A table Postgres reports only through its columns is still a table: if
	// every column of a collection is new, the collection itself is new.
	newCollections := map[string]bool{}
	for k := range after.columns {
		collection, _, _ := strings.Cut(k, ".")
		newCollections[collection] = true
	}
	for k := range before.columns {
		collection, _, _ := strings.Cut(k, ".")
		delete(newCollections, collection)
	}
	for collection := range newCollections {
		d.Added = append(d.Added, experiment.Object{Kind: experiment.ObjectCollection, Collection: collection})
	}

	sortObjects(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Changed)
	return d
}

func sortObjects(objs []experiment.Object) {
	sort.Slice(objs, func(i, j int) bool {
		if objs[i].Collection != objs[j].Collection {
			return objs[i].Collection < objs[j].Collection
		}
		if objs[i].Kind != objs[j].Kind {
			return objs[i].Kind < objs[j].Kind
		}
		return objs[i].Name < objs[j].Name
	})
}

// Split breaks SQL into statements on semicolons, ignoring those inside string
// literals, quoted identifiers and comments, and discarding comment-only and
// empty fragments.
func (s *Store) Split(sql string) []string {
	var out []string
	var cur strings.Builder
	var inSingle, inDouble, inLineComment, inBlockComment bool
	runes := []rune(sql)

	for i := 0; i < len(runes); i++ {
		c := runes[i]
		next := func() rune {
			if i+1 < len(runes) {
				return runes[i+1]
			}
			return 0
		}

		switch {
		case inLineComment:
			if c == '\n' {
				inLineComment = false
				cur.WriteRune(c)
			}
			continue
		case inBlockComment:
			if c == '*' && next() == '/' {
				inBlockComment = false
				i++
			}
			continue
		case inSingle:
			cur.WriteRune(c)
			if c == '\'' {
				inSingle = false
			}
			continue
		case inDouble:
			cur.WriteRune(c)
			if c == '"' {
				inDouble = false
			}
			continue
		}

		switch {
		case c == '-' && next() == '-':
			inLineComment = true
			i++
		case c == '/' && next() == '*':
			inBlockComment = true
			i++
		case c == '\'':
			inSingle = true
			cur.WriteRune(c)
		case c == '"':
			inDouble = true
			cur.WriteRune(c)
		case c == ';':
			if t := strings.TrimSpace(cur.String()); t != "" {
				out = append(out, t)
			}
			cur.Reset()
		default:
			cur.WriteRune(c)
		}
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		out = append(out, t)
	}
	return out
}

func firstLine(stmt string) string {
	line := strings.TrimSpace(strings.SplitN(stmt, "\n", 2)[0])
	if len(line) > 72 {
		line = line[:69] + "..."
	}
	return line
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
