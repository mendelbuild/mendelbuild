package domain

import "time"

// When Mendel asks for Key Result measurements, and when it starts insisting.
//
// See dev/claude_plans/15_key_result_measurement.md.

// MeasurementCadence is how often a project is asked for its Key Result
// values.
//
// Weekly, for every Key Result, and not settable per Key Result. That is a
// position rather than a simplification: a Key Result that can only be measured
// at the end of the quarter cannot tell anyone whether the work is going well
// while there is still time to change it, so the right response to "this one
// can only be measured quarterly" is a better Key Result, not a slower ask.
const MeasurementCadence = 7 * 24 * time.Hour

// MeasurementEscalation is how long an unanswered ask waits before it stops
// being a quiet request and starts claiming the reader's attention: twice the
// cadence, so one missed week is forgiven and two is not.
const MeasurementEscalation = 2 * MeasurementCadence

// MeasurementDue reports whether a project should be asked for measurements.
//
// askedAt is when the last request was filed, nil if none ever was. It is
// deliberately not the open request's created_at: a request updated in place as
// the period rolls over must not read as a fresh one.
func MeasurementDue(askedAt *time.Time, now time.Time) bool {
	if askedAt == nil {
		return true
	}
	return now.Sub(*askedAt) >= MeasurementCadence
}

// MeasurementStaleness describes how out of date a Key Result's value is.
type MeasurementStaleness struct {
	// Measured reports whether there is any measurement at all. A Key Result
	// nobody has ever measured is a different situation from one measured a
	// while ago, and the two must not be shown alike.
	Measured bool
	// Stale reports that nothing has been recorded within one cadence, so the
	// value on screen is older than the rhythm the project agreed to.
	Stale bool
	// Overdue reports that nothing has been recorded within two, which is when
	// the request escalates into the reader's way.
	Overdue bool
	// Age is how long since the last measurement, zero when there is none.
	Age time.Duration
}

// ReadStaleness judges a Key Result's latest measurement against the clock.
//
// createdAt is the Key Result's own age, used when it has never been measured:
// a Key Result written this morning is not overdue, and one written a month ago
// and never measured since plainly is.
func ReadStaleness(measuredAt *time.Time, createdAt time.Time, now time.Time) MeasurementStaleness {
	if measuredAt == nil {
		since := now.Sub(createdAt)
		return MeasurementStaleness{
			Measured: false,
			Stale:    since >= MeasurementCadence,
			Overdue:  since >= MeasurementEscalation,
		}
	}
	age := now.Sub(*measuredAt)
	return MeasurementStaleness{
		Measured: true,
		Stale:    age >= MeasurementCadence,
		Overdue:  age >= MeasurementEscalation,
		Age:      age,
	}
}

// StatusView renders staleness for a reader. It says nothing about whether the
// Key Result is being met -- that is a separate question, and one this cannot
// answer when the number is old.
func (m MeasurementStaleness) StatusView() StatusView {
	switch {
	case !m.Measured && m.Overdue:
		return StatusView{"Never measured", ToneWarning}
	case !m.Measured:
		return StatusView{"Not measured yet", ToneNeutral}
	case m.Overdue:
		return StatusView{"Badly out of date", ToneWarning}
	case m.Stale:
		return StatusView{"Out of date", ToneWaiting}
	default:
		return StatusView{"Up to date", ToneSuccess}
	}
}

// MeasurementImportance scores the recurring ask for the queue.
//
// It starts low: nothing is blocked on it, and a request that shouts on the day
// it is filed teaches people to ignore the queue. Once it is overdue it
// outranks most things, because by then the project has been running for a
// fortnight on numbers nobody has checked.
func MeasurementImportance(overdue bool) float64 {
	if overdue {
		return 0.8
	}
	return 0.2
}
