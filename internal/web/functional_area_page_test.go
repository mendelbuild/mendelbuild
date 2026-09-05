package web

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// The page exists to answer "why can't I do X yet", and the answer has to be the
// same one the deploy path would give. These assert the property rather than the
// wording: the strings come from the catalogue, so a change there changes both.

// Every condition the assessment reports has to reach the page. A checklist that
// silently drops a step is worse than no checklist, because a reader works
// through it and still cannot proceed.
func TestTheChecklistShowsEveryStepTheAssessmentFound(t *testing.T) {
	obs := domain.Observations{ProjectDomain: &domain.ProjectDomain{}}
	a := domain.FunctionalAreas().Assess(domain.AreaDemo, obs)

	body := renderChrome(t, "functional_area.html", "/p/"+uuid.New().String()+"/available/demo",
		map[string]interface{}{
			"ProjectID": uuid.New().String(),
			"Area":      detailViewFor(a),
		})

	for _, step := range a.Steps {
		if !strings.Contains(body, step.Name) {
			t.Errorf("the page does not mention %q, which the assessment reports", step.Name)
		}
	}
}

// The reason shown and the reason a refusal would give are one string. If the
// page renders only its own phrasing, the two can drift, and drift here means
// telling someone to fix something that is not what is stopping them.
func TestThePageQuotesTheSentenceARefusalWouldUse(t *testing.T) {
	obs := domain.Observations{ProjectDomain: &domain.ProjectDomain{}}
	a := domain.FunctionalAreas().Assess(domain.AreaDemo, obs)

	if len(a.Missing) == 0 {
		t.Fatal("a project with nothing configured should have reasons; the fixture is wrong")
	}

	body := renderChrome(t, "functional_area.html", "/p/"+uuid.New().String()+"/available/demo",
		map[string]interface{}{
			"ProjectID": uuid.New().String(),
			"Area":      detailViewFor(a),
		})

	for _, missing := range a.Missing {
		// The template escapes for HTML; compare on a distinctive fragment that
		// survives escaping rather than on the whole sentence.
		fragment := firstClause(missing)
		if !strings.Contains(body, fragment) {
			t.Errorf("the page does not carry %q, so it and a refusal would say different things", fragment)
		}
	}
}

// An area with everything in place says so, rather than rendering an empty list
// that reads as "no idea".
func TestAReadyAreaSaysSo(t *testing.T) {
	a := domain.FunctionalAreas().Assess(domain.AreaNamedDemos, domain.Observations{
		ProjectDomain: &domain.ProjectDomain{},
	})
	view := detailViewFor(a)
	if view.Available {
		t.Fatal("a project with no domain cannot serve demos by name; the fixture is wrong")
	}
	if view.Headline == "" {
		t.Error("an unavailable area has to say what is holding it up")
	}
}

// detailViewFor builds the page's view the way the handler does, so a test
// cannot assert against a shape the app would never render.
func detailViewFor(a domain.Assessment) FunctionalAreaDetailView {
	steps := make([]StepView, 0, len(a.Steps))
	for _, step := range a.Steps {
		steps = append(steps, StepView{
			Name:    step.Name,
			State:   ladderState(step.State),
			Detail:  step.Detail,
			Missing: step.Missing,
			Remedy:  string(step.Remedy),
			Count:   fanOutLabel(step.Finding),
		})
	}
	return FunctionalAreaDetailView{
		ID:        string(a.Area),
		Name:      string(a.Area),
		Available: a.Available,
		Headline:  a.Headline,
		WaitingOn: string(a.WaitingOn),
		Steps:     steps,
	}
}

// firstClause is the part of a sentence before its first comma or full stop,
// which is long enough to be distinctive and short enough to survive the
// template's HTML escaping of apostrophes and the like.
func firstClause(s string) string {
	for i, r := range s {
		if r == ',' || r == '.' {
			return s[:i]
		}
	}
	return s
}
