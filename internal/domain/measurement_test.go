package domain

import (
	"testing"
	"time"
)

func TestMeasurementDue(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }

	// A project that has never been asked is due immediately: the first ask is
	// how anyone learns the request exists.
	if !MeasurementDue(nil, now) {
		t.Error("a project never asked should be due")
	}
	if MeasurementDue(ago(3*24*time.Hour), now) {
		t.Error("three days into a weekly cadence is not due")
	}
	if !MeasurementDue(ago(8*24*time.Hour), now) {
		t.Error("eight days into a weekly cadence is due")
	}
	// Exactly on the boundary counts, so a weekly ask does not drift a day
	// later every time it is answered promptly.
	if !MeasurementDue(ago(MeasurementCadence), now) {
		t.Error("the cadence boundary should be due")
	}
}

func TestReadStaleness(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	at := func(d time.Duration) *time.Time { t := ago(d); return &t }

	t.Run("fresh", func(t *testing.T) {
		s := ReadStaleness(at(2*24*time.Hour), ago(30*24*time.Hour), now)
		if !s.Measured || s.Stale || s.Overdue {
			t.Errorf("%+v: two days old is current", s)
		}
		if got := s.StatusView().Tone; got != ToneSuccess {
			t.Errorf("tone = %q", got)
		}
	})

	t.Run("one missed week is forgiven, two is not", func(t *testing.T) {
		one := ReadStaleness(at(8*24*time.Hour), ago(30*24*time.Hour), now)
		if !one.Stale || one.Overdue {
			t.Errorf("%+v: eight days is stale but not yet overdue", one)
		}
		two := ReadStaleness(at(15*24*time.Hour), ago(30*24*time.Hour), now)
		if !two.Overdue {
			t.Errorf("%+v: a fortnight is overdue", two)
		}
	})

	// Never measured and measured-a-while-ago are different situations and must
	// not read alike: one is a gap in the record, the other is a gap in habit.
	t.Run("never measured is its own state", func(t *testing.T) {
		fresh := ReadStaleness(nil, ago(2*24*time.Hour), now)
		if fresh.Measured {
			t.Error("no measurement means not measured")
		}
		if fresh.Stale || fresh.Overdue {
			t.Error("a key result written two days ago is not overdue")
		}
		if got := fresh.StatusView().Label; got != "Not measured yet" {
			t.Errorf("label = %q", got)
		}

		old := ReadStaleness(nil, ago(40*24*time.Hour), now)
		if !old.Overdue {
			t.Error("a key result written a month ago and never measured is overdue")
		}
		if got := old.StatusView().Label; got != "Never measured" {
			t.Errorf("label = %q", got)
		}
	})
}

// The ask is quiet until it is not. A request that shouts on the day it is
// filed teaches people to ignore the queue.
func TestMeasurementImportanceRisesWhenOverdue(t *testing.T) {
	quiet, loud := MeasurementImportance(false), MeasurementImportance(true)
	if quiet >= loud {
		t.Errorf("an overdue ask should outrank a fresh one: %v vs %v", quiet, loud)
	}
	if DecisionImportance(quiet) != "Low" {
		t.Errorf("a fresh ask should read as low importance, got %q", DecisionImportance(quiet))
	}
	if DecisionImportance(loud) != "High" {
		t.Errorf("an overdue ask should read as high importance, got %q", DecisionImportance(loud))
	}
}
