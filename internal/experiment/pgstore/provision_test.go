package pgstore

import (
	"strings"
	"testing"
)

// A sandbox's name has to survive being an identifier, be recognisable as
// Mendel's, and stay distinct from another experiment's. Truncation is the one
// that bites silently: Postgres cuts identifiers at 63 bytes without saying so,
// and two long experiment names sharing a database would stop being sandboxed
// from each other with nothing to notice.
func TestVerifyDatabaseNameIsLegalAndDistinct(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"exp-a1b2", "mendel_verify_exp_a1b2"},
		{"Checkout Redesign", "mendel_verify_checkout_redesign"},
		{"--weird--", "mendel_verify_weird"},
	} {
		if got := VerifyDatabaseName(tc.in); got != tc.want {
			t.Errorf("VerifyDatabaseName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	long := VerifyDatabaseName(strings.Repeat("experiment", 12))
	if len(long) > 63 {
		t.Errorf("a name Postgres would truncate silently reached it anyway: %d bytes", len(long))
	}
	if !strings.HasPrefix(long, "mendel_verify_") {
		t.Errorf("truncation ate the prefix a reader identifies it by: %q", long)
	}

	// And truncation must not reintroduce the collision it exists to prevent.
	// Two names differing only past the cut are the case that matters, because
	// they are exactly what a per-experiment sandbox must keep apart.
	base := strings.Repeat("experiment", 12)
	if a, b := VerifyDatabaseName(base+"alpha"), VerifyDatabaseName(base+"beta"); a == b {
		t.Errorf("two long names that differ only past the truncation both got %q", a)
	}

	// An empty name must not collapse to the bare prefix, or two experiments
	// with unusable names would land in one database.
	if a, b := VerifyDatabaseName("!!"), VerifyDatabaseName("??"); a == b {
		t.Errorf("two unnameable experiments both got %q", a)
	}
}

// The sandbox is a different database on the same server, so everything except
// the database name has to survive: credentials, host, port, and the options
// that decide whether the connection works at all.
func TestReplaceDatabaseKeepsEverythingElse(t *testing.T) {
	for _, tc := range []struct{ in, db, want string }{
		{
			"postgres://u:p@host:5432/app", "mendel_verify_x",
			"postgres://u:p@host:5432/mendel_verify_x",
		},
		{
			"postgres://u:p@host:5432/app?sslmode=require", "mendel_verify_x",
			"postgres://u:p@host:5432/mendel_verify_x?sslmode=require",
		},
	} {
		if got := ReplaceDatabase(tc.in, tc.db); got != tc.want {
			t.Errorf("ReplaceDatabase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// sslmode is the one that matters: dropping it turns a required-TLS
	// connection into a refused one, and the failure reads as "cannot connect"
	// rather than as anything about the query string.
	if got := ReplaceDatabase("postgres://u:p@host/app?sslmode=require", "x"); !strings.Contains(got, "sslmode=require") {
		t.Errorf("the connection options were dropped: %q", got)
	}
}

// An identifier that could carry a quote must not be able to end the quoting.
func TestQuoteIdentCannotBeEscaped(t *testing.T) {
	if got := quoteIdent(`a"b`); got != `"a""b"` {
		t.Errorf("quoteIdent = %s", got)
	}
}

// Without a server connection the answer is that Mendel cannot provision, which
// is a designed outcome the caller reports rather than an error to retry.
func TestNoConnectionIsCannotProvisionRatherThanACrash(t *testing.T) {
	p := NewProvisioner(nil, "")
	if err := p.CanProvision(t.Context()); err == nil {
		t.Fatal("a provisioner with no connection cannot provision")
	}
	if _, _, err := p.Provision(t.Context(), "exp"); err == nil {
		t.Fatal("expected a refusal")
	}
}
