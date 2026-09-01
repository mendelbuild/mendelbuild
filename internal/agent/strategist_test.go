package agent

import (
	"errors"
	"strings"
	"testing"
)

// TestDraftDefect covers what counts as a usable draft.
//
// Two real failures motivate this. json.Unmarshal ignores unknown fields, so a
// schema-valid empty response once parsed cleanly and produced a project with
// no objectives and no error. Later, a half-finished generation got through
// with one objective, no strategy name, and JSON fragments leaking into a
// target value — a review screen titled "Initial strategy" with nothing on it
// to say Mendel had gone wrong.
func TestDraftDefect(t *testing.T) {
	good := DraftedStrategy{
		StrategyName: "Constituent Poll MVP",
		Summary:      "A polling tool for one district.",
		Objectives: []DraftedObjective{{
			Description: "An official can poll a sample of constituents",
			KeyResults:  []DraftedKeyResult{{Description: "Polls sent", TargetComparator: "at_least", TargetValue: 1, TargetUnit: "polls"}},
		}},
	}
	if defect := draftDefect(good); defect != "" {
		t.Errorf("a complete draft should be usable, got %q", defect)
	}

	cases := []struct {
		name  string
		draft DraftedStrategy
		want  string
	}{
		{"wholly empty", DraftedStrategy{}, "no objectives"},
		{
			// The shape actually observed: prose written, structure abandoned.
			"commentary but no objectives",
			DraftedStrategy{StrategyName: "MVP", Summary: "A tool.", Assumptions: []string{"Web only."}},
			"no objectives",
		},
		{
			// The half-finished generation. Its tell is the missing name.
			"no strategy name",
			func() DraftedStrategy { d := good; d.StrategyName = "   "; return d }(),
			"no strategy name",
		},
		{
			"objective without key results",
			func() DraftedStrategy {
				d := good
				d.Objectives = []DraftedObjective{{Description: "Ship something"}}
				return d
			}(),
			"key results",
		},
		{
			"objective without a description",
			func() DraftedStrategy {
				d := good
				d.Objectives = []DraftedObjective{{
					KeyResults: []DraftedKeyResult{{Description: "x", TargetComparator: "at_least", TargetValue: 1}},
				}}
				return d
			}(),
			"description",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defect := draftDefect(c.draft)
			if defect == "" {
				t.Fatal("expected this draft to be rejected")
			}
			if !strings.Contains(defect, c.want) {
				t.Errorf("defect = %q, want something mentioning %q", defect, c.want)
			}
		})
	}
}

// TestUnusableDraftIsRetryable: the retry hinges on telling an unusable draft
// apart from a real failure. If the sentinel stops matching, these stop being
// retried and start reaching users as dead ends.
func TestUnusableDraftIsRetryable(t *testing.T) {
	wrapped := wrapUnusableDraft("no objectives", "end_turn", `{"strategy":{}}`)
	if !errors.Is(wrapped, errUnusableDraft) {
		t.Error("an unusable draft must be recognisable as retryable")
	}
	if !strings.Contains(wrapped.Error(), "no objectives") {
		t.Error("the error should say what was wrong, for whoever reads the log")
	}
	if errors.Is(errors.New("connection reset"), errUnusableDraft) {
		t.Error("an unrelated failure must not be retried as an unusable draft")
	}
}
