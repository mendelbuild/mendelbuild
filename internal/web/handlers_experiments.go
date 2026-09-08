package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/assigner"
	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/domain"
)

// The live-traffic experiments settings area.
//
// Every property here is project-scoped -- a cluster, a domain, a datastore --
// and none is implied by the deployment channel working. Gathering them in one
// place is the difference between a user who can see what enabling experiments
// costs them and one who discovers it a piece at a time, each time something is
// already blocked.

func (s *Server) handleProjectExperiments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	project, err := s.db.GetProject(ctx, projectID)
	if err != nil || project == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	candidates, err := s.db.ListExperimentCandidates(ctx, projectID)
	if err != nil {
		log.Printf("experiments[%s]: could not list candidates: %v", projectID, err)
	}
	var running []*domain.Experiment
	failures := map[uuid.UUID]*domain.FailureReport{}
	for _, c := range candidates {
		if c.ExperimentID == nil {
			continue
		}
		if e, err := s.db.GetExperiment(ctx, *c.ExperimentID); err == nil && e != nil {
			running = append(running, e)
			if f := s.latestFailure(ctx, e.ID); f != nil {
				failures[e.ID] = f
			}
		}
	}

	obs, observedAt := s.experimentObservationFor(projectID)
	steps := domain.ExperimentReadiness(obs)
	headline, blocked := domain.ExperimentHeadline(steps)

	data := map[string]interface{}{
		"Title":        "Live-traffic experiments: " + project.Name,
		"SettingsTab":  "experiments",
		"ProjectID":    projectID.String(),
		"Project":      project,
		"Steps":        steps,
		"Headline":     headline,
		"Blocked":      blocked,
		"Checking":     observedAt.IsZero(),
		"CheckedLabel": checkedLabel(observedAt),
		"Observation":  obs,
		"DatastoreVar": VerifyDatastoreVar,
		"Candidates":   candidates,
		"Experiments":  running,
		"Failures":     failures,
		"Ready":        len(domain.ExperimentBlockers(domain.ExperimentReadiness(obs))) == 0,
		"Fingerprint":  obs.Fingerprint() + "|" + s.experimentFingerprint(ctx, projectID),
		"Success":      r.URL.Query().Get("success") == "1",
		"Installing":   r.URL.Query().Get("installing") == "1",
		"Starting":     r.URL.Query().Get("starting") == "1",
		"Stopping":     r.URL.Query().Get("stopping") == "1",
		"Error":        r.URL.Query().Get("error"),
	}
	s.addOpenInputCount(ctx, data)

	if err := s.renderPageFor(w, r, "project_experiments.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleSaveVerifyDatastore stores the connection to the non-production
// datastore, encrypted beside the project's other secrets.
func (s *Server) handleSaveVerifyDatastore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	back := "/p/" + projectID.String() + "/experiments"

	value := strings.TrimSpace(r.FormValue("connection"))
	if value == "" {
		// Clearing it is a real intention: a project that stops running
		// experiments should be able to stop handing Mendel a database.
		if err := s.db.DeleteProjectEnvVar(ctx, projectID, VerifyDatastoreVar); err != nil {
			http.Redirect(w, r, back+"?error=could+not+remove+the+connection", http.StatusSeeOther)
			return
		}
		s.invalidateExperimentObservation(projectID)
		http.Redirect(w, r, back+"?success=1", http.StatusSeeOther)
		return
	}

	key, err := crypto.GetKey()
	if err != nil {
		http.Redirect(w, r, back+"?error=encryption+is+not+configured", http.StatusSeeOther)
		return
	}
	encrypted, err := crypto.Encrypt([]byte(value), key)
	if err != nil {
		http.Redirect(w, r, back+"?error=could+not+encrypt+the+connection", http.StatusSeeOther)
		return
	}
	if err := s.db.UpsertProjectEnvVar(ctx, projectID, VerifyDatastoreVar, encrypted); err != nil {
		http.Redirect(w, r, back+"?error=could+not+store+the+connection", http.StatusSeeOther)
		return
	}

	// Mendel has just changed something the observation describes, so serving
	// the old one would show the state from before the change.
	s.invalidateExperimentObservation(projectID)
	http.Redirect(w, r, back+"?success=1", http.StatusSeeOther)
}


// handleInstallExperimentController installs the gateway controller that can
// match cookies.
//
// Mendel does this rather than handing over a command, because it has to be able
// to for any project: a user who must run kubectl themselves before their first
// experiment has been handed a prerequisite, not a product. The command remains
// for the case Mendel's credentials cannot, which is a real case and is reported
// rather than guessed at.
func (s *Server) handleInstallExperimentController(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	back := "/p/" + projectID.String() + "/experiments"

	// Detached from the request: installing pulls a manifest, applies it and
	// waits for a controller to come up, which outlasts a browser that has given
	// up. The page reports what happened from the observation afterwards.
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()

		logInfo := func(msg string) { log.Printf("experiments[%s]: %s", projectID, msg) }
		if err := s.installExperimentController(bg, projectID, logInfo); err != nil {
			log.Printf("experiments[%s]: installing the controller failed: %v", projectID, err)
		}
	}()

	http.Redirect(w, r, back+"?installing=1", http.StatusSeeOther)
}


// handleExperimentStatus reports whether anything on the page has changed.
//
// The readiness checks run in the background -- they reach a cluster and a
// database -- so the first render of a cold page shows "Checking" and would sit
// there until someone reloaded. Telling the page when to reload itself is a few
// lines; telling the user to reload is a few lines of documentation and a worse
// product.
func (s *Server) handleExperimentStatus(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	obs, observedAt := s.experimentObservationFor(projectID)

	// Readiness and the experiments together. The first version sent only
	// readiness, so an experiment moving from running to stopped changed
	// nothing the page was watching -- it sat there still offering Stop, which
	// reads as a button that did not work.
	fingerprint := obs.Fingerprint() + "|" + s.experimentFingerprint(r.Context(), projectID)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `{"fingerprint":%q,"checking":%t}`, fingerprint, observedAt.IsZero())
}

// handleCreateExperiment turns a Hop's Variations into Arms.
//
// Equal shares, because nothing is known yet that would justify anything else,
// and an uneven split chosen by default is a decision nobody made.
func (s *Server) handleCreateExperiment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	back := "/p/" + projectID.String() + "/experiments"

	hopID, err := uuid.Parse(r.FormValue("hop_id"))
	if err != nil {
		http.Redirect(w, r, back+"?error=pick+a+hop", http.StatusSeeOther)
		return
	}

	variations, err := s.db.GetVariationsByHop(ctx, hopID)
	if err != nil {
		http.Redirect(w, r, back+"?error=could+not+read+the+variations", http.StatusSeeOther)
		return
	}
	var usable []domain.Variation
	for _, v := range variations {
		if v.Status != domain.VariationStatusCreating && v.Status != domain.VariationStatusTerminated {
			usable = append(usable, v)
		}
	}
	if len(usable) == 0 {
		http.Redirect(w, r, back+"?error=that+hop+has+no+variations+to+compare", http.StatusSeeOther)
		return
	}

	mde := 0.02
	if v, err := strconv.ParseFloat(r.FormValue("mde"), 64); err == nil && v > 0 {
		mde = v
	}
	hours := 336
	if v, err := strconv.Atoi(r.FormValue("duration_hours")); err == nil && v > 0 {
		hours = v
	}
	rule := domain.StoppingRule(r.FormValue("stopping_rule"))
	if rule != domain.StoppingSequential {
		rule = domain.StoppingFixedHorizon
	}

	exp := &domain.Experiment{
		ProjectID: projectID, HopID: hopID,
		// The device path: Mendel's own cookie identifies a browser, which is
		// what it can establish without the application's help. Per-user
		// assignment needs the application to mint a bucket, which it does not
		// do yet.
		AssignmentUnit:      domain.AssignmentUnitSession,
		AssignmentKeySource: domain.AssignmentKeyCookie,
		AssignmentKeyName:   assigner.CookieName,
		MinimumDetectableEffect: &mde,
		PlannedDurationHours:    &hours,
		StoppingRule:            rule,
	}
	if err := s.db.CreateExperiment(ctx, exp); err != nil {
		http.Redirect(w, r, back+"?error=could+not+create+the+experiment", http.StatusSeeOther)
		return
	}

	// Mainline plus one Arm per Variation, sharing traffic evenly. The remainder
	// goes to mainline rather than being dropped, so the shares always total a
	// whole and no visitor falls through an allocation that adds to 99.
	arms := len(usable) + 1
	each := 100 / arms
	if err := s.db.CreateExperimentArm(ctx, &domain.ExperimentArm{
		ExperimentID: exp.ID, Slug: domain.MainlineSlug,
		AllocationWeight: 100 - each*len(usable),
	}); err != nil {
		http.Redirect(w, r, back+"?error=could+not+create+the+control+arm", http.StatusSeeOther)
		return
	}
	for i := range usable {
		if err := s.db.UpsertExperimentArm(ctx, &domain.ExperimentArm{
			ExperimentID: exp.ID, VariationID: &usable[i].ID,
			Slug:             armSlugFor(usable[i]),
			AllocationWeight: each,
		}); err != nil {
			http.Redirect(w, r, back+"?error=could+not+create+an+arm", http.StatusSeeOther)
			return
		}
	}

	http.Redirect(w, r, fmt.Sprintf("/p/%s/hops/%s?created=1", projectID, hopID), http.StatusSeeOther)
}

// handleStartExperiment takes live traffic.
func (s *Server) handleStartExperiment(w http.ResponseWriter, r *http.Request) {
	s.runExperimentAction(w, r, "starting", func(ctx context.Context, id uuid.UUID, log func(string)) error {
		return s.StartExperiment(ctx, id, log, log)
	})
}

// handleStopExperiment returns every visitor to mainline.
func (s *Server) handleStopExperiment(w http.ResponseWriter, r *http.Request) {
	s.runExperimentAction(w, r, "stopping", func(ctx context.Context, id uuid.UUID, log func(string)) error {
		return s.StopExperiment(ctx, id, "Stopped from the experiments page", log)
	})
}

// runExperimentAction runs one in the background and returns immediately.
//
// Starting builds an image per Arm, which outlasts any browser's patience. The
// page reports what happened from the experiment's own status afterwards.
func (s *Server) runExperimentAction(w http.ResponseWriter, r *http.Request, verb string,
	action func(context.Context, uuid.UUID, func(string)) error) {

	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	experimentID, err := uuid.Parse(chi.URLParam(r, "experimentID"))
	if err != nil {
		http.Error(w, "invalid experiment ID", http.StatusBadRequest)
		return
	}

	// Where to come back to, read before the work starts: the experiment is the
	// only thing that knows which Hop it belongs to, and it is about to be
	// changed by a goroutine.
	back := "/p/" + projectID.String() + "/experiments"
	if exp, err := s.db.GetExperiment(r.Context(), experimentID); err == nil && exp != nil {
		back = s.experimentReturnTo(r.Context(), exp)
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		logf := func(msg string) { log.Printf("experiment[%s]: %s", experimentID, msg) }

		err := action(ctx, experimentID, logf)
		if err == nil {
			return
		}
		// The log is for whoever maintains Mendel. The record is for whoever
		// pressed the button, who cannot act on a kubectl error and needs to
		// know what happened to their traffic. Without this a failed action
		// left no trace in the product at all, and the page simply offered the
		// button again -- which reads as the button not working.
		log.Printf("experiment[%s]: %s failed: %v", experimentID, verb, err)
		s.recordFailure(ctx, experimentID, verb, err)
	}()

	http.Redirect(w, r, back+"?"+verb+"=1", http.StatusSeeOther)
}

// armSlugFor names an Arm after its Variation, uniquely.
func armSlugFor(v domain.Variation) string {
	name := strings.TrimSpace(v.Name)
	if name == "" {
		name = "arm"
	}
	// The id's prefix because Variation names are not unique, and two Arms
	// sharing a slug would share a cookie value and a route.
	return sanitizeAppName(name) + "-" + v.ID.String()[:6]
}


// experimentFingerprint changes whenever an experiment's state does.
//
// Separate from the readiness observation because the two move for different
// reasons and on different clocks: readiness is polled from the cluster, while
// an experiment changes when somebody presses a button and again when the work
// behind it finishes. A page watching only the first misses the second entirely.
func (s *Server) experimentFingerprint(ctx context.Context, projectID uuid.UUID) string {
	candidates, err := s.db.ListExperimentCandidates(ctx, projectID)
	if err != nil {
		return "unknown"
	}
	var b strings.Builder
	for _, c := range candidates {
		if c.ExperimentID == nil {
			// A hop with no experiment is still worth tracking: creating one is
			// a change the page should notice.
			fmt.Fprintf(&b, "%s=none;", c.HopID)
			continue
		}
		fmt.Fprintf(&b, "%s=%s;", c.ExperimentID, c.Status)
	}
	return b.String()
}


// recordFailure writes what went wrong where the page can find it.
func (s *Server) recordFailure(ctx context.Context, experimentID uuid.UUID, verb string, cause error) {
	report := domain.ReportStartFailure(cause.Error())
	if verb == "stopping" {
		// Whether traffic came back decides what to tell them, and it is the
		// first thing the stop does -- so a failure after that point is far less
		// serious than one before it.
		returned := !strings.Contains(cause.Error(), "point")
		report = domain.ReportStopFailure(cause.Error(), returned)
	}

	detail, err := json.Marshal(map[string]string{
		"summary": report.Summary, "effect": report.Effect, "detail": report.Detail,
	})
	if err != nil {
		detail = nil
	}
	s.db.RecordExperimentEvent(ctx, &domain.ExperimentEvent{
		ExperimentID: experimentID,
		Kind:         domain.EventFailed,
		Detail:       report.Summary + " " + report.Effect,
		Data:         detail,
	})
}

// latestFailure returns the most recent failure of an experiment, or nil.
//
// Only shown while it is still the last thing that happened: a failure followed
// by a successful start is history, not a warning, and leaving it on screen
// would report a problem that has been fixed.
func (s *Server) latestFailure(ctx context.Context, experimentID uuid.UUID) *domain.FailureReport {
	events, err := s.db.GetExperimentEvents(ctx, experimentID)
	if err != nil || len(events) == 0 {
		return nil
	}
	last := events[len(events)-1]
	if last.Kind != domain.EventFailed {
		return nil
	}

	var fields map[string]string
	if err := json.Unmarshal(last.Data, &fields); err != nil {
		return &domain.FailureReport{Summary: last.Detail}
	}
	return &domain.FailureReport{
		Summary: fields["summary"], Effect: fields["effect"], Detail: fields["detail"],
	}
}

// --- Managing one experiment, from its Hop's page ---

// handleRefreshExperiment rebases every Arm onto main, rebuilds and rolls out.
func (s *Server) handleRefreshExperiment(w http.ResponseWriter, r *http.Request) {
	s.runExperimentAction(w, r, "refreshing", func(ctx context.Context, id uuid.UUID, log func(string)) error {
		return s.RefreshExperimentArms(ctx, id, nil, log, log)
	})
}

// handleRefreshExperimentArm does the same for one Arm.
//
// Both scopes, because both are real. A change to one Variation deserves one
// rebuild; mainline moving deserves all of them, since every Arm is then being
// compared against a control it no longer contains.
func (s *Server) handleRefreshExperimentArm(w http.ResponseWriter, r *http.Request) {
	armID, err := uuid.Parse(chi.URLParam(r, "armID"))
	if err != nil {
		http.Error(w, "invalid arm ID", http.StatusBadRequest)
		return
	}
	s.runExperimentAction(w, r, "refreshing", func(ctx context.Context, id uuid.UUID, log func(string)) error {
		return s.RefreshExperimentArms(ctx, id, &armID, log, log)
	})
}

// handleSampleSplit sends a few unassigned requests and reports which Arm served
// each.
//
// In the foreground, unlike start and stop. It takes seconds rather than
// minutes, and the person who pressed it is standing there waiting for the
// answer -- a background job whose result appears on a later reload would be a
// worse version of running curl by hand.
func (s *Server) handleSampleSplit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	experimentID, err := uuid.Parse(chi.URLParam(r, "experimentID"))
	if err != nil {
		http.Error(w, "invalid experiment ID", http.StatusBadRequest)
		return
	}
	exp, err := s.db.GetExperiment(ctx, experimentID)
	if err != nil || exp == nil {
		http.Error(w, "experiment not found", http.StatusNotFound)
		return
	}

	host := ""
	if pd, err := s.db.GetProjectDomain(ctx, exp.ProjectID); err == nil && pd != nil {
		host = pd.ProdHost()
	}

	sampleCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	s.recordSplitSample(experimentID, s.SampleSplit(sampleCtx, exp, host))

	http.Redirect(w, r, s.experimentReturnTo(ctx, exp)+"?sampled=1", http.StatusSeeOther)
}

// handleExperimentJSON reports one experiment in full, for a reader who would
// rather have the data than the page.
//
// Project-scoped and behind the same authentication as everything else here.
// Mendel's own /version is neither -- it is public and says nothing about any
// project -- so putting per-project experiment state on it would publish which
// projects exist and what they are running to whoever asks the load balancer.
func (s *Server) handleExperimentJSON(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	experimentID, err := uuid.Parse(chi.URLParam(r, "experimentID"))
	if err != nil {
		http.Error(w, "invalid experiment ID", http.StatusBadRequest)
		return
	}
	exp, err := s.db.GetExperiment(ctx, experimentID)
	if err != nil || exp == nil || exp.ProjectID != projectID {
		http.Error(w, "experiment not found", http.StatusNotFound)
		return
	}

	view := s.experimentViewFor(ctx, projectID, exp.HopID)
	if view == nil {
		http.Error(w, "experiment not found", http.StatusNotFound)
		return
	}

	type armJSON struct {
		Slug       string          `json:"slug"`
		Name       string          `json:"name"`
		Mainline   bool            `json:"mainline"`
		Allocation int             `json:"allocation_weight"`
		Build      domain.ArmBuild `json:"build"`
		Reach      string          `json:"reach"`
	}
	out := struct {
		ID     uuid.UUID `json:"id"`
		HopID  uuid.UUID `json:"hop_id"`
		Status string    `json:"status"`
		Host   string    `json:"production_host"`

		// Named so a reader can find the arm they are on without being told
		// separately what to look for.
		ArmHeader  string `json:"arm_header"`
		ArmCookie  string `json:"arm_cookie"`
		Blockers   []string `json:"blockers,omitempty"`
		Checking   bool     `json:"readiness_checking"`
		Arms       []armJSON `json:"arms"`
	}{
		ID: exp.ID, HopID: exp.HopID, Status: string(exp.Status), Host: view.ProdHost,
		ArmHeader: ArmHeader, ArmCookie: assigner.CookieName,
		Blockers: view.Blockers, Checking: view.Checking,
	}
	for _, a := range view.Arms {
		out.Arms = append(out.Arms, armJSON{
			Slug: a.Arm.Slug, Name: a.Name(), Mainline: a.Mainline(),
			Allocation: a.Arm.AllocationWeight, Build: a.Build, Reach: a.Reach,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}

// experimentReturnTo is the Hop's page, which is where an experiment is managed
// from. Derived from the experiment rather than passed in a form field, so a
// redirect cannot be aimed anywhere else.
func (s *Server) experimentReturnTo(ctx context.Context, exp *domain.Experiment) string {
	return fmt.Sprintf("/p/%s/hops/%s", exp.ProjectID, exp.HopID)
}
