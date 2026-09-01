package web

import (
	"testing"

	"github.com/bhs/mendelbuild/internal/domain"
)

// The one place a target is validated. Both the OKR editor and the setup
// screen go through it, so they cannot disagree about what a valid target is.
func TestKeyResultTargetFields(t *testing.T) {
	t.Run("the ordinary case", func(t *testing.T) {
		c, v, u, err := keyResultTargetFields(domain.TargetAtLeast, "1000", "users")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c != domain.TargetAtLeast || v != 1000 || u != "users" {
			t.Errorf("got %q %v %q", c, v, u)
		}
	})

	// "At least this much" is far and away the commonest target, so a form
	// that does not offer the choice still produces a valid one.
	t.Run("an absent comparison defaults to at-least", func(t *testing.T) {
		c, _, _, err := keyResultTargetFields("", "10", "signups")
		if err != nil || c != domain.TargetAtLeast {
			t.Errorf("comparator = %q, err = %v", c, err)
		}
	})

	// People type what they read. None of these should be rejected for
	// punctuation the app can strip.
	t.Run("tolerates how numbers are written", func(t *testing.T) {
		for raw, want := range map[string]float64{
			"1,000":  1000,
			"$2500":  2500,
			" 99.9 ": 99.9,
			"0.5":    0.5,
		} {
			_, v, _, err := keyResultTargetFields(domain.TargetAtLeast, raw, "x")
			if err != nil || v != want {
				t.Errorf("%q -> %v (err %v), want %v", raw, v, err, want)
			}
		}
	})

	// A target that is not a number is the thing this whole change removes.
	// Accepting one would put the app back where it started: a Key Result that
	// can be displayed and never judged.
	t.Run("refuses a target that is not a number", func(t *testing.T) {
		for _, raw := range []string{"", "lots", "100 users", "~100"} {
			if _, _, _, err := keyResultTargetFields(domain.TargetAtLeast, raw, "users"); err == nil {
				t.Errorf("%q should not be accepted as a target value", raw)
			}
		}
	})

	// The boolean mode discards the number and unit rather than storing
	// whatever the form happened to carry in fields it does not use.
	t.Run("a boolean target keeps no number or unit", func(t *testing.T) {
		c, v, u, err := keyResultTargetFields(domain.TargetDone, "500", "widgets")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c != domain.TargetDone || v != 1 || u != "" {
			t.Errorf("got %q %v %q, want done/1/empty", c, v, u)
		}
	})

	t.Run("refuses a judgement mode it cannot make", func(t *testing.T) {
		if _, _, _, err := keyResultTargetFields("roughly", "10", "x"); err == nil {
			t.Error("an unknown judgement mode should be refused, not stored")
		}
	})

	// The unit is display-only, so it takes whatever qualifier the reader
	// needs and is never required.
	t.Run("the unit is free text and optional", func(t *testing.T) {
		_, _, u, err := keyResultTargetFields(domain.TargetAtMost, "200", " ms p99 ")
		if err != nil || u != "ms p99" {
			t.Errorf("unit = %q, err = %v", u, err)
		}
		if _, _, u, err := keyResultTargetFields(domain.TargetAtLeast, "3", ""); err != nil || u != "" {
			t.Errorf("an empty unit should be allowed, got %q / %v", u, err)
		}
	})
}
