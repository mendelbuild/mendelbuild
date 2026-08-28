package web

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// predictedDeployURL is where a deployment will be reachable, when that is
// knowable before it exists.
//
// Fly.io's hostname is derived from the app name, so an acknowledgement that
// names the URL — registering an OAuth redirect URI — can be satisfied before
// the first deploy. Cloud Run assigns a hash at deploy time and GKE a
// LoadBalancer IP after provisioning, so on those platforms the URL only
// exists afterwards. Returning "" says so, and such acknowledgements are
// deferred rather than blocking a deploy on a string nobody can produce yet.
func predictedDeployURL(platformSlug, appName string) string {
	if platformSlug == "fly-io" {
		return fmt.Sprintf("https://%s.fly.dev", appName)
	}
	return ""
}

// variationRequirementStatus judges a variation's requirements against a
// deployment at deployURL, which may be "" when the URL is not yet known.
func (s *Server) variationRequirementStatus(
	ctx context.Context,
	projectID, variationID uuid.UUID,
	deployURL string,
) ([]domain.RequirementStatus, error) {
	reqs, err := s.db.ListVariationRequirements(ctx, variationID)
	if err != nil {
		return nil, fmt.Errorf("list requirements: %w", err)
	}
	if len(reqs) == 0 {
		return nil, nil
	}

	ev, err := s.db.RequirementEvidenceFor(ctx, projectID, reqs)
	if err != nil {
		return nil, fmt.Errorf("gather requirement evidence: %w", err)
	}
	return domain.EvaluateRequirements(reqs, ev, deployURL), nil
}

// prodRequirementStatus judges the requirements of everything merged to main,
// which is what a production deploy actually runs.
func (s *Server) prodRequirementStatus(
	ctx context.Context,
	projectID uuid.UUID,
	deployURL string,
) ([]domain.RequirementStatus, error) {
	reqs, err := s.db.ListMergedVariationRequirements(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list merged requirements: %w", err)
	}
	if len(reqs) == 0 {
		return nil, nil
	}

	ev, err := s.db.RequirementEvidenceFor(ctx, projectID, reqs)
	if err != nil {
		return nil, fmt.Errorf("gather requirement evidence: %w", err)
	}
	return domain.EvaluateRequirements(reqs, ev, deployURL), nil
}

// appSecretsFor decrypts the values for the 'secret' requirements among reqs,
// ready to be injected into the deployment as environment variables.
//
// A missing value is not an error here: gating runs before this and is where
// that is reported. Deploying without one produces an app that misbehaves in
// its own way, which is strictly more informative than a deploy that fails on
// a lookup the user has already been told about.
func (s *Server) appSecretsFor(
	ctx context.Context,
	projectID uuid.UUID,
	statuses []domain.RequirementStatus,
) (map[string]string, error) {
	var names []string
	for _, st := range statuses {
		if st.Requirement.Kind == domain.RequirementKindSecret {
			names = append(names, st.Requirement.Name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}

	key, err := crypto.GetKey()
	if err != nil {
		return nil, fmt.Errorf("encryption not configured: %w", err)
	}

	secrets := make(map[string]string, len(names))
	for _, name := range names {
		v, err := s.db.GetProjectEnvVar(ctx, projectID, name)
		if err != nil {
			continue // Not stored; gating already reported it.
		}
		decrypted, err := crypto.Decrypt(v.EncryptedValue, key)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", name, err)
		}
		secrets[name] = string(decrypted)
	}
	return secrets, nil
}

// handleSetRequirementValue stores the value behind a 'secret' requirement.
// Values are project-scoped, so entering one here serves every variation that
// needs it and production too.
func (s *Server) handleSetRequirementValue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	variationID := chi.URLParam(r, "variationID")

	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	req, err := s.requirementForProject(ctx, projectUUID, chi.URLParam(r, "requirementID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if req.Kind != domain.RequirementKindSecret {
		http.Error(w, "this requirement is not a value", http.StatusBadRequest)
		return
	}

	value := r.FormValue("value")
	if value == "" {
		http.Error(w, "no value provided", http.StatusBadRequest)
		return
	}

	key, err := crypto.GetKey()
	if err != nil {
		http.Error(w, "encryption not configured: "+err.Error(), http.StatusInternalServerError)
		return
	}
	encrypted, err := crypto.Encrypt([]byte(value), key)
	if err != nil {
		http.Error(w, "failed to encrypt value: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.db.UpsertProjectEnvVar(ctx, projectUUID, req.Name, encrypted); err != nil {
		http.Error(w, "failed to store value: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// handleAcknowledgeRequirement records that the user carried out an
// acknowledgement for one particular resolved string.
func (s *Server) handleAcknowledgeRequirement(w http.ResponseWriter, r *http.Request) {
	s.recordAcknowledgement(w, r, true)
}

// handleRetractAcknowledgement undoes one, for when the user finds they had
// not in fact done it.
func (s *Server) handleRetractAcknowledgement(w http.ResponseWriter, r *http.Request) {
	s.recordAcknowledgement(w, r, false)
}

func (s *Server) recordAcknowledgement(w http.ResponseWriter, r *http.Request, done bool) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")
	variationID := chi.URLParam(r, "variationID")

	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	req, err := s.requirementForProject(ctx, projectUUID, chi.URLParam(r, "requirementID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if req.Kind != domain.RequirementKindAcknowledgement {
		http.Error(w, "this requirement is not an acknowledgement", http.StatusBadRequest)
		return
	}

	// The form carries the exact string the user was shown. Confirmation is
	// about that string, not about the requirement in the abstract, so a
	// deployment at a different URL is knowingly still unconfirmed.
	resolved := r.FormValue("resolved_value")
	if resolved == "" {
		http.Error(w, "nothing to confirm", http.StatusBadRequest)
		return
	}

	if done {
		var by *uuid.UUID
		if user := UserFromContext(ctx); user != nil {
			by = &user.ID
		}
		err = s.db.AcknowledgeRequirement(ctx, req.ID, resolved, by)
	} else {
		err = s.db.RetractAcknowledgement(ctx, req.ID, resolved)
	}
	if err != nil {
		http.Error(w, "failed to record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// requirementForProject looks up a requirement and confirms it belongs to this
// project, so a requirement ID from elsewhere cannot be used to write a value
// into an unrelated project.
func (s *Server) requirementForProject(ctx context.Context, projectID uuid.UUID, requirementID string) (*domain.VariationRequirement, error) {
	id, err := uuid.Parse(requirementID)
	if err != nil {
		return nil, fmt.Errorf("invalid requirement ID")
	}

	req, err := s.db.GetVariationRequirement(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("requirement not found")
	}

	owner, err := s.db.GetProjectIDForVariation(ctx, req.VariationID)
	if err != nil || owner != projectID {
		return nil, fmt.Errorf("requirement not found")
	}
	return req, nil
}
