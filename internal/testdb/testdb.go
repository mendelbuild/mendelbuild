// Package testdb resolves where tests find PostgreSQL.
//
// It exists because the answer has to be the same in every package that needs a
// database, and had previously been spelled out separately in each. Tests that
// disagree about the target fail in confusing ways, and a new one is easy to
// write against the wrong default.
package testdb

import "os"

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
