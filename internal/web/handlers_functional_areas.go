package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/hosting"
)

// The page that answers "why can't I do X yet".
//
// Before this, the answer to that question was spread across an error string in
// one handler, an empty state in a template, a plan document, and a background
// job that only says so after burning a deploy. There was no page, no list, and
// no way to learn the second reason without first fixing the first.
//
// What this renders is the same assessment the gates consult, which is the
// point: a checklist assembled separately from the gate would drift from it,
// and drift here means telling someone to fix something that is not what is
// stopping them. The gates themselves are below, in assessDeployArea and
// declineReason.

// FunctionalAreaView is one row of the index.
type FunctionalAreaView struct {
	ID        string
	Name      string
	Available bool
	Headline  string
	WaitingOn string
	Outstanding int
}

// StepView is one condition on the detail page.
type StepView struct {
	Name    string
	State   string
	Detail  string
	Missing string
	Remedy  string

	// Count renders fan-out where a condition covers several instances of the
	// same task, and is empty where it covers one.
	Count string
}

// FunctionalAreaDetailView is one area and everything it needs.
type FunctionalAreaDetailView struct {
	ID        string
	Name      string
	Available bool
	Headline  string
	WaitingOn string
	Steps     []StepView
}

// projectObservations judges the project and the code merged to main, which is
// what production deploys and what the "why can't I do X" page is about.
func (s *Server) projectObservations(ctx context.Context, projectID uuid.UUID) domain.Observations {
	return s.gatherObservations(ctx, projectID, uuid.Nil)
}

// variationObservations judges one variation's branch, which is what a demo
// deploys.
func (s *Server) variationObservations(ctx context.Context, projectID, variationID uuid.UUID) domain.Observations {
	return s.gatherObservations(ctx, projectID, variationID)
}

// gatherObservations collects what every condition is judged against.
//
// One place, because the conditions are shared between areas and gathering per
// area would mean asking the same questions several times and, worse, being
// able to get different answers to them within one page. Since step 4 of
// dev/claude_plans/17_functional_area_matrix.md it is also the one place the
// deploy paths gather from, so a gate and the checklist it links to are looking
// at the same facts as well as rendering the same sentences.
//
// variationID names the code being asked about: uuid.Nil is everything merged
// to main, and a variation is that variation's branch. It decides which
// requirements exist and, through the app name, what the deployment's URL will
// be -- and those two have to be decided together, since judging a variation's
// acknowledgements against production's URL would vouch for a redirect URI
// nobody registered.
//
// Failures are deliberately not fatal. A lookup that does not come back leaves
// its field zero, and the condition it feeds reports that it could not tell
// rather than that the answer is no -- which is the whole reason unchecked is a
// state. A page that refused to render because one query timed out would be
// less useful than one that says which part it could not see.
func (s *Server) gatherObservations(ctx context.Context, projectID, variationID uuid.UUID) domain.Observations {
	obs := domain.Observations{}

	if pd, err := s.db.GetProjectDomain(ctx, projectID); err == nil && pd != nil {
		obs.ProjectDomain = pd
		// The cached observation, not a fresh lookup: this page renders on
		// demand and a DNS round trip per render would make it slow for an
		// answer that changes on the order of minutes.
		obs.Domain, _ = s.observationFor(projectID, pd)
	} else {
		// Every condition has to answer whatever it is asked about, and the
		// domain conditions read through this pointer.
		obs.ProjectDomain = &domain.ProjectDomain{}
	}

	if r, err := s.db.GetProjectReadiness(ctx, projectID); err == nil {
		obs.Readiness = r
	}
	_, keyErr := crypto.GetKey()
	obs.EncryptionKeyConfigured = keyErr == nil

	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err == nil && channel != nil {
		obs.Channel = channel
		obs.ChannelCombinationSupported = s.comboIsSupported(ctx, channel)
		obs.MissingChannelCredentials = s.missingChannelCredentials(ctx, projectID, channel)
	}

	// Requirements are judged against a particular deployment, because it is the
	// deploy URL that decides what an acknowledgement resolves to -- and on the
	// platforms that assign a hostname at deploy time there is no URL yet, which
	// defers such a requirement rather than blocking on a string nobody can
	// produce.
	deployURL := s.predictedURLFor(ctx, projectID, variationID, obs.Channel)
	statuses, err := s.prodRequirementStatus(ctx, projectID, deployURL)
	if variationID != uuid.Nil {
		statuses, err = s.variationRequirementStatus(ctx, projectID, variationID, deployURL)
	}
	if err == nil {
		obs.Requirements = statuses
	}

	return obs
}

// predictedURLFor is where this deployment will answer, when that is knowable
// before it exists.
//
// It lives beside the gathering rather than at each call site because it was
// duplicated at three of them, and two of those had to agree with a fourth
// copy inside the deploy itself for an acknowledgement to be judged against the
// URL the code would actually run on.
func (s *Server) predictedURLFor(ctx context.Context, projectID, variationID uuid.UUID, channel *domain.ProjectDeploymentChannel) string {
	if channel == nil || channel.HostingPlatform == nil {
		return ""
	}
	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		return ""
	}
	return predictedDeployURL(channel.HostingPlatform.Slug, deployAppName(sanitizeAppName(project.Name), variationID))
}

// assessDeployArea judges one functional area for a project, and for the
// variation being demoed where there is one.
//
// This and the page call the same catalogue over the same observations, which
// is decision D39 of dev/claude_plans/17_functional_area_matrix.md: a gate that
// composed its own sentence would drift from the checklist that tells the
// reader how to clear it, and a reader who fixes what the refusal named and is
// refused again has been sent on a walk for nothing.
func (s *Server) assessDeployArea(ctx context.Context, area domain.AreaID, projectID, variationID uuid.UUID) domain.Assessment {
	a, _ := s.assessDeployAreaWith(ctx, area, projectID, variationID)
	return a
}

// assessDeployAreaWith also hands back what it judged, for a caller that needs
// the observations as well as the verdict.
//
// The demo deploy is that caller: it reads the requirements back out to decrypt
// the values behind them, and gathering them a second time could disagree with
// what the gate had just allowed.
func (s *Server) assessDeployAreaWith(ctx context.Context, area domain.AreaID, projectID, variationID uuid.UUID) (domain.Assessment, domain.Observations) {
	obs := s.projectObservations(ctx, projectID)
	if variationID != uuid.Nil {
		obs = s.variationObservations(ctx, projectID, variationID)
	}
	return domain.FunctionalAreas().Assess(area, obs), obs
}

// declineReason is what a refusal says: the assessment's own sentences, in the
// order they can be worked through.
//
// All of them rather than the first, because a reader told one thing at a time
// learns how far off they are only by fixing something and being refused again.
func declineReason(a domain.Assessment) string {
	if len(a.Missing) > 0 {
		return strings.Join(a.Missing, " ")
	}
	// Unavailable with nothing to say is the in-progress case: a channel
	// validation that is running is not satisfied and names no remedy, because
	// the remedy is to wait. The headline is what that case has.
	if a.Headline != "" {
		return a.Headline
	}
	return "Mendel cannot do this yet."
}

// declineWithChecklist is declineReason plus where to see the rest, which is
// the one thing a refusal can offer that the sentences cannot: the steps that
// are fine, so the reader knows the list ends.
func declineWithChecklist(a domain.Assessment, projectID uuid.UUID) string {
	return declineReason(a) + " The full list is at " + areaPath(projectID, a.Area) + "."
}

// areaPath is one functional area's checklist page.
func areaPath(projectID uuid.UUID, area domain.AreaID) string {
	return "/p/" + projectID.String() + "/available/" + string(area)
}

// comboIsSupported reports whether Mendel has a deployment path for the
// channel's (artifact kind, platform) pair.
func (s *Server) comboIsSupported(ctx context.Context, channel *domain.ProjectDeploymentChannel) bool {
	combos, err := s.db.ListSupportedDeploymentCombos(ctx)
	if err != nil {
		return false
	}
	for _, combo := range combos {
		if combo.ArtifactKind == channel.ArtifactKind && combo.HostingPlatformID == channel.HostingPlatformID {
			return true
		}
	}
	return false
}

// missingChannelCredentials names the credentials this channel needs and does
// not have.
//
// Reported before anything starts rather than discovered partway through:
// runChannelDemoDeployment decrypts them one at a time and fails on the first
// absent one, which costs a deploy to learn something knowable in advance.
func (s *Server) missingChannelCredentials(ctx context.Context, projectID uuid.UUID, channel *domain.ProjectDeploymentChannel) []string {
	if channel.HostingPlatform == nil {
		return nil
	}
	var missing []string
	for _, name := range hosting.RequiredCredentialsForCombo(channel.ArtifactKind, channel.HostingPlatform.Slug) {
		if _, err := s.db.GetProjectCredential(ctx, projectID, name); err != nil {
			missing = append(missing, name)
		}
	}
	return missing
}

// handleFunctionalAreas lists what Mendel can and cannot do for this project.
func (s *Server) handleFunctionalAreas(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	obs := s.projectObservations(ctx, projectID)
	cat := domain.FunctionalAreas()

	var areas []FunctionalAreaView
	for _, area := range cat.Areas() {
		a := cat.Assess(area.ID, obs)
		outstanding := 0
		for _, step := range a.Steps {
			if step.State != domain.CondSatisfied {
				outstanding++
			}
		}
		areas = append(areas, FunctionalAreaView{
			ID:          string(area.ID),
			Name:        area.Name,
			Available:   a.Available,
			Headline:    a.Headline,
			WaitingOn:   string(a.WaitingOn),
			Outstanding: outstanding,
		})
	}

	if err := s.renderPageFor(w, r, "functional_areas.html", map[string]interface{}{
		"ProjectID":   projectID.String(),
		"Nav":         "settings",
		"SettingsTab": "available",
		"Areas":       areas,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleFunctionalArea renders one area's checklist.
func (s *Server) handleFunctionalArea(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	areaID := domain.AreaID(chi.URLParam(r, "areaID"))
	cat := domain.FunctionalAreas()
	area, ok := findArea(cat, areaID)
	if !ok {
		http.Error(w, "no such functional area", http.StatusNotFound)
		return
	}

	a := cat.Assess(areaID, s.projectObservations(ctx, projectID))

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

	if err := s.renderPageFor(w, r, "functional_area.html", map[string]interface{}{
		"ProjectID":   projectID.String(),
		"Nav":         "settings",
		"SettingsTab": "available",
		"Area": FunctionalAreaDetailView{
			ID:        string(area.ID),
			Name:      area.Name,
			Available: a.Available,
			Headline:  a.Headline,
			WaitingOn: string(a.WaitingOn),
			Steps:     steps,
		},
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func findArea(cat *domain.Catalogue, id domain.AreaID) (domain.FunctionalArea, bool) {
	for _, a := range cat.Areas() {
		if a.ID == id {
			return a, true
		}
	}
	return domain.FunctionalArea{}, false
}

// ladderState maps a condition's state onto the vocabulary the ladder markup
// already styles, so this page looks like the Domain tab rather than beside it.
//
// The two states meaning "Mendel does not know" stay distinct, as they do
// everywhere else: unchecked is not having looked, unknown is having looked and
// been unable to tell.
func ladderState(s domain.ConditionState) string {
	switch s {
	case domain.CondSatisfied:
		return "done"
	case domain.CondBlocked:
		return "blocked"
	case domain.CondUnchecked:
		return "checking"
	case domain.CondUndetermined, domain.CondUnimplemented:
		return "unknown"
	case domain.CondOffered:
		return "offered"
	default:
		return "yourmove"
	}
}

// fanOutLabel renders a count where one condition covers several instances of
// the same task, and nothing where it covers one. A reader who supplied one of
// three values and is looking at a step that still says "your move" needs to
// know the other two exist.
func fanOutLabel(f domain.Finding) string {
	if f.Total <= 1 || f.Outstanding == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d outstanding", f.Outstanding, f.Total)
}
