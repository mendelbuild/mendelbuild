// Package conformance states what experiment.Datastore *means*, separately from
// anyone who implements it.
//
// The interface has had exactly one implementation, so its semantics have been
// whatever Postgres happens to do. That is a corner: a second adapter would be
// written by reading pgstore and copying it, which propagates Postgres's
// assumptions rather than testing them, and there would be no way to tell a
// working adapter from one that compiles.
//
// So the contract lives here as a suite an adapter runs against. Two things
// follow from that which are worth being explicit about, because they are the
// point rather than a side effect:
//
//   - **An adapter is verifiable in a way a migration judgment is not.** §13 D6
//     refuses to let an LLM assert that a particular migration is safe, and it is
//     right to: that assertion is about one change and nothing downstream checks
//     it. An *adapter* is different. It is code with observable behaviour, and a
//     suite either passes or does not. That is what makes a generated adapter a
//     reasonable thing to attempt, where a generated safety verdict is not.
//
//   - **Failing fast is the feature.** An adapter that cannot support something
//     says so through Capabilities and is refused by RequireForExperiments, and
//     this suite checks that the refusal actually happens. Declining is a
//     designed outcome, so an adapter that declines correctly passes.
//
// The contract is generic; the *samples* it needs are not. A caller supplies a
// Fixture with changes written in its own dialect, which is the seam that keeps
// this file from knowing any SQL.
package conformance

import (
	"errors"
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/experiment"
)

// Fixture is what a suite run needs from the adapter under test: a way to get a
// datastore, and changes written in its dialect.
//
// Everything dialect-specific is here. If this struct ever needs a field that
// only one engine could fill in, that is evidence the interface has leaked an
// assumption and is worth chasing rather than accommodating.
type Fixture struct {
	// NewStore returns a datastore that exists to be experimented on, already
	// carrying Collection with at least one identifying field. The suite writes
	// to it and does not clean up, so it must be disposable.
	NewStore func(t *testing.T) experiment.Datastore

	// NewLiveStore returns the same data through a *non-disposable* store, and
	// exists because the two answer differently on purpose.
	//
	// VerifySpeculatively means "find out what this change does". On a store
	// that is not disposable, finding out must leave no trace, because the only
	// place to find out is somewhere that matters. On a disposable one there is
	// nothing to protect, so applying the change and keeping it is a legitimate
	// -- and for an engine without transactional DDL, the only -- way to answer.
	//
	// The postcondition therefore depends on which store it is, and the check
	// that matters most is the one that cannot be made against a disposable
	// store. Leave nil where the adapter has no non-disposable form.
	NewLiveStore func(t *testing.T) experiment.Datastore

	// Collection is a collection the store already has.
	Collection string

	// AdditiveChange adds one field to Collection and nothing else.
	AdditiveChange string

	// AddedField is what AdditiveChange adds, so the suite can check the Delta
	// names it rather than merely reporting something.
	AddedField string

	// DestructiveChanges are changes that are categorically unsafe in this
	// dialect, which Forbidden must catch without executing anything.
	DestructiveChanges []string

	// TwoStatements is a change containing exactly two statements, for Split.
	TwoStatements string

	// CollectionWithoutIdentity is a collection that has no identifying field,
	// or empty when the datastore cannot have one. Without an identity an
	// archive cannot be restored, so admission refuses -- and an adapter that
	// reported one anyway would turn that refusal into silent data loss.
	CollectionWithoutIdentity string
}

// Run checks an adapter against the contract.
//
// Every failure names the requirement rather than the observation, because the
// intended reader is whoever is writing the next adapter -- possibly an agent,
// which makes a diagnostic that says what was wanted worth more than one that
// says what was seen.
func Run(t *testing.T, f Fixture) {
	t.Helper()

	t.Run("Kind names the datastore for a person", func(t *testing.T) {
		if strings.TrimSpace(f.NewStore(t).Kind()) == "" {
			t.Error("Kind must return a name; it appears in the sentence a user reads when Mendel declines")
		}
	})

	t.Run("Capabilities are self-reported honestly", func(t *testing.T) {
		s := f.NewStore(t)
		caps := s.Capabilities()
		if !caps.Disposable {
			t.Error("the suite writes to this store and does not clean up, so NewStore must return " +
				"a disposable one; an adapter that reports otherwise would have the suite " +
				"experimenting on something real")
		}
		if !caps.SpeculativeApply && !caps.Disposable {
			t.Error("a store that can neither undo a change nor be thrown away cannot be verified " +
				"against at all, and RequireForExperiments must refuse it")
		}
	})

	t.Run("Forbidden catches the categorically destructive without executing", func(t *testing.T) {
		s := f.NewStore(t)
		for _, change := range f.DestructiveChanges {
			if reasons := s.Forbidden(change); len(reasons) == 0 {
				t.Errorf("Forbidden let this through: %s\n"+
					"It runs before anything is executed and is the backstop that a confidently "+
					"wrong judgment cannot talk past.", change)
			}
		}
		if reasons := s.Forbidden(f.AdditiveChange); len(reasons) != 0 {
			t.Errorf("Forbidden refused a purely additive change (%v). It is a deny-list for what is "+
				"categorically unsafe, not the affirmative judgment -- that is VerifySpeculatively's.",
				reasons)
		}
	})

	t.Run("Split yields the units Exec accepts", func(t *testing.T) {
		if f.TwoStatements == "" {
			t.Skip("no two-statement sample supplied")
		}
		if got := f.NewStore(t).Split(f.TwoStatements); len(got) != 2 {
			t.Errorf("Split returned %d units for a two-statement change, want 2: %q", len(got), got)
		}
	})

	t.Run("VerifySpeculatively reports what a change did", func(t *testing.T) {
		s := f.NewStore(t)
		if !s.Capabilities().StructuralDiff {
			t.Skip("adapter reports it cannot describe structural change")
		}
		delta, err := s.VerifySpeculatively(t.Context(), f.AdditiveChange)
		if err != nil {
			t.Fatalf("VerifySpeculatively: %v", err)
		}
		if !delta.PurelyAdditive() {
			t.Errorf("an additive change was reported as %s", delta.Describe())
		}
		if !namesField(delta.Added, f.Collection, f.AddedField) {
			t.Errorf("the Delta does not name %s.%s among %v.\n"+
				"Admission namespaces and archives what a change added, so the objects have to be "+
				"named rather than counted.", f.Collection, f.AddedField, delta.Added)
		}
	})

	t.Run("VerifySpeculatively leaves nothing behind on a store that matters", func(t *testing.T) {
		if f.NewLiveStore == nil {
			t.Skip("adapter has no non-disposable form")
		}
		s := f.NewLiveStore(t)
		if s.Capabilities().Disposable {
			t.Fatal("NewLiveStore must return a store that is not disposable; the whole point of " +
				"this check is that learning what a change does must not change anything")
		}
		if !s.Capabilities().SpeculativeApply {
			// Not a failure. An engine that commits DDL immediately cannot do
			// this, says so, and is verified against a disposable copy instead
			// -- which is exactly why that arrangement exists.
			t.Skip("adapter reports it cannot apply speculatively, so it must be given a disposable copy")
		}
		before, err := s.Shape(t.Context(), f.Collection)
		if err != nil {
			t.Fatalf("Shape before: %v", err)
		}
		if _, err := s.VerifySpeculatively(t.Context(), f.AdditiveChange); err != nil {
			t.Fatalf("VerifySpeculatively: %v", err)
		}
		after, err := s.Shape(t.Context(), f.Collection)
		if err != nil {
			t.Fatalf("Shape after: %v", err)
		}
		if _, leaked := after[f.AddedField]; leaked {
			t.Errorf("%s.%s survived a speculative apply on a store that is not disposable.\n"+
				"SpeculativeApply means the change can be learned from and undone without a trace. "+
				"An adapter that cannot do that must report SpeculativeApply false and be verified "+
				"against a disposable copy instead.", f.Collection, f.AddedField)
		}
		if len(before) != len(after) {
			t.Errorf("the structure changed across a speculative apply: %d fields became %d",
				len(before), len(after))
		}
	})

	t.Run("A disposable store may keep what it applied to find out", func(t *testing.T) {
		s := f.NewStore(t)
		if !s.Capabilities().StructuralDiff {
			t.Skip("adapter reports it cannot describe structural change")
		}
		// Stated as a check rather than left implicit, because the opposite
		// reading is the natural one and would have an adapter contorting to
		// roll back on a datastore that exists to be thrown away -- or worse,
		// reporting SpeculativeApply false and refusing experiments it could
		// have run.
		if _, err := s.VerifySpeculatively(t.Context(), f.AdditiveChange); err != nil {
			t.Fatalf("VerifySpeculatively on a disposable store: %v", err)
		}
		if _, err := s.Shape(t.Context(), f.Collection); err != nil {
			t.Errorf("the store stopped answering after a speculative apply: %v", err)
		}
	})

	t.Run("Shape reflects what Exec did", func(t *testing.T) {
		s := f.NewStore(t)
		if !s.Capabilities().StructuralDiff {
			t.Skip("adapter reports it cannot describe structural change")
		}
		if err := s.Exec(t.Context(), f.AdditiveChange); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		shape, err := s.Shape(t.Context(), f.Collection)
		if err != nil {
			t.Fatalf("Shape: %v", err)
		}
		if _, ok := shape[f.AddedField]; !ok {
			t.Errorf("after Exec added %s, Shape does not report it: %v.\n"+
				"Shape is what admission compares between the copy and production, so a field it "+
				"cannot see is a difference it will invent.", f.AddedField, shape)
		}
	})

	t.Run("Shape of a collection that does not exist is empty, not an invention", func(t *testing.T) {
		s := f.NewStore(t)
		shape, err := s.Shape(t.Context(), "mendel_conformance_absent")
		if err == nil && len(shape) != 0 {
			t.Errorf("a collection that does not exist reported a structure: %v.\n"+
				"Admission reads this to decide whether a migration can add to a collection at all.",
				shape)
		}
	})

	t.Run("Identity names what a record is keyed by", func(t *testing.T) {
		s := f.NewStore(t)
		id, err := s.Identity(t.Context(), f.Collection)
		if err != nil {
			t.Fatalf("Identity: %v", err)
		}
		if len(id) == 0 {
			t.Errorf("%s has an identifying field and Identity did not name it.\n"+
				"Without one an archive cannot be put back, and admission refuses rather than "+
				"capturing data it could never restore.", f.Collection)
		}
		if f.CollectionWithoutIdentity != "" {
			none, err := s.Identity(t.Context(), f.CollectionWithoutIdentity)
			if err == nil && len(none) != 0 {
				t.Errorf("%s has no identity and Identity claimed %v", f.CollectionWithoutIdentity, none)
			}
		}
	})

	t.Run("Dump and Load round-trip a record", func(t *testing.T) {
		s := f.NewStore(t)
		id, err := s.Identity(t.Context(), f.Collection)
		if err != nil || len(id) == 0 {
			t.Skip("no identity to round-trip against")
		}
		before, err := s.Dump(t.Context(), experiment.DumpQuery{
			Collection: f.Collection, Whole: true, Identity: id,
		})
		if err != nil {
			t.Fatalf("Dump: %v", err)
		}
		if err := s.Load(t.Context(), f.Collection, id, before); err != nil {
			t.Errorf("Load could not put back what Dump produced: %v.\n"+
				"Rollback archives an Arm's data and restores it, so a dump this adapter cannot "+
				"reload is data captured and lost.", err)
		}
	})

	t.Run("An adapter that cannot be verified against is refused", func(t *testing.T) {
		s := f.NewStore(t)
		// The live store must not be the disposable one. Verifying and applying
		// against the same throwaway copy is the arrangement this check exists
		// to prevent, and it is checked here so an adapter cannot pass by being
		// permissive.
		if err := experiment.RequireForExperiments(s, s); err == nil {
			t.Error("RequireForExperiments accepted the same disposable store as both the " +
				"verification and the live datastore, so a migration would be proved and applied " +
				"against a copy and never reach production")
		} else if !errors.Is(err, experiment.ErrUnsupportedDatastore) {
			t.Errorf("the refusal should be ErrUnsupportedDatastore so callers can tell a designed "+
				"decline from a failure, got %v", err)
		}
	})
}

func namesField(added []experiment.Object, collection, field string) bool {
	for _, o := range added {
		if o.Collection == collection && o.Name == field {
			return true
		}
	}
	return false
}
