package web

import (
	"context"
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
	for _, c := range candidates {
		if c.ExperimentID == nil {
			continue
		}
		if e, err := s.db.GetExperiment(ctx, *c.ExperimentID); err == nil && e != nil {
			running = append(running, e)
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
		"Ready":        len(domain.ExperimentBlockers(domain.ExperimentReadiness(obs))) == 0,
		"Fingerprint":  obs.Fingerprint(),
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
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `{"fingerprint":%q,"checking":%t}`, obs.Fingerprint(), observedAt.IsZero())
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

	http.Redirect(w, r, back+"?success=1", http.StatusSeeOther)
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

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		logf := func(msg string) { log.Printf("experiment[%s]: %s", experimentID, msg) }
		if err := action(ctx, experimentID, logf); err != nil {
			log.Printf("experiment[%s]: %s failed: %v", experimentID, verb, err)
		}
	}()

	http.Redirect(w, r, "/p/"+projectID.String()+"/experiments?"+verb+"=1", http.StatusSeeOther)
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
