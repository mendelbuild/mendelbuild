package domain

import (
	"testing"
	"time"
)

var readNow = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

func daysAgo(d int) time.Time { return readNow.AddDate(0, 0, -d) }

func measured(v float64, d int) *KeyResultHistory {
	at := daysAgo(d)
	return &KeyResultHistory{MeasuredValue: v, MeasuredAt: at}
}

func growing(target float64, createdDaysAgo, dueInDays int) KeyResult {
	due := readNow.AddDate(0, 0, dueInDays)
	return KeyResult{
		TargetComparator: TargetAtLeast, TargetValue: target, TargetUnit: "users",
		CreatedAt: daysAgo(createdDaysAgo), TargetDate: &due,
	}
}

func shrinking(target float64, createdDaysAgo, dueInDays int) KeyResult {
	due := readNow.AddDate(0, 0, dueInDays)
	return KeyResult{
		TargetComparator: TargetAtMost, TargetValue: target, TargetUnit: "ms",
		CreatedAt: daysAgo(createdDaysAgo), TargetDate: &due,
	}
}

// A growing target counts from zero, which is both true and the reading anyone
// would expect: "820 of 1000".
func TestGrowingTargetCountsFromZero(t *testing.T) {
	kr := growing(1000, 50, 50) // half the time gone
	r := ReadKeyResult(kr, measured(100, 40), measured(820, 1), readNow)

	if !r.HasProgress {
		t.Fatal("a growing target has a baseline from the first reading onward")
	}
	if r.Progress < 0.81 || r.Progress > 0.83 {
		t.Errorf("Progress = %v, want about 0.82", r.Progress)
	}
	if !r.OnTrack() {
		t.Errorf("82%% of the goal with 50%% of the time gone is on track (elapsed %v)", r.Elapsed)
	}
	if r.Status.Label != "On track" {
		t.Errorf("Status = %q", r.Status.Label)
	}
}

func TestGrowingTargetBehindPace(t *testing.T) {
	kr := growing(1000, 80, 20) // 80% of the time gone
	r := ReadKeyResult(kr, measured(50, 70), measured(300, 1), readNow)

	if r.OnTrack() {
		t.Error("30% of the goal with 80% of the time gone is behind")
	}
	if r.Status.Label != "Behind" || r.Status.Tone != ToneFailure {
		t.Errorf("Status = %+v", r.Status)
	}
}

// A shrinking target has no natural zero: latency does not start at zero and
// improve upwards. One reading of 260ms against a 200ms goal says nothing about
// whether the number is falling, and reporting "0% done" would claim a
// standstill where the truth is that we have looked once.
func TestShrinkingTargetNeedsTwoReadings(t *testing.T) {
	kr := shrinking(200, 40, 40)

	only := measured(260, 1)
	r := ReadKeyResult(kr, only, only, readNow)
	if r.HasProgress {
		t.Error("one reading of a shrinking target is not a trend")
	}
	if r.Status.Label != "Measured, no trend yet" {
		t.Errorf("Status = %q", r.Status.Label)
	}

	// With a starting point, progress is how far it has come from there.
	r = ReadKeyResult(kr, measured(400, 30), measured(260, 1), readNow)
	if !r.HasProgress {
		t.Fatal("two readings give a shrinking target its baseline")
	}
	if r.Progress < 0.69 || r.Progress > 0.71 {
		t.Errorf("400 to 260 against a 200 target is about 70%%, got %v", r.Progress)
	}
}

// Being met is the one reading that needs no baseline at all.
func TestMetNeedsNoBaseline(t *testing.T) {
	kr := shrinking(200, 40, 40)
	only := measured(180, 1)
	r := ReadKeyResult(kr, only, only, readNow)

	if !r.Met || !r.HasProgress || r.Progress != 1 {
		t.Errorf("a met target is complete regardless of where it started: %+v", r)
	}
	if r.Status.Label != "Met" {
		t.Errorf("Status = %q", r.Status.Label)
	}
}

// A stale reading cannot support "on track". It supports "was on track a
// fortnight ago", which is a different claim and not one a green bar makes.
func TestStalenessOutranksBeingOnTrack(t *testing.T) {
	kr := growing(1000, 50, 50)
	r := ReadKeyResult(kr, measured(100, 40), measured(900, 20), readNow)

	if !r.OnTrack() {
		t.Fatal("the numbers say on track")
	}
	if got := r.FillTone(); got != ToneWaiting {
		t.Errorf("a stale reading should not be drawn as success, got %q", got)
	}
	if r.Status.Label != "Out of date" {
		t.Errorf("Status = %q", r.Status.Label)
	}
}

// The payoff of the earlier decision, as a rule: a boolean never reports
// progress, so the timeline cannot draw a half-full bar for one.
func TestBooleanNeverShowsProgress(t *testing.T) {
	due := readNow.AddDate(0, 0, 40)
	kr := KeyResult{TargetComparator: TargetDone, TargetValue: 1,
		CreatedAt: daysAgo(40), TargetDate: &due}

	notYet := ReadKeyResult(kr, nil, nil, readNow)
	if notYet.HasProgress || notYet.OnTrack() {
		t.Error("a boolean has no early signal to report")
	}
	if notYet.Status.Label != "Not yet" {
		t.Errorf("Status = %q", notYet.Status.Label)
	}

	done := ReadKeyResult(kr, measured(1, 1), measured(1, 1), readNow)
	if !done.Met || done.Status.Label != "Done" {
		t.Errorf("a ticked boolean is done: %+v", done)
	}
	if done.HasProgress {
		t.Error("even a met boolean draws no bar; it was never a quantity")
	}
}

func TestNeverMeasuredSaysSo(t *testing.T) {
	kr := growing(1000, 40, 60)
	r := ReadKeyResult(kr, nil, nil, readNow)
	if r.HasProgress {
		t.Error("nothing measured, nothing to draw")
	}
	if r.Status.Label != "Never measured" {
		t.Errorf("Status = %q", r.Status.Label)
	}
}

// Counts, not percentages, and an unmeasured Key Result counts toward neither:
// not looking at something is not evidence that it is going well.
func TestAttainmentCounts(t *testing.T) {
	growingKR := growing(1000, 50, 50)
	shrinkingKR := shrinking(200, 50, 50)

	readings := []KeyResultReading{
		ReadKeyResult(growingKR, measured(10, 40), measured(1000, 1), readNow),  // met
		ReadKeyResult(growingKR, measured(10, 40), measured(900, 1), readNow),   // on track
		ReadKeyResult(growingKR, measured(10, 40), measured(100, 1), readNow),   // behind
		ReadKeyResult(growingKR, nil, nil, readNow),                             // never measured
		ReadKeyResult(shrinkingKR, measured(400, 40), measured(390, 20), readNow), // stale
	}

	a := ReadAttainment(readings)
	if a.Total != 5 {
		t.Fatalf("Total = %d", a.Total)
	}
	if a.Met != 1 {
		t.Errorf("Met = %d, want 1", a.Met)
	}
	if a.OnTrack != 2 {
		t.Errorf("OnTrack = %d, want 2 (the met one and the one keeping pace)", a.OnTrack)
	}
	if a.MetFraction() != 0.2 || a.OnTrackFraction() != 0.4 {
		t.Errorf("fractions = %v / %v", a.MetFraction(), a.OnTrackFraction())
	}
}

// No key results at all must not divide by zero.
func TestAttainmentOfNothing(t *testing.T) {
	a := ReadAttainment(nil)
	if a.MetFraction() != 0 || a.OnTrackFraction() != 0 {
		t.Error("an empty strategy has no attainment, not a NaN")
	}
}
