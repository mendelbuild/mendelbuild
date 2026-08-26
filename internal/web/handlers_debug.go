package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// DebugInfo contains diagnostic information about a project's state
type DebugInfo struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`

	// Config from database
	ProjectConfig map[string]interface{} `json:"project_config"`

	// Deployment channel
	DeploymentChannel     string `json:"deployment_channel,omitempty"`
	DeploymentChannelDemo bool   `json:"deployment_channel_demo_validated"`
	DeploymentChannelProd bool   `json:"deployment_channel_prod_validated"`

	// Credentials
	Credentials []string `json:"credentials"`

	// Repository info
	RepoURL      string `json:"repo_url"`
	MainBranchOK bool   `json:"main_branch_ok"`

	// Work directory state
	WorkDirExists bool   `json:"work_dir_exists"`
	WorkDir       string `json:"work_dir"`

	// Input requests summary
	PendingInputs int `json:"pending_inputs"`
}

func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	info := DebugInfo{
		ProjectID: projectID.String(),
	}

	// Get project
	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	info.ProjectName = project.Name

	// Parse project config
	if project.Config != nil {
		json.Unmarshal(project.Config, &info.ProjectConfig)
	}

	// Get deployment channel
	channel, _ := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if channel != nil {
		if channel.HostingPlatform != nil {
			info.DeploymentChannel = string(channel.ArtifactKind) + " -> " + channel.HostingPlatform.Name
		}
		info.DeploymentChannelDemo = channel.IsDemoValidated()
		info.DeploymentChannelProd = channel.IsProdValidated()
	}

	// Get credentials
	creds, _ := s.db.ListProjectCredentials(ctx, projectID)
	for _, c := range creds {
		info.Credentials = append(info.Credentials, c.Name)
	}

	// Get repository
	repo, _ := s.db.GetRepositoryByProject(ctx, projectID)
	if repo != nil && repo.URL != nil {
		info.RepoURL = *repo.URL
	}

	// Check main branch work directory
	mainWorkDir := filepath.Join(os.Getenv("MENDEL_WORK_DIR"), projectID.String(), "main")
	if mainWorkDir == "" {
		mainWorkDir = filepath.Join("/work", projectID.String(), "main")
	}
	info.WorkDir = mainWorkDir
	if _, err := os.Stat(mainWorkDir); err == nil {
		info.WorkDirExists = true
		info.MainBranchOK = true
	}

	// Get pending input requests
	inputs, _ := s.db.GetInputRequestsByProject(ctx, projectID)
	for _, ir := range inputs {
		if ir.Status != "resolved" {
			info.PendingInputs++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(info)
}
