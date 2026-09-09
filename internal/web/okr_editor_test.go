package web

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// The objective detail page renders an "add a key result" form, and that form
// has no key result to render its target fields against. It used to pass the
// whole view instead, which html/template only rejects when it reaches the
// field -- after a 200 and two thirds of the page have gone out. The page then
// stopped just short of the script block that defines toggleEdit, so every Edit
// button on it silently did nothing, for every project, for a week.
//
// So this asserts two things: that the template executes at all, and that the
// foot of the page arrived. The second is the one that matters -- a truncated
// page is still a page, and only the missing tail says the render died.
func TestObjectiveDetailPageRendersToTheEnd(t *testing.T) {
	strategyID := uuid.New()
	objective := domain.Objective{ID: uuid.New(), StrategyID: strategyID, Description: "Officials can run this themselves"}

	view := OKREditorView{
		Project:  &domain.Project{ID: uuid.New(), Name: "Civic Mirror"},
		Strategy: &domain.Strategy{ID: strategyID, Name: "Pilot"},
	}
	current := ObjectiveTreeView{Objective: objective}

	var out strings.Builder
	err := parsePageTemplate("okr_editor.html").ExecuteTemplate(&out, "page-content", map[string]interface{}{
		"ProjectID": view.Project.ID.String(), "OKR": view, "CurrentObjective": current,
	})
	if err != nil {
		t.Fatalf("objective detail page did not render: %v", err)
	}

	body := out.String()
	for _, want := range []string{"function toggleEdit(", "Add Key Result"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered page is missing %q -- it stopped early", want)
		}
	}
}

// An objective with no key results yet is the ordinary state of a fresh draft,
// and the state the page above is most often reached in.
func TestOKREditorRendersWithNoKeyResults(t *testing.T) {
	strategyID := uuid.New()
	view := OKREditorView{
		Project:  &domain.Project{ID: uuid.New(), Name: "Civic Mirror"},
		Strategy: &domain.Strategy{ID: strategyID, Name: "Pilot"},
		Objectives: []ObjectiveTreeView{
			{Objective: domain.Objective{ID: uuid.New(), StrategyID: strategyID, Description: "Officials can run this themselves"}},
		},
	}

	var out strings.Builder
	err := parsePageTemplate("okr_editor.html").ExecuteTemplate(&out, "page-content", map[string]interface{}{
		"ProjectID": view.Project.ID.String(), "OKR": view,
	})
	if err != nil {
		t.Fatalf("OKR editor did not render: %v", err)
	}
	if !strings.Contains(out.String(), "function toggleEdit(") {
		t.Error("rendered page is missing its script block -- it stopped early")
	}
}
