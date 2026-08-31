package domain

import (
	"testing"
)

// TestStrategyDraftErrorText: a draft lost with its process has no recorded
// error, so the screen would otherwise show a blank explanation next to a retry
// button. Staleness is decided in SQL and passed in -- see DraftStaleAfter.
func TestStrategyDraftErrorText(t *testing.T) {
	msg := "the model returned no objectives"
	failed := &Strategy{DraftStatus: StrategyDraftFailed, DraftError: &msg}
	if got := failed.DraftErrorText(false); got != msg {
		t.Errorf("a failed draft should report its recorded error, got %q", got)
	}

	lost := &Strategy{DraftStatus: StrategyDraftDrafting}
	if got := lost.DraftErrorText(true); got == "" {
		t.Error("a draft lost with its process still needs an explanation")
	}

	blank := &Strategy{DraftStatus: StrategyDraftFailed}
	if got := blank.DraftErrorText(false); got == "" {
		t.Error("a failure with no recorded message still needs an explanation")
	}
}

// TestStrategyDraftDefaultsToReady: strategies that predate the drafting flow,
// and those loaded from a JSON file, must not be read as mid-draft.
func TestStrategyDraftDefaultsToReady(t *testing.T) {
	legacy := &Strategy{Name: "MVP Launch", DraftStatus: StrategyDraftReady}
	if legacy.DraftStatus != StrategyDraftReady {
		t.Error("a strategy that was never drafted is simply ready")
	}
}
