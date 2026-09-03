package web

import (
	"net/http"
	"strings"

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
		"Success":      r.URL.Query().Get("success") == "1",
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
