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

	// CompositeIdentityCollection is keyed by more than one field. Reporting
	// one of two is worse than reporting none: admission would accept, and the
	// archive would restore rows to the wrong place.
	CompositeIdentityCollection string
	CompositeIdentityFields     []string

	// NonAdditiveChange modifies something that already exists, and is NOT one
	// the deny-list catches.
	//
	// This is the affirmative judgment, and the most important sample in this
	// struct. The deny-list handles what is categorically destructive; every
	// other change is admitted or refused on what VerifySpeculatively observed
	// it do. An adapter that reports this one as additive would have Mendel
	// admit a migration that reinterprets data mainline is still writing, and
	// nothing downstream would catch it.
	NonAdditiveChange string

	// CreateCollectionChange adds a whole collection, and CreatedCollection is
	// what it is called. A field and a collection are different ObjectKinds, and
	// admission archives and namespaces them differently.
	CreateCollectionChange string
	CreatedCollection      string

	// AddIndexChange adds an index, and AddedIndex is its name.
	AddIndexChange string
	AddedIndex     string

	// PartiallyFailingChange has a first statement that succeeds and a second
	// that does not.
	PartiallyFailingChange string
	PartiallyFailingField  string
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

	// Run against both store forms, and this is not thoroughness for its own
	// sake. Answering "what does this change do" on a disposable store and on
	// one that matters are frequently two implementations -- apply and keep
	// versus apply and roll back -- and a suite that exercises one leaves the
	// other unverified while appearing to cover it. A deliberately broken
	// adapter passed this check until it ran against both.
	for _, form := range f.storeForms() {
		t.Run("A change that is not purely additive is reported as such, on "+form.name, func(t *testing.T) {
			if f.NonAdditiveChange == "" {
				t.Skip("no non-additive sample supplied")
			}
			s := form.new(t)
			if !s.Capabilities().StructuralDiff {
				t.Skip("adapter reports it cannot describe structural change")
			}
			if reasons := s.Forbidden(f.NonAdditiveChange); len(reasons) != 0 {
				t.Fatalf("the sample is caught by the deny-list (%v), so it cannot exercise the "+
					"affirmative judgment. Supply one the deny-list does not catch.", reasons)
			}
			delta, err := s.VerifySpeculatively(t.Context(), f.NonAdditiveChange)
			if err != nil {
				t.Fatalf("VerifySpeculatively: %v", err)
			}
			if delta.PurelyAdditive() {
				t.Errorf("a change that modifies an existing object was reported as purely additive.\n"+
					"The deny-list catches what is categorically destructive; everything else is "+
					"admitted on what this method observed. An adapter that misses a modification "+
					"here has Mendel admit a migration that reinterprets data mainline is still "+
					"writing, and nothing downstream catches it.")
			}
			if strings.TrimSpace(delta.Describe()) == "" {
				t.Error("a non-additive delta must describe itself; the refusal quotes it to the user")
			}
		})

		t.Run("The Delta names what was added, on "+form.name, func(t *testing.T) {
			s := form.new(t)
			if !s.Capabilities().StructuralDiff {
				t.Skip("adapter reports it cannot describe structural change")
			}
			delta, err := s.VerifySpeculatively(t.Context(), f.AdditiveChange)
			if err != nil {
				t.Fatalf("VerifySpeculatively: %v", err)
			}
			if !namesField(delta.Added, f.Collection, f.AddedField) {
				t.Errorf("the Delta does not name %s.%s among %v", f.Collection, f.AddedField, delta.Added)
			}
		})
	}

	t.Run("Delta distinguishes a collection from a field", func(t *testing.T) {
		if f.CreateCollectionChange == "" {
			t.Skip("no create-collection sample supplied")
		}
		s := f.NewStore(t)
		if !s.Capabilities().StructuralDiff {
			t.Skip("adapter reports it cannot describe structural change")
		}
		delta, err := s.VerifySpeculatively(t.Context(), f.CreateCollectionChange)
		if err != nil {
			t.Fatalf("VerifySpeculatively: %v", err)
		}
		var found bool
		for _, o := range delta.Added {
			if o.Kind == experiment.ObjectCollection && o.Collection == f.CreatedCollection {
				found = true
			}
		}
		if !found {
			t.Errorf("creating %s did not produce an ObjectCollection in %v.\n"+
				"Admission treats the two differently: a whole collection is archived entirely "+
				"and a field only where it is set, and the namespace rule reads a different name "+
				"for each.", f.CreatedCollection, delta.Added)
		}
	})

	t.Run("Delta names an added index", func(t *testing.T) {
		if f.AddIndexChange == "" {
			t.Skip("no add-index sample supplied")
		}
		s := f.NewStore(t)
		if !s.Capabilities().StructuralDiff {
			t.Skip("adapter reports it cannot describe structural change")
		}
		delta, err := s.VerifySpeculatively(t.Context(), f.AddIndexChange)
		if err != nil {
			t.Fatalf("VerifySpeculatively: %v", err)
		}
		var found bool
		for _, o := range delta.Added {
			if o.Kind == experiment.ObjectIndex && o.Name == f.AddedIndex {
				found = true
			}
		}
		if !found {
			t.Errorf("adding index %s did not produce an ObjectIndex in %v.\n"+
				"An index an experiment created has to be named so the namespace rule can check "+
				"it and rollback can drop it.", f.AddedIndex, delta.Added)
		}
	})

	t.Run("A composite identity is reported in full", func(t *testing.T) {
		if f.CompositeIdentityCollection == "" {
			t.Skip("no composite-key collection supplied")
		}
		got, err := f.NewStore(t).Identity(t.Context(), f.CompositeIdentityCollection)
		if err != nil {
			t.Fatalf("Identity: %v", err)
		}
		if len(got) != len(f.CompositeIdentityFields) {
			t.Errorf("Identity(%s) = %v, want all of %v.\n"+
				"Half a key is worse than none: admission accepts, and the archive restores rows "+
				"to the wrong place or refuses to restore at all.",
				f.CompositeIdentityCollection, got, f.CompositeIdentityFields)
		}
	})

	t.Run("A change that fails partway leaves nothing applied", func(t *testing.T) {
		if f.PartiallyFailingChange == "" || f.NewLiveStore == nil {
			t.Skip("no partially-failing sample, or no non-disposable store")
		}
		s := f.NewLiveStore(t)
		if !s.Capabilities().SpeculativeApply {
			t.Skip("adapter cannot apply speculatively")
		}
		if _, err := s.VerifySpeculatively(t.Context(), f.PartiallyFailingChange); err == nil {
			t.Fatal("a change whose second statement is invalid should not verify cleanly")
		}
		shape, err := s.Shape(t.Context(), f.Collection)
		if err != nil {
			t.Fatalf("Shape: %v", err)
		}
		if _, leaked := shape[f.PartiallyFailingField]; leaked {
			t.Errorf("%s survived a change that failed partway.\n"+
				"Finding out what a change does has to be all or nothing, or a migration Mendel "+
				"refused has still half-happened to the datastore it refused against.",
				f.PartiallyFailingField)
		}
	})

	t.Run("Forbidden decides without executing", func(t *testing.T) {
		s := f.NewStore(t)
		before, err := s.Shape(t.Context(), f.Collection)
		if err != nil {
			t.Fatalf("Shape before: %v", err)
		}
		for _, change := range f.DestructiveChanges {
			_ = s.Forbidden(change)
		}
		after, err := s.Shape(t.Context(), f.Collection)
		if err != nil {
			t.Fatalf("Shape after: %v", err)
		}
		if len(before) != len(after) {
			t.Errorf("asking Forbidden about %d destructive changes altered the structure "+
				"(%d fields became %d).\n"+
				"It runs before anything is executed precisely so that a categorically destructive "+
				"change never reaches the step that would run it.",
				len(f.DestructiveChanges), len(before), len(after))
		}
	})

	t.Run("Two stores of the same structure report the same Shape", func(t *testing.T) {
		a, b := f.NewStore(t), f.NewStore(t)
		if !a.Capabilities().StructuralDiff {
			t.Skip("adapter reports it cannot describe structural change")
		}
		shapeA, err := a.Shape(t.Context(), f.Collection)
		if err != nil {
			t.Fatalf("Shape: %v", err)
		}
		shapeB, err := b.Shape(t.Context(), f.Collection)
		if err != nil {
			t.Fatalf("Shape: %v", err)
		}
		if len(shapeA) != len(shapeB) {
			t.Fatalf("the same structure reported %d fields and %d", len(shapeA), len(shapeB))
		}
		for field, typeA := range shapeA {
			if typeB, ok := shapeB[field]; !ok || typeA != typeB {
				t.Errorf("field %s reported as %q and %q across two stores of identical structure.\n"+
					"Admission compares the verification datastore against production with exactly "+
					"this, and declines when they differ -- so any instability here is a difference "+
					"Mendel invents and then refuses an experiment over.", field, typeA, typeB)
			}
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
		if len(before) == 0 {
			t.Fatal("the fixture collection must carry rows for the round trip to mean anything")
		}

		// A field-scoped dump is what an archive actually is: the rows that
		// took part, not the table. §13 rests an admission criterion on that
		// being bounded by experiment traffic rather than table size, so an
		// adapter that ignores Fields and returns everything turns a small
		// archive into a copy of production.
		if err := s.Exec(t.Context(), f.AdditiveChange); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		scoped, err := s.Dump(t.Context(), experiment.DumpQuery{
			Collection: f.Collection, Identity: id, Fields: []string{f.AddedField},
		})
		if err != nil {
			t.Fatalf("Dump scoped to a field: %v", err)
		}
		if len(scoped) != 0 {
			t.Errorf("dumping %s.%s returned %d rows before anything wrote to it, want 0.\n"+
				"An archive is bounded by the participants, which is what makes its size "+
				"estimable at admission.", f.Collection, f.AddedField, len(scoped))
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

// storeForm is one way of getting the datastore under test. There are two
// because an adapter frequently implements the same contract twice, and the
// suite is worth nothing on the half it does not reach.
type storeForm struct {
	name string
	new  func(*testing.T) experiment.Datastore
}

func (f Fixture) storeForms() []storeForm {
	forms := []storeForm{{name: "a disposable store", new: f.NewStore}}
	if f.NewLiveStore != nil {
		forms = append(forms, storeForm{name: "a store that matters", new: f.NewLiveStore})
	}
	return forms
}

func namesField(added []experiment.Object, collection, field string) bool {
	for _, o := range added {
		if o.Collection == collection && o.Name == field {
			return true
		}
	}
	return false
}
