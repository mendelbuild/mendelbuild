package domain

import "testing"

// Every value of every status enum must map to a real word. The fallback exists
// for values added after this file, and it must never be what a shipped enum
// value gets — that is how a bare enum reaches a page.
func TestStatusViewsCoverEveryValue(t *testing.T) {
	seen := func(t *testing.T, name string, sv StatusView) {
		t.Helper()
		if sv.Label == "" {
			t.Errorf("%s: empty label", name)
		}
		if len(sv.Label) > 9 && sv.Label[:9] == "Unrecogni" {
			t.Errorf("%s: fell through to the unknown-value fallback", name)
		}
		if sv.Tone == "" {
			t.Errorf("%s: no tone", name)
		}
	}

	for _, s := range []DemoInstanceStatus{
		DemoInstanceStatusStarting, DemoInstanceStatusRunning,
		DemoInstanceStatusStopped, DemoInstanceStatusError,
	} {
		seen(t, "demo "+string(s), DemoStatus(s))
	}

	for _, s := range []VariationRevisionStatus{
		VariationRevisionStatusPending, VariationRevisionStatusInProgress,
		VariationRevisionStatusCompleted, VariationRevisionStatusFailed,
	} {
		seen(t, "revision "+string(s), RevisionStatus(s))
	}

	for _, s := range []HostingDeploymentStatus{
		HostingDeploymentStatusDeploying, HostingDeploymentStatusRunning,
		HostingDeploymentStatusFailed, HostingDeploymentStatusTerminated,
	} {
		seen(t, "deployment "+string(s), DeploymentStatus(s))
	}
}

// The rule that motivates the whole file: a thing that went wrong and a thing
// that finished must not share a tone.
func TestStatusViewsSeparateFailureFromSuccess(t *testing.T) {
	if DemoStatus(DemoInstanceStatusError).Tone == DemoStatus(DemoInstanceStatusRunning).Tone {
		t.Error("a failed demo shares a tone with a running one")
	}
	if DeploymentStatus(HostingDeploymentStatusFailed).Tone == DeploymentStatus(HostingDeploymentStatusRunning).Tone {
		t.Error("a failed deploy shares a tone with a live one")
	}
	if RevisionStatus(VariationRevisionStatusFailed).Tone == RevisionStatus(VariationRevisionStatusCompleted).Tone {
		t.Error("a failed revision shares a tone with an applied one")
	}
}

// A demo that was deliberately torn down did not go wrong. Colouring it as a
// failure is how a reader learns to ignore the failure colour.
func TestStoppedIsNotAFailure(t *testing.T) {
	if got := DemoStatus(DemoInstanceStatusStopped).Tone; got != ToneNeutral {
		t.Errorf("a stopped demo should be neutral, got %q", got)
	}
	if got := DeploymentStatus(HostingDeploymentStatusTerminated).Tone; got != ToneNeutral {
		t.Errorf("a torn-down deployment should be neutral, got %q", got)
	}
}

// An unvalidated channel is an unfinished setup step, not an error.
func TestValidationStatusStates(t *testing.T) {
	cases := []struct {
		name                  string
		validating, validated bool
		errMsg                string
		wantTone              Tone
	}{
		{"never run", false, false, "", ToneNeutral},
		{"running now", true, false, "", ToneProgress},
		{"succeeded", false, true, "", ToneSuccess},
		{"failed", false, false, "boom", ToneFailure},
		// A channel that failed once and has since been validated is validated.
		{"failed then succeeded", false, true, "boom", ToneSuccess},
	}
	for _, c := range cases {
		got := ValidationStatus(c.validating, c.validated, c.errMsg)
		if got.Tone != c.wantTone {
			t.Errorf("%s: tone = %q, want %q", c.name, got.Tone, c.wantTone)
		}
	}
}

// A Hop in a table and the same Hop on its own page must not disagree, which is
// why both read from the Ribbon rather than from the status column.
func TestBadgeViewsAgreeWithRibbons(t *testing.T) {
	h := &Hop{Status: HopStatusSelecting}
	if got, want := HopStatusView(h), HopLifecycle(h, nil); got.Tone != want.Tone || got.Label != want.Headline {
		t.Errorf("hop badge %+v disagrees with its ribbon (%q/%q)", got, want.Headline, want.Tone)
	}

	v := &Variation{Status: VariationStatusTerminated}
	if got, want := VariationStatusView(v, nil, nil), VariationLifecycle(v, nil, nil); got.Tone != want.Tone {
		t.Errorf("variation badge tone %q disagrees with its ribbon %q", got.Tone, want.Tone)
	}
}
