package web

import (
	"strings"
	"testing"
	"time"

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

// The sentences a refusal gives and the sentences the checklist renders are one
// set of strings. This is decision D39 and the whole reason the hand-written
// gates were replaced: before, handleStartDemo, runChannelDemoDeployment and
// runChannelProdDeployment each composed their own wording for conditions the
// page also described, and nothing held the four together.
//
// Asserted across both renderings rather than about either, because a refusal
// written by hand can match today's checklist by coincidence and stop matching
// the moment either side is edited.
func TestARefusalAndTheChecklistQuoteTheSameSentences(t *testing.T) {
	projectID := uuid.New()
	a := domain.FunctionalAreas().Assess(domain.AreaDemo, domain.Observations{
		ProjectDomain: &domain.ProjectDomain{},
	})
	if a.Available {
		t.Fatal("a project with nothing configured cannot run a demo; the fixture is wrong")
	}

	// The string the gates hand to http.Error and to failDemo.
	decline := declineWithChecklist(a, projectID)

	body := renderChrome(t, "functional_area.html", "/p/"+projectID.String()+"/available/demo",
		map[string]interface{}{
			"ProjectID": projectID.String(),
			"Area":      detailViewFor(a),
		})

	for _, missing := range a.Missing {
		if !strings.Contains(decline, missing) {
			t.Errorf("a refusal drops %q, which the assessment gives as a reason", missing)
		}
		// The template escapes for HTML; compare on a distinctive fragment that
		// survives escaping rather than on the whole sentence.
		if fragment := firstClause(missing); !strings.Contains(body, fragment) {
			t.Errorf("the checklist does not carry %q, so it and a refusal would say different things", fragment)
		}
	}

	// A refusal names every reason, but only the checklist can show the steps
	// that are already fine -- which is how a reader knows the list ends.
	if !strings.Contains(decline, areaPath(projectID, domain.AreaDemo)) {
		t.Errorf("a refusal does not say where the rest of the list is: %q", decline)
	}
}

// A missing channel credential is named before a deploy starts. It used to
// surface from inside runChannelDemoDeployment, which decrypts credentials one
// at a time and failed on the first absent one -- so it cost a deploy to learn,
// named only itself, and said nothing about the ones after it.
func TestAMissingChannelCredentialIsNamedBeforeAnythingStarts(t *testing.T) {
	platform := &domain.HostingPlatform{Name: "Google Kubernetes Engine", Slug: "gke"}
	validated := time.Now()

	a := domain.FunctionalAreas().Assess(domain.AreaDemo, domain.Observations{
		ProjectDomain:               &domain.ProjectDomain{},
		Readiness:                   domain.ProjectReadiness{HasRepoURL: true, HasAuthToken: true},
		EncryptionKeyConfigured:     true,
		ChannelCombinationSupported: true,
		Channel: &domain.ProjectDeploymentChannel{
			ArtifactKind:      domain.DeployArtifactKubernetes,
			HostingPlatform:   platform,
			DemoValidatedAt:   &validated,
		},
		MissingChannelCredentials: []string{"GCP_SERVICE_ACCOUNT_KEY", "GCP_PROJECT_ID"},
	})

	if a.Available {
		t.Fatal("a channel with no credentials cannot deploy; the fixture is wrong")
	}

	decline := declineWithChecklist(a, uuid.New())
	for _, name := range []string{"GCP_SERVICE_ACCOUNT_KEY", "GCP_PROJECT_ID"} {
		if !strings.Contains(decline, name) {
			t.Errorf("a refusal does not name the missing credential %s: %q", name, decline)
		}
	}
}
