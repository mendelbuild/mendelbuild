package web

import (
	"strings"
	"testing"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
)

// Starting a probe costs a build and a pod in someone else's cluster, so the
// decision to spend that is worth checking on its own.

func answeredAt(t time.Time) *domain.AdapterInvocation {
	return &domain.AdapterInvocation{
		Phase: "probe", Outcome: domain.AdapterOutcomeCompleted,
		ReportedAt: &t, ExpiresAt: t,
	}
}

func failedAt(t time.Time) *domain.AdapterInvocation {
	return &domain.AdapterInvocation{
		Phase: "probe", Outcome: domain.AdapterOutcomeFailed,
		ReportedAt: &t, ExpiresAt: t,
	}
}

// Not knowing is the thing a probe fixes, and both ways of not knowing count:
// never asked, and asked but never heard from.
func TestNotKnowingIsWhatStartsAProbe(t *testing.T) {
	now := time.Now()

	if d := ShouldProbe(nil, now, false); !d.Start {
		t.Errorf("a project nobody has probed should be probed: %s", d.Because)
	}

	abandoned := &domain.AdapterInvocation{Phase: "probe", ExpiresAt: now.Add(-time.Hour)}
	if d := ShouldProbe(abandoned, now, false); !d.Start {
		t.Errorf("a probe that never reported leaves Mendel not knowing: %s", d.Because)
	}
}

// Two jobs against one datastore is the one case where starting another is
// harmful rather than merely wasteful, so even asking explicitly does not do it.
func TestARunningProbeIsNotJoinedByAnother(t *testing.T) {
	now := time.Now()
	running := &domain.AdapterInvocation{Phase: "probe", ExpiresAt: now.Add(time.Hour)}

	for _, forced := range []bool{false, true} {
		d := ShouldProbe(running, now, forced)
		if d.Start {
			t.Errorf("forced=%v: started a second probe while one was running", forced)
		}
		if !strings.Contains(d.Because, "already running") {
			t.Errorf("forced=%v: the reason should say one is on its way, got %q", forced, d.Because)
		}
	}
}

// A recent answer stands. The question is what engine this is, which changes
// when someone changes it rather than on a timer.
func TestARecentAnswerStands(t *testing.T) {
	now := time.Now()

	if d := ShouldProbe(answeredAt(now.Add(-time.Hour)), now, false); d.Start {
		t.Error("an answer an hour old was thrown away")
	}
	if d := ShouldProbe(answeredAt(now.Add(-ProbeStaleAfter-time.Minute)), now, false); !d.Start {
		t.Errorf("an answer past its window should be refreshed: %s", d.Because)
	}
}

// A failure is not a finding, so it is worth asking again -- but on the same
// schedule as anything else. Retrying at once turns a datastore that is down
// into a build every few seconds.
func TestAFailureIsRetriedOnTheScheduleRatherThanImmediately(t *testing.T) {
	now := time.Now()

	d := ShouldProbe(failedAt(now.Add(-time.Minute)), now, false)
	if d.Start {
		t.Error("a failure a minute old was retried immediately")
	}
	if !strings.Contains(d.Because, "every few seconds") {
		t.Errorf("the reason should say why it waits, got %q", d.Because)
	}

	if d := ShouldProbe(failedAt(now.Add(-ProbeStaleAfter-time.Minute)), now, false); !d.Start {
		t.Errorf("an old failure should be tried again: %s", d.Because)
	}
}

// Someone who has just fixed a grant should not wait twelve hours to find out.
func TestAskingExplicitlyOverridesTheSchedule(t *testing.T) {
	now := time.Now()

	if d := ShouldProbe(answeredAt(now.Add(-time.Minute)), now, true); !d.Start {
		t.Errorf("asking for a refresh should refresh: %s", d.Because)
	}
	if d := ShouldProbe(failedAt(now.Add(-time.Minute)), now, true); !d.Start {
		t.Errorf("asking after a failure should retry: %s", d.Because)
	}
}

// Doing nothing always says which of the several good reasons applied. "Nothing
// happened when I asked" is the hardest thing to diagnose from outside.
func TestNotStartingAlwaysSaysWhy(t *testing.T) {
	now := time.Now()
	for _, latest := range []*domain.AdapterInvocation{
		{Phase: "probe", ExpiresAt: now.Add(time.Hour)},
		answeredAt(now.Add(-time.Minute)),
		failedAt(now.Add(-time.Minute)),
	} {
		if d := ShouldProbe(latest, now, false); !d.Start && strings.TrimSpace(d.Because) == "" {
			t.Errorf("declined to probe %+v without saying why", latest)
		}
	}
}
