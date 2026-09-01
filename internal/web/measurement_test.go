package web

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

func kr(desc, mode string, value float64, unit string) domain.KeyResult {
	return domain.KeyResult{
		ID: uuid.New(), Description: desc,
		TargetComparator: mode, TargetValue: value, TargetUnit: unit,
	}
}

func TestMeasurementsFromForm(t *testing.T) {
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	users := kr("Weekly active users", domain.TargetAtLeast, 1000, "users")
	latency := kr("Checkout latency", domain.TargetAtMost, 200, "ms p99")
	launched := kr("Public launch", domain.TargetDone, 1, "")
	all := []domain.KeyResult{users, latency, launched}

	form := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	// The distinction the whole form turns on. A key result nobody could
	// measure this week must record nothing: writing a zero would show a
	// collapse where there is only a gap.
	t.Run("a blank row records nothing, not zero", func(t *testing.T) {
		got, err := measurementsFromForm(all, form(map[string]string{
			"kr_" + users.ID.String() + "_value": "820",
		}), now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected only the answered row, got %d", len(got))
		}
		if got[0].KeyResultID != users.ID || got[0].MeasuredValue != 820 {
			t.Errorf("got %+v", got[0])
		}
	})

	// Someone answering on Thursday often knows Monday's figure. Recording it
	// as Thursday's would put the trend line in the wrong place.
	t.Run("a reading can be backdated", func(t *testing.T) {
		got, err := measurementsFromForm(all, form(map[string]string{
			"kr_" + users.ID.String() + "_value": "820",
			"kr_" + users.ID.String() + "_at":    "2026-08-31",
		}), now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got[0].MeasuredAt.Format("2006-01-02") != "2026-08-31" {
			t.Errorf("MeasuredAt = %v, want the supplied date", got[0].MeasuredAt)
		}
	})

	t.Run("an absent date means now", func(t *testing.T) {
		got, _ := measurementsFromForm(all, form(map[string]string{
			"kr_" + latency.ID.String() + "_value": "180",
		}), now)
		if !got[0].MeasuredAt.Equal(now) {
			t.Errorf("MeasuredAt = %v, want %v", got[0].MeasuredAt, now)
		}
	})

	// A boolean is a checkbox: ticked records 1, unticked records nothing at
	// all rather than a 0, because "not done yet" and "we checked and it is
	// not done" are the same thing here and neither is a reading of zero.
	t.Run("a boolean records one when ticked and nothing when not", func(t *testing.T) {
		ticked, _ := measurementsFromForm(all, form(map[string]string{
			"kr_" + launched.ID.String() + "_done": "1",
		}), now)
		if len(ticked) != 1 || ticked[0].MeasuredValue != 1 {
			t.Fatalf("got %+v", ticked)
		}
		unticked, _ := measurementsFromForm(all, form(map[string]string{}), now)
		if len(unticked) != 0 {
			t.Errorf("an unticked box should record nothing, got %+v", unticked)
		}
	})

	t.Run("refuses a value that is not a number", func(t *testing.T) {
		_, err := measurementsFromForm(all, form(map[string]string{
			"kr_" + users.ID.String() + "_value": "lots",
		}), now)
		if err == nil {
			t.Error("a non-numeric reading should be refused, not stored")
		}
		// The message has to name which key result, since the form has many.
		if err != nil && !strings.Contains(err.Error(), "Weekly active users") {
			t.Errorf("error should name the key result: %v", err)
		}
	})

	t.Run("refuses a date it cannot read", func(t *testing.T) {
		_, err := measurementsFromForm(all, form(map[string]string{
			"kr_" + users.ID.String() + "_value": "820",
			"kr_" + users.ID.String() + "_at":    "last tuesday",
		}), now)
		if err == nil {
			t.Error("an unparseable date should be refused")
		}
	})

	t.Run("tolerates how people write numbers", func(t *testing.T) {
		got, err := measurementsFromForm(all, form(map[string]string{
			"kr_" + users.ID.String() + "_value": "1,240",
		}), now)
		if err != nil || got[0].MeasuredValue != 1240 {
			t.Errorf("got %v, err %v", got, err)
		}
	})
}

// The form has to render, and has to put the rows that need answering first.
func TestMeasurementFormRenders(t *testing.T) {
	projectID := uuid.New()
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)

	users := kr("Weekly active users", domain.TargetAtLeast, 1000, "users")
	launched := kr("Public launch", domain.TargetDone, 1, "")
	users.CreatedAt, launched.CreatedAt = old, old

	measured := now.Add(-2 * 24 * time.Hour)
	view := &InputRequestDetailView{
		InputRequest: &domain.InputRequest{
			ID: uuid.New(), ProjectID: projectID,
			Kind:   domain.InputRequestKindMeasurement,
			Title:  "Record this period's key result values",
			Status: domain.InputRequestStatusNeedsAssignment,
		},
		Measurements: []MeasurementRow{
			{KeyResult: launched, Staleness: domain.ReadStaleness(nil, old, now)},
			{
				KeyResult: users,
				Latest:    &domain.KeyResultHistory{MeasuredValue: 820, MeasuredAt: measured},
				Staleness: domain.ReadStaleness(&measured, old, now),
			},
		},
	}

	body := renderForTest(t, "input_request_measurement.html", projectID, view)

	for _, want := range []string{
		"Weekly active users", "at least 1000 users",
		"Public launch", "Done",
		"820", // last reading, shown but not prefilled into the input
		`name="kr_` + users.ID.String() + `_value"`,
		`name="kr_` + users.ID.String() + `_at"`,
		// A boolean gets a checkbox, not a number box.
		`name="kr_` + launched.ID.String() + `_done"`,
		"Never measured",
		"Nothing is blocked on this",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("measurement form missing %q", want)
		}
	}

	// Prefilling the previous figure would collect last week's number again
	// from anyone who taps straight through.
	if strings.Contains(body, `name="kr_`+users.ID.String()+`_value" value=`) {
		t.Error("the current-value box must not be prefilled with the last reading")
	}
}
