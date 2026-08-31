package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migration is one Arm's schema change against the user's datastore.
type Migration struct {
	// ArmID identifies the Arm this belongs to. It appears in archive
	// metadata, so a dump found later can be traced back.
	ArmID string
	Up    string
	Down  string
}

// TableSchema is the shape of one table at a moment in time: column name to
// declared type.
//
// Stored verbatim rather than hashed. A hash answers "did this change"; when
// the answer is yes, the reader needs to know *what* changed, and a hash
// cannot say (§7).
type TableSchema map[string]string

// Admission is a classified Migration with the evidence gathered at the time
// it was judged. Applying it later re-checks that evidence.
type Admission struct {
	Migration Migration
	Verdict   Verdict

	// Schemas are the touched tables as they stood at classification. Mendel
	// is not the only writer of this database, so they are re-read before
	// applying and the difference decides whether the verdict still holds.
	Schemas map[string]TableSchema
}

// Locker serialises migration application. The implementation lives in
// Mendel's own database rather than the user's: advisory locks are a Postgres
// and MySQL feature, and Mendel only needs to order itself against itself, so
// requiring one from the user's datastore would narrow what Mendel supports
// for no gain (§7, D14).
type Locker interface {
	// Acquire blocks until the named lock is held, and returns a release.
	Acquire(ctx context.Context, key string) (release func(), err error)
}

// PGLocker holds locks as Postgres advisory locks in Mendel's own database.
type PGLocker struct{ Pool *pgxpool.Pool }

// Acquire takes a session-scoped advisory lock on a connection it holds until
// release, so the lock outlives a single statement but not the process.
func (l PGLocker) Acquire(ctx context.Context, key string) (func(), error) {
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection for lock: %w", err)
	}

	h := fnv.New64a()
	h.Write([]byte(key))
	id := int64(h.Sum64())

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, id); err != nil {
		conn.Release()
		return nil, fmt.Errorf("take advisory lock: %w", err)
	}

	return func() {
		conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, id)
		conn.Release()
	}, nil
}

// Applier runs experiment migrations against the user's datastore.
type Applier struct {
	// Target is the user's database — the one the experiment's Arms share
	// with mainline.
	Target *pgxpool.Pool

	// Lock serialises Mendel's own migrations against each other.
	Lock Locker

	// LockKey names what is being serialised, normally project and datastore.
	LockKey string
}

// Admit classifies a migration and records the state of what it touches.
//
// It refuses rather than reports: a migration that is not purely additive, or
// that creates objects outside the experiment namespace, cannot run against
// live traffic at all, and saying so here is cheaper than discovering it at
// apply time.
func (a *Applier) Admit(ctx context.Context, m Migration) (*Admission, error) {
	// 1. The deny-list, before anything is executed: a categorically
	// destructive statement must not reach the verification run, which would
	// briefly lock live tables to find out what it already tells us.
	if reasons := Forbidden(m.Up); len(reasons) > 0 {
		return nil, fmt.Errorf("migration is not additive:\n  %s", strings.Join(reasons, "\n  "))
	}

	v := Classify(m.Up)
	if !v.Additive {
		return nil, fmt.Errorf("migration is not additive:\n  %s", strings.Join(v.Reasons, "\n  "))
	}
	if bad := CheckNamespace(v.Adds); len(bad) > 0 {
		names := make([]string, 0, len(bad))
		for _, o := range bad {
			names = append(names, o.String())
		}
		return nil, fmt.Errorf("every object an experiment creates must be prefixed %q, these are not: %s",
			NamespacePrefix, strings.Join(names, ", "))
	}
	if strings.TrimSpace(m.Down) == "" {
		return nil, fmt.Errorf("migration has no down: an experiment that cannot be withdrawn cannot be run")
	}

	// 2. The empirical check, which is the affirmative judgment: run the
	// migration against the real schema and read what changed. Patterns say
	// what a statement looks like; only this says what it does.
	delta, err := a.VerifyAdditive(ctx, m)
	if err != nil {
		return nil, err
	}
	if !delta.PurelyAdditive() {
		return nil, fmt.Errorf("migration is not additive: applying it %s", delta.Describe())
	}
	if len(delta.Added) == 0 {
		return nil, fmt.Errorf("migration changes nothing, so there is nothing to run or to withdraw")
	}

	schemas := make(map[string]TableSchema)
	for _, t := range TouchedTables(v.Adds) {
		s, err := readTableSchema(ctx, a.Target, t)
		if err != nil {
			return nil, fmt.Errorf("read schema of %s: %w", t, err)
		}
		if len(s) == 0 {
			return nil, fmt.Errorf("table %s does not exist, so the migration cannot add to it", t)
		}

		// Without a primary key there is no way to say which row an archived
		// value belongs to, so the experiment's data could be captured but
		// never put back. Refused here rather than discovered at rollback,
		// when the data is already on its way out.
		pk, err := primaryKeyColumns(ctx, a.Target, t)
		if err != nil {
			return nil, fmt.Errorf("read primary key of %s: %w", t, err)
		}
		if len(pk) == 0 {
			return nil, fmt.Errorf("table %s has no primary key, so anything this experiment writes to it could not be archived in a restorable form", t)
		}

		schemas[t] = s
	}

	return &Admission{Migration: m, Verdict: v, Schemas: schemas}, nil
}

// Apply runs the up migration, under the lock, after confirming that nothing
// it depends on has changed since it was admitted.
//
// Statements are applied one at a time rather than in a transaction, because
// CREATE INDEX CONCURRENTLY cannot run inside one. That is safe here precisely
// because everything is additive and namespaced: a half-applied migration
// leaves objects nothing reads, and the down migration removes them. On
// failure that cleanup runs immediately.
func (a *Applier) Apply(ctx context.Context, adm *Admission) error {
	release, err := a.Lock.Acquire(ctx, a.LockKey)
	if err != nil {
		return err
	}
	defer release()

	if err := a.checkDrift(ctx, adm); err != nil {
		return err
	}

	for _, stmt := range SplitStatements(adm.Migration.Up) {
		if _, err := a.Target.Exec(ctx, stmt); err != nil {
			applyErr := fmt.Errorf("apply %q: %w", firstLine(stmt), err)
			if cleanupErr := a.runDown(ctx, adm); cleanupErr != nil {
				return fmt.Errorf("%w (and cleanup failed: %v)", applyErr, cleanupErr)
			}
			return applyErr
		}
	}
	return nil
}

// checkDrift re-reads the touched tables and compares them with what was
// recorded at admission. Mendel is not the only writer of this database, so a
// verdict reached earlier may no longer describe reality.
func (a *Applier) checkDrift(ctx context.Context, adm *Admission) error {
	for table, was := range adm.Schemas {
		now, err := readTableSchema(ctx, a.Target, table)
		if err != nil {
			return fmt.Errorf("re-read schema of %s: %w", table, err)
		}
		if diff := describeSchemaDiff(was, now); diff != "" {
			return fmt.Errorf("%s changed since this migration was admitted, so it must be re-classified: %s", table, diff)
		}
	}
	return nil
}

// Rollback archives whatever the experiment wrote and then removes it.
//
// The archive is taken first and deliberately: the down migration destroys the
// experiment's own data, and a losing Variation is exactly the case where
// somebody may later want to ask whether rejecting it was a mistake (§9).
func (a *Applier) Rollback(ctx context.Context, adm *Admission) (*Archive, error) {
	release, err := a.Lock.Acquire(ctx, a.LockKey)
	if err != nil {
		return nil, err
	}
	defer release()

	archive, err := a.archive(ctx, adm)
	if err != nil {
		return nil, fmt.Errorf("archive before rollback: %w", err)
	}
	if err := a.runDown(ctx, adm); err != nil {
		return nil, err
	}
	return archive, nil
}

func (a *Applier) runDown(ctx context.Context, adm *Admission) error {
	for _, stmt := range SplitStatements(adm.Migration.Down) {
		if _, err := a.Target.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("down migration %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// readTableSchema returns a table's columns and their declared types, or an
// empty map if the table does not exist.
func readTableSchema(ctx context.Context, pool *pgxpool.Pool, table string) (TableSchema, error) {
	rows, err := pool.Query(ctx, `
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = ANY(current_schemas(false)) AND table_name = $1
	`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	s := make(TableSchema)
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		s[name] = typ
	}
	return s, rows.Err()
}

// describeSchemaDiff says what changed between two readings of a table, or
// returns empty if nothing did. It names the columns rather than reporting a
// mismatch, because the reader's next question is always "which one".
func describeSchemaDiff(was, now TableSchema) string {
	var added, removed, changed []string
	for col, typ := range now {
		old, ok := was[col]
		switch {
		case !ok:
			added = append(added, col)
		case old != typ:
			changed = append(changed, fmt.Sprintf("%s (%s to %s)", col, old, typ))
		}
	}
	for col := range was {
		if _, ok := now[col]; !ok {
			removed = append(removed, col)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)

	var parts []string
	if len(added) > 0 {
		parts = append(parts, "added "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed "+strings.Join(removed, ", "))
	}
	if len(changed) > 0 {
		parts = append(parts, "retyped "+strings.Join(changed, ", "))
	}
	return strings.Join(parts, "; ")
}

// Archive is the experiment's own data, captured before the down migration
// destroys it.
type Archive struct {
	ArmID string `json:"arm_id"`

	// Tables holds the full contents of tables the experiment created.
	Tables map[string][]map[string]any `json:"tables,omitempty"`

	// Columns holds, per table, the rows where an added column is non-null:
	// the primary key to identify the row, and the added values. Bounded by
	// the Assignment Units that actually participated, not by table size (§9).
	Columns map[string][]map[string]any `json:"columns,omitempty"`
}

// JSON renders the archive for storage.
func (a *Archive) JSON() ([]byte, error) { return json.MarshalIndent(a, "", "  ") }

func (a *Applier) archive(ctx context.Context, adm *Admission) (*Archive, error) {
	arch := &Archive{
		ArmID:   adm.Migration.ArmID,
		Tables:  map[string][]map[string]any{},
		Columns: map[string][]map[string]any{},
	}

	byTable := map[string][]Object{}
	created := map[string]bool{}
	for _, o := range adm.Verdict.Adds {
		if o.Kind == ObjectTable {
			created[o.Table] = true
			continue
		}
		if o.Kind == ObjectColumn {
			byTable[o.Table] = append(byTable[o.Table], o)
		}
	}

	for table := range created {
		rows, err := queryRows(ctx, a.Target, fmt.Sprintf(`SELECT * FROM %s`, quoteIdent(table)))
		if err != nil {
			return nil, fmt.Errorf("dump %s: %w", table, err)
		}
		arch.Tables[table] = rows
	}

	for table, cols := range byTable {
		if created[table] {
			continue // already captured whole
		}
		pk, err := primaryKeyColumns(ctx, a.Target, table)
		if err != nil {
			return nil, fmt.Errorf("primary key of %s: %w", table, err)
		}
		if len(pk) == 0 {
			// Admit refuses these, so reaching here means the table lost its
			// primary key while the experiment was running.
			return nil, fmt.Errorf("table %s no longer has a primary key, so its experiment data cannot be archived in a restorable form", table)
		}

		names := make([]string, 0, len(pk)+len(cols))
		names = append(names, pk...)
		var anyNonNull []string
		for _, c := range cols {
			names = append(names, c.Name)
			anyNonNull = append(anyNonNull, fmt.Sprintf("%s IS NOT NULL", quoteIdent(c.Name)))
		}
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = quoteIdent(n)
		}

		sql := fmt.Sprintf(`SELECT %s FROM %s WHERE %s`,
			strings.Join(quoted, ", "), quoteIdent(table), strings.Join(anyNonNull, " OR "))
		rows, err := queryRows(ctx, a.Target, sql)
		if err != nil {
			return nil, fmt.Errorf("dump %s columns: %w", table, err)
		}
		arch.Columns[table] = rows
	}
	return arch, nil
}

// Restore puts an archive back. It is what makes the archive a backup rather
// than a belief, and the round-trip is exercised before an experiment runs
// (§5, item 3) so its first use is never in anger.
func (a *Applier) Restore(ctx context.Context, adm *Admission, arch *Archive) error {
	for table, rows := range arch.Tables {
		for _, row := range rows {
			if err := insertRow(ctx, a.Target, table, row); err != nil {
				return fmt.Errorf("restore into %s: %w", table, err)
			}
		}
	}

	for table, rows := range arch.Columns {
		pk, err := primaryKeyColumns(ctx, a.Target, table)
		if err != nil {
			return fmt.Errorf("primary key of %s: %w", table, err)
		}
		for _, row := range rows {
			if err := updateRow(ctx, a.Target, table, pk, row); err != nil {
				return fmt.Errorf("restore columns of %s: %w", table, err)
			}
		}
	}
	return nil
}

func queryRows(ctx context.Context, pool *pgxpool.Pool, sql string) ([]map[string]any, error) {
	rows, err := pool.Query(ctx, sql)
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

func insertRow(ctx context.Context, pool *pgxpool.Pool, table string, row map[string]any) error {
	cols := sortedKeys(row)
	placeholders := make([]string, len(cols))
	args := make([]any, len(cols))
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = row[c]
	}
	_, err := pool.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		quoteIdent(table), strings.Join(quoted, ", "), strings.Join(placeholders, ", ")), args...)
	return err
}

func updateRow(ctx context.Context, pool *pgxpool.Pool, table string, pk []string, row map[string]any) error {
	isPK := map[string]bool{}
	for _, k := range pk {
		isPK[k] = true
	}

	var sets, wheres []string
	var args []any
	for _, c := range sortedKeys(row) {
		if isPK[c] {
			continue
		}
		args = append(args, row[c])
		sets = append(sets, fmt.Sprintf("%s = $%d", quoteIdent(c), len(args)))
	}
	for _, k := range pk {
		args = append(args, row[k])
		wheres = append(wheres, fmt.Sprintf("%s = $%d", quoteIdent(k), len(args)))
	}
	if len(sets) == 0 {
		return nil
	}
	_, err := pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s WHERE %s`,
		quoteIdent(table), strings.Join(sets, ", "), strings.Join(wheres, " AND ")), args...)
	return err
}

func primaryKeyColumns(ctx context.Context, pool *pgxpool.Pool, table string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass AND i.indisprimary
		ORDER BY a.attnum
	`, table)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
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

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// quoteIdent quotes an identifier for interpolation. Identifiers here come
// from the classifier's reading of the migration rather than from a request,
// but they are still interpolated into SQL, so they are quoted rather than
// trusted.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
