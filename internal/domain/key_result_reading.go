package domain

import "time"

// A Key Result read against its measurements and the clock.
//
// The reading this produces is the one the timeline draws, and every rule here
// exists so that it never claims more than the data supports. The two things it
// refuses to do are worth stating up front: it will not report progress for a
// target it has no baseline for, and it will not report a boolean as on track,
// because a checkbox says nothing until it flips.

// KeyResultReading is where a Key Result stands.
type KeyResultReading struct {
	// Status is how it is doing: met, on track, behind, or not known.
	Status StatusView
	// Staleness is how old the reading is, which is a separate question from
	// how good it is. A Key Result can be comfortably ahead on a number nobody
	// has checked for a month, and the page must be able to say both.
	Staleness MeasurementStaleness

	// Progress is how much of the goal is done, 0 to 1. Only meaningful when
	// HasProgress; otherwise there was no baseline to measure from.
	Progress    float64
	HasProgress bool

	// Elapsed is how much of the time is gone, 0 to 1, from the day the Key
	// Result was written to the day it is due. Comparing it against Progress is
	// the whole reading: fill ahead of the line is on track.
	Elapsed    float64
	HasElapsed bool

	Met    bool
	Latest *KeyResultHistory
}

// FillTone is how to colour the progress bar, and empty when there is nothing
// honest to draw.
//
// Staleness outranks being on track. A number a fortnight old cannot support
// "on track" -- it supports "was on track a fortnight ago", which is a
// different claim and not one a green bar makes.
func (r KeyResultReading) FillTone() Tone {
	switch {
	case !r.HasProgress:
		return ""
	case r.Met:
		return ToneSuccess
	case r.Staleness.Stale:
		return ToneWaiting
	case r.OnTrack():
		return ToneSuccess
	default:
		return ToneFailure
	}
}

// OnTrack reports whether the goal is at least as far along as the time is.
func (r KeyResultReading) OnTrack() bool {
	if !r.HasProgress || !r.HasElapsed {
		return false
	}
	return r.Progress >= r.Elapsed
}

// ReadKeyResult judges a Key Result.
//
// first and latest are its earliest and most recent measurements, either of
// which may be nil. They are passed rather than looked up so that this stays a
// function of its inputs, and so every state it can produce is reachable in a
// test rather than only in a database.
func ReadKeyResult(kr KeyResult, first, latest *KeyResultHistory, now time.Time) KeyResultReading {
	r := KeyResultReading{Latest: latest}

	var measuredAt *time.Time
	if latest != nil {
		measuredAt = &latest.MeasuredAt
	}
	r.Staleness = ReadStaleness(measuredAt, kr.CreatedAt, now)

	if latest != nil {
		r.Met = kr.Met(latest.MeasuredValue)
	}

	// Elapsed needs a deadline. A Key Result without one cannot be behind,
	// because there is nothing for it to be behind.
	if kr.TargetDate != nil {
		total := kr.TargetDate.Sub(kr.CreatedAt)
		if total > 0 {
			r.Elapsed = clamp01(now.Sub(kr.CreatedAt).Seconds() / total.Seconds())
			r.HasElapsed = true
		}
	}

	// A boolean has no progress to report, ever. That is the whole reason it is
	// the weaker kind of Key Result, and drawing a half-full bar for one would
	// invent the very signal it does not have.
	if kr.IsBoolean() {
		switch {
		case r.Met:
			r.Status = StatusView{"Done", ToneSuccess}
		default:
			r.Status = StatusView{"Not yet", ToneNeutral}
		}
		return r
	}

	if latest == nil {
		r.Status = r.Staleness.StatusView()
		return r
	}

	if r.Met {
		r.Progress, r.HasProgress = 1, true
		r.Status = StatusView{"Met", ToneSuccess}
		return r
	}

	baseline, ok := progressBaseline(kr, first, latest)
	if !ok {
		// Measured, but there is no starting point to measure from yet. Saying
		// "0% done" here would report a standstill where the truth is that we
		// have only looked once.
		r.Status = StatusView{"Measured, no trend yet", ToneNeutral}
		return r
	}

	span := kr.TargetValue - baseline
	if span == 0 {
		// It started at its target and is no longer meeting it.
		r.Progress, r.HasProgress = 0, true
	} else {
		r.Progress = clamp01((latest.MeasuredValue - baseline) / span)
		r.HasProgress = true
	}

	switch {
	case r.Staleness.Stale:
		r.Status = StatusView{"Out of date", ToneWaiting}
	case r.OnTrack():
		r.Status = StatusView{"On track", ToneSuccess}
	case r.HasElapsed:
		r.Status = StatusView{"Behind", ToneFailure}
	default:
		r.Status = StatusView{"Measured", ToneNeutral}
	}
	return r
}

// progressBaseline is the value progress is counted from.
//
// For a target that should grow, it is zero: you start with no users, no
// signups, no revenue, and "820 of 1000" is a reading anyone would recognise.
//
// For a target that should shrink there is no such natural floor -- latency
// does not start at zero and improve upwards -- so the baseline is where it
// actually started, which is the first measurement. That means a shrinking
// target shows no progress until it has been measured twice, and it should:
// one reading of 260ms against a 200ms goal says nothing about whether the
// number is falling.
func progressBaseline(kr KeyResult, first, latest *KeyResultHistory) (float64, bool) {
	if kr.TargetComparator == TargetAtLeast {
		return 0, true
	}
	if first == nil || latest == nil || !first.MeasuredAt.Before(latest.MeasuredAt) {
		return 0, false
	}
	return first.MeasuredValue, true
}

func clamp01(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	default:
		return f
	}
}

// Attainment counts how a strategy's Key Results are doing, for the line that
// sits beside spend.
//
// Counts rather than a percentage: turning measurements into "43% complete"
// needs an assumption about the shape of every growth curve, and counting needs
// none. A Key Result nobody has measured counts toward neither figure, because
// not looking at something is not evidence that it is going well.
type Attainment struct {
	Met     int
	OnTrack int // met, or measured and keeping pace
	Total   int
}

// ReadAttainment tallies a set of readings.
func ReadAttainment(readings []KeyResultReading) Attainment {
	a := Attainment{Total: len(readings)}
	for _, r := range readings {
		if r.Met {
			a.Met++
			a.OnTrack++
			continue
		}
		if r.OnTrack() && !r.Staleness.Stale {
			a.OnTrack++
		}
	}
	return a
}

// MetFraction and OnTrackFraction size the bar beside the spend meter.
func (a Attainment) MetFraction() float64     { return fraction(a.Met, a.Total) }
func (a Attainment) OnTrackFraction() float64 { return fraction(a.OnTrack, a.Total) }

func fraction(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total)
}
