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
		{TargetAtLeast, 1000, "users", "at least 1000 users"},
		{TargetAtMost, 200, "ms p99", "at most 200 ms p99"},
		{TargetAtMost, 0.5, "% error rate", "at most 0.5 % error rate"},
		{TargetAtLeast, 99.9, "%", "at least 99.9 %"},
		// A boolean target has no number worth showing.
		{TargetDone, 1, "", "Done"},
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
	faster := KeyResult{TargetComparator: TargetAtMost, TargetValue: 200, TargetUnit: "ms"}
	if !faster.Met(180) {
		t.Error("180ms should satisfy at most 200ms")
	}
	if faster.Met(240) {
		t.Error("240ms should not satisfy at most 200ms")
	}

	more := KeyResult{TargetComparator: TargetAtLeast, TargetValue: 1000, TargetUnit: "users"}
	if !more.Met(1000) {
		t.Error("at least is inclusive at the boundary")
	}
	if more.Met(999) {
		t.Error("999 should not satisfy at least 1000")
	}
}

// A boolean Key Result stores a target of 1 and records 0 or 1.
func TestBooleanTargetIsMetWhenDone(t *testing.T) {
	kr := KeyResult{TargetComparator: TargetDone, TargetValue: 1}
	if kr.Met(0) {
		t.Error("0 is not done")
	}
	if !kr.Met(1) {
		t.Error("1 is done")
	}
	if !kr.IsBoolean() {
		t.Error("IsBoolean should recognise a done target")
	}
}

// The reason a boolean Key Result is weaker, stated as a rule rather than a
// comment: a number can be compared against the pace needed to reach it, and a
// checkbox says nothing at all until it flips. Anything claiming to know
// whether a boolean KR is "on track" would be inventing the reading.
func TestOnlyNumericTargetsCarryAnEarlySignal(t *testing.T) {
	if !(KeyResult{TargetComparator: TargetAtLeast}).ProgressSignal() {
		t.Error("a numeric target can be judged before it is met")
	}
	if (KeyResult{TargetComparator: TargetDone}).ProgressSignal() {
		t.Error("a boolean target cannot be on track, only met or not yet")
	}
}

// A mode nobody defined must not report a Key Result as met. Of the two ways to
// be wrong, claiming success is the worse one.
func TestUnknownModeIsNeverMet(t *testing.T) {
	kr := KeyResult{TargetComparator: "roughly", TargetValue: 10}
	if kr.Met(10) || kr.Met(1000) {
		t.Error("an unrecognised judgement mode should never report met")
	}
}
