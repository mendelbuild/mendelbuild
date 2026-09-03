package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migration is one Arm's change to the datastore its application shares with
// mainline. The change is opaque here: SQL for a SQL datastore, something else
// elsewhere, and only the Datastore adapter reads it.
type Migration struct {
	// ArmID identifies the Arm this belongs to. It appears in archive
	// metadata, so a dump found later can be traced back.
	ArmID string
	Up    string
	Down  string
}

// Admission is a judged Migration with the evidence gathered when it was
// judged. Applying it later re-checks that evidence.
type Admission struct {
	Migration Migration
	Delta     Delta

	// Shapes are the touched collections as they stood at admission. Mendel is
	// not the only writer of this datastore, so they are re-read before
	// applying and the difference decides whether the judgment still holds.
	Shapes map[string]TableSchema
}

// Locker serialises migration application.
//
// The implementation lives in Mendel's own database rather than the user's:
// advisory locks are a Postgres and MySQL feature, and Mendel only needs to
// order itself against itself, so requiring one from the user's datastore
// would narrow what Mendel supports for no gain.
type Locker interface {
	Acquire(ctx context.Context, key string) (release func(), err error)
}

// PGLocker holds locks as Postgres advisory locks in Mendel's own database.
// Legitimately Postgres-specific: this is Mendel's database, not the user's.
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

// Applier runs experiment migrations against the datastore a user's
// application shares with mainline.
type Applier struct {
	// Store is the user's live datastore, whatever it is. Mendel's own choice of
	// Postgres says nothing about it.
	//
	// Read during admission and written only by Apply. Nothing speculative ever
	// touches it.
	Store Datastore

	// Verify is a non-production datastore Mendel may change and reset. The
	// migration is run here to find out what it does.
	//
	// Separate from Store because learning what a change does means running it,
	// and running it against production is not free even when it is rolled
	// back: ADD COLUMN takes an exclusive lock held until the rollback, and a
	// CREATE INDEX CONCURRENTLY -- written precisely to avoid locking
	// production -- cannot run inside a transaction at all, so it would have to
	// be verified as the locking build it was written to avoid.
	//
	// Defaults to Store when unset, which preserves the old single-datastore
	// behaviour for a caller that has nowhere else to look.
	Verify Datastore

	// Lock serialises Mendel's own migrations against each other.
	Lock Locker

	// LockKey names what is being serialised, normally project and datastore.
	LockKey string
}

// Admit judges whether a migration can run against live traffic, and records
// the state of what it touches.
//
// Checks run in increasing cost, each able to refuse:
//
//  1. Whether the datastore can support an experiment at all, so an
//     unsupported one is declined by name rather than approximated.
//  2. The adapter's deny-list, before anything is executed, so a
//     categorically destructive change never reaches step 3 — which would
//     briefly lock live tables to learn what the deny-list already says.
//  3. Application to the verification datastore: run the change, read what it
//     did. This is the affirmative judgment. Patterns say what a change looks
//     like; only this says what it does.
//  4. The namespace and identity rules, which are about whether the
//     experiment can be withdrawn afterwards.
//  5. That the two datastores agree about the collections being touched. A
//     verification is only evidence about production while the shape it ran
//     against matches production's -- which is the same drift check Apply
//     makes across time, made here across databases.
func (a *Applier) Admit(ctx context.Context, m Migration) (*Admission, error) {
	verify := a.verifyStore()
	if err := RequireForExperiments(verify, a.Store); err != nil {
		return nil, err
	}

	if reasons := verify.Forbidden(m.Up); len(reasons) > 0 {
		return nil, fmt.Errorf("migration is not additive:\n  %s", strings.Join(reasons, "\n  "))
	}
	if strings.TrimSpace(m.Down) == "" {
		return nil, fmt.Errorf("migration has no down: an experiment that cannot be withdrawn cannot be run")
	}

	delta, err := verify.VerifySpeculatively(ctx, m.Up)
	if err != nil {
		return nil, err
	}
	if !delta.PurelyAdditive() {
		return nil, fmt.Errorf("migration is not additive: applying it %s", delta.Describe())
	}
	if len(delta.Added) == 0 {
		return nil, fmt.Errorf("migration changes nothing, so there is nothing to run or to withdraw")
	}
	if bad := CheckNamespace(delta.Added); len(bad) > 0 {
		names := make([]string, 0, len(bad))
		for _, o := range bad {
			names = append(names, o.String())
		}
		return nil, fmt.Errorf("every object an experiment creates must be prefixed %q, these are not: %s",
			NamespacePrefix, strings.Join(names, ", "))
	}

	shapes := make(map[string]TableSchema)
	for _, c := range TouchedCollections(delta.Added) {
		// Read from the live datastore: these are what Apply re-reads and
		// refuses on if they have moved, so they have to describe the place the
		// migration will actually run.
		shape, err := a.Store.Shape(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("read shape of %s: %w", c, err)
		}
		if len(shape) == 0 {
			return nil, fmt.Errorf("%s does not exist, so the migration cannot add to it", c)
		}

		// And the verification only means something about production while the
		// two agree. A drifted copy gives a confident answer about the wrong
		// schema, which is worse than no answer -- so name the difference and
		// decline rather than trusting the copy.
		if verify != a.Store {
			mirror, err := verify.Shape(ctx, c)
			if err != nil {
				return nil, fmt.Errorf("read shape of %s on the verification datastore: %w", c, err)
			}
			// The mirror was read after the migration ran on it, so the objects
			// this change adds are expected to be present there and absent
			// here. Everything else must match.
			if diff := describeShapeDiff(shape, withoutAdded(mirror, c, delta.Added)); diff != "" {
				return nil, fmt.Errorf(
					"the verification datastore does not match production for %s, so what it "+
						"proved does not apply: %s", c, diff)
			}
		}

		// Without an identity there is no way to say which record an archived
		// value belongs to, so the experiment's data could be captured but
		// never put back. Refused here rather than discovered at rollback,
		// when the data is already on its way out.
		id, err := a.Store.Identity(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("read identity of %s: %w", c, err)
		}
		if len(id) == 0 {
			return nil, fmt.Errorf("%s has no primary key, so anything this experiment writes to it could not be archived in a restorable form", c)
		}
		shapes[c] = shape
	}

	return &Admission{Migration: m, Delta: delta, Shapes: shapes}, nil
}

// verifyStore is where speculative work happens.
//
// Falls back to the live datastore so a caller with nowhere else to look still
// works, exactly as it did before there were two. That path takes production
// locks, and RequireForExperiments will refuse it outright for any datastore
// without transactional DDL.
func (a *Applier) verifyStore() Datastore {
	if a.Verify != nil {
		return a.Verify
	}
	return a.Store
}

// withoutAdded removes the objects this migration created from a shape read
// after it ran, so the remainder can be compared against a datastore where it
// has not run yet.
func withoutAdded(shape TableSchema, collection string, added []Object) TableSchema {
	out := make(TableSchema, len(shape))
	for k, v := range shape {
		out[k] = v
	}
	for _, o := range added {
		if o.Collection == collection && o.Name != "" {
			delete(out, o.Name)
		}
	}
	return out
}

// Apply runs the up migration, under the lock, after confirming that nothing
// it depends on has changed since it was admitted.
//
// Changes are applied one at a time rather than as a unit, because some — a
// concurrent index build, for one — cannot run inside a transaction. That is
// safe here precisely because everything is additive and namespaced: a
// half-applied migration leaves objects nothing reads, and the down migration
// removes them. On failure that cleanup runs immediately.
func (a *Applier) Apply(ctx context.Context, adm *Admission) error {
	release, err := a.Lock.Acquire(ctx, a.LockKey)
	if err != nil {
		return err
	}
	defer release()

	if err := a.checkDrift(ctx, adm); err != nil {
		return err
	}

	for _, stmt := range a.Store.Split(adm.Migration.Up) {
		if err := a.Store.Exec(ctx, stmt); err != nil {
			applyErr := fmt.Errorf("apply %q: %w", firstLine(stmt), err)
			if cleanupErr := a.runDown(ctx, adm); cleanupErr != nil {
				return fmt.Errorf("%w (and cleanup failed: %v)", applyErr, cleanupErr)
			}
			return applyErr
		}
	}
	return nil
}

// checkDrift re-reads the touched collections and compares them with what was
// recorded at admission. Mendel is not the only writer of this datastore, so a
// judgment reached earlier may no longer describe reality.
func (a *Applier) checkDrift(ctx context.Context, adm *Admission) error {
	for c, was := range adm.Shapes {
		now, err := a.Store.Shape(ctx, c)
		if err != nil {
			return fmt.Errorf("re-read shape of %s: %w", c, err)
		}
		if diff := describeShapeDiff(was, now); diff != "" {
			return fmt.Errorf("%s changed since this migration was admitted, so it must be re-classified: %s", c, diff)
		}
	}
	return nil
}

// Rollback archives whatever the experiment wrote and then removes it.
//
// The archive is taken first and deliberately: the down migration destroys the
// experiment's own data, and a losing Variation is exactly the case where
// somebody may later want to ask whether rejecting it was a mistake.
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
	for _, stmt := range a.Store.Split(adm.Migration.Down) {
		if err := a.Store.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("down migration %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// Archive is the experiment's own data, captured before the down migration
// destroys it.
type Archive struct {
	ArmID string `json:"arm_id"`

	// Collections holds the full contents of collections the experiment
	// created.
	Collections map[string][]map[string]any `json:"collections,omitempty"`

	// Fields holds, per collection, the records where an added field is
	// present: the identity to find the record again, and the added values.
	// Bounded by the Assignment Units that actually participated, not by how
	// much the collection holds.
	Fields map[string][]map[string]any `json:"fields,omitempty"`
}

// JSON renders the archive for storage.
func (a *Archive) JSON() ([]byte, error) { return json.MarshalIndent(a, "", "  ") }

func (a *Applier) archive(ctx context.Context, adm *Admission) (*Archive, error) {
	arch := &Archive{
		ArmID:       adm.Migration.ArmID,
		Collections: map[string][]map[string]any{},
		Fields:      map[string][]map[string]any{},
	}

	created := map[string]bool{}
	byCollection := map[string][]string{}
	for _, o := range adm.Delta.Added {
		switch o.Kind {
		case ObjectCollection:
			created[o.Collection] = true
		case ObjectField:
			byCollection[o.Collection] = append(byCollection[o.Collection], o.Name)
		}
	}

	for c := range created {
		records, err := a.Store.Dump(ctx, DumpQuery{Collection: c, Whole: true})
		if err != nil {
			return nil, fmt.Errorf("dump %s: %w", c, err)
		}
		arch.Collections[c] = records
	}

	for c, fields := range byCollection {
		if created[c] {
			continue // already captured whole
		}
		id, err := a.Store.Identity(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("identity of %s: %w", c, err)
		}
		if len(id) == 0 {
			// Admit refuses these, so reaching here means the collection lost
			// its identity while the experiment was running.
			return nil, fmt.Errorf("%s no longer has a primary key, so its experiment data cannot be archived in a restorable form", c)
		}
		records, err := a.Store.Dump(ctx, DumpQuery{Collection: c, Identity: id, Fields: fields})
		if err != nil {
			return nil, fmt.Errorf("dump %s fields: %w", c, err)
		}
		arch.Fields[c] = records
	}
	return arch, nil
}

// Restore puts an archive back. It is what makes the archive a backup rather
// than a belief, and the round trip is exercised before an experiment runs so
// its first use is never in anger.
func (a *Applier) Restore(ctx context.Context, arch *Archive) error {
	for c, records := range arch.Collections {
		if err := a.Store.Load(ctx, c, nil, records); err != nil {
			return fmt.Errorf("restore into %s: %w", c, err)
		}
	}
	for c, records := range arch.Fields {
		id, err := a.Store.Identity(ctx, c)
		if err != nil {
			return fmt.Errorf("identity of %s: %w", c, err)
		}
		if err := a.Store.Load(ctx, c, id, records); err != nil {
			return fmt.Errorf("restore fields of %s: %w", c, err)
		}
	}
	return nil
}

// describeShapeDiff says what changed between two readings of a collection, or
// returns empty if nothing did. It names the fields rather than reporting a
// mismatch, because the reader's next question is always "which one".
func describeShapeDiff(was, now TableSchema) string {
	var added, removed, changed []string
	for f, typ := range now {
		old, ok := was[f]
		switch {
		case !ok:
			added = append(added, f)
		case old != typ:
			changed = append(changed, fmt.Sprintf("%s (%s to %s)", f, old, typ))
		}
	}
	for f := range was {
		if _, ok := now[f]; !ok {
			removed = append(removed, f)
		}
	}
	sortStrings(added)
	sortStrings(removed)
	sortStrings(changed)

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

func firstLine(stmt string) string {
	line := strings.TrimSpace(strings.SplitN(stmt, "\n", 2)[0])
	if len(line) > 72 {
		line = line[:69] + "..."
	}
	return line
}
