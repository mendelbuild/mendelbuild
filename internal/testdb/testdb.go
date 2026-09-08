// Package testdb resolves where tests find PostgreSQL.
//
// It exists because the answer has to be the same in every package that needs a
// database, and had previously been spelled out separately in each. Tests that
// disagree about the target fail in confusing ways, and a new one is easy to
// write against the wrong default.
package testdb

import (
	"os"
	"testing"
)

// DefaultConnString is the database tests use when MENDEL_TEST_DB_URL is unset,
// mirroring the fallback the binary itself uses in getConnString.
//
// A default rather than a hard error because the setting had nowhere good to
// live: it was carried in .claude/settings.local.json, which is gitignored and
// per-worktree, so every new worktree or clone silently lost it. Defaulting
// here means `go test ./...` works anywhere with no setup.
//
// One database per machine, shared across worktrees, is safe: each test creates
// its own throwaway schemas inside it, so concurrent runs from separate
// checkouts cannot collide.
const DefaultConnString = "postgres://localhost:5432/mendel_test?sslmode=disable"

// ConnString returns the database tests should connect to. MENDEL_TEST_DB_URL
// overrides it, for a server on another host or port.
//
// Callers must still fail rather than skip when the server is unreachable: a
// schema or SQL change that silently goes unverified is worse than a noisy
// failure.
func ConnString() string {
	if s := os.Getenv("MENDEL_TEST_DB_URL"); s != "" {
		return s
	}
	return DefaultConnString
}

// Require is how a test says it needs a real database, and it exists to keep
// one rule in one place rather than in each package's own words.
//
// The rule has two halves, and they pull in opposite directions:
//
//   - **A missing database is a failure, not a skip.** A test that quietly skips
//     stops covering anything and reports a pass while doing it, which is how a
//     suite rots without anyone noticing. `go test ./schema/...` has always been
//     deliberate about this, and the reasoning is the same everywhere: a change
//     that silently goes unverified is worse than a noisy failure. Require does
//     not enforce that half — the caller connects and fails — but it is the
//     reason this is not simply a skip helper.
//
//   - **`-short` is the quick loop and is allowed to skip.** Someone iterating on
//     a pure function should not wait for containers, and asking for `-short` is
//     asking for exactly that trade with your eyes open. It is a request, not a
//     default, so nothing is silently lost.
//
// Anything heavier than a database — a fixture repository's own datastore, in a
// container, per test — belongs in the heavy tier instead. See CLAUDE.md.
func Require(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs a database; -short is the quick loop, so this is deliberately not run")
	}
	return ConnString()
}
