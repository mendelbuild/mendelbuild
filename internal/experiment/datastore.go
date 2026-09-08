package experiment

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Datastore is everything the experiment machinery needs from the datastore a
// user's application runs on.
//
// It exists because Mendel runs on Postgres and its users do not necessarily.
// A project may be on MySQL, SQLite, Mongo, DynamoDB, or a storage engine an
// infrastructure Hop built from scratch. Nothing about Mendel's own stack is
// evidence about theirs, so every operation here that could be answered
// "the way Postgres does it" is behind this interface instead.
//
// Adapters are expected to be honest about what they cannot do. An adapter
// that cannot verify a change speculatively should say so rather than
// approximate, because the caller's alternative — refusing the experiment and
// explaining why — is a good outcome, and a wrong answer here corrupts
// production data.
type Datastore interface {
	// Kind names the datastore for messages a person reads: "postgres".
	Kind() string

	// Capabilities reports what this adapter can actually do, so the caller
	// can refuse an experiment it cannot make safe rather than proceeding on
	// an assumption.
	Capabilities() Capabilities

	// Forbidden reports changes that are categorically destructive in this
	// datastore's own dialect, cheaply and without executing anything. An
	// empty result does not mean the change is additive.
	Forbidden(change string) []string

	// VerifySpeculatively applies the change, reports what it did to the
	// structure, and leaves nothing behind. Only called when Capabilities
	// says SpeculativeApply is available.
	VerifySpeculatively(ctx context.Context, change string) (Delta, error)

	// Exec applies one change for real.
	Exec(ctx context.Context, change string) error

	// Split breaks a change into the units Exec accepts, since what counts as
	// a statement is the datastore's business.
	Split(change string) []string

	// Shape describes a collection's current structure, for detecting drift
	// between admission and application.
	Shape(ctx context.Context, collection string) (TableSchema, error)

	// Identity returns the fields that identify a record in a collection, or
	// empty if it has none. Without one an archive cannot be restored.
	Identity(ctx context.Context, collection string) ([]string, error)

	// Dump reads records for the archive; Load puts them back.
	Dump(ctx context.Context, query DumpQuery) ([]map[string]any, error)
	Load(ctx context.Context, collection string, identity []string, records []map[string]any) error
}

// Delta is what a change did to a datastore's structure.
type Delta struct {
	// Added are the objects the change created, structured so the generic
	// layer can namespace-check and archive them without knowing the dialect
	// that produced them.
	Added []Object

	// Removed and Changed are rendered for a person to read, because a change
	// that is not purely additive is refused rather than acted on, and what
	// the reader needs is the reason.
	Removed []string
	Changed []string
}

// PurelyAdditive reports whether the change only added.
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

// ObjectKind distinguishes the things a change can add. The names are generic
// on purpose: a collection is a table in SQL and something else elsewhere.
type ObjectKind string

const (
	ObjectCollection ObjectKind = "collection"
	ObjectField      ObjectKind = "field"
	ObjectIndex      ObjectKind = "index"
	ObjectConstraint ObjectKind = "constraint"
)

// Object is one thing a change created.
type Object struct {
	Kind ObjectKind
	// Collection is the collection the object lives in, or is.
	Collection string
	// Name is the field, index or constraint name. Empty for a collection.
	Name string
}

// String renders an object for a message a person reads.
func (o Object) String() string {
	if o.Kind == ObjectCollection {
		return fmt.Sprintf("collection %s", o.Collection)
	}
	return fmt.Sprintf("%s %s.%s", o.Kind, o.Collection, o.Name)
}

// Ident is the name the namespace rule applies to.
func (o Object) Ident() string {
	if o.Kind == ObjectCollection {
		return o.Collection
	}
	return o.Name
}

// TableSchema is a collection's structure: field name to declared type.
//
// Recorded verbatim rather than hashed. A hash answers "did this change"; when
// the answer is yes the reader needs to know what, and a hash cannot say.
type TableSchema map[string]string

// Capabilities is what an adapter can support. Each false answer narrows what
// Mendel will attempt, and the narrowing is reported to the user rather than
// worked around.
type Capabilities struct {
	// SpeculativeApply is whether a change can be applied and then undone
	// without a trace, so Mendel can learn what it does before committing to
	// it. Postgres has transactional DDL and can; MySQL commits DDL
	// immediately and cannot.
	//
	// Without this there is no empirical answer to "is this purely additive",
	// and the textual checks alone are not enough to risk production data on.
	SpeculativeApply bool

	// StructuralDiff is whether the adapter can describe a change's effect on
	// structure at all. A schemaless datastore may not be able to.
	StructuralDiff bool

	// Disposable is whether this datastore exists to be experimented on and
	// reset. Set by the constructor rather than derived from the engine: it
	// describes the role the connection is being used in, not what the software
	// can do.
	//
	// It is what lets a datastore without transactional DDL be verified against
	// at all. MySQL commits DDL immediately, so applying a change to learn what
	// it does is indistinguishable from making the change -- which is
	// unacceptable on the live datastore and completely fine on one that is
	// about to be thrown away.
	Disposable bool
}

// ErrUnsupportedDatastore is returned when Mendel has no adapter for what the
// project runs on. It is a designed outcome, not a failure: the user can still
// compare Variations at the demo level, and manage a bigger bet themselves.
var ErrUnsupportedDatastore = errors.New("no experiment adapter for this datastore")

// DumpQuery describes what the archive needs out of a collection.
type DumpQuery struct {
	// Collection is the table or equivalent.
	Collection string

	// Whole is set when the experiment created the collection, so all of it
	// belongs to the experiment.
	Whole bool

	// Identity are the fields identifying a record, included so the dump can
	// be restored to the right one.
	Identity []string

	// Fields are the experiment's own fields, dumped where any is present.
	Fields []string
}

// RequireForExperiments checks that a datastore can support a live experiment
// with schema changes, and explains what is missing when it cannot.
//
// This is where "Mendel does not know a safe way to do this against your
// datastore" gets said out loud, which is better than doing something
// Postgres-shaped and finding out in production.
func RequireForExperiments(verify, live Datastore) error {
	if verify == nil || live == nil {
		return ErrUnsupportedDatastore
	}

	vc := verify.Capabilities()
	if !vc.StructuralDiff {
		return fmt.Errorf("%w: %s cannot describe what a change does to its structure, which is what admitting an experiment depends on",
			ErrUnsupportedDatastore, verify.Kind())
	}

	// Learning what a change does means running it. That is only acceptable
	// where it can be undone without a trace, or where the datastore exists to
	// be thrown away -- and the second is why a project is asked for a
	// non-production datastore at all. Without either, there is no empirical
	// answer to "is this purely additive", and the textual checks alone are not
	// enough to risk production data on.
	if !vc.SpeculativeApply && !vc.Disposable {
		return fmt.Errorf("%w: %s cannot apply a change and undo it without a trace, so it can only be verified against if it is a non-production datastore Mendel may reset",
			ErrUnsupportedDatastore, verify.Kind())
	}

	// The live datastore is only ever read during admission, so it needs no
	// capability beyond being readable. It must not be the disposable one: a
	// verification that ran against production is the thing this arrangement
	// exists to prevent.
	if live.Capabilities().Disposable {
		return fmt.Errorf("%w: the live datastore is marked disposable, so the migration would be verified and applied against the same throwaway copy and never reach production",
			ErrUnsupportedDatastore)
	}
	return nil
}
