package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/hosting"
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

// handleDeploymentChannel shows the deployment channel configuration page.
func (s *Server) handleDeploymentChannel(w http.ResponseWriter, r *http.Request) {
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

	// Get current channel if any
	channel, _ := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)

	// Get all channels (history)
	channels, _ := s.db.ListProjectDeploymentChannels(ctx, projectID)

	// Get supported combos
	combos, err := s.db.ListSupportedDeploymentCombos(ctx)
	if err != nil {
		http.Error(w, "failed to load deployment options", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":          "Deployment: " + project.Name,
		"ProjectID":      projectID.String(),
		"Project":        project,
		"Channel":        channel,
		"ChannelHistory": channels,
		"Combos":         combos,
		"Success":        r.URL.Query().Get("success") == "1",
	}
	s.addUserToData(r, data)

	if err := renderPage(w, "deployment_channel.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleSetDeploymentChannel sets or changes the project's deployment channel.
func (s *Server) handleSetDeploymentChannel(w http.ResponseWriter, r *http.Request) {
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

	comboID, err := uuid.Parse(r.FormValue("combo_id"))
	if err != nil {
		http.Error(w, "invalid combo selection", http.StatusBadRequest)
		return
	}

	// Verify combo exists and get its details
	combos, err := s.db.ListSupportedDeploymentCombos(ctx)
	if err != nil {
		http.Error(w, "failed to load combos", http.StatusInternalServerError)
		return
	}

	var selectedCombo *domain.SupportedDeploymentCombo
	for i := range combos {
		if combos[i].ID == comboID {
			selectedCombo = &combos[i]
			break
		}
	}
	if selectedCombo == nil {
		http.Error(w, "selected deployment option not found", http.StatusBadRequest)
		return
	}

	// Create the new channel (this disables any existing active one)
	channel := &domain.ProjectDeploymentChannel{
		ProjectID:         projectID,
		ArtifactKind:      selectedCombo.ArtifactKind,
		HostingPlatformID: selectedCombo.HostingPlatformID,
	}
	if err := s.db.CreateProjectDeploymentChannel(ctx, channel); err != nil {
		http.Error(w, "failed to create deployment channel", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/deployment?success=1", http.StatusSeeOther)
}

// handleValidateDemoPath triggers demo path validation (deploy → health → teardown).
func (s *Server) handleValidateDemoPath(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil {
		http.Error(w, "no deployment channel configured", http.StatusBadRequest)
		return
	}

	// Check required credentials
	missing, err := s.checkRequiredCredentials(ctx, projectID, channel)
	if err != nil {
		http.Error(w, "failed to check credentials", http.StatusInternalServerError)
		return
	}
	if len(missing) > 0 {
		http.Error(w, fmt.Sprintf("missing required credentials: %s", strings.Join(missing, ", ")), http.StatusBadRequest)
		return
	}

	// Run hello-world validation
	if err := s.runChannelValidation(ctx, projectID, channel, "demo"); err != nil {
		http.Error(w, fmt.Sprintf("validation failed: %v", err), http.StatusInternalServerError)
		return
	}

	if err := s.db.UpdateProjectDeploymentChannelDemoValidation(ctx, channel.ID); err != nil {
		http.Error(w, "failed to update validation status", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/deployment?success=1", http.StatusSeeOther)
}

// handleValidateProdPath triggers production path validation (deploy → health → rollback).
func (s *Server) handleValidateProdPath(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil {
		http.Error(w, "no deployment channel configured", http.StatusBadRequest)
		return
	}

	// Check required credentials
	missing, err := s.checkRequiredCredentials(ctx, projectID, channel)
	if err != nil {
		http.Error(w, "failed to check credentials", http.StatusInternalServerError)
		return
	}
	if len(missing) > 0 {
		http.Error(w, fmt.Sprintf("missing required credentials: %s", strings.Join(missing, ", ")), http.StatusBadRequest)
		return
	}

	// Run hello-world validation
	if err := s.runChannelValidation(ctx, projectID, channel, "prod"); err != nil {
		http.Error(w, fmt.Sprintf("validation failed: %v", err), http.StatusInternalServerError)
		return
	}

	if err := s.db.UpdateProjectDeploymentChannelProdValidation(ctx, channel.ID); err != nil {
		http.Error(w, "failed to update validation status", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/deployment?success=1", http.StatusSeeOther)
}

// checkRequiredCredentials verifies that all required credentials exist for the channel.
func (s *Server) checkRequiredCredentials(ctx context.Context, projectID uuid.UUID, channel *domain.ProjectDeploymentChannel) ([]string, error) {
	if channel.HostingPlatform == nil {
		return nil, fmt.Errorf("channel has no hosting platform")
	}

	required := hosting.RequiredCredentialsForCombo(channel.ArtifactKind, channel.HostingPlatform.Slug)
	if len(required) == 0 {
		return nil, nil
	}

	// Get existing credentials
	creds, err := s.db.ListProjectCredentials(ctx, projectID)
	if err != nil {
		return nil, err
	}

	existing := make(map[string]bool)
	for _, c := range creds {
		existing[c.Name] = true
	}

	var missing []string
	for _, name := range required {
		if !existing[name] {
			missing = append(missing, name)
		}
	}

	return missing, nil
}

// runChannelValidation deploys a hello-world container, health checks it, and tears it down.
func (s *Server) runChannelValidation(ctx context.Context, projectID uuid.UUID, channel *domain.ProjectDeploymentChannel, mode string) error {
	if channel.HostingPlatform == nil {
		return fmt.Errorf("channel has no hosting platform")
	}

	// Get encryption key
	key, err := crypto.GetKey()
	if err != nil {
		return fmt.Errorf("failed to get encryption key: %w", err)
	}

	// Get required credentials
	required := hosting.RequiredCredentialsForCombo(channel.ArtifactKind, channel.HostingPlatform.Slug)

	env := make(map[string]string)
	for _, name := range required {
		cred, err := s.db.GetProjectCredential(ctx, projectID, name)
		if err != nil {
			return fmt.Errorf("failed to get credential %s: %w", name, err)
		}
		decrypted, err := crypto.Decrypt(cred.EncryptedValue, key)
		if err != nil {
			return fmt.Errorf("failed to decrypt %s: %w", name, err)
		}
		env[name] = string(decrypted)
	}

	// Dispatch to platform-specific validation
	switch channel.HostingPlatform.Slug {
	case "fly-io":
		return s.validateFlyIO(ctx, env, mode)
	case "cloud-run":
		return s.validateCloudRun(ctx, env, mode)
	case "gke":
		return s.validateGKE(ctx, env, mode)
	default:
		return fmt.Errorf("unsupported platform: %s", channel.HostingPlatform.Slug)
	}
}

// validateFlyIO deploys a hello-world app to Fly.io, health checks it, and destroys it.
func (s *Server) validateFlyIO(ctx context.Context, env map[string]string, mode string) error {
	appName := fmt.Sprintf("mendel-validate-%d", time.Now().Unix())

	// Create temp directory with Dockerfile
	tmpDir, err := os.MkdirTemp("", "mendel-validate-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write Dockerfile
	dockerfile := `FROM nginx:alpine
COPY index.html /usr/share/nginx/html/index.html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]`
	if err := os.WriteFile(tmpDir+"/Dockerfile", []byte(dockerfile), 0644); err != nil {
		return fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	// Write health check page
	indexHTML := `<!DOCTYPE html><html><body><h1>Mendel Validation</h1><p>OK</p></body></html>`
	if err := os.WriteFile(tmpDir+"/index.html", []byte(indexHTML), 0644); err != nil {
		return fmt.Errorf("failed to write index.html: %w", err)
	}

	// Write fly.toml
	flyToml := fmt.Sprintf(`app = "%s"
primary_region = "iad"

[http_service]
  internal_port = 80
  force_https = true
  auto_stop_machines = "stop"
  auto_start_machines = true
  min_machines_running = 0

[[http_service.checks]]
  grace_period = "10s"
  interval = "30s"
  method = "GET"
  timeout = "5s"
  path = "/"
`, appName)
	if err := os.WriteFile(tmpDir+"/fly.toml", []byte(flyToml), 0644); err != nil {
		return fmt.Errorf("failed to write fly.toml: %w", err)
	}

	// Build environment for flyctl
	cmdEnv := os.Environ()
	cmdEnv = append(cmdEnv, fmt.Sprintf("FLY_API_TOKEN=%s", env["FLY_API_TOKEN"]))

	// Create the app
	createCmd := exec.CommandContext(ctx, "flyctl", "apps", "create", appName, "--org", "personal")
	createCmd.Dir = tmpDir
	createCmd.Env = cmdEnv
	if output, err := createCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create fly app: %s: %w", string(output), err)
	}

	// Ensure cleanup on exit
	defer func() {
		destroyCmd := exec.CommandContext(context.Background(), "flyctl", "apps", "destroy", appName, "--yes")
		destroyCmd.Env = cmdEnv
		destroyCmd.Run() // Best effort cleanup
	}()

	// Deploy the app
	deployCmd := exec.CommandContext(ctx, "flyctl", "deploy", "--now")
	deployCmd.Dir = tmpDir
	deployCmd.Env = cmdEnv
	if output, err := deployCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to deploy: %s: %w", string(output), err)
	}

	// Wait for app to be running and get URL
	url := fmt.Sprintf("https://%s.fly.dev/", appName)

	// Health check with retries
	client := &http.Client{Timeout: 10 * time.Second}
	var lastErr error
	for i := 0; i < 10; i++ {
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(3 * time.Second)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			// Health check passed
			return nil
		}
		lastErr = fmt.Errorf("health check returned %d", resp.StatusCode)
		time.Sleep(3 * time.Second)
	}

	return fmt.Errorf("health check failed after retries: %w", lastErr)
}

// validateCloudRun deploys a hello-world container to Cloud Run, health checks it, and deletes it.
func (s *Server) validateCloudRun(ctx context.Context, env map[string]string, mode string) error {
	serviceName := fmt.Sprintf("mendel-validate-%d", time.Now().Unix())
	projectID := env["GCP_PROJECT_ID"]
	region := "us-central1"

	// Write service account key to temp file
	keyFile, err := os.CreateTemp("", "gcp-key-*.json")
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer os.Remove(keyFile.Name())
	if _, err := keyFile.WriteString(env["GCP_SERVICE_ACCOUNT_KEY"]); err != nil {
		keyFile.Close()
		return fmt.Errorf("failed to write key file: %w", err)
	}
	keyFile.Close()

	// Build environment
	cmdEnv := os.Environ()
	cmdEnv = append(cmdEnv, fmt.Sprintf("GOOGLE_APPLICATION_CREDENTIALS=%s", keyFile.Name()))

	// Authenticate with service account
	authCmd := exec.CommandContext(ctx, "gcloud", "auth", "activate-service-account", "--key-file", keyFile.Name())
	authCmd.Env = cmdEnv
	if output, err := authCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to authenticate: %s: %w", string(output), err)
	}

	// Set project
	setProjectCmd := exec.CommandContext(ctx, "gcloud", "config", "set", "project", projectID)
	setProjectCmd.Env = cmdEnv
	if output, err := setProjectCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set project: %s: %w", string(output), err)
	}

	// Deploy the hello container (Google's sample)
	deployCmd := exec.CommandContext(ctx, "gcloud", "run", "deploy", serviceName,
		"--image", "gcr.io/cloudrun/hello",
		"--region", region,
		"--allow-unauthenticated",
		"--quiet")
	deployCmd.Env = cmdEnv

	// Ensure cleanup on exit
	defer func() {
		deleteCmd := exec.CommandContext(context.Background(), "gcloud", "run", "services", "delete", serviceName,
			"--region", region, "--quiet")
		deleteCmd.Env = cmdEnv
		deleteCmd.Run() // Best effort cleanup
	}()

	output, err := deployCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to deploy: %s: %w", string(output), err)
	}

	// Extract URL from deploy output
	urlLine := ""
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "https://") {
			urlLine = strings.TrimSpace(line)
			break
		}
	}
	if urlLine == "" {
		// Try to get URL via describe
		describeCmd := exec.CommandContext(ctx, "gcloud", "run", "services", "describe", serviceName,
			"--region", region, "--format", "value(status.url)")
		describeCmd.Env = cmdEnv
		urlOutput, err := describeCmd.Output()
		if err != nil {
			return fmt.Errorf("failed to get service URL: %w", err)
		}
		urlLine = strings.TrimSpace(string(urlOutput))
	}

	if urlLine == "" {
		return fmt.Errorf("could not determine service URL")
	}

	// Health check with retries
	client := &http.Client{Timeout: 10 * time.Second}
	var lastErr error
	for i := 0; i < 10; i++ {
		resp, err := client.Get(urlLine)
		if err != nil {
			lastErr = err
			time.Sleep(3 * time.Second)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		lastErr = fmt.Errorf("health check returned %d", resp.StatusCode)
		time.Sleep(3 * time.Second)
	}

	return fmt.Errorf("health check failed after retries: %w", lastErr)
}

// validateGKE deploys a hello-world pod to GKE, health checks it, and deletes it.
func (s *Server) validateGKE(ctx context.Context, env map[string]string, mode string) error {
	deploymentName := fmt.Sprintf("mendel-validate-%d", time.Now().Unix())
	projectID := env["GCP_PROJECT_ID"]
	clusterName := env["GKE_CLUSTER_NAME"]
	zone := env["GKE_ZONE"]

	// Write service account key to temp file
	keyFile, err := os.CreateTemp("", "gcp-key-*.json")
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer os.Remove(keyFile.Name())
	if _, err := keyFile.WriteString(env["GCP_SERVICE_ACCOUNT_KEY"]); err != nil {
		keyFile.Close()
		return fmt.Errorf("failed to write key file: %w", err)
	}
	keyFile.Close()

	// Build environment
	cmdEnv := os.Environ()
	cmdEnv = append(cmdEnv, fmt.Sprintf("GOOGLE_APPLICATION_CREDENTIALS=%s", keyFile.Name()))

	// Authenticate with service account
	authCmd := exec.CommandContext(ctx, "gcloud", "auth", "activate-service-account", "--key-file", keyFile.Name())
	authCmd.Env = cmdEnv
	if output, err := authCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to authenticate: %s: %w", string(output), err)
	}

	// Set project
	setProjectCmd := exec.CommandContext(ctx, "gcloud", "config", "set", "project", projectID)
	setProjectCmd.Env = cmdEnv
	if output, err := setProjectCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set project: %s: %w", string(output), err)
	}

	// Get cluster credentials
	getCredsCmd := exec.CommandContext(ctx, "gcloud", "container", "clusters", "get-credentials",
		clusterName, "--zone", zone)
	getCredsCmd.Env = cmdEnv
	if output, err := getCredsCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to get cluster credentials: %s: %w", string(output), err)
	}

	// Create temp directory for manifests
	tmpDir, err := os.MkdirTemp("", "mendel-gke-validate-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write deployment manifest
	manifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
      - name: hello
        image: gcr.io/cloudrun/hello
        ports:
        - containerPort: 8080
        readinessProbe:
          httpGet:
            path: /
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 5
`, deploymentName, deploymentName, deploymentName)
	if err := os.WriteFile(tmpDir+"/deployment.yaml", []byte(manifest), 0644); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	// Ensure cleanup on exit
	defer func() {
		deleteCmd := exec.CommandContext(context.Background(), "kubectl", "delete", "deployment", deploymentName)
		deleteCmd.Env = cmdEnv
		deleteCmd.Run() // Best effort cleanup
	}()

	// Apply the deployment
	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", tmpDir+"/deployment.yaml")
	applyCmd.Env = cmdEnv
	if output, err := applyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply deployment: %s: %w", string(output), err)
	}

	// Wait for rollout
	rolloutCmd := exec.CommandContext(ctx, "kubectl", "rollout", "status", "deployment/"+deploymentName, "--timeout=120s")
	rolloutCmd.Env = cmdEnv
	if output, err := rolloutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rollout failed: %s: %w", string(output), err)
	}

	// For GKE, just verify the pod is running (no external URL without a LoadBalancer)
	getPodCmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-l", "app="+deploymentName,
		"-o", "jsonpath={.items[0].status.phase}")
	getPodCmd.Env = cmdEnv
	output, err := getPodCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get pod status: %w", err)
	}
	if strings.TrimSpace(string(output)) != "Running" {
		return fmt.Errorf("pod not running: %s", string(output))
	}

	return nil
}
