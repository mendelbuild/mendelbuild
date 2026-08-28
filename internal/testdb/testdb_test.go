package testdb

import "testing"

// The default is the whole point of this package: without it, a worktree with
// no settings file cannot run the database-backed tests at all.
func TestConnStringFallsBackToTheDefault(t *testing.T) {
	t.Setenv("MENDEL_TEST_DB_URL", "")
	if got := ConnString(); got != DefaultConnString {
		t.Errorf("ConnString() = %q, want the default %q", got, DefaultConnString)
	}
}

// The override has to survive, or a server on another host or port becomes
// unreachable for tests.
func TestConnStringPrefersTheEnvironment(t *testing.T) {
	const custom = "postgres://elsewhere:6000/other?sslmode=disable"
	t.Setenv("MENDEL_TEST_DB_URL", custom)
	if got := ConnString(); got != custom {
		t.Errorf("ConnString() = %q, want the override %q", got, custom)
	}
}
