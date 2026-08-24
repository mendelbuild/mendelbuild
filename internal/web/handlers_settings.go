package web

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

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

	// Get members (only if auth is enabled)
	var members []struct {
		User domain.User
		Role domain.ProjectMemberRole
	}
	var isOwner bool
	if s.authEnabled {
		members, _ = s.db.GetProjectMembers(ctx, projectID)
		user := UserFromContext(ctx)
		if user != nil {
			role, err := s.db.GetProjectMemberRole(ctx, projectID, user.ID)
			isOwner = err == nil && role == domain.ProjectMemberRoleOwner
		}
	}

	// Get demo hosting status from project config
	var demoHostingPlatform, demoScriptStatus string
	if project != nil && project.Config != nil {
		var cfg map[string]interface{}
		if json.Unmarshal(project.Config, &cfg) == nil {
			if platform, ok := cfg["demo_hosting_platform"].(string); ok {
				demoHostingPlatform = platform
			}
			if status, ok := cfg["demo_script_status"].(string); ok {
				demoScriptStatus = status
			}
		}
	}

	data := map[string]interface{}{
		"Title":               "Project Settings",
		"ProjectID":           projectID.String(),
		"Settings":            settings,
		"CloudCredentials":    cloudCredentials,
		"RequiredCredentials": requiredCredentials,
		"Success":             success,
		"AuthEnabled":         s.authEnabled,
		"Members":             members,
		"IsOwner":             isOwner,
		"DemoHostingPlatform": demoHostingPlatform,
		"DemoScriptStatus":    demoScriptStatus,
	}
	s.addOpenInputCount(ctx, data)
	s.addUserToData(r, data)

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

	// Validate GitHub token has push permissions before saving
	if repoURL != "" && authToken != "" {
		if err := validateGitHubToken(repoURL, authToken); err != nil {
			renderSettingsWithError(w, projectID, err.Error())
			return
		}
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

// validateGitHubToken checks if a GitHub token has push permission to the repo.
// Returns nil if valid, or an error with clear guidance on how to fix.
func validateGitHubToken(repoURL, token string) error {
	if token == "" {
		return nil // No token is allowed (public repos, or user will add later)
	}

	// Extract owner/repo from URL
	// Handles: https://github.com/owner/repo, https://github.com/owner/repo.git
	pattern := regexp.MustCompile(`github\.com[/:]([^/]+)/([^/.]+)`)
	matches := pattern.FindStringSubmatch(repoURL)
	if len(matches) < 3 {
		return nil // Not a GitHub URL, skip validation
	}
	owner, repo := matches[1], matches[2]

	// Call GitHub API to check permissions
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to GitHub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return fmt.Errorf("GitHub token is invalid or expired. Please generate a new token at https://github.com/settings/tokens")
	}

	if resp.StatusCode == 404 {
		return fmt.Errorf("Repository not found or token doesn't have access. Check that the repo URL is correct and the token has repository access.")
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API error (%d): %s", resp.StatusCode, string(body))
	}

	// Parse response to check permissions
	var repoInfo struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&repoInfo); err != nil {
		return fmt.Errorf("failed to parse GitHub response: %w", err)
	}

	if !repoInfo.Permissions.Push {
		return fmt.Errorf(`Token doesn't have push permission to this repository.

To fix this, create a new token at https://github.com/settings/tokens with:
• For Fine-grained tokens: Set "Contents" permission to "Read and write"
• For Classic tokens: Enable the "repo" scope

Then paste the new token here.`)
	}

	return nil
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

	// Trigger demo script validation if waiting for credentials
	go s.TriggerDemoScriptValidation(projectID)

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

// handleAddMember adds a member to a project.
func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	// Check that current user is owner
	currentUser := UserFromContext(ctx)
	if currentUser == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	role, err := s.db.GetProjectMemberRole(ctx, projectID, currentUser.ID)
	if err != nil || role != domain.ProjectMemberRoleOwner {
		http.Error(w, "only owners can add members", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	email := r.FormValue("email")
	if email == "" {
		http.Redirect(w, r, "/p/"+projectID.String()+"/settings?error=email+required", http.StatusSeeOther)
		return
	}

	// Find or create user by email
	user, err := s.db.GetUserByEmail(ctx, email)
	if err != nil {
		// User doesn't exist yet - create a placeholder
		user = &domain.User{
			ID:        uuid.New(),
			Email:     email,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := s.db.CreateUser(ctx, user); err != nil {
			http.Error(w, "failed to create user", http.StatusInternalServerError)
			return
		}
	}

	// Add as member
	if err := s.db.AddProjectMember(ctx, projectID, user.ID, domain.ProjectMemberRoleMember); err != nil {
		http.Error(w, "failed to add member", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/settings?success=1", http.StatusSeeOther)
}

// handleRemoveMember removes a member from a project.
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		http.Error(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	// Check that current user is owner
	currentUser := UserFromContext(ctx)
	if currentUser == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	role, err := s.db.GetProjectMemberRole(ctx, projectID, currentUser.ID)
	if err != nil || role != domain.ProjectMemberRoleOwner {
		http.Error(w, "only owners can remove members", http.StatusForbidden)
		return
	}

	// Don't allow removing owners
	targetRole, _ := s.db.GetProjectMemberRole(ctx, projectID, userID)
	if targetRole == domain.ProjectMemberRoleOwner {
		http.Error(w, "cannot remove owner", http.StatusForbidden)
		return
	}

	if err := s.db.RemoveProjectMember(ctx, projectID, userID); err != nil {
		http.Error(w, "failed to remove member", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/settings?success=1", http.StatusSeeOther)
}
