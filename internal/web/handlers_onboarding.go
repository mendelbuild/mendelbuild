package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Guided project creation.
//
// The old path was "write a strategy JSON file and upload it", which asks a
// newcomer to know what an Objective, a Key Result, a funding source and a
// repository config are before they have seen Mendel do anything. This flow
// asks instead for the three things a person actually knows on day one -- what
// they want, when they need it, what they will spend -- and drafts the rest for
// them to correct.
//
// Three screens:
//
//	/new                    describe the project
//	/p/{id}/setup/okrs      validate the drafted objectives
//	/p/{id}/inputs          the queue, with a roadmap review waiting in it
//
// The project row is created before the drafting agent runs, deliberately. The
// draft costs real money, and a charge that cannot be filed against a strategy
// makes the project's cost understate itself from its very first minute.

// setupTimeout bounds the drafting calls. They are slow -- the user is watching
// a spinner -- but a request that hangs forever is worse than one that fails.
const setupTimeout = 3 * time.Minute

// setupPollSeconds is how often the review screen re-checks a running draft.
// Short enough to feel responsive on a call that takes 30-45 seconds, long
// enough not to hammer the database while someone watches.
const setupPollSeconds = 3

// SetupOKRView holds the drafted-OKR review screen.
type SetupOKRView struct {
	Project    *domain.Project
	Strategy   *domain.Strategy
	Notes      *domain.StrategyDraftNotes
	Objectives []SetupObjectiveView

	// What the first drafting pass worked out, and what the second pass did
	// about each item. Empty when that pass could not run, which the screen
	// says rather than showing as "nothing to consider".
	Considerations []SetupConsiderationView

	Funding    *domain.FundingSource
	Ribbon     *RibbonView
	Error      string

	// Drafting and DraftFailed are mutually exclusive with showing the draft:
	// the screen is a spinner, an error with a retry, or the objectives.
	Drafting     bool
	DraftFailed  bool
	DraftError   string
	PollSeconds  int // How soon the waiting page should re-check

	// Approved switches this screen between the two jobs it does. Before
	// approval it is the review: draft notes, considerations, a redraft box and
	// a button that sets the project in motion. After, it is the OKR editor,
	// and the same fields save in place.
	//
	// One screen rather than two because there were two, and the second could
	// do three things the first could not -- nest objectives, share a key
	// result between them, and add or remove rows -- of which the first two
	// were used by nobody and were invisible everywhere except that editor.
	Approved bool

	// The end of the planning cycle, which is when every key result is due.
	// Shown once rather than as a date field on each row.
	CycleEnd *time.Time
}

// SetupConsiderationView is one Strategic Consideration as the review screen
// shows it: what was worked out about the project, and what the draft did about
// it.
//
// The covering objective travels as its position and its text rather than as an
// id, because the point on screen is for a reader to look up and find the line
// that answers this one.
type SetupConsiderationView struct {
	Consideration domain.StrategicConsideration
	CoveredBy     string // The covering objective's text, empty if none covers it.
	CoveredIndex  int    // Its 1-based position on the page, 0 if none covers it.
}

// SetupObjectiveView is one drafted objective with its key results.
type SetupObjectiveView struct {
	Objective  domain.Objective
	KeyResults []domain.KeyResult
}

// handleNewProject renders the "describe your project" form.
func (s *Server) handleNewProject(w http.ResponseWriter, r *http.Request) {
	s.renderNewProject(w, r, newProjectForm{
		Deadline: time.Now().AddDate(0, 3, 0).Format("2006-01-02"),
		Budget:   "250",
	}, "")
}

// newProjectForm is what the first screen collects, kept as strings so a
// rejected submission can be handed straight back with the user's own text in
// it rather than a cleared form.
type newProjectForm struct {
	Name     string
	Brief    string
	Deadline string
	Budget   string
}

func (s *Server) renderNewProject(w http.ResponseWriter, r *http.Request, form newProjectForm, errMsg string) {
	data := map[string]interface{}{
		"Title":  "New Project",
		"Form":   form,
		"Error":  errMsg,
		// ribbonView, not the bare Ribbon: the partial reads Actions, which
		// only the view carries. Passing the domain value renders most of the
		// ribbon and then dies on that field, which took the form on this page
		// with it for nine days.
		"Ribbon": ribbonView(domain.OnboardingLifecycle(domain.OnboardingState{})),
	}
	if err := s.renderPageFor(w, r, "new_project.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleCreateProject creates the project and drafts its strategy.
//
// Drafting runs inline rather than in the background: the user has nothing to
// look at until it finishes, and doing it here means a failure lands back on
// their own form with their text still in it instead of stranding a half-built
// project they would have to find and clean up.
func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	form := newProjectForm{
		Name:     strings.TrimSpace(r.FormValue("name")),
		Brief:    strings.TrimSpace(r.FormValue("brief")),
		Deadline: strings.TrimSpace(r.FormValue("deadline")),
		Budget:   strings.TrimSpace(r.FormValue("budget_usd")),
	}

	if form.Name == "" {
		s.renderNewProject(w, r, form, "Give the project a name.")
		return
	}
	if len(form.Brief) < 20 {
		s.renderNewProject(w, r, form, "Describe what you want built in a sentence or two. Mendel drafts your objectives from this, so a few words is not enough to work with.")
		return
	}

	var deadline *time.Time
	if form.Deadline != "" {
		d, err := time.Parse("2006-01-02", form.Deadline)
		if err != nil {
			s.renderNewProject(w, r, form, "That deadline is not a date Mendel could read. Use the date picker.")
			return
		}
		if d.Before(time.Now().Truncate(24 * time.Hour)) {
			s.renderNewProject(w, r, form, "That deadline is in the past.")
			return
		}
		deadline = &d
	}

	var budgetUSD float64
	if form.Budget != "" {
		v, err := strconv.ParseFloat(strings.TrimPrefix(form.Budget, "$"), 64)
		if err != nil || v < 0 {
			s.renderNewProject(w, r, form, "Enter the budget as a number of US dollars, e.g. 250.")
			return
		}
		budgetUSD = v
	}

	ctx := r.Context()

	var ownerID *uuid.UUID
	if user := UserFromContext(ctx); user != nil && s.authEnabled {
		ownerID = &user.ID
	}

	// The strategy is named by the drafting agent, but it needs a name now: the
	// row exists before the agent runs so the draft's spend has somewhere to go,
	// and so there is a page to send the browser to while the draft runs.
	projectID, strategyID, err := s.db.CreateProjectWithStrategy(ctx, form.Name, form.Brief, "Initial strategy", ownerID)
	if err != nil {
		s.renderNewProject(w, r, form, "Could not create the project: "+err.Error())
		return
	}

	brief := agent.StrategyBrief{
		ProjectName: form.Name,
		Brief:       form.Brief,
		BudgetUSD:   budgetUSD,
		TodayISO:    time.Now().Format("2006-01-02"),
	}
	if deadline != nil {
		brief.DeadlineISO = deadline.Format("2006-01-02")
	}

	// CreateProjectWithStrategy already marked the strategy 'drafting', so the
	// review screen shows the waiting state from the very first render.
	s.startDraft(projectID, strategyID, brief, deadline, budgetUSD, nil, "")

	http.Redirect(w, r, fmt.Sprintf("/p/%s/setup/okrs", projectID), http.StatusSeeOther)
}

// startDraft runs a drafting call detached from the request that asked for it.
//
// Drafting takes 30-45 seconds against the model. An HTTP request cannot safely
// block that long -- the GCE ingress in front of the app closes the connection
// at 30 -- and the first version of this flow did exactly that, so a user got a
// gateway error while their project was drafted perfectly well behind it.
//
// The goroutine takes its own context: the request's is cancelled the moment
// the redirect is written.
func (s *Server) startDraft(projectID, strategyID uuid.UUID, brief agent.StrategyBrief,
	deadline *time.Time, budgetUSD float64, current *agent.DraftedStrategy, feedback string) {

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
		defer cancel()

		err := s.draftStrategy(ctx, projectID, strategyID, brief, deadline, budgetUSD, current, feedback)
		if err != nil {
			log.Printf("setup: drafting for project %s failed: %v", projectID, err)
		}
		// Recorded on its own context: if the draft failed because ctx expired,
		// that same context cannot be used to write down that it did.
		finish, cancelFinish := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelFinish()
		if ferr := s.db.FinishStrategyDraft(finish, strategyID, err); ferr != nil {
			log.Printf("setup: could not record draft outcome for project %s: %v", projectID, ferr)
		}
	}()
}

// draftStrategy runs the drafting agent and replaces the strategy's draft OKRs
// with the result. With current != nil and feedback set, it revises instead.
//
// Also (re)points the strategy's budget at the drafted key results: a budget
// that names no key results records an amount but not what it is meant to buy.
func (s *Server) draftStrategy(ctx context.Context, projectID, strategyID uuid.UUID,
	brief agent.StrategyBrief, deadline *time.Time, budgetUSD float64,
	current *agent.DraftedStrategy, feedback string) error {

	client, err := agent.NewClient("")
	if err != nil {
		return fmt.Errorf("agent client: %w", err)
	}
	// Pass one: what does success depend on, before anyone writes an objective.
	// Its output is not shown as a draft and nothing is planned against it -- it
	// exists so pass two has a standard to be checked against, and so the user
	// can see what was weighed rather than only what was chosen.
	considerations, keyed := s.drawConsiderations(ctx, strategyID, brief, feedback)

	// Pass two.
	strategist := agent.NewStrategist(client)

	var drafted *agent.DraftedStrategy
	var spend agent.Spend
	if current != nil {
		drafted, spend, err = strategist.ReviseStrategy(ctx, brief, keyed, current, feedback)
	} else {
		drafted, spend, err = strategist.DraftStrategy(ctx, brief, keyed)
	}
	s.recordStrategySpend(ctx, strategyID, "strategist", spend)
	if err != nil {
		return err
	}

	if err := s.saveDraftedStrategy(ctx, strategyID, drafted, considerations, deadline, budgetUSD); err != nil {
		return err
	}

	// Quality feedback on the draft, so the user reviewing it sees which items
	// are vague before they approve them rather than after.
	s.tuneStrategyOKRs(ctx, strategyID)

	// And then act on it. Showing someone a key result over Mendel's own
	// verdict that it cannot say whether the work is going well, without having
	// tried to fix it, asks them to do the edit the grader just described --
	// which is exactly the edit a person who is new to writing key results
	// cannot make. One repair pass, then whatever survives it is shown with its
	// score, because a critique that survives a rewrite is worth reading.
	s.repairWeakOKRs(ctx, projectID, strategyID, brief, keyed, deadline, budgetUSD)
	return nil
}

// saveDraftedStrategy writes a drafted strategy over whatever was there: its
// objectives and key results, what each consideration's fate was, the name, the
// notes, and the budget the key results are what buys.
//
// Shared by the drafting pass and the repair pass that follows it, because a
// repaired draft is a draft and has to land the same way -- the first version of
// the repair pass wrote only the objectives, and left the coverage pointing at
// rows that no longer existed.
func (s *Server) saveDraftedStrategy(ctx context.Context, strategyID uuid.UUID,
	drafted *agent.DraftedStrategy, considerations []domain.StrategicConsideration,
	deadline *time.Time, budgetUSD float64) error {

	objectives := make([]db.DraftObjective, 0, len(drafted.Objectives))
	for _, obj := range drafted.Objectives {
		o := db.DraftObjective{Description: obj.Description}
		for _, kr := range obj.KeyResults {
			o.KeyResults = append(o.KeyResults, db.DraftKeyResult{
				Description:      kr.Description,
				TargetComparator: kr.TargetComparator,
				TargetValue:      kr.TargetValue,
				TargetUnit:       kr.TargetUnit,
				// Every key result in a cycle is due at the end of it. The
				// agent is not asked for a date and has none to give.
				TargetDate: deadline,
			})
		}
		objectives = append(objectives, o)
	}

	objectiveIDs, err := s.db.ReplaceDraftOKRs(ctx, strategyID, objectives)
	if err != nil {
		return fmt.Errorf("save draft: %w", err)
	}

	// Coverage is written after the objectives exist, because until they do
	// there is nothing for a covered consideration to point at.
	if err := s.db.SetConsiderationCoverage(ctx,
		coverageFromDraft(drafted, considerations, objectiveIDs)); err != nil {
		return fmt.Errorf("save coverage: %w", err)
	}
	if drafted.StrategyName != "" {
		if err := s.db.RenameStrategy(ctx, strategyID, drafted.StrategyName); err != nil {
			return fmt.Errorf("name strategy: %w", err)
		}
	}
	if err := s.db.SetStrategyDraftNotes(ctx, strategyID, &domain.StrategyDraftNotes{
		Summary:       drafted.Summary,
		Assumptions:   drafted.Assumptions,
		OpenQuestions: drafted.OpenQuestions,
		BudgetNote:    drafted.BudgetNote,
	}); err != nil {
		return fmt.Errorf("save draft notes: %w", err)
	}

	if err := s.syncDraftBudget(ctx, strategyID, drafted.BudgetName, budgetUSD, deadline); err != nil {
		return fmt.Errorf("save budget: %w", err)
	}

	if err := s.syncDraftBudget(ctx, strategyID, drafted.BudgetName, budgetUSD, deadline); err != nil {
		return fmt.Errorf("save budget: %w", err)
	}
	return nil
}

// weakEnoughToRepair is the score below which a drafted line is redrafted
// before anyone sees it.
//
// The tuner's own guide puts 0.6-0.8 at "right in kind, and one edit away", and
// everything below that at "needs work" or worse. A first draft nobody has
// looked at should not go out carrying one of those: the cost of being wrong is
// one rewrite the user can still edit, and the cost of not trying is a plan
// built on a line Mendel had already said was not measurable.
const weakEnoughToRepair = 0.6

// repairWeakOKRs redrafts the lines Mendel's own grader marked down, once.
//
// Once, deliberately. A second pass would be a loop with a model in it and a
// user waiting on the end of it, and the second-order gain is small: if a line
// is still weak after the reviser has been told exactly what is wrong with it,
// another go around is unlikely to be what fixes it. What is shown instead is
// the line and the surviving critique, which is a truthful account of where the
// draft got to.
func (s *Server) repairWeakOKRs(ctx context.Context, projectID, strategyID uuid.UUID,
	brief agent.StrategyBrief, considerations []agent.KeyedConsideration,
	deadline *time.Time, budgetUSD float64) {

	complaints, err := s.weakLineComplaints(ctx, strategyID)
	if err != nil {
		log.Printf("setup: could not read grades for strategy %s: %v", strategyID, err)
		return
	}
	if len(complaints) == 0 {
		return
	}

	strategy, err := s.db.GetStrategy(ctx, strategyID)
	if err != nil {
		log.Printf("setup: could not read strategy %s to repair it: %v", strategyID, err)
		return
	}
	current := s.currentDraft(ctx, strategy)
	if current == nil || len(current.Objectives) == 0 {
		return
	}

	feedback := "Mendel graded this draft against the same rules you drafted it under, and marked these lines down. " +
		"Rewrite them so the criticism no longer applies, and leave everything else exactly as it is.\n\n" +
		strings.Join(complaints, "\n") +
		"\n\nIf one of these genuinely cannot be fixed -- there is nothing to count, say -- keep it and put the reason in open_questions."

	client, err := agent.NewClient("")
	if err != nil {
		log.Printf("setup: agent client for repairing strategy %s: %v", strategyID, err)
		return
	}
	repaired, spend, err := agent.NewStrategist(client).ReviseStrategy(ctx, brief, considerations, current, feedback)
	s.recordStrategySpend(ctx, strategyID, "strategist_repair", spend)
	if err != nil {
		// The unrepaired draft is still a draft, and it is already saved. This
		// costs the user the improvement, not the screen.
		log.Printf("setup: repairing weak lines for project %s failed: %v", projectID, err)
		return
	}

	stored, err := s.db.GetStrategicConsiderations(ctx, strategyID)
	if err != nil {
		log.Printf("setup: could not read considerations for strategy %s: %v", strategyID, err)
		return
	}
	if err := s.saveDraftedStrategy(ctx, strategyID, repaired, stored, deadline, budgetUSD); err != nil {
		log.Printf("setup: saving the repaired draft for project %s failed: %v", projectID, err)
		return
	}

	// Re-grade: the rewritten lines have no score, and the point of the pass
	// was to change the ones that did.
	s.tuneStrategyOKRs(ctx, strategyID)
}

// weakLineComplaints is one line per graded item that scored below the bar,
// carrying the grader's own words about it.
//
// The grader's sentence goes through verbatim rather than being summarised as a
// number. "Done/not-done obscures whether the method works; pair it with an
// accuracy benchmark" tells the reviser what to write; "scored 0.35" does not.
func (s *Server) weakLineComplaints(ctx context.Context, strategyID uuid.UUID) ([]string, error) {
	objectives, err := s.db.GetObjectivesByStrategy(ctx, strategyID)
	if err != nil {
		return nil, err
	}

	krs := make(map[uuid.UUID][]domain.KeyResult, len(objectives))
	for _, obj := range objectives {
		found, err := s.db.GetKeyResultsByObjective(ctx, obj.ID)
		if err != nil {
			return nil, err
		}
		krs[obj.ID] = found
	}
	return complaintsFrom(objectives, krs), nil
}

// complaintsFrom is the judgement, kept apart from the reading so it can be
// tested without a database: which lines are below the bar, and what the grader
// said about each.
func complaintsFrom(objectives []domain.Objective, krs map[uuid.UUID][]domain.KeyResult) []string {
	var complaints []string
	for _, obj := range objectives {
		// A nil score is not a low one. Tuning may have failed or not run, and
		// treating unknown as failing would rewrite a draft nobody had judged.
		if obj.TuneScore != nil && *obj.TuneScore < weakEnoughToRepair {
			complaints = append(complaints, fmt.Sprintf("- Objective %q: %s",
				obj.Description, derefOr(obj.TuneFeedback, "graded below the bar")))
		}
		for _, kr := range krs[obj.ID] {
			if kr.TuneScore != nil && *kr.TuneScore < weakEnoughToRepair {
				complaints = append(complaints, fmt.Sprintf("- Key result %q (%s): %s",
					kr.Description, kr.Target(), derefOr(kr.TuneFeedback, "graded below the bar")))
			}
		}
	}
	return complaints
}

func derefOr(s *string, fallback string) string {
	if s == nil || *s == "" {
		return fallback
	}
	return *s
}

// syncDraftBudget keeps a single funding source in step with the draft: the
// user's dollar figure, running from today to their deadline, spent against
// every key result in the current draft.
func (s *Server) syncDraftBudget(ctx context.Context, strategyID uuid.UUID, name string, budgetUSD float64, deadline *time.Time) error {
	if budgetUSD <= 0 {
		return nil
	}
	if name == "" {
		name = "Initial budget"
	}

	sources, err := s.db.GetFundingSourcesByStrategy(ctx, strategyID)
	if err != nil {
		return err
	}

	var source *domain.FundingSource
	if len(sources) > 0 {
		source = &sources[0]
	} else {
		start := time.Now()
		source = &domain.FundingSource{
			StrategyID:  strategyID,
			Name:        name,
			AmountUSD:   budgetUSD,
			PeriodStart: &start,
			PeriodEnd:   deadline,
		}
		if err := s.db.CreateFundingSource(ctx, source); err != nil {
			return err
		}
	}

	// ReplaceDraftOKRs dropped the previous links along with the key results
	// they pointed at, so every redraft has to re-link.
	krs, err := s.keyResultsForStrategy(ctx, strategyID)
	if err != nil {
		return err
	}
	for _, kr := range krs {
		if err := s.db.LinkFundingToKeyResult(ctx, source.ID, kr.ID, 1.0); err != nil {
			return err
		}
	}
	return nil
}

// keyResultsForStrategy returns every live key result under a strategy.
func (s *Server) keyResultsForStrategy(ctx context.Context, strategyID uuid.UUID) ([]domain.KeyResult, error) {
	objectives, err := s.db.GetRootObjectives(ctx, strategyID)
	if err != nil {
		return nil, err
	}
	var out []domain.KeyResult
	for _, obj := range objectives {
		krs, err := s.db.GetKeyResultsByObjective(ctx, obj.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, krs...)
	}
	return out, nil
}

// tuneStrategyOKRs scores the draft with the OKR tuner. Best-effort: quality
// feedback missing is a worse review screen, not a failed one.
func (s *Server) tuneStrategyOKRs(ctx context.Context, strategyID uuid.UUID) {
	untunedObjs, err := s.db.GetUntunedObjectives(ctx, strategyID)
	if err != nil {
		log.Printf("setup: could not load objectives to tune: %v", err)
		return
	}
	untunedKRs, err := s.db.GetUntunedKeyResults(ctx, strategyID)
	if err != nil {
		log.Printf("setup: could not load key results to tune: %v", err)
		return
	}
	if len(untunedObjs) == 0 && len(untunedKRs) == 0 {
		return
	}

	input := agent.OKRTuneInput{}
	for _, obj := range untunedObjs {
		input.Objectives = append(input.Objectives, agent.ObjectiveForTuning{
			ID:          obj.ID.String(),
			Description: obj.Description,
		})
	}
	for _, kr := range untunedKRs {
		input.KeyResults = append(input.KeyResults, agent.KeyResultForTuning{
			ID:          kr.ID.String(),
			Description: kr.Description,
			TargetUnits: kr.Target(),
		})
	}

	client, err := agent.NewClient("")
	if err != nil {
		log.Printf("setup: could not create agent client for tuning: %v", err)
		return
	}
	result, spend, err := agent.NewOKRTuner(client).TuneOKRs(ctx, input)
	s.recordStrategySpend(ctx, strategyID, "okr_tuner", spend)
	if err != nil {
		log.Printf("setup: OKR tuning failed: %v", err)
		return
	}

	for _, score := range result.ObjectiveScores {
		if id, err := uuid.Parse(score.ID); err == nil {
			s.db.UpdateObjectiveTuning(ctx, id, score.Score, score.Feedback)
		}
	}
	for _, score := range result.KeyResultScores {
		if id, err := uuid.Parse(score.ID); err == nil {
			s.db.UpdateKeyResultTuning(ctx, id, score.Score, score.Feedback)
		}
	}
}

// handleSetupOKRs renders the drafted OKRs for validation.
func (s *Server) handleSetupOKRs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	s.renderSetupOKRs(ctx, w, r, projectID, r.URL.Query().Get("error"))
}

func (s *Server) renderSetupOKRs(ctx context.Context, w http.ResponseWriter, r *http.Request, projectID uuid.UUID, errMsg string) {
	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		http.Error(w, "no strategy found", http.StatusNotFound)
		return
	}
	strategy := strategies[0]

	state, err := s.db.GetOnboardingState(ctx, projectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Drafting and DraftFailed come from the onboarding state, which decides
	// staleness in SQL. See domain.DraftStaleAfter for why that judgement must
	// not be made against Go's clock.
	view := SetupOKRView{
		Project:     project,
		Strategy:    &strategy,
		Ribbon:      ribbonView(domain.OnboardingLifecycle(state)),
		Error:       errMsg,
		Drafting:    state.Drafting,
		DraftFailed: state.DraftFailed,
		PollSeconds: setupPollSeconds,
		Approved:    strategy.OKRsApproved(),
	}
	if view.DraftFailed {
		stale := strategy.DraftStatus == domain.StrategyDraftDrafting
		view.DraftError = strategy.DraftErrorText(stale)
	}

	// A running or failed draft has nothing to show: the objectives are either
	// not written yet or are the leftovers of a previous attempt, and rendering
	// stale rows under a "here is your draft" heading would be a lie.
	if !view.Drafting && !view.DraftFailed {
		// Every objective, not only the roots. Nothing can nest one any more,
		// so the two queries agree -- and if something ever does, this screen
		// shows it rather than losing it silently, which is what the old
		// review screen did to anything the full editor nested.
		objectives, err := s.db.GetObjectivesByStrategy(ctx, strategy.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		objViews := make([]SetupObjectiveView, 0, len(objectives))
		for _, obj := range objectives {
			krs, _ := s.db.GetKeyResultsByObjective(ctx, obj.ID)
			objViews = append(objViews, SetupObjectiveView{Objective: obj, KeyResults: krs})
		}
		view.Objectives = objViews
		view.Notes = strategy.Notes()
		view.Considerations = considerationViews(ctx, s, strategy.ID, objViews)

		if sources, err := s.db.GetFundingSourcesByStrategy(ctx, strategy.ID); err == nil && len(sources) > 0 {
			view.Funding = &sources[0]
			view.CycleEnd = sources[0].PeriodEnd
		}
	}

	data := map[string]interface{}{
		"Title":     "Review objectives",
		"ProjectID": projectID.String(),
		"View":      view,
	}
	if err := s.renderPageFor(w, r, "setup_okrs.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleReviseSetupOKRs redrafts the OKRs from the user's feedback.
func (s *Server) handleReviseSetupOKRs(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	feedback := strings.TrimSpace(r.FormValue("feedback"))

	ctx := r.Context()

	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		http.Error(w, "no strategy found", http.StatusNotFound)
		return
	}
	strategy := strategies[0]
	if strategy.OKRsApproved() {
		http.Error(w, "these OKRs have already been approved", http.StatusConflict)
		return
	}

	brief, deadline, budgetUSD := s.rebuildBrief(ctx, project, &strategy)

	// The revision needs to see what it is revising, which means reading the
	// current draft back out of the database rather than out of the form: the
	// user may have edited fields without saving them, and asking the agent to
	// revise text the user has since changed produces a confusing result.
	//
	// Read before the status flips, so a redraft still knows what it replaces.
	current := s.currentDraft(ctx, &strategy)

	// Claim the draft first. If another one is already running, say so rather
	// than starting a second agent call against the same rows.
	started, err := s.db.BeginStrategyDraft(ctx, strategy.ID)
	if err != nil {
		http.Error(w, "could not start the draft: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if started {
		s.startDraft(projectID, strategy.ID, brief, deadline, budgetUSD, current, feedback)
	}

	http.Redirect(w, r, fmt.Sprintf("/p/%s/setup/okrs", projectID), http.StatusSeeOther)
}

// handleRedraftSetupOKRs retries a draft that failed, from the original brief.
func (s *Server) handleRedraftSetupOKRs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		http.Error(w, "no strategy found", http.StatusNotFound)
		return
	}
	strategy := strategies[0]
	if strategy.OKRsApproved() {
		http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs", projectID), http.StatusSeeOther)
		return
	}

	brief, deadline, budgetUSD := s.rebuildBrief(ctx, project, &strategy)

	started, err := s.db.BeginStrategyDraft(ctx, strategy.ID)
	if err != nil {
		http.Error(w, "could not start the draft: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if started {
		s.startDraft(projectID, strategy.ID, brief, deadline, budgetUSD, nil, "")
	}

	http.Redirect(w, r, fmt.Sprintf("/p/%s/setup/okrs", projectID), http.StatusSeeOther)
}

// rebuildBrief reconstructs the drafting agent's input from what was stored:
// the user's own words on the project, and the budget and deadline that became
// the strategy's funding source.
func (s *Server) rebuildBrief(ctx context.Context, project *domain.Project, strategy *domain.Strategy) (agent.StrategyBrief, *time.Time, float64) {
	brief := agent.StrategyBrief{
		ProjectName: project.Name,
		TodayISO:    time.Now().Format("2006-01-02"),
	}
	if project.Brief != nil {
		brief.Brief = *project.Brief
	}

	var deadline *time.Time
	var budgetUSD float64
	if sources, err := s.db.GetFundingSourcesByStrategy(ctx, strategy.ID); err == nil && len(sources) > 0 {
		budgetUSD = sources[0].AmountUSD
		deadline = sources[0].PeriodEnd
		brief.BudgetUSD = budgetUSD
		if deadline != nil {
			brief.DeadlineISO = deadline.Format("2006-01-02")
		}
	}
	return brief, deadline, budgetUSD
}

// currentDraft rebuilds the drafted strategy from what is stored, so a revision
// can be told what it is revising.
func (s *Server) currentDraft(ctx context.Context, strategy *domain.Strategy) *agent.DraftedStrategy {
	draft := &agent.DraftedStrategy{StrategyName: strategy.Name}
	if notes := strategy.Notes(); notes != nil {
		draft.Summary = notes.Summary
		draft.Assumptions = notes.Assumptions
		draft.OpenQuestions = notes.OpenQuestions
		draft.BudgetNote = notes.BudgetNote
	}

	objectives, err := s.db.GetObjectivesByStrategy(ctx, strategy.ID)
	if err != nil {
		return draft
	}

	// Coverage travels with the draft, so a revision starts from the decisions
	// already made about each consideration rather than re-deciding them all.
	// Without this a repair pass aimed at one weak key result could quietly
	// drop a decline the user had already read.
	coveredBy, uncovered := considerationRefs(ctx, s, strategy.ID)
	draft.Uncovered = uncovered

	for _, obj := range objectives {
		o := agent.DraftedObjective{Description: obj.Description, Covers: coveredBy[obj.ID]}
		krs, _ := s.db.GetKeyResultsByObjective(ctx, obj.ID)
		for _, kr := range krs {
			d := agent.DraftedKeyResult{
				Description:      kr.Description,
				TargetComparator: kr.TargetComparator,
				TargetValue:      kr.TargetValue,
				TargetUnit:       kr.TargetUnit,
			}
			o.KeyResults = append(o.KeyResults, d)
		}
		draft.Objectives = append(draft.Objectives, o)
	}
	return draft
}

// considerationRefs reads stored coverage back into the reference keys the
// drafting agent speaks in: which refs each objective covers, and which were
// declined with what reason.
//
// The keys are positional, the same way they are handed out, so they line up
// with the list the agent is given alongside this draft.
func considerationRefs(ctx context.Context, s *Server, strategyID uuid.UUID) (
	map[uuid.UUID][]string, []agent.UncoveredConsideration) {

	considerations, err := s.db.GetStrategicConsiderations(ctx, strategyID)
	if err != nil {
		return nil, nil
	}

	coveredBy := make(map[uuid.UUID][]string)
	var uncovered []agent.UncoveredConsideration
	for i, c := range considerations {
		ref := agent.ConsiderationRef(i)
		switch {
		case c.CoveredByObjectiveID != nil:
			coveredBy[*c.CoveredByObjectiveID] = append(coveredBy[*c.CoveredByObjectiveID], ref)
		case c.UncoveredReason != nil:
			uncovered = append(uncovered, agent.UncoveredConsideration{Ref: ref, Reason: *c.UncoveredReason})
		}
	}
	return coveredBy, uncovered
}

// handleApproveSetupOKRs saves the user's edits, marks the OKRs approved, and
// sets the rest of the project in motion.
func (s *Server) handleApproveSetupOKRs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		http.Error(w, "no strategy found", http.StatusNotFound)
		return
	}
	strategy := strategies[0]
	if strategy.OKRsApproved() {
		http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs", projectID), http.StatusSeeOther)
		return
	}

	// The approve form is not rendered while a draft is running, but a tab left
	// open from before a redraft can still submit it. Approving there would
	// freeze objectives that are about to be replaced, and would race the
	// drafting goroutine's delete-and-reinsert.
	state, err := s.db.GetOnboardingState(ctx, projectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if state.Drafting {
		http.Redirect(w, r, fmt.Sprintf("/p/%s/setup/okrs", projectID), http.StatusSeeOther)
		return
	}

	if err := s.saveOKREdits(ctx, &strategy, r); err != nil {
		s.renderSetupOKRs(ctx, w, r, projectID, "Could not save your edits: "+err.Error())
		return
	}

	objectives, err := s.db.GetRootObjectives(ctx, strategy.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(objectives) == 0 {
		s.renderSetupOKRs(ctx, w, r, projectID, "A strategy needs at least one objective. Ask Mendel to redraft, or write one yourself in the OKR editor.")
		return
	}

	if err := s.db.ApproveOKRs(ctx, strategy.ID); err != nil {
		http.Error(w, "could not approve: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.ensureRepositoryRequest(ctx, projectID)

	// The roadmap proposer takes tens of seconds and has nothing to say to this
	// request. Run it detached, on its own context: the user's request context
	// is cancelled the moment the redirect is written.
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), setupTimeout)
		defer cancel()
		if _, err := s.proposeRoadmapForStrategy(bg, &strategy); err != nil {
			log.Printf("setup: roadmap proposal for project %s failed: %v", projectID, err)
		}
	}()

	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs", projectID), http.StatusSeeOther)
}

// saveOKREdits applies the review screen's inline edits. Fields are named
// obj_<id> and kr_<id>_desc / kr_<id>_units / kr_<id>_date; anything left blank
// on an objective removes it and its key results.
func (s *Server) saveOKREdits(ctx context.Context, strategy *domain.Strategy, r *http.Request) error {
	objectives, err := s.db.GetRootObjectives(ctx, strategy.ID)
	if err != nil {
		return err
	}

	for _, obj := range objectives {
		krs, err := s.db.GetKeyResultsByObjective(ctx, obj.ID)
		if err != nil {
			return err
		}

		desc := strings.TrimSpace(r.FormValue("obj_" + obj.ID.String()))
		if desc == "" {
			for _, kr := range krs {
				if err := s.db.UnlinkKeyResultFromObjective(ctx, obj.ID, kr.ID); err != nil {
					return err
				}
			}
			if err := s.db.SoftDeleteObjective(ctx, obj.ID); err != nil {
				return err
			}
			continue
		}
		if desc != obj.Description {
			edited := obj
			edited.Description = desc
			if err := s.db.UpdateObjective(ctx, &edited); err != nil {
				return err
			}
		}

		for _, kr := range krs {
			krDesc := strings.TrimSpace(r.FormValue("kr_" + kr.ID.String() + "_desc"))
			if krDesc == "" {
				if err := s.db.UnlinkKeyResultFromObjective(ctx, obj.ID, kr.ID); err != nil {
					return err
				}
				continue
			}
			field := "kr_" + kr.ID.String() + "_"
			comparator, value, unit, targetErr := keyResultTargetFields(
				r.FormValue(field+"comparator"), r.FormValue(field+"value"), r.FormValue(field+"unit"))
			if targetErr != nil {
				return targetErr
			}
			unchanged := krDesc == kr.Description &&
				comparator == kr.TargetComparator && value == kr.TargetValue &&
				unit == kr.TargetUnit
			if unchanged {
				continue
			}
			edited := kr
			edited.Description = krDesc
			edited.TargetComparator = comparator
			edited.TargetValue = value
			edited.TargetUnit = unit
			if err := s.db.UpdateKeyResult(ctx, &edited); err != nil {
				return err
			}
		}

		if err := s.addKeyResultFromForm(ctx, strategy, obj.ID, r); err != nil {
			return err
		}
	}

	return s.addObjectiveFromForm(ctx, strategy, r)
}

// addKeyResultFromForm creates the key result typed into an objective's blank
// row, if anything was typed there. Its target date is the cycle's, like every
// other key result's.
func (s *Server) addKeyResultFromForm(ctx context.Context, strategy *domain.Strategy,
	objectiveID uuid.UUID, r *http.Request) error {

	field := "new_kr_" + objectiveID.String() + "_"
	desc := strings.TrimSpace(r.FormValue(field + "desc"))
	if desc == "" {
		return nil
	}

	comparator, value, unit, err := keyResultTargetFields(
		r.FormValue(field+"comparator"), r.FormValue(field+"value"), r.FormValue(field+"unit"))
	if err != nil {
		return err
	}

	kr := &domain.KeyResult{
		ID: uuid.New(), StrategyID: strategy.ID, Description: desc,
		TargetComparator: comparator, TargetValue: value, TargetUnit: unit,
		TargetDate: s.cycleEnd(ctx, strategy.ID),
	}
	if err := s.db.CreateKeyResult(ctx, kr); err != nil {
		return err
	}
	return s.db.LinkKeyResultToObjective(ctx, objectiveID, kr.ID)
}

// addObjectiveFromForm creates the objective typed into the blank row at the
// foot of the list, if anything was typed there.
//
// Single level: nothing here sets a parent. The column stays, and so do the
// queries that read it, so a richer OKR model remains an addition rather than a
// migration -- but nothing in the product creates depth today, and the screens
// that would have had to show it never did.
func (s *Server) addObjectiveFromForm(ctx context.Context, strategy *domain.Strategy, r *http.Request) error {
	desc := strings.TrimSpace(r.FormValue("new_objective"))
	if desc == "" {
		return nil
	}
	return s.db.CreateObjective(ctx, &domain.Objective{
		ID: uuid.New(), StrategyID: strategy.ID, Description: desc,
	})
}

// cycleEnd is when key results in this strategy are due: the end of the funding
// period. Nil when there is no budget yet, which leaves a key result undated
// rather than inventing a deadline for it.
func (s *Server) cycleEnd(ctx context.Context, strategyID uuid.UUID) *time.Time {
	sources, err := s.db.GetFundingSourcesByStrategy(ctx, strategyID)
	if err != nil || len(sources) == 0 {
		return nil
	}
	return sources[0].PeriodEnd
}

// ensureRepositoryRequest files the "connect a repository" ask into the input
// queue, unless the repository is already configured or the ask is already
// there. Mendel cannot write code without somewhere to write it, but that is
// not needed until the roadmap is approved, so it goes in the queue next to the
// roadmap review rather than blocking the first screen.
func (s *Server) ensureRepositoryRequest(ctx context.Context, projectID uuid.UUID) {
	readiness, err := s.db.GetProjectReadiness(ctx, projectID)
	if err == nil && readiness.IsReady() {
		return
	}

	existing, err := s.db.FindOpenInputRequestByKind(ctx, projectID, domain.InputRequestKindManualSetup)
	if err != nil {
		log.Printf("setup: could not check for an existing repository request: %v", err)
		return
	}
	if existing != nil && existing.Title == repositoryRequestTitle {
		return
	}

	now := time.Now()
	details := "Mendel writes each variation as a branch in your repository, so it needs a repository URL and a token that can push to it. Nothing is written until you approve a variation."
	instructions := "Open Settings and fill in the repository URL, the main branch, and a git auth token with push access. For GitHub, a fine-grained token needs Contents set to Read and write; a classic token needs the repo scope."
	link := fmt.Sprintf("/p/%s/settings", projectID)

	req := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        projectID,
		Kind:             domain.InputRequestKindManualSetup,
		Title:            repositoryRequestTitle,
		Details:          &details,
		Instructions:     &instructions,
		Link:             &link,
		ObjectivityScore: 1.0,
		ImportanceScore:  0.9,
		Status:           domain.InputRequestStatusNeedsAssignment,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.db.CreateInputRequest(ctx, req); err != nil {
		log.Printf("setup: could not create the repository request: %v", err)
	}
}

// repositoryRequestTitle identifies the repository ask so it is neither
// duplicated nor left open once settings are filled in.
const repositoryRequestTitle = "Connect a repository"

// resolveRepositoryRequest closes the repository ask once the repository is
// actually configured. Called after settings are saved: an open request for
// something already done is noise that teaches people to ignore the queue.
func (s *Server) resolveRepositoryRequest(ctx context.Context, projectID uuid.UUID) {
	readiness, err := s.db.GetProjectReadiness(ctx, projectID)
	if err != nil || !readiness.IsReady() {
		return
	}

	req, err := s.db.FindOpenInputRequestByKind(ctx, projectID, domain.InputRequestKindManualSetup)
	if err != nil || req == nil || req.Title != repositoryRequestTitle {
		return
	}

	req.Status = domain.InputRequestStatusResolved
	req.Resolution = strPtr("approved")
	resolvedAt := time.Now()
	req.ResolvedAt = &resolvedAt
	if err := s.db.UpdateInputRequest(ctx, req); err != nil {
		log.Printf("setup: could not resolve the repository request: %v", err)
	}
}

// addOnboardingRibbon puts the setup ribbon on a page while the project is
// still being set up. It retires once the first variation exists: from then on
// the per-Hop ribbons say more about where the project stands than this one.
func (s *Server) addOnboardingRibbon(ctx context.Context, data map[string]interface{}) {
	projectIDStr, ok := data["ProjectID"].(string)
	if !ok || projectIDStr == "" {
		return
	}
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		return
	}
	state, err := s.db.GetOnboardingState(ctx, projectID)
	if err != nil || state.Complete() {
		return
	}
	data["OnboardingRibbon"] = ribbonView(domain.OnboardingLifecycle(state))
}

// drawConsiderations runs the first drafting pass and stores what it produced,
// returning both the stored rows and the keyed form the second pass reads.
//
// A failure here is deliberately not fatal. Drafting objectives from the brief
// alone is what Mendel did before this pass existed, and it is a much better
// outcome than a user watching a spinner turn into an error because the step
// that was meant to broaden their objectives could not run. What is lost is the
// coverage check, and the review screen says so rather than showing an empty
// list that reads as "nothing to consider".
func (s *Server) drawConsiderations(ctx context.Context, strategyID uuid.UUID,
	brief agent.StrategyBrief, feedback string) ([]domain.StrategicConsideration, []agent.KeyedConsideration) {

	client, err := agent.NewClient("")
	if err != nil {
		log.Printf("setup: consideration client for strategy %s: %v", strategyID, err)
		return nil, nil
	}

	// The previous list travels with a revision so that feedback refines the
	// reasoning instead of rerolling it: a user who says "you are ignoring the
	// people being surveyed" should watch that appear and watch the rest survive.
	existing, err := s.db.GetStrategicConsiderations(ctx, strategyID)
	if err != nil {
		log.Printf("setup: reading considerations for strategy %s: %v", strategyID, err)
	}
	prior := make([]agent.DrawnConsideration, 0, len(existing))
	for _, c := range existing {
		prior = append(prior, agent.DrawnConsideration{Kind: c.Kind, Statement: c.Statement})
	}

	drawn, spend, err := agent.NewConsiderationDrawer(client).Draw(ctx, agent.ConsiderationsInput{
		Brief:    brief,
		Existing: prior,
		Feedback: feedback,
	})
	s.recordStrategySpend(ctx, strategyID, "consideration_drawer", spend)
	if err != nil {
		log.Printf("setup: drawing considerations for strategy %s: %v", strategyID, err)
		return nil, nil
	}

	toStore := make([]db.DraftConsideration, 0, len(drawn))
	for _, c := range drawn {
		toStore = append(toStore, db.DraftConsideration{Kind: c.Kind, Statement: c.Statement})
	}
	ids, err := s.db.ReplaceStrategicConsiderations(ctx, strategyID, toStore)
	if err != nil {
		log.Printf("setup: saving considerations for strategy %s: %v", strategyID, err)
		return nil, nil
	}

	stored := make([]domain.StrategicConsideration, 0, len(drawn))
	keyed := make([]agent.KeyedConsideration, 0, len(drawn))
	for i, c := range drawn {
		stored = append(stored, domain.StrategicConsideration{
			ID: ids[i], StrategyID: strategyID, Kind: c.Kind, Statement: c.Statement, Position: i,
		})
		keyed = append(keyed, agent.KeyedConsideration{
			Ref: agent.ConsiderationRef(i), Kind: c.Kind, Statement: c.Statement,
		})
	}
	return stored, keyed
}

// coverageFromDraft turns the drafting agent's answer -- which talks in
// reference keys and objective order -- into rows about real ids.
//
// Two things are dropped on the floor on purpose. A reference key that matches
// no consideration is a hallucination and names nothing to record. And a
// consideration the agent mentioned in neither place is left unjudged rather
// than written down as a decline: "it did not say" and "it said no, because"
// are different facts, and the screen shows them differently.
func coverageFromDraft(drafted *agent.DraftedStrategy, considerations []domain.StrategicConsideration,
	objectiveIDs []uuid.UUID) []db.ConsiderationCoverage {

	byRef := make(map[string]uuid.UUID, len(considerations))
	for i, c := range considerations {
		byRef[agent.ConsiderationRef(i)] = c.ID
	}

	coverage := make([]db.ConsiderationCoverage, 0, len(considerations))
	claimed := make(map[string]bool, len(considerations))

	for i, obj := range drafted.Objectives {
		if i >= len(objectiveIDs) {
			break
		}
		objectiveID := objectiveIDs[i]
		for _, ref := range obj.Covers {
			considerationID, ok := byRef[ref]
			if !ok || claimed[ref] {
				continue
			}
			claimed[ref] = true
			coverage = append(coverage, db.ConsiderationCoverage{
				ConsiderationID: considerationID, ObjectiveID: &objectiveID,
			})
		}
	}

	// Covered wins over declined when the agent said both, because an objective
	// naming a consideration is the more specific claim of the two.
	for _, u := range drafted.Uncovered {
		considerationID, ok := byRef[u.Ref]
		if !ok || claimed[u.Ref] || u.Reason == "" {
			continue
		}
		claimed[u.Ref] = true
		coverage = append(coverage, db.ConsiderationCoverage{
			ConsiderationID: considerationID, UncoveredReason: u.Reason,
		})
	}

	return coverage
}

// considerationViews pairs each Strategic Consideration with the objective that
// answers it, so the review screen can show the two together.
//
// A read failure here yields no considerations rather than an error page. The
// draft itself is the thing the user came for, and losing the reasoning beside
// it is worth strictly less than losing the screen.
func considerationViews(ctx context.Context, s *Server, strategyID uuid.UUID,
	objectives []SetupObjectiveView) []SetupConsiderationView {

	considerations, err := s.db.GetStrategicConsiderations(ctx, strategyID)
	if err != nil {
		log.Printf("setup: reading considerations for strategy %s: %v", strategyID, err)
		return nil
	}

	type placed struct {
		index int
		text  string
	}
	byObjective := make(map[uuid.UUID]placed, len(objectives))
	for i, o := range objectives {
		byObjective[o.Objective.ID] = placed{index: i + 1, text: o.Objective.Description}
	}

	views := make([]SetupConsiderationView, 0, len(considerations))
	for _, c := range considerations {
		v := SetupConsiderationView{Consideration: c}
		if c.CoveredByObjectiveID != nil {
			if p, ok := byObjective[*c.CoveredByObjectiveID]; ok {
				v.CoveredIndex, v.CoveredBy = p.index, p.text
			}
		}
		views = append(views, v)
	}
	return views
}

// handleSaveOKRs applies edits to an approved strategy's OKRs.
//
// The same form as the review screen, saved in place instead of approving:
// after approval there is nothing left to approve, and a redraft would replace
// objectives the roadmap was drawn against.
func (s *Server) handleSaveOKRs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		http.Error(w, "no strategy found", http.StatusNotFound)
		return
	}
	strategy := strategies[0]

	// Before approval the same form belongs to the approve handler, which does
	// this and then sets the project in motion. Saving here instead would leave
	// a project that had reviewed its objectives without ever approving them.
	if !strategy.OKRsApproved() {
		s.handleApproveSetupOKRs(w, r)
		return
	}

	if err := s.saveOKREdits(ctx, &strategy, r); err != nil {
		s.renderSetupOKRs(ctx, w, r, projectID, "Could not save your edits: "+err.Error())
		return
	}

	// Newly written lines have no score yet. Scoring them is a model call, so
	// it runs detached on its own context -- the request's is cancelled the
	// moment the redirect is written, and the score is worth having on the next
	// render rather than worth waiting for on this one.
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), setupTimeout)
		defer cancel()
		s.tuneStrategyOKRs(bg, strategy.ID)
	}()

	http.Redirect(w, r, fmt.Sprintf("/p/%s/okr", projectID), http.StatusSeeOther)
}
