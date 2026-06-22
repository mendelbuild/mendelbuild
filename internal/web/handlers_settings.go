package web

import (
	"encoding/json"
	"net/http"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ProjectSettings holds the settings form data.
type ProjectSettings struct {
	RepoURL         string
	MainBranch      string
	TestCommand     string
	AuthToken       string
	AnthropicAPIKey string
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
				MainBranch  string `json:"main_branch"`
				AuthToken   string `json:"auth_token"`
				TestCommand string `json:"test_command"`
			}
			if json.Unmarshal(repo.Config, &repoConfig) == nil {
				if repoConfig.MainBranch != "" {
					settings.MainBranch = repoConfig.MainBranch
				}
				settings.AuthToken = repoConfig.AuthToken
				settings.TestCommand = repoConfig.TestCommand
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

	data := map[string]interface{}{
		"Title":     "Project Settings",
		"ProjectID": projectID.String(),
		"Settings":  settings,
		"Success":   success,
	}

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
	testCommand := r.FormValue("test_command")
	authToken := r.FormValue("auth_token")
	anthropicAPIKey := r.FormValue("anthropic_api_key")

	if mainBranch == "" {
		mainBranch = "main"
	}

	// Update repository config
	repoConfig, _ := json.Marshal(map[string]string{
		"main_branch":  mainBranch,
		"test_command": testCommand,
		"auth_token":   authToken,
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
