// Package experiment carries the machinery for running Variations against live
// production traffic: deciding which are safe to run at all, applying the
// schema changes they need without disturbing each other or mainline, and
// taking those changes back out again.
//
// See dev/claude_plans/13_live_traffic_experiments.md for the design this
// implements, in particular §3 (tiers), §4 (classification) and §7 (schema
// drift).
package experiment

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// NamespacePrefix marks every database object an experiment creates.
//
// It does two jobs. Concurrent Arms adding a column of the same name would
// otherwise collide, and additive changes are only genuinely commutative if
// their names cannot coincide. And a human reading the schema can tell at a
// glance which objects belong to an experiment in flight and which are
// permanent — every one of these is temporary by construction.
//
// Objects are renamed out of the namespace when a Variation is promoted.
const NamespacePrefix = "mendel_exp_"

// Verdict is what classification concluded about one migration.
type Verdict struct {
	// Additive reports whether every statement only adds. A migration that is
	// not additive cannot run against live traffic under any Tier.
	Additive bool

	// Reasons lists why the migration was refused, in the order encountered.
	// Empty when Additive is true.
	Reasons []string

	// Hazards are statements that are additive but would disturb the
	// production workload — a lock held too long, a table rewritten. These do
	// not refuse the migration outright; they are shown to the user, who
	// decides.
	Hazards []string

	// Adds names the objects the migration creates, for the archive to find
	// later and for the namespace check.
	Adds []Object

	// Unrecognised lists statements the patterns could not read. These do not
	// refuse anything on their own — VerifyAdditive judges the migration by
	// what it does — but an object created by one of them would be missing
	// from Adds, so the catalogue check has the last word.
	Unrecognised []string
}

// ObjectKind distinguishes the things a migration can add.
type ObjectKind string

const (
	ObjectTable  ObjectKind = "table"
	ObjectColumn ObjectKind = "column"
	ObjectIndex  ObjectKind = "index"
)

// Object is one thing a migration creates.
type Object struct {
	Kind ObjectKind
	// Table is the table the object lives in or is. For a column, the table it
	// was added to; for a table or index, the object's own name.
	Table string
	// Name is the column or index name. Empty for a table.
	Name string
}

// String renders an object for a message a human reads.
func (o Object) String() string {
	if o.Kind == ObjectColumn {
		return fmt.Sprintf("column %s.%s", o.Table, o.Name)
	}
	if o.Kind == ObjectIndex {
		return fmt.Sprintf("index %s", o.Name)
	}
	return fmt.Sprintf("table %s", o.Table)
}

// Statement patterns, used to name what a migration adds so the archive knows
// where to look. They are not the safety judgment — VerifyAdditive is, by
// applying the migration and reading the catalogue. A statement these do not
// recognise is reported as unrecognised rather than refused: the empirical
// check will say whether it was additive, and it is right far more often than
// a pattern can be.
var (
	reCreateTable = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_."]+)\s*\(`)
	reCreateIndex = regexp.MustCompile(`(?is)^CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_."]+)\s+ON\s+([a-z0-9_."]+)`)
	reAddColumn   = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?([a-z0-9_."]+)\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_."]+)\s+(.*)$`)
)

// Statements that are never safe against a shared production database, whatever
// anything else concludes.
//
// This is a deny-list, and deliberately short. It runs before the migration is
// executed even speculatively, so an obviously destructive statement never
// takes a lock on a live table; and it is the backstop that a confidently wrong
// judge, or text engineered to mislead one, cannot talk its way past. Being
// small is what makes it auditable at a glance — the affirmative judgment of
// whether something is additive belongs to VerifyAdditive and to the reviewing
// agent, not here.
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

// Classify decides whether a migration only adds, and collects what it adds.
//
// It works on statements rather than on the migration as a whole so that the
// reason given back names the statement that caused the refusal — a user told
// "not additive" learns nothing, and a user told which line to change can act.
func Classify(sql string) Verdict {
	v := Verdict{Additive: true}

	for _, stmt := range SplitStatements(sql) {
		v.classifyStatement(stmt)
	}

	if len(v.Reasons) > 0 {
		v.Additive = false
	}
	sort.Slice(v.Adds, func(i, j int) bool {
		if v.Adds[i].Table != v.Adds[j].Table {
			return v.Adds[i].Table < v.Adds[j].Table
		}
		return v.Adds[i].Name < v.Adds[j].Name
	})
	return v
}

func (v *Verdict) classifyStatement(stmt string) {
	refuse := func(because string) {
		v.Reasons = append(v.Reasons, fmt.Sprintf("%s: %s", firstLine(stmt), because))
	}

	// Named refusals first, so the reason is specific.
	for _, r := range refusals {
		if r.pattern.MatchString(stmt) {
			refuse(r.because)
			return
		}
	}

	switch {
	case reCreateTable.MatchString(stmt):
		m := reCreateTable.FindStringSubmatch(stmt)
		v.Adds = append(v.Adds, Object{Kind: ObjectTable, Table: unquote(m[1])})

	case reCreateIndex.MatchString(stmt):
		m := reCreateIndex.FindStringSubmatch(stmt)
		v.Adds = append(v.Adds, Object{Kind: ObjectIndex, Table: unquote(m[2]), Name: unquote(m[1])})
		// An index build takes a write lock for its duration unless built
		// concurrently. On a table of any size that is an outage, not a
		// slowdown — but it is still additive, so it is the user's call.
		if !regexp.MustCompile(`(?is)CREATE\s+(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY`).MatchString(stmt) {
			v.Hazards = append(v.Hazards, fmt.Sprintf(
				"%s: builds an index without CONCURRENTLY, holding a write lock on %s for the duration",
				firstLine(stmt), unquote(m[2])))
		}

	case reAddColumn.MatchString(stmt):
		m := reAddColumn.FindStringSubmatch(stmt)
		table, col, rest := unquote(m[1]), unquote(m[2]), m[3]
		v.Adds = append(v.Adds, Object{Kind: ObjectColumn, Table: table, Name: col})

		// A NOT NULL column with no default cannot be added to a table that
		// has rows: existing rows have no value for it. Mainline is writing
		// those rows, so this fails in production even when it passed on an
		// empty test database.
		notNull := regexp.MustCompile(`(?is)\bNOT\s+NULL\b`).MatchString(rest)
		hasDefault := regexp.MustCompile(`(?is)\bDEFAULT\b`).MatchString(rest)
		if notNull && !hasDefault {
			refuse("adds a NOT NULL column with no default, which cannot apply to rows that already exist")
			return
		}
		// A volatile default has to be evaluated per row, rewriting the table
		// under a lock. A constant one is metadata-only on any Postgres since 11.
		if hasDefault && volatileDefault(rest) {
			v.Hazards = append(v.Hazards, fmt.Sprintf(
				"%s: the default is computed per row, so adding it rewrites every row of %s under a lock",
				firstLine(stmt), table))
		}

	default:
		// Not recognised, which is a gap in what this can name rather than a
		// verdict. VerifyAdditive decides; Unrecognised only means the archive
		// may not know about objects this statement creates, which
		// VerifyAdditive also detects because the catalogue shows them.
		v.Unrecognised = append(v.Unrecognised, firstLine(stmt))
	}
}

// volatileDefault reports whether a column default has to be evaluated per row
// rather than stored once as metadata.
func volatileDefault(rest string) bool {
	// A function call in the default is the signal. Bare literals — numbers,
	// strings, true/false/null — are constant and stored as metadata.
	return regexp.MustCompile(`(?is)DEFAULT\s+[a-z_]+\s*\(`).MatchString(rest)
}

// SplitStatements breaks SQL into statements on semicolons, ignoring those
// inside string literals, quoted identifiers and comments, and discarding
// comment-only and empty fragments.
func SplitStatements(sql string) []string {
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
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		default:
			cur.WriteRune(c)
		}
	}

	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// CheckNamespace reports the objects a migration adds that are not inside the
// experiment namespace. Concurrent Arms are only safe from each other while
// every object they create is namespaced (§7).
func CheckNamespace(adds []Object) []Object {
	var bad []Object
	for _, o := range adds {
		name := o.Name
		if o.Kind == ObjectTable {
			name = o.Table
		}
		if !strings.HasPrefix(strings.ToLower(name), NamespacePrefix) {
			bad = append(bad, o)
		}
	}
	return bad
}

// TouchedTables lists the existing tables a migration alters, which is what
// must be re-read before applying to detect drift (§7). A table the migration
// creates is not touched in this sense: it did not exist to drift.
func TouchedTables(adds []Object) []string {
	created := map[string]bool{}
	for _, o := range adds {
		if o.Kind == ObjectTable {
			created[o.Table] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, o := range adds {
		if o.Kind == ObjectTable || created[o.Table] || seen[o.Table] {
			continue
		}
		seen[o.Table] = true
		out = append(out, o.Table)
	}
	sort.Strings(out)
	return out
}

func firstLine(stmt string) string {
	line := strings.TrimSpace(strings.SplitN(stmt, "\n", 2)[0])
	if len(line) > 72 {
		line = line[:69] + "..."
	}
	return line
}

func unquote(ident string) string {
	return strings.ToLower(strings.Trim(ident, `"`))
}

// Forbidden reports statements that are never safe against a shared production
// database. An empty result does not mean the migration is additive — that is
// VerifyAdditive's question — only that nothing here is categorically
// destructive.
//
// It exists as a separate, cheap check so an obviously destructive migration is
// refused before it is executed even inside a transaction that will be rolled
// back, which would still briefly lock a live table.
func Forbidden(sql string) []string {
	var reasons []string
	for _, stmt := range SplitStatements(sql) {
		for _, r := range refusals {
			if r.pattern.MatchString(stmt) {
				reasons = append(reasons, fmt.Sprintf("%s: %s", firstLine(stmt), r.because))
				break
			}
		}
	}
	return reasons
}
