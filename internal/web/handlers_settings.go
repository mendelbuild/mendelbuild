package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/hosting"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
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

// RequiredCredentialView shows a credential the chosen channel asks for.
type RequiredCredentialView struct {
	Name         string
	IsConfigured bool

	// Optional marks a credential that adds a capability rather than gating a
	// deployment, so its absence is a choice rather than a fault.
	Optional bool

	// Purpose says what supplying it gets you, which is the only thing that
	// makes an optional credential worth the trouble of finding.
	Purpose string
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
		"SettingsTab":         "project",
		"Settings":            settings,
		"Success":             success,
		"AuthEnabled":         s.authEnabled,
		"Members":             members,
		"IsOwner":             isOwner,
		"DemoHostingPlatform": demoHostingPlatform,
		"DemoScriptStatus":    demoScriptStatus,
	}

	if err := s.renderPageFor(w, r, "project_settings.html", data); err != nil {
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
			s.renderSettingsWithError(w, r, projectID, err.Error())
			return
		}
	}

	// Update repository config
	repoConfig, _ := json.Marshal(map[string]string{
		"main_branch": mainBranch,
		"auth_token":  authToken,
	})

	if err := s.db.UpsertRepository(ctx, projectID, repoURL, repoConfig); err != nil {
		s.renderSettingsWithError(w, r, projectID, "Failed to save repository settings: "+err.Error())
		return
	}

	// Update project config (API key)
	projectConfig, _ := json.Marshal(domain.ProjectConfig{
		AnthropicAPIKey: anthropicAPIKey,
	})

	if err := s.db.UpdateProjectConfig(ctx, projectID, projectConfig); err != nil {
		s.renderSettingsWithError(w, r, projectID, "Failed to save project settings: "+err.Error())
		return
	}

	// Close the setup flow's "connect a repository" ask, if that is what just
	// happened. Leaving it open once the repository works teaches people that
	// the queue is full of things they can ignore.
	s.resolveRepositoryRequest(ctx, projectID)

	// Redirect back with success message
	http.Redirect(w, r, "/p/"+projectID.String()+"/settings?success=1", http.StatusSeeOther)
}

func (s *Server) renderSettingsWithError(w http.ResponseWriter, r *http.Request, projectID uuid.UUID, errMsg string) {
	data := map[string]interface{}{
		"Title":       "Project Settings",
		"ProjectID":   projectID.String(),
		"SettingsTab": "project",
		"Error":       errMsg,
		"Settings":  ProjectSettings{MainBranch: "main"},
	}
	s.renderPageFor(w, r, "project_settings.html", data)
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
		s.renderSettingsWithError(w, r, projectID, "Credential name is required")
		return
	}

	// Get encryption key and encrypt the value
	key, err := crypto.GetKey()
	if err != nil {
		s.renderSettingsWithError(w, r, projectID, "Encryption not configured: "+err.Error())
		return
	}

	encryptedValue, err := crypto.Encrypt([]byte(value), key)
	if err != nil {
		s.renderSettingsWithError(w, r, projectID, "Failed to encrypt credential: "+err.Error())
		return
	}

	cred := &domain.ProjectCredential{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Name:           name,
		EncryptedValue: encryptedValue,
	}

	if err := s.db.CreateProjectCredential(ctx, cred); err != nil {
		s.renderSettingsWithError(w, r, projectID, "Failed to save credential: "+err.Error())
		return
	}

	// Auto-resolve any credential_request InputRequests that were waiting for this credential
	_ = s.db.ResolveCredentialRequestsByName(ctx, projectID, name)

	// Supplying the last missing value should close the ask, not leave it open
	// in the queue after the thing it asked for has been done.
	s.syncDeploymentCredentialRequest(ctx, projectID)
	// Credentials may be the piece that was missing before Mendel could reserve
	// an address and request a certificate.
	s.ensureDomainInfrastructure(ctx, projectID)

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
		s.renderSettingsWithError(w, r, projectID, "Encryption not configured: "+err.Error())
		return
	}

	encryptedValue, err := crypto.Encrypt([]byte(value), key)
	if err != nil {
		s.renderSettingsWithError(w, r, projectID, "Failed to encrypt credential: "+err.Error())
		return
	}

	if err := s.db.UpdateProjectCredential(ctx, credID, encryptedValue); err != nil {
		s.renderSettingsWithError(w, r, projectID, "Failed to update credential: "+err.Error())
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

	// Supplying the last missing value should close the ask, not leave it open
	// in the queue after the thing it asked for has been done.
	s.syncDeploymentCredentialRequest(ctx, projectID)
	// Credentials may be the piece that was missing before Mendel could reserve
	// an address and request a certificate.
	s.ensureDomainInfrastructure(ctx, projectID)

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
		s.renderSettingsWithError(w, r, projectID, "Failed to delete credential: "+err.Error())
		return
	}

	// Supplying the last missing value should close the ask, not leave it open
	// in the queue after the thing it asked for has been done.
	s.syncDeploymentCredentialRequest(ctx, projectID)
	// Credentials may be the piece that was missing before Mendel could reserve
	// an address and request a certificate.
	s.ensureDomainInfrastructure(ctx, projectID)

	http.Redirect(w, r, "/p/"+projectID.String()+"/settings?success=1", http.StatusSeeOther)
}

// handleRedeploy deploys the current main branch to production via the project's channel.
func (s *Server) handleRedeploy(w http.ResponseWriter, r *http.Request) {
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
	if !channel.IsProdValidated() {
		http.Error(w, "production deployment path is not validated", http.StatusBadRequest)
		return
	}

	go func() {
		bgCtx := context.Background()
		if _, err := s.runChannelProdDeployment(bgCtx, projectID, channel); err != nil {
			fmt.Printf("[prod-deploy %s] failed: %v\n", projectID, err)
		}
	}()

	http.Redirect(w, r, "/p/"+projectID.String()+"/deployment", http.StatusSeeOther)
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

	// Keep the queue honest about this channel. Selection files the ask, but a
	// channel chosen before that existed -- or one whose credentials were since
	// removed -- would otherwise sit here needing values with nothing in the
	// queue saying so.
	if channel != nil {
		s.ensureDeploymentCredentialRequest(ctx, projectID, channel)
	}

	// Get all channels (history)
	channels, _ := s.db.ListProjectDeploymentChannels(ctx, projectID)

	// Get supported combos
	combos, err := s.db.ListSupportedDeploymentCombos(ctx)
	if err != nil {
		http.Error(w, "failed to load deployment options", http.StatusInternalServerError)
		return
	}

	// Production deployment state: the live one, the latest attempt (which may
	// be in progress or failed), and recent history.
	prodDeployment, _ := s.db.GetCurrentProdDeployment(ctx, projectID)
	latestProdDeployment, _ := s.db.GetLatestProdDeployment(ctx, projectID)
	prodHistory, _ := s.db.ListHostingDeployments(ctx, projectID, domain.HostingDeploymentKindProd, 10)

	// Logs for the most recent attempt, so failures are diagnosable in the UI.
	// Streams in place while the deploy is still running.
	var prodLogPanel *LogPanel
	if latestProdDeployment != nil {
		logs, _ := s.db.GetHostingDeploymentLogs(ctx, latestProdDeployment.ID)
		prodLogPanel = &LogPanel{
			DOMID:     "prod-deploy-logs",
			FeedURL:   fmt.Sprintf("/api/deployments/%s/logs", latestProdDeployment.ID),
			Status:    string(latestProdDeployment.Status),
			Live:      latestProdDeployment.Status == domain.HostingDeploymentStatusDeploying,
			Tall:      true,
			Empty:     "No deploy logs yet.",
			Lines:     logLinesFromDeployment(logs),
		}
	}

	// What the merged code needs before production can run it. Gating happens
	// in runChannelProdDeployment; this is where the reader answers it, since
	// production's redirect URI is a different string from the demo's and the
	// variation page has no way to know about it.
	prodRequirements, err := s.prodRequirementsPanel(ctx, projectID, project, channel, prodDeployment)
	if err != nil {
		http.Error(w, "failed to check requirements: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Credentials live here rather than in project settings, because this is
	// the page that says what they are for. Which ones are needed comes from
	// the chosen channel, not from a file in the user's repository: the channel
	// is what actually gates a demo.
	cloudCredentials, configured := s.cloudCredentialViews(ctx, projectID)
	var requiredCredentials, optionalCredentials []RequiredCredentialView
	if channel != nil {
		platformSlug := ""
		if channel.HostingPlatform != nil {
			platformSlug = channel.HostingPlatform.Slug
		}
		for _, name := range hosting.RequiredCredentialsForCombo(channel.ArtifactKind, platformSlug) {
			requiredCredentials = append(requiredCredentials, RequiredCredentialView{
				Name:         name,
				IsConfigured: configured[name],
			})
		}
		// Optional credentials need somewhere to be entered too. Listed only
		// with the required ones they would read as missing; listed nowhere,
		// the capability they unlock is unreachable however carefully it is
		// implemented.
		for _, name := range hosting.OptionalCredentialsForCombo(channel.ArtifactKind, platformSlug) {
			optionalCredentials = append(optionalCredentials, RequiredCredentialView{
				Name:         name,
				IsConfigured: configured[name],
				Optional:     true,
			})
		}
	}

	// Where the setup script lives. Someone managing credentials is on this page,
	// and until now nothing here led to the instructions for obtaining them --
	// deleting one to "get the instructions back" left you on a page that had
	// none.
	var credentialAskID string
	if ask, err := s.db.FindOpenInputRequestByKind(ctx, projectID, domain.InputRequestKindCredentialRequest); err == nil &&
		ask != nil && ask.Title == deploymentCredentialRequestTitle {
		credentialAskID = ask.ID.String()
	}

	// The setup script belongs to the channel, not to whatever is currently
	// missing. Showing it only while a credential is outstanding made it
	// unreachable exactly when it was needed for another reason -- a new API to
	// enable, a role to grant -- and the only way back to it was to delete a
	// credential in order to break something.
	var setupScript string
	var setupScriptLines []SetupScriptLine
	var setupPrerequisites []string
	var setupInputLabel, setupInputCredential, setupPlaceholder string
	if channel != nil && channel.HostingPlatform != nil {
		p := channel.HostingPlatform
		setupScript = setupScriptText(p.SetupScript)
		setupScriptLines = markUpSetupScript(setupScript)
		setupPrerequisites = p.SetupPrerequisites
		setupInputLabel, setupInputCredential = p.SetupInputLabel, p.SetupInputCredential
		setupPlaceholder = setupPlaceholder0(p.SetupScript)
	}

	data := map[string]interface{}{
		"Title":                "Deployment: " + project.Name,
		"CredentialAskID":      credentialAskID,
		"SetupScript":          setupScript,
		"SetupScriptLines":     setupScriptLines,
		"SetupPrerequisites":   setupPrerequisites,
		"SetupInputLabel":      setupInputLabel,
		"SetupInputCredential": setupInputCredential,
		"SetupPlaceholder":     setupPlaceholder,
		"SettingsTab":          "deployment",
		"SupportMatrix":        s.buildSupportMatrix(ctx, projectID, channel),
		"CloudCredentials":     cloudCredentials,
		"RequiredCredentials":  requiredCredentials,
		"OptionalCredentials":  optionalCredentials,
		"ProjectID":            projectID.String(),
		"Project":              project,
		"Channel":              channel,
		"ChannelHistory":       channels,
		"Combos":               combos,
		"ProdDeployment":       prodDeployment,
		"LatestProdDeployment": latestProdDeployment,
		"ProdLogPanel":         prodLogPanel,
		"ProdHistory":          prodHistory,
		"ProdRequirements":     prodRequirements,
		"Success":              r.URL.Query().Get("success") == "1",
	}

	if err := s.renderPageFor(w, r, "deployment_channel.html", data); err != nil {
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

	// Choosing a channel is the moment Mendel learns what it will need, so the
	// ask belongs here rather than at the first deploy, where the same missing
	// value arrives as a failure instead of a question.
	if active, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID); err == nil {
		s.ensureDeploymentCredentialRequest(ctx, projectID, active)

		// A platform that issues no hostnames raises a question this is the
		// moment for: whether these demos have to be reachable by name. Asking
		// here rather than later means the DNS errand starts alongside the
		// credentials, not after variations exist and someone wants a demo now.
		if active != nil && active.HostingPlatform != nil &&
			active.HostingPlatform.HostnameSource == domain.HostnameFromUser {
			if pd, err := s.db.GetProjectDomain(ctx, projectID); err == nil && pd.ShouldAskAboutNamedDemos() {
				http.Redirect(w, r, "/p/"+projectID.String()+"/domain", http.StatusSeeOther)
				return
			}
		}
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/deployment?success=1", http.StatusSeeOther)
}

// deploymentCredentialRequestTitle identifies the deployment-credential ask so
// it is neither duplicated nor left open once the values are in.
const deploymentCredentialRequestTitle = "Provide deployment credentials"

// ensureDeploymentCredentialRequest keeps the input queue in step with what the
// chosen channel still needs: it files the ask, updates it when the channel
// changes under it, and resolves it once nothing is missing.
//
// The platform's own instructions ride along, because for something like GKE the
// hard part is not typing the value in — it is knowing which service account to
// create and what to grant it.
func (s *Server) ensureDeploymentCredentialRequest(ctx context.Context, projectID uuid.UUID, channel *domain.ProjectDeploymentChannel) {
	if channel == nil || channel.HostingPlatform == nil {
		return
	}

	missing, err := s.checkRequiredCredentials(ctx, projectID, channel)
	if err != nil {
		log.Printf("deployment: could not check required credentials: %v", err)
		return
	}

	existing, err := s.db.FindOpenInputRequestByKind(ctx, projectID, domain.InputRequestKindCredentialRequest)
	if err != nil {
		log.Printf("deployment: could not check for an existing credential request: %v", err)
		return
	}
	if existing != nil && existing.Title != deploymentCredentialRequestTitle {
		existing = nil // Someone else's credential ask; leave it alone.
	}

	if len(missing) == 0 {
		if existing != nil {
			existing.Status = domain.InputRequestStatusResolved
			existing.Resolution = strPtr("approved")
			resolvedAt := time.Now()
			existing.ResolvedAt = &resolvedAt
			if err := s.db.UpdateInputRequest(ctx, existing); err != nil {
				log.Printf("deployment: could not resolve the credential request: %v", err)
			}
		}
		return
	}

	details := fmt.Sprintf("Deploying to %s needs %s. Mendel keeps these encrypted and injects them at deploy time; nothing is written to your repository.",
		channel.HostingPlatform.Name, strings.Join(missing, ", "))
	instructions := channel.HostingPlatform.Instructions
	// No link. This used to point at the deployment page, labelled "Open the
	// console", which was neither: it sent the reader away from the setup script
	// and back to the page they had come from.
	link := ""

	if existing != nil {
		// The channel may have changed under an open ask, which changes both
		// what is missing and how to obtain it.
		existing.Details = &details
		existing.Instructions = &instructions
		existing.Link = &link
		existing.RequiredCapabilities = missing
		if err := s.db.UpdateInputRequest(ctx, existing); err != nil {
			log.Printf("deployment: could not update the credential request: %v", err)
		}
		return
	}

	now := time.Now()
	req := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        projectID,
		Kind:             domain.InputRequestKindCredentialRequest,
		Title:            deploymentCredentialRequestTitle,
		Details:          &details,
		Instructions:     &instructions,
		Link:             &link,
		// The names are what let the form ask for these specific values instead
		// of offering a blank box and an example from another platform.
		RequiredCapabilities: missing,
		ObjectivityScore:     1.0, // Nothing to weigh: the values are needed or they are not.
		ImportanceScore:  0.9, // Blocks every demo and deploy on this channel.
		Status:           domain.InputRequestStatusNeedsAssignment,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.db.CreateInputRequest(ctx, req); err != nil {
		log.Printf("deployment: could not create the credential request: %v", err)
	}
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

	// Don't start if already validating
	if channel.IsDemoValidating() {
		http.Redirect(w, r, "/p/"+projectID.String()+"/deployment", http.StatusSeeOther)
		return
	}

	// Check required credentials
	missing, err := s.checkRequiredCredentials(ctx, projectID, channel)
	if err != nil {
		http.Error(w, "failed to check credentials", http.StatusInternalServerError)
		return
	}
	if len(missing) > 0 {
		// Record it where the page already shows validation failures, and file
		// the ask, rather than answering with a bare error page that leaves the
		// user on a dead end with no way to supply what was asked for.
		s.ensureDeploymentCredentialRequest(ctx, projectID, channel)
		s.db.FailDemoValidation(ctx, channel.ID, missingCredentialsMessage(missing))
		http.Redirect(w, r, "/p/"+projectID.String()+"/deployment", http.StatusSeeOther)
		return
	}

	// Mark as validating and run in background
	if err := s.db.StartDemoValidation(ctx, channel.ID); err != nil {
		http.Error(w, "failed to start validation", http.StatusInternalServerError)
		return
	}

	go func() {
		bgCtx := context.Background()
		if err := s.runChannelValidation(bgCtx, projectID, channel, "demo"); err != nil {
			s.db.FailDemoValidation(bgCtx, channel.ID, err.Error())
			return
		}
		s.db.CompleteDemoValidation(bgCtx, channel.ID)
	}()

	http.Redirect(w, r, "/p/"+projectID.String()+"/deployment", http.StatusSeeOther)
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

	// Don't start if already validating
	if channel.IsProdValidating() {
		http.Redirect(w, r, "/p/"+projectID.String()+"/deployment", http.StatusSeeOther)
		return
	}

	// Check required credentials
	missing, err := s.checkRequiredCredentials(ctx, projectID, channel)
	if err != nil {
		http.Error(w, "failed to check credentials", http.StatusInternalServerError)
		return
	}
	if len(missing) > 0 {
		s.ensureDeploymentCredentialRequest(ctx, projectID, channel)
		s.db.FailProdValidation(ctx, channel.ID, missingCredentialsMessage(missing))
		http.Redirect(w, r, "/p/"+projectID.String()+"/deployment", http.StatusSeeOther)
		return
	}

	// Mark as validating and run in background
	if err := s.db.StartProdValidation(ctx, channel.ID); err != nil {
		http.Error(w, "failed to start validation", http.StatusInternalServerError)
		return
	}

	go func() {
		bgCtx := context.Background()
		if err := s.runChannelValidation(bgCtx, projectID, channel, "prod"); err != nil {
			s.db.FailProdValidation(bgCtx, channel.ID, err.Error())
			return
		}
		s.db.CompleteProdValidation(bgCtx, channel.ID)
	}()

	http.Redirect(w, r, "/p/"+projectID.String()+"/deployment", http.StatusSeeOther)
}

// checkRequiredCredentials verifies that all required credentials exist for the channel.
// missingCredentialsMessage says which values are missing and where to put
// them, so the message is actionable from the page that shows it.
func missingCredentialsMessage(missing []string) string {
	return fmt.Sprintf("Missing %s. Add them under Credentials below, then validate again.",
		strings.Join(missing, ", "))
}

// syncDeploymentCredentialRequest refreshes the credential ask after the stored
// credentials change, so supplying the last one closes it.
func (s *Server) syncDeploymentCredentialRequest(ctx context.Context, projectID uuid.UUID) {
	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil || channel == nil {
		return
	}
	s.ensureDeploymentCredentialRequest(ctx, projectID, channel)
}

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

	// Create the app (no --org flag, uses default org)
	createCmd := exec.CommandContext(ctx, "flyctl", "apps", "create", appName)
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

	// Deploy the app (--remote-only builds on Fly's infrastructure)
	deployCmd := exec.CommandContext(ctx, "flyctl", "deploy", "--remote-only")
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
	region := env["GCP_REGION"]
	if region == "" {
		return fmt.Errorf("GCP_REGION is not set; re-run the setup script, which reports it")
	}

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

	// Validation proves the credentials work by doing what a real deployment
	// does, through the same session — so a break in authentication shows up
	// here rather than on the user's first demo.
	session, err := newGKESession(ctx, env)
	if err != nil {
		return err
	}
	defer session.cleanup()

	if err := session.ensureNamespace(ctx); err != nil {
		return err
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

	// Ensure cleanup on exit. Detached from ctx so a cancelled validation still
	// removes what it created rather than leaving it in the user's cluster.
	defer func() {
		deleteCmd := session.kubectl(context.Background(), "delete", "deployment", deploymentName, "--ignore-not-found")
		deleteCmd.Run() // Best effort cleanup
	}()

	// Apply the deployment
	applyCmd := session.kubectl(ctx, "apply", "-f", tmpDir+"/deployment.yaml")
	if output, err := applyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply deployment: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// Wait for rollout
	rolloutCmd := session.kubectl(ctx, "rollout", "status", "deployment/"+deploymentName, "--timeout=120s")
	if output, err := rolloutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rollout failed: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// For GKE, just verify the pod is running (no external URL without a LoadBalancer)
	getPodCmd := session.kubectl(ctx, "get", "pods", "-l", "app="+deploymentName,
		"-o", "jsonpath={.items[0].status.phase}")
	output, err := getPodCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get pod status: %w", err)
	}
	if strings.TrimSpace(string(output)) != "Running" {
		return fmt.Errorf("pod not running: %s", string(output))
	}

	return nil
}


// cloudCredentialViews lists the project's stored credentials by name, never by
// value, along with a set for checking whether a required one is present.
func (s *Server) cloudCredentialViews(ctx context.Context, projectID uuid.UUID) ([]CloudCredentialView, map[string]bool) {
	configured := make(map[string]bool)
	creds, err := s.db.ListProjectCredentials(ctx, projectID)
	if err != nil {
		return nil, configured
	}
	views := make([]CloudCredentialView, 0, len(creds))
	for _, c := range creds {
		views = append(views, CloudCredentialView{
			ID:       c.ID.String(),
			Name:     c.Name,
			HasValue: true, // If it is in the list, it has a value.
		})
		configured[c.Name] = true
	}
	return views, configured
}

// SupportMatrix is the grid of what Mendel can deploy where: one row per
// hosting platform, one column per artifact kind, and a cell saying whether
// that pairing is a channel Mendel knows how to run.
//
// The list on the deployment page says what you may choose. It does not say
// what you may not, which is the question someone asks when the thing they
// wanted is absent -- is it unsupported, or have I misread the list?
//
// Both axes come from the database. CLAUDE.md forbids hardcoding platform lists
// in Go, and that applies to the shape of a table about them as much as to the
// options in a form: seeding a new platform must make it appear here on its own.
type SupportMatrix struct {
	// ProjectID is carried so the grid can post to this project's channel
	// route without its host template threading the ID through.
	ProjectID     string
	ArtifactKinds []string
	Rows          []SupportRow
}

// SupportRow is one platform and its cell for each artifact kind.
type SupportRow struct {
	Platform string
	Cells    []SupportCell
}

// SupportCell is one platform/artifact pairing.
type SupportCell struct {
	Supported bool
	Current   bool   // The channel this project is using
	Note      string // The combo's own note, when it has one
	// ComboID is what selecting this cell posts. The grid is the channel
	// picker: a list of the supported pairings could say what you may choose
	// but never what you may not, and the answer to "why is my platform not
	// here" was a gap in a list rather than a row with nothing ticked.
	ComboID string
}

// Configured reports whether the project has already chosen a channel, which
// decides whether the grid reads as a first choice or a change.
func (m *SupportMatrix) Configured() bool {
	if m == nil {
		return false
	}
	for _, row := range m.Rows {
		for _, cell := range row.Cells {
			if cell.Current {
				return true
			}
		}
	}
	return false
}

// HasAny reports whether the matrix is worth drawing.
func (m *SupportMatrix) HasAny() bool {
	return m != nil && len(m.Rows) > 0 && len(m.ArtifactKinds) > 0
}

// buildSupportMatrix reads the platforms and combos, then pivots them.
func (s *Server) buildSupportMatrix(ctx context.Context, projectID uuid.UUID, current *domain.ProjectDeploymentChannel) *SupportMatrix {
	platforms, err := s.db.ListHostingPlatforms(ctx)
	if err != nil {
		return nil
	}
	combos, err := s.db.ListSupportedDeploymentCombos(ctx)
	if err != nil {
		return nil
	}
	m := pivotSupportMatrix(platforms, combos, current)
	if m != nil {
		m.ProjectID = projectID.String()
	}
	return m
}

// pivotSupportMatrix is the pivot itself, kept free of the database so every
// shape it can produce is reachable in a test.
func pivotSupportMatrix(platforms []domain.HostingPlatform, combos []domain.SupportedDeploymentCombo,
	current *domain.ProjectDeploymentChannel) *SupportMatrix {

	if len(platforms) == 0 {
		return nil
	}

	// Columns are the artifact kinds that appear in the combo table. A kind
	// nothing supports is a kind Mendel has never heard of, and inventing a
	// column for it would be inventing the fact that it exists.
	var kinds []string
	seen := map[string]bool{}
	supported := map[string]*domain.SupportedDeploymentCombo{}
	for i := range combos {
		c := &combos[i]
		kind := string(c.ArtifactKind)
		if !seen[kind] {
			seen[kind] = true
			kinds = append(kinds, kind)
		}
		supported[kind+"\x00"+c.HostingPlatformID.String()] = c
	}
	sort.Strings(kinds)

	matrix := &SupportMatrix{ArtifactKinds: kinds}
	for i := range platforms {
		p := &platforms[i]
		row := SupportRow{Platform: p.Name}
		for _, kind := range kinds {
			cell := SupportCell{}
			if combo, ok := supported[kind+"\x00"+p.ID.String()]; ok {
				cell.Supported = true
				cell.ComboID = combo.ID.String()
				if combo.Notes != nil {
					cell.Note = *combo.Notes
				}
				cell.Current = current != nil &&
					string(current.ArtifactKind) == kind &&
					current.HostingPlatformID == p.ID
			}
			row.Cells = append(row.Cells, cell)
		}
		matrix.Rows = append(matrix.Rows, row)
	}
	return matrix
}

// setupPlaceholder0 is the token in a setup script the user has to replace.
func setupPlaceholder0(script string) string {
	return setupPlaceholder.FindString(script)
}
