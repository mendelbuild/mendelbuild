package domain

import "testing"

// The prose form is derived from the number, so the two cannot drift apart --
// which is what a single free-text target column allowed.
func TestKeyResultTargetReadsAsAPhrase(t *testing.T) {
	cases := []struct {
		comparator string
		value      float64
		unit       string
		want       string
	}{
		{">=", 1000, "users", "≥ 1000 users"},
		{"<=", 200, "ms p99", "≤ 200 ms p99"},
		{"<", 0.5, "% error rate", "< 0.5 % error rate"},
		{">", 99.9, "%", "> 99.9 %"},
		// An exact target reads better without the operator: "3 releases", not
		// "= 3 releases".
		{"=", 3, "releases", "3 releases"},
	}
	for _, c := range cases {
		kr := KeyResult{TargetComparator: c.comparator, TargetValue: c.value, TargetUnit: c.unit}
		if got := kr.Target(); got != c.want {
			t.Errorf("Target() = %q, want %q", got, c.want)
		}
	}
}

// Met has to respect the direction of the comparison. A latency target is hit
// by going down, and treating every target as "bigger is better" would report
// a slow service as having met its speed goal.
func TestMetRespectsDirection(t *testing.T) {
	faster := KeyResult{TargetComparator: "<=", TargetValue: 200, TargetUnit: "ms"}
	if !faster.Met(180) {
		t.Error("180ms should satisfy <= 200ms")
	}
	if faster.Met(240) {
		t.Error("240ms should not satisfy <= 200ms")
	}

	more := KeyResult{TargetComparator: ">=", TargetValue: 1000, TargetUnit: "users"}
	if !more.Met(1000) {
		t.Error(">= is inclusive at the boundary")
	}
	if more.Met(999) {
		t.Error("999 should not satisfy >= 1000")
	}

	strict := KeyResult{TargetComparator: ">", TargetValue: 1000}
	if strict.Met(1000) {
		t.Error("> is exclusive at the boundary")
	}
}

// A comparator nobody defined must not report a Key Result as met. Of the two
// ways to be wrong, claiming success is the worse one.
func TestUnknownComparatorIsNeverMet(t *testing.T) {
	kr := KeyResult{TargetComparator: "~=", TargetValue: 10}
	if kr.Met(10) || kr.Met(1000) {
		t.Error("an unrecognised comparator should never report met")
	}
}
