// Package testrepo resolves where tests find the fixture user repositories.
//
// It exists for the same reason testdb does: the answer has to be the same in
// every package that needs one, and a path spelled out separately in each is a
// path that goes wrong differently in each.
//
// The repositories themselves are in testdata/user_repos, and what each is for
// is written down beside them. The short version is that they are cases some
// decision turns on rather than examples: one where the whole path works, one
// on a datastore Mendel has no adapter for so the decline can be exercised, and
// one with no datastore at all so the simplest experiment is not blocked on
// requirements it does not have.
package testrepo

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Names of the fixtures, so a test names one rather than spelling a path.
const (
	// Ledger is a project whose whole path works: a datastore Mendel has an
	// adapter for, a schema change, and requirements to satisfy.
	Ledger = "ledger"

	// Notes is a project on a datastore Mendel has no adapter for. It exists so
	// the decline is exercised, which is a designed outcome and the easiest
	// thing in this set to leave untested.
	Notes = "notes"

	// Pong is a project with no datastore. It exists so that the simplest
	// experiment there is -- two presentations and a cookie -- is not
	// accidentally made to satisfy the requirements of the hardest.
	Pong = "pong"
)

// Root is the fixture directory.
//
// Found from this file's own location rather than from the working directory,
// because `go test` runs each package in its own directory and a relative path
// would be a different number of `..` from every caller. runtime.Caller is the
// one thing that answers the same way regardless of who is asking.
func Root() string {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Join(filepath.Dir(self), "..", "..", "testdata", "user_repos")
}

// Path is one fixture's directory, failing the test if it is not there.
//
// Fails rather than skips. A fixture that has been moved or deleted is a broken
// test rather than an absent prerequisite, and silently skipping is how a set of
// tests stops covering anything without anyone noticing -- the same reasoning
// the schema tests are under.
func Path(t *testing.T, name string) string {
	t.Helper()

	dir := filepath.Join(Root(), name)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("fixture repository %q is not at %s. These are checked in under "+
			"testdata/user_repos; if one was moved, the tests that name it move with it.", name, dir)
	}
	return dir
}

// All is every fixture, for a test that should hold of all of them.
//
// A test written against one fixture proves something about that fixture. The
// interesting assertions are the ones true of every project Mendel might meet --
// that a declaration either parses or is refused with a reason, say -- and those
// want the whole set rather than whichever was to hand.
func All() []string { return []string{Ledger, Notes, Pong} }

// Schema returns a fixture's database schema, or "" when it has none.
//
// Read from the fixture rather than built in Go, for the same reason the
// `.mendel` specs are: a schema written beside an assertion is the shape its
// author expected, and a schema written as a repository's own file is the shape
// something would actually have. The two drift, and only the second notices.
//
// An empty result is an answer rather than a gap. `pong` has no datastore, and
// `notes` has one Mendel cannot adapt — which declines before anything connects,
// so there is nothing for a schema to be applied to.
func Schema(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(Path(t, name), "schema.sql"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("reading %s's schema: %v", name, err)
	}
	return string(raw)
}
