package web

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/domain"
)

func drawn(statements ...string) []domain.StrategicConsideration {
	out := make([]domain.StrategicConsideration, 0, len(statements))
	for i, st := range statements {
		out = append(out, domain.StrategicConsideration{
			ID: uuid.New(), Kind: domain.ConsiderationFailureMode, Statement: st, Position: i,
		})
	}
	return out
}

func TestCoverageMapsRefsToObjectives(t *testing.T) {
	considerations := drawn("nobody replies", "the official is embarrassed", "it is down on vote day")
	objectiveIDs := []uuid.UUID{uuid.New(), uuid.New()}
	draft := &agent.DraftedStrategy{
		Objectives: []agent.DraftedObjective{
			{Description: "first", Covers: []string{"C1"}},
			{Description: "second", Covers: []string{"C2"}},
		},
		Uncovered: []agent.UncoveredConsideration{
			{Ref: "C3", Reason: "there is no traffic yet to be down for."},
		},
	}

	coverage := coverageFromDraft(draft, considerations, objectiveIDs)
	if len(coverage) != 3 {
		t.Fatalf("want 3 coverage rows, got %d", len(coverage))
	}

	byID := map[uuid.UUID]int{}
	for i, c := range coverage {
		byID[c.ConsiderationID] = i
	}
	first := coverage[byID[considerations[0].ID]]
	if first.ObjectiveID == nil || *first.ObjectiveID != objectiveIDs[0] {
		t.Errorf("C1 should be covered by the first objective, got %+v", first)
	}
	third := coverage[byID[considerations[2].ID]]
	if third.ObjectiveID != nil || !strings.Contains(third.UncoveredReason, "no traffic") {
		t.Errorf("C3 should carry the decline reason, got %+v", third)
	}
}

// A consideration the agent mentioned in neither place is not a decline. The
// difference matters on screen: "it did not say" is a gap in Mendel's reasoning
// the user should see, and "it said no, because" is a judgement they can argue
// with. Writing the first down as the second would put words in its mouth.
func TestUnmentionedConsiderationStaysUnjudged(t *testing.T) {
	considerations := drawn("nobody replies", "the sample skews old")
	objectiveIDs := []uuid.UUID{uuid.New()}
	draft := &agent.DraftedStrategy{
		Objectives: []agent.DraftedObjective{{Description: "first", Covers: []string{"C1"}}},
	}

	coverage := coverageFromDraft(draft, considerations, objectiveIDs)
	for _, c := range coverage {
		if c.ConsiderationID == considerations[1].ID {
			t.Fatalf("C2 was never mentioned and must not be recorded, got %+v", c)
		}
	}
	if len(coverage) != 1 {
		t.Errorf("want only the covered row, got %d rows", len(coverage))
	}
}

// The reference keys come back from a model, so some of them will not be real.
func TestUnknownRefsAreDropped(t *testing.T) {
	considerations := drawn("nobody replies")
	draft := &agent.DraftedStrategy{
		Objectives: []agent.DraftedObjective{
			{Description: "first", Covers: []string{"C1", "C7", "the second one"}},
		},
		Uncovered: []agent.UncoveredConsideration{{Ref: "C9", Reason: "invented"}},
	}

	coverage := coverageFromDraft(draft, considerations, []uuid.UUID{uuid.New()})
	if len(coverage) != 1 {
		t.Fatalf("only C1 exists, so want 1 row, got %d", len(coverage))
	}
}

// Claiming a consideration in an objective and declining it in the same breath
// is a contradiction the schema will not store, so one has to win. The objective
// does: naming a consideration is the more specific claim.
func TestCoveredBeatsDeclined(t *testing.T) {
	considerations := drawn("nobody replies")
	objectiveID := uuid.New()
	draft := &agent.DraftedStrategy{
		Objectives: []agent.DraftedObjective{{Description: "first", Covers: []string{"C1"}}},
		Uncovered:  []agent.UncoveredConsideration{{Ref: "C1", Reason: "no budget"}},
	}

	coverage := coverageFromDraft(draft, considerations, []uuid.UUID{objectiveID})
	if len(coverage) != 1 {
		t.Fatalf("want 1 row, got %d", len(coverage))
	}
	if coverage[0].ObjectiveID == nil || coverage[0].UncoveredReason != "" {
		t.Errorf("covered should win over declined, got %+v", coverage[0])
	}
}

// The review screen shows considerations before the objectives written against
// them, so a draft that covered nothing still has to render.
func TestReviewScreenShowsCoverage(t *testing.T) {
	objectiveID := uuid.New()
	reason := "there is no traffic yet to be down for."
	view := SetupOKRView{
		Project:  &domain.Project{ID: uuid.New(), Name: "Civic Mirror"},
		Strategy: &domain.Strategy{ID: uuid.New(), Name: "Pilot"},
		Ribbon:   ribbonView(domain.OnboardingLifecycle(domain.OnboardingState{})),
		Objectives: []SetupObjectiveView{
			{Objective: domain.Objective{ID: objectiveID, Description: "Officials succeed on their own"}},
		},
		Considerations: []SetupConsiderationView{
			{
				Consideration: domain.StrategicConsideration{
					Kind: domain.ConsiderationFailureMode, Statement: "Nobody replies to the poll",
					CoveredByObjectiveID: &objectiveID,
				},
				CoveredIndex: 1, CoveredBy: "Officials succeed on their own",
			},
			{Consideration: domain.StrategicConsideration{
				Kind: domain.ConsiderationParty, Statement: "The constituent answering it",
				UncoveredReason: &reason,
			}},
			{Consideration: domain.StrategicConsideration{
				Kind: domain.ConsiderationFailureMode, Statement: "The estimate is wrong in public",
			}},
		},
	}

	var out strings.Builder
	err := parsePageTemplate("setup_okrs.html").ExecuteTemplate(&out, "page-content", map[string]interface{}{
		"ProjectID": view.Project.ID.String(), "View": view,
	})
	if err != nil {
		t.Fatalf("review screen did not render: %v", err)
	}

	body := out.String()
	for _, want := range []string{
		"Taken into account",
		"Nobody replies to the poll",
		"Objective 1",                      // the covered one points somewhere findable
		"no traffic yet",                   // the decline shows its reason
		"Mendel did not say",               // the unjudged one is not shown as a decline
		"Has to serve",                     // a party reads differently from a failure mode
	} {
		if !strings.Contains(body, want) {
			t.Errorf("review screen is missing %q", want)
		}
	}
}
