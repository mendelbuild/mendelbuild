package web

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// ProjectSettings holds the settings form data.
type ProjectSettings struct {
	RepoURL         string
	MainBranch      string
	AuthToken       string
	AnthropicAPIKey string
}

// CloudCredentialView represents a credential for display in the UI.
type CloudCredentialView struct {
	ID       string
	Name     string
	HasValue bool // true if a value is set (don't expose actual value)
}

// RequiredCredentialView shows a credential required by deploy-config.yml
type RequiredCredentialView struct {
	Name         string
	IsConfigured bool
}

// fetchRequiredCredentials fetches deploy-config.yml from GitHub and returns required credential names.
func fetchRequiredCredentials(repoURL, authToken string) []string {
	if repoURL == "" || authToken == "" {
		return nil
	}

	// Convert repo URL to GitHub API URL
	// https://github.com/user/repo -> https://api.github.com/repos/user/repo/contents/.mendel/deploy-config.yml
	repoURL = strings.TrimSuffix(repoURL, ".git")
	if !strings.HasPrefix(repoURL, "https://github.com/") {
		return nil
	}
	repoPath := strings.TrimPrefix(repoURL, "https://github.com/")
	apiURL := "https://api.github.com/repos/" + repoPath + "/contents/.mendel/deploy-config.yml"

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	// Parse GitHub API response
	var ghResp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(body, &ghResp); err != nil {
		return nil
	}

	if ghResp.Encoding != "base64" {
		return nil
	}

	content, err := base64.StdEncoding.DecodeString(ghResp.Content)
	if err != nil {
		return nil
	}

	// Parse deploy config
	var config struct {
		Credentials []string `yaml:"credentials"`
	}
	if err := yaml.Unmarshal(content, &config); err != nil {
		return nil
	}

	return config.Credentials
}

func (s *Server) handleProjectSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	// Load current settings
	settings := ProjectSettings{
		MainBranch: "main", // Default
	}

	// Get repository config
	repo, err := s.db.GetRepositoryByProject(ctx, projectID)
	if err == nil && repo != nil {
		if repo.URL != nil {
			settings.RepoURL = *repo.URL
		}
		if repo.Config != nil {
			var repoConfig struct {
				MainBranch string `json:"main_branch"`
				AuthToken  string `json:"auth_token"`
			}
			if json.Unmarshal(repo.Config, &repoConfig) == nil {
				if repoConfig.MainBranch != "" {
					settings.MainBranch = repoConfig.MainBranch
				}
				settings.AuthToken = repoConfig.AuthToken
			}
		}
	}

	// Get project config (API key)
	project, err := s.db.GetProject(ctx, projectID)
	if err == nil && project != nil && project.Config != nil {
		var projectConfig domain.ProjectConfig
		if json.Unmarshal(project.Config, &projectConfig) == nil {
			settings.AnthropicAPIKey = projectConfig.AnthropicAPIKey
		}
	}

	// Get cloud credentials (names only, not values)
	var cloudCredentials []CloudCredentialView
	configuredCreds := make(map[string]bool)
	creds, err := s.db.ListProjectCredentials(ctx, projectID)
	if err == nil {
		for _, c := range creds {
			cloudCredentials = append(cloudCredentials, CloudCredentialView{
				ID:       c.ID.String(),
				Name:     c.Name,
				HasValue: true, // If it's in the list, it has a value
			})
			configuredCreds[c.Name] = true
		}
	}

	// Fetch required credentials from deploy-config.yml
	var requiredCredentials []RequiredCredentialView
	requiredNames := fetchRequiredCredentials(settings.RepoURL, settings.AuthToken)
	for _, name := range requiredNames {
		requiredCredentials = append(requiredCredentials, RequiredCredentialView{
			Name:         name,
			IsConfigured: configuredCreds[name],
		})
	}

	// Check for success message
	success := r.URL.Query().Get("success") == "1"

	data := map[string]interface{}{
		"Title":               "Project Settings",
		"ProjectID":           projectID.String(),
		"Settings":            settings,
		"CloudCredentials":    cloudCredentials,
		"RequiredCredentials": requiredCredentials,
		"Success":             success,
	}
	s.addOpenInputCount(ctx, data)

	if err := renderPage(w, "project_settings.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleSaveProjectSettings(w http.ResponseWriter, r *http.Request) {
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

	// Get form values
	repoURL := r.FormValue("repo_url")
	mainBranch := r.FormValue("main_branch")
	authToken := r.FormValue("auth_token")
	anthropicAPIKey := r.FormValue("anthropic_api_key")

	if mainBranch == "" {
		mainBranch = "main"
	}

	// Update repository config
	repoConfig, _ := json.Marshal(map[string]string{
		"main_branch": mainBranch,
		"auth_token":  authToken,
	})

	if err := s.db.UpsertRepository(ctx, projectID, repoURL, repoConfig); err != nil {
		renderSettingsWithError(w, projectID, "Failed to save repository settings: "+err.Error())
		return
	}

	// Update project config (API key)
	projectConfig, _ := json.Marshal(domain.ProjectConfig{
		AnthropicAPIKey: anthropicAPIKey,
	})

	if err := s.db.UpdateProjectConfig(ctx, projectID, projectConfig); err != nil {
		renderSettingsWithError(w, projectID, "Failed to save project settings: "+err.Error())
		return
	}

	// Redirect back with success message
	http.Redirect(w, r, "/p/"+projectID.String()+"/settings?success=1", http.StatusSeeOther)
}

func renderSettingsWithError(w http.ResponseWriter, projectID uuid.UUID, errMsg string) {
	data := map[string]interface{}{
		"Title":     "Project Settings",
		"ProjectID": projectID.String(),
		"Error":     errMsg,
		"Settings":  ProjectSettings{MainBranch: "main"},
	}
	renderPage(w, "project_settings.html", data)
}

// handleAddCloudCredential handles adding a new cloud credential.
func (s *Server) handleAddCloudCredential(w http.ResponseWriter, r *http.Request) {
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

	name := strings.TrimSpace(r.FormValue("credential_name"))
	value := r.FormValue("credential_value")

	if name == "" {
		renderSettingsWithError(w, projectID, "Credential name is required")
		return
	}

	// Get encryption key and encrypt the value
	key, err := crypto.GetKey()
	if err != nil {
		renderSettingsWithError(w, projectID, "Encryption not configured: "+err.Error())
		return
	}

	encryptedValue, err := crypto.Encrypt([]byte(value), key)
	if err != nil {
		renderSettingsWithError(w, projectID, "Failed to encrypt credential: "+err.Error())
		return
	}

	cred := &domain.ProjectCredential{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Name:           name,
		EncryptedValue: encryptedValue,
	}

	if err := s.db.CreateProjectCredential(ctx, cred); err != nil {
		renderSettingsWithError(w, projectID, "Failed to save credential: "+err.Error())
		return
	}

	// Auto-resolve any credential_request InputRequests that were waiting for this credential
	_ = s.db.ResolveCredentialRequestsByName(ctx, projectID, name)

	http.Redirect(w, r, "/p/"+projectID.String()+"/settings?success=1", http.StatusSeeOther)
}

// handleUpdateCloudCredential handles updating an existing cloud credential.
func (s *Server) handleUpdateCloudCredential(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	credID, err := uuid.Parse(chi.URLParam(r, "credentialID"))
	if err != nil {
		http.Error(w, "invalid credential ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	value := r.FormValue("credential_value")

	// Get encryption key and encrypt the value
	key, err := crypto.GetKey()
	if err != nil {
		renderSettingsWithError(w, projectID, "Encryption not configured: "+err.Error())
		return
	}

	encryptedValue, err := crypto.Encrypt([]byte(value), key)
	if err != nil {
		renderSettingsWithError(w, projectID, "Failed to encrypt credential: "+err.Error())
		return
	}

	if err := s.db.UpdateProjectCredential(ctx, credID, encryptedValue); err != nil {
		renderSettingsWithError(w, projectID, "Failed to update credential: "+err.Error())
		return
	}

	// Get the credential name to resolve matching InputRequests
	creds, _ := s.db.ListProjectCredentials(ctx, projectID)
	for _, c := range creds {
		if c.ID == credID {
			_ = s.db.ResolveCredentialRequestsByName(ctx, projectID, c.Name)
			break
		}
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/settings?success=1", http.StatusSeeOther)
}

// handleDeleteCloudCredential handles deleting a cloud credential.
func (s *Server) handleDeleteCloudCredential(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	credID, err := uuid.Parse(chi.URLParam(r, "credentialID"))
	if err != nil {
		http.Error(w, "invalid credential ID", http.StatusBadRequest)
		return
	}

	if err := s.db.DeleteProjectCredential(ctx, credID); err != nil {
		renderSettingsWithError(w, projectID, "Failed to delete credential: "+err.Error())
		return
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/settings?success=1", http.StatusSeeOther)
}

// handleRedeploy triggers a redeployment of the current main branch to production.
func (s *Server) handleRedeploy(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	// TODO: Implement actual deployment logic
	// 1. Get repository info
	// 2. Clone main branch
	// 3. Run deploy script with credentials
	// 4. Update deployed_instances table

	// For now, just redirect back with a message
	http.Redirect(w, r, "/p/"+projectID.String()+"/strategy?redeploy=pending", http.StatusSeeOther)
}
