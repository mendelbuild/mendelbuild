package web

import "testing"

// The one place a target is validated. Both the OKR editor and the setup
// screen go through it, so they cannot disagree about what a valid target is.
func TestKeyResultTargetFields(t *testing.T) {
	t.Run("the ordinary case", func(t *testing.T) {
		c, v, u, err := keyResultTargetFields(">=", "1000", "users")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c != ">=" || v != 1000 || u != "users" {
			t.Errorf("got %q %v %q", c, v, u)
		}
	})

	// "At least this much" is far and away the commonest target, so a form
	// that does not offer the choice still produces a valid one.
	t.Run("an absent comparison defaults to at-least", func(t *testing.T) {
		c, _, _, err := keyResultTargetFields("", "10", "signups")
		if err != nil || c != ">=" {
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
			_, v, _, err := keyResultTargetFields(">=", raw, "x")
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
			if _, _, _, err := keyResultTargetFields(">=", raw, "users"); err == nil {
				t.Errorf("%q should not be accepted as a target value", raw)
			}
		}
	})

	t.Run("refuses a comparison it cannot make", func(t *testing.T) {
		if _, _, _, err := keyResultTargetFields("~=", "10", "x"); err == nil {
			t.Error("an unknown comparator should be refused, not stored")
		}
	})

	// The unit is display-only, so it takes whatever qualifier the reader
	// needs and is never required.
	t.Run("the unit is free text and optional", func(t *testing.T) {
		_, _, u, err := keyResultTargetFields("<=", "200", " ms p99 ")
		if err != nil || u != "ms p99" {
			t.Errorf("unit = %q, err = %v", u, err)
		}
		if _, _, u, err := keyResultTargetFields("=", "3", ""); err != nil || u != "" {
			t.Errorf("an empty unit should be allowed, got %q / %v", u, err)
		}
	})
}
