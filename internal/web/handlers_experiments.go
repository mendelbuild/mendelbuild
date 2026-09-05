package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

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
		"Fingerprint":  obs.Fingerprint(),
		"Success":      r.URL.Query().Get("success") == "1",
		"Installing":   r.URL.Query().Get("installing") == "1",
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
