package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bhs/mendelbuild/internal/codegen/executor"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
	"github.com/bhs/mendelbuild/internal/hosting"
	"github.com/bhs/mendelbuild/internal/test"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// buildGitHubBranchURL constructs a GitHub URL for viewing a branch or commit.
// Handles both HTTPS and SSH repo URLs.
func buildGitHubBranchURL(repoURL, branchName string, commitRef *string) string {
	// Convert repo URL to GitHub base URL
	// https://github.com/user/repo.git -> https://github.com/user/repo
	// git@github.com:user/repo.git -> https://github.com/user/repo
	base := repoURL
	base = strings.TrimSuffix(base, ".git")

	if strings.HasPrefix(base, "git@github.com:") {
		base = strings.Replace(base, "git@github.com:", "https://github.com/", 1)
	}

	// Only works for GitHub URLs
	if !strings.Contains(base, "github.com") {
		return ""
	}

	// If we have a commit ref, link to that specific commit
	if commitRef != nil && *commitRef != "" {
		return fmt.Sprintf("%s/tree/%s", base, *commitRef)
	}

	// Otherwise link to the branch
	return fmt.Sprintf("%s/tree/%s", base, branchName)
}

// buildGitHubDiffURL constructs a GitHub compare URL (main...branch).
func buildGitHubDiffURL(repoURL, mainBranch, branchName string) string {
	base := repoURL
	base = strings.TrimSuffix(base, ".git")

	if strings.HasPrefix(base, "git@github.com:") {
		base = strings.Replace(base, "git@github.com:", "https://github.com/", 1)
	}

	if !strings.Contains(base, "github.com") {
		return ""
	}

	return fmt.Sprintf("%s/compare/%s...%s", base, mainBranch, branchName)
}

// VariationWithLogs holds a variation and its recent logs.
type VariationWithLogs struct {
	Variation  domain.Variation
	RecentLogs []domain.VariationLog
}

// HopDetailView holds data for rendering the hop detail page.
type HopDetailView struct {
	Hop                      *domain.Hop
	Strategy                 *domain.Strategy
	Project                  *domain.Project
	Variations               []VariationWithLogs
	Objectives               []domain.Objective
	PendingReview            *domain.InputRequest
	PendingSelection         *domain.InputRequest
	HasCreatingVariations    bool
	HasPendingVariations     bool
	IsStuck                  bool // No pending variations and no unresolved decisions
	NeedsProductionCredentials bool // requires_production but no credentials configured
	Cost                     *HopCostView

	Ribbon domain.Ribbon // Plain-English lifecycle position and next action
	Roadmap *MiniRoadmap  // The project roadmap, scrolled to this Hop
}

func (s *Server) handleHopDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	hopID, err := uuid.Parse(chi.URLParam(r, "hopID"))
	if err != nil {
		http.Error(w, "invalid hop ID", http.StatusBadRequest)
		return
	}

	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	hop, err := s.db.GetHop(ctx, hopID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	strategy, err := s.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		http.Error(w, "strategy not found", http.StatusNotFound)
		return
	}

	rawVariations, _ := s.db.GetVariationsByHop(ctx, hopID)

	// Fetch recent logs for each variation
	var variations []VariationWithLogs
	for _, v := range rawVariations {
		logs, _ := s.db.GetRecentVariationLogs(ctx, v.ID, 5)
		// Reverse logs so oldest is first (they come back newest first)
		for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
			logs[i], logs[j] = logs[j], logs[i]
		}
		variations = append(variations, VariationWithLogs{
			Variation:  v,
			RecentLogs: logs,
		})
	}

	// Parse objective IDs from hop params
	var objectives []domain.Objective
	if hop.Params != nil {
		var params struct {
			ObjectiveIDs []string `json:"objective_ids"`
		}
		if err := json.Unmarshal(hop.Params, &params); err == nil {
			for _, objIDStr := range params.ObjectiveIDs {
				if objID, err := uuid.Parse(objIDStr); err == nil {
					allObjs, _ := s.db.GetObjectivesByStrategy(ctx, strategy.ID)
					for _, obj := range allObjs {
						if obj.ID == objID {
							objectives = append(objectives, obj)
							break
						}
					}
				}
			}
		}
	}

	// Check for pending input requests
	inputRequests, _ := s.db.GetInputRequestsBySubject(ctx, "hop", hopID)
	var pendingReview *domain.InputRequest
	var pendingSelection *domain.InputRequest
	for i := range inputRequests {
		ir := &inputRequests[i]
		if ir.Status != domain.InputRequestStatusResolved {
			if ir.Kind == domain.InputRequestKindVariationReview {
				pendingReview = ir
			} else if ir.Kind == domain.InputRequestKindVariationSelection {
				pendingSelection = ir
			}
		}
	}

	// Check variation statuses
	hasCreatingVariations := false
	hasPendingVariations := false
	for _, v := range variations {
		if v.Variation.Status == domain.VariationStatusCreating {
			hasCreatingVariations = true
		}
		if v.Variation.Status == domain.VariationStatusPending {
			hasPendingVariations = true
		}
	}

	// Detect stuck state: has variations but none pending/creating, no unresolved decisions
	isStuck := len(variations) > 0 && !hasCreatingVariations && !hasPendingVariations && pendingReview == nil && pendingSelection == nil

	// Check if production credentials are needed but missing
	needsProductionCredentials := false
	if hop.RequiresProduction {
		creds, err := s.db.ListProjectCredentials(ctx, projectID)
		if err != nil || len(creds) == 0 {
			needsProductionCredentials = true
		}
	}

	// Estimate, ceiling, spend to date and the token counts behind it.
	costView := s.hopCostView(ctx, hopID)

	view := &HopDetailView{
		Hop:                        hop,
		Strategy:                   strategy,
		Project:                    project,
		Variations:                 variations,
		Objectives:                 objectives,
		PendingReview:              pendingReview,
		PendingSelection:           pendingSelection,
		HasCreatingVariations:      hasCreatingVariations,
		HasPendingVariations:       hasPendingVariations,
		IsStuck:                    isStuck,
		NeedsProductionCredentials: needsProductionCredentials,
		Cost:                       costView,
		Ribbon:                     domain.HopLifecycle(hop, rawVariations),
		Roadmap:                      s.buildMiniRoadmap(ctx, projectID, hop, uuid.Nil),
	}

	data := map[string]interface{}{
		"Title":     "Hop: " + hop.Name,
		"ProjectID": projectID.String(),
		"View":      view,
	}
	s.addOpenInputCount(ctx, data)

	if err := s.renderPageFor(w, r, "hop_detail.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleProposeVariations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	hopID, err := uuid.Parse(chi.URLParam(r, "hopID"))
	if err != nil {
		http.Error(w, "invalid hop ID", http.StatusBadRequest)
		return
	}

	hop, err := s.db.GetHop(ctx, hopID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// Use the shared function to propose variations
	if err := s.proposeVariationsForHop(ctx, hop); err != nil {
		http.Error(w, "error proposing variations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get the created input request to redirect to it
	inputRequest, err := s.db.GetInputRequestBySubjectAndKind(ctx, "hop", hopID, domain.InputRequestKindVariationReview)
	if err != nil {
		// Input request was created but we can't find it - redirect to hop page
		http.Redirect(w, r, fmt.Sprintf("/p/%s/hops/%s", projectID, hopID), http.StatusSeeOther)
		return
	}

	// Redirect to input request page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, inputRequest.ID), http.StatusSeeOther)
}

// VariationDetailView holds data for rendering the variation detail page.
type VariationDetailView struct {
	Variation    *domain.Variation
	Hop          *domain.Hop
	Logs         []domain.VariationLog
	DemoInstance *domain.DemoInstance  // Current or recent demo instance
	DemoLogs     []domain.VariationLog // Logs specific to the current demo

	// Streaming log panels, rendered by the "log-tail" partial.
	CodegenPanel *LogPanel
	DemoPanel    *LogPanel

	// Cost is what generating this Variation actually cost, from the ledger.
	Cost *VariationCostView
	GitHubURL    string                // Link to branch on GitHub (if applicable)
	DiffURL      string                // Link to GitHub compare (main...branch)
	CanRetryFix  bool                  // True if "Retry with Fix" is available
	LastError    string                // Last error message (for retry context)

	// Deployment channel status (replaces old demo-hosting.yml approach)
	HasDeploymentChannel    bool     // True if project has a deployment channel configured
	DeploymentChannelName   string   // Platform name for display (e.g., "Fly.io")
	IsDemoValidated         bool     // True if channel is validated for demos
	MissingCredentials      []string // Credentials required but not configured
	DemoReady               bool     // True if channel is validated AND all credentials present

	// What this variation's code needs in order to run, judged against the
	// demo deployment. Requirements block the demo the same way a missing
	// platform credential does, but the user resolves them here.
	Requirements *RequirementsPanel

	Revisions []domain.VariationRevision // User-requested changes; drives the Refine track
	Ribbon    domain.Ribbon              // Plain-English lifecycle position and next action
	Roadmap   *MiniRoadmap               // The project roadmap, scrolled to the parent Hop
}

func (s *Server) handleVariationDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	hop, err := s.db.GetHop(ctx, variation.HopID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// Get codegen logs only (not demo logs)
	logs, _ := s.db.GetVariationLogsByType(ctx, variationID, domain.SourceTypeCodegen, 500)

	// Get the most recent demo instance (any status) for display
	demoInstance, _ := s.db.GetLatestDemoByVariation(ctx, variationID)

	// Get demo-specific logs if there's a demo instance
	var demoLogs []domain.VariationLog
	if demoInstance != nil {
		demoLogs, _ = s.db.GetVariationLogsBySource(ctx, domain.SourceTypeDemo, demoInstance.ID, 200)
	}

	// Build GitHub URL for the branch
	branchName := fmt.Sprintf("mendel/%s/%s", hop.Name, variation.Name)
	var githubURL, diffURL string
	if repo, err := s.db.GetRepositoryByProject(ctx, projectID); err == nil && repo != nil && repo.URL != nil {
		githubURL = buildGitHubBranchURL(*repo.URL, branchName, variation.CommitRef)
		// Get main branch for diff URL
		mainBranch := "main"
		if repo.Config != nil {
			var repoConfig struct {
				MainBranch string `json:"main_branch"`
			}
			if err := json.Unmarshal(repo.Config, &repoConfig); err == nil && repoConfig.MainBranch != "" {
				mainBranch = repoConfig.MainBranch
			}
		}
		diffURL = buildGitHubDiffURL(*repo.URL, mainBranch, branchName)
	}

	// Check if retry-fix is available (terminated + work dir exists)
	var canRetryFix bool
	var lastError string
	if variation.Status == domain.VariationStatusTerminated {
		strategy, _ := s.db.GetStrategy(ctx, hop.StrategyID)
		if strategy != nil {
			workDir := git.WorkDirForVariation(strategy.ProjectID.String(), variation.ID.String())
			if _, err := os.Stat(workDir); err == nil {
				canRetryFix = true
				// Find the last error message
				for i := len(logs) - 1; i >= 0; i-- {
					if logs[i].Level == domain.LogLevelError {
						lastError = logs[i].Message
						break
					}
				}
			}
		}
	}

	// Check deployment channel status
	var hasDeploymentChannel bool
	var deploymentChannelName string
	var isDemoValidated bool
	var missingCredentials []string
	var demoReady bool

	channel, _ := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if channel != nil {
		hasDeploymentChannel = true
		isDemoValidated = channel.IsDemoValidated()

		// Get platform name from joined field
		if channel.HostingPlatform != nil {
			deploymentChannelName = channel.HostingPlatform.Name
		}

		// Check required credentials
		creds, _ := s.db.ListProjectCredentials(ctx, projectID)
		credSet := make(map[string]bool)
		for _, c := range creds {
			credSet[c.Name] = true
		}

		// Get required credentials from hosting package
		platformSlug := ""
		if channel.HostingPlatform != nil {
			platformSlug = channel.HostingPlatform.Slug
		}
		required := hosting.RequiredCredentialsForCombo(channel.ArtifactKind, platformSlug)
		for _, name := range required {
			if !credSet[name] {
				missingCredentials = append(missingCredentials, name)
			}
		}

		demoReady = isDemoValidated && len(missingCredentials) == 0
	}

	// Judge the variation's requirements against where the demo will run. A
	// running demo settles the URL outright; otherwise it is predicted, which
	// only Fly.io permits. On the other platforms a URL-dependent
	// acknowledgement stays deferred until the first deploy produces one.
	demoURL := ""
	if demoInstance != nil && demoInstance.URL != "" {
		demoURL = demoInstance.URL
	} else if channel != nil && channel.HostingPlatform != nil {
		if project, err := s.db.GetProject(ctx, projectID); err == nil {
			demoURL = predictedDeployURL(channel.HostingPlatform.Slug,
				demoAppName(sanitizeAppName(project.Name), variationID))
		}
	}
	statuses, err := s.variationRequirementStatus(ctx, projectID, variationID, demoURL)
	if err != nil {
		http.Error(w, "failed to check requirements: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var requirements *RequirementsPanel
	if len(statuses) > 0 {
		platformName := "this platform"
		if channel != nil && channel.HostingPlatform != nil {
			platformName = channel.HostingPlatform.Name
		}
		requirements = &RequirementsPanel{
			Title:      "What This Needs to Run",
			Intro:      "This variation's code cannot function without the following.",
			ActionBase: fmt.Sprintf("/p/%s/variations/%s/requirements", projectID, variationID),
			DeferredNote: "This step names the demo's URL, which " + platformName +
				" only assigns once the app exists. Start the demo, then come back to confirm it.",
			Statuses: statuses,
		}
	}
	if requirements.Blocked() {
		demoReady = false
	}

	// Revisions are required for an accurate Refine track: handleRequestChange
	// sets the Variation back to "creating", so status alone cannot distinguish
	// a first build from a revision in flight.
	revisions, _ := s.db.GetVariationRevisions(ctx, variationID)

	// Code generation streams while the variation is being created.
	codegenPanel := &LogPanel{
		DOMID:     "codegen-logs",
		FeedURL:   fmt.Sprintf("/api/variations/%s/logs", variationID),
		Status:    string(variation.Status),
		Live:      variation.Status == domain.VariationStatusCreating,
		Tall:      true,
		Empty:     "No logs yet.",
		Lines:     logLinesFromVariation(logs),
	}

	// The demo panel only exists once a demo has been started.
	var demoPanel *LogPanel
	if demoInstance != nil {
		demoPanel = &LogPanel{
			DOMID:     "demo-logs",
			FeedURL:   fmt.Sprintf("/api/demos/%s/logs", demoInstance.ID),
			Status:    string(demoInstance.Status),
			Live:      demoInstance.Status == domain.DemoInstanceStatusStarting,
			Empty:     "No demo logs yet.",
			Lines:     logLinesFromVariation(demoLogs),
		}
	}

	view := &VariationDetailView{
		Variation:             variation,
		Hop:                   hop,
		Logs:                  logs,
		Cost:                  s.variationCostView(ctx, variation.ID),
		CodegenPanel:          codegenPanel,
		DemoPanel:             demoPanel,
		Revisions:             revisions,
		Ribbon:                domain.VariationLifecycle(variation, revisions, hop),
		Roadmap:                 s.buildMiniRoadmap(ctx, projectID, hop, variationID),
		DemoInstance:          demoInstance,
		DemoLogs:              demoLogs,
		GitHubURL:             githubURL,
		DiffURL:               diffURL,
		CanRetryFix:           canRetryFix,
		LastError:             lastError,
		HasDeploymentChannel:  hasDeploymentChannel,
		DeploymentChannelName: deploymentChannelName,
		IsDemoValidated:       isDemoValidated,
		MissingCredentials:    missingCredentials,
		DemoReady:             demoReady,
		Requirements:          requirements,
	}

	data := map[string]interface{}{
		"Title":     "Variation: " + variation.Name,
		"ProjectID": projectID.String(),
		"View":      view,
	}
	s.addOpenInputCount(ctx, data)

	if err := s.renderPageFor(w, r, "variation_detail.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleRetryVariation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	// Allow retry for error, terminated, pending, or a run paused at its spend
	// ceiling. The last is not a retry so much as an approval to spend more:
	// the work directory is intact, so generation continues from the
	// half-finished code rather than starting over.
	if variation.Status != domain.VariationStatusError &&
		variation.Status != domain.VariationStatusTerminated &&
		variation.Status != domain.VariationStatusPending &&
		!(variation.Status == domain.VariationStatusBlocked && variation.PausedForBudget()) {
		http.Error(w, "can only retry variations in error, terminated, or pending status, "+
			"or continue one paused at its spend ceiling", http.StatusBadRequest)
		return
	}

	oldStatus := variation.Status

	// Atomically transition to creating - if this fails, someone else got there first
	updated, err := s.db.AtomicUpdateVariationStatus(ctx, variationID, oldStatus, domain.VariationStatusCreating)
	if err != nil {
		http.Error(w, "database error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !updated {
		http.Error(w, "variation is already being processed", http.StatusConflict)
		return
	}

	// Record state transition
	reason := "manual retry"
	if oldStatus == domain.VariationStatusBlocked && variation.PausedForBudget() {
		reason = fmt.Sprintf("approved to continue past the $%.2f spend ceiling", *variation.BudgetCeilingUSD)
	}
	s.db.CreateVariationStateTransition(ctx, variationID, string(oldStatus), string(domain.VariationStatusCreating), reason)

	// Redirect back to variation detail to watch progress
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

func (s *Server) handleTerminateVariation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	// Only allow terminate for creating status (stuck generations)
	if variation.Status != domain.VariationStatusCreating {
		http.Error(w, "can only terminate variations in creating status", http.StatusBadRequest)
		return
	}

	// Set status to terminated
	variation.Status = domain.VariationStatusTerminated
	variation.UpdatedAt = time.Now()
	if err := s.db.UpdateVariation(ctx, variation); err != nil {
		http.Error(w, "error updating variation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Record state transition
	s.db.CreateVariationStateTransition(ctx, variationID, string(domain.VariationStatusCreating), string(domain.VariationStatusTerminated), "manual termination")

	// Redirect back to variation detail
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

func (s *Server) handleRebaseVariation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	// Only allow rebase for pending or error status (not creating)
	if variation.Status != domain.VariationStatusPending &&
		variation.Status != domain.VariationStatusError &&
		variation.Status != domain.VariationStatusTerminated {
		http.Error(w, "can only rebase variations in pending, error, or terminated status", http.StatusBadRequest)
		return
	}

	// Get hop to find branch name
	hop, err := s.db.GetHop(ctx, variation.HopID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// Get repository info
	repo, err := s.db.GetRepositoryByProject(ctx, projectID)
	if err != nil || repo.URL == nil {
		http.Error(w, "repository not configured", http.StatusBadRequest)
		return
	}

	var repoConfig struct {
		AuthToken  string `json:"auth_token"`
		MainBranch string `json:"main_branch"`
	}
	if repo.Config != nil {
		json.Unmarshal(repo.Config, &repoConfig)
	}
	if repoConfig.MainBranch == "" {
		repoConfig.MainBranch = "main"
	}

	// Build branch name (must match what codegen creates)
	branchName := fmt.Sprintf("mendel/%s/%s", hop.Name, variation.Name)

	// Create temp work directory
	workDir := fmt.Sprintf("/work/%s/rebase-%s", projectID, variationID)
	os.MkdirAll(workDir, 0755)
	defer os.RemoveAll(workDir)

	// Clone and checkout the variation branch
	gitClient := git.NewClient(workDir)
	if err := gitClient.Clone(ctx, *repo.URL, branchName, repoConfig.AuthToken); err != nil {
		// Log the rebase operation
		s.db.CreateVariationLog(ctx, variationID, domain.LogLevelError, fmt.Sprintf("Rebase failed - could not clone branch: %v", err))
		http.Error(w, "failed to clone variation branch: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Rebase onto main
	if err := gitClient.RebaseOnto(ctx, repoConfig.MainBranch, repoConfig.AuthToken); err != nil {
		s.db.CreateVariationLog(ctx, variationID, domain.LogLevelError, fmt.Sprintf("Rebase failed - conflicts or error: %v", err))
		http.Error(w, "rebase failed (likely conflicts): "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Push the rebased branch
	if err := gitClient.Push(ctx, repoConfig.AuthToken); err != nil {
		s.db.CreateVariationLog(ctx, variationID, domain.LogLevelError, fmt.Sprintf("Rebase failed - could not push: %v", err))
		http.Error(w, "failed to push rebased branch: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get new commit ref
	newCommit, err := gitClient.GetCurrentCommit(ctx)
	if err != nil {
		newCommit = "" // Non-fatal
	}

	// Update variation with new commit ref
	if newCommit != "" {
		variation.CommitRef = &newCommit
		variation.UpdatedAt = time.Now()
		s.db.UpdateVariation(ctx, variation)
	}

	// Log success
	s.db.CreateVariationLog(ctx, variationID, domain.LogLevelMilestone, fmt.Sprintf("Rebased onto %s", repoConfig.MainBranch))

	// Redirect back to variation detail
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// handleRequestChange creates a VariationRevision and puts the variation back into "creating" state.
func (s *Server) handleRequestChange(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	feedback := strings.TrimSpace(r.FormValue("feedback"))
	if feedback == "" {
		http.Error(w, "feedback is required", http.StatusBadRequest)
		return
	}

	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	// Only allow change requests for pending or error status
	if variation.Status != domain.VariationStatusPending &&
		variation.Status != domain.VariationStatusError &&
		variation.Status != domain.VariationStatusBlocked {
		http.Error(w, "can only request changes for variations in pending, error, or blocked status", http.StatusBadRequest)
		return
	}

	// Create the revision record
	revision := &domain.VariationRevision{
		ID:          uuid.New(),
		VariationID: variationID,
		Feedback:    feedback,
		Status:      domain.VariationRevisionStatusPending,
		CreatedAt:   time.Now(),
	}

	if err := s.db.CreateVariationRevision(ctx, revision); err != nil {
		http.Error(w, "failed to create revision: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Set variation back to "creating" status
	s.db.AtomicUpdateVariationStatus(ctx, variationID, variation.Status, domain.VariationStatusCreating)

	// Log the revision request
	s.db.CreateVariationLog(ctx, variationID, domain.LogLevelMilestone, fmt.Sprintf("Change requested: %s", feedback))

	// Redirect back to variation detail
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

func (s *Server) handleRetryWithFix(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	// Only allow retry-fix for terminated status - use atomic update to prevent race
	if variation.Status != domain.VariationStatusTerminated {
		http.Error(w, "can only retry-fix terminated variations", http.StatusBadRequest)
		return
	}

	// Atomically transition to creating - if this fails, someone else got there first
	updated, err := s.db.AtomicUpdateVariationStatus(ctx, variationID, domain.VariationStatusTerminated, domain.VariationStatusCreating)
	if err != nil {
		http.Error(w, "database error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !updated {
		http.Error(w, "variation is already being processed", http.StatusConflict)
		return
	}

	hop, err := s.db.GetHop(ctx, variation.HopID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	strategy, err := s.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		http.Error(w, "strategy not found", http.StatusNotFound)
		return
	}

	// Check work directory exists
	workDir := git.WorkDirForVariation(strategy.ProjectID.String(), variation.ID.String())
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		http.Error(w, "work directory not found - use regular Retry instead", http.StatusBadRequest)
		return
	}

	// Build error context from recent logs (last 20 lines or so)
	logs, _ := s.db.GetVariationLogsByType(ctx, variationID, domain.SourceTypeCodegen, 100)
	var errorContext strings.Builder
	var foundError bool

	// Find the last error and include context around it
	for i := len(logs) - 1; i >= 0; i-- {
		if logs[i].Level == domain.LogLevelError {
			foundError = true
			// Include up to 15 lines before the error for context
			startIdx := i - 15
			if startIdx < 0 {
				startIdx = 0
			}
			for j := startIdx; j <= i; j++ {
				prefix := ""
				if logs[j].Level == domain.LogLevelError {
					prefix = "[ERROR] "
				} else if logs[j].Level == domain.LogLevelMilestone {
					prefix = "[MILESTONE] "
				}
				errorContext.WriteString(prefix)
				errorContext.WriteString(logs[j].Message)
				errorContext.WriteString("\n")
			}
			break
		}
	}

	if !foundError {
		http.Error(w, "no error found to fix", http.StatusBadRequest)
		return
	}

	// Status was already atomically updated above, just record the transition
	s.db.CreateVariationStateTransition(ctx, variationID, string(domain.VariationStatusTerminated), string(domain.VariationStatusCreating), "retry with fix")

	// Run the fix in background
	go s.runFixForVariation(strategy.ProjectID, variation, hop, workDir, errorContext.String())

	// Redirect back to variation detail to watch progress
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// runFixForVariation runs the executor to fix a failed variation, re-runs tests, and commits if passing.
func (s *Server) runFixForVariation(projectID uuid.UUID, variation *domain.Variation, hop *domain.Hop, workDir, errorContext string) {
	ctx := context.Background()

	logger := func(level domain.LogLevel, message string) {
		s.db.CreateVariationLog(ctx, variation.ID, level, message)
	}

	logger(domain.LogLevelMilestone, "Attempting to fix previous error...")
	// Log just the first 500 chars of context to avoid huge log entries
	contextPreview := errorContext
	if len(contextPreview) > 500 {
		contextPreview = contextPreview[:500] + "..."
	}
	logger(domain.LogLevelInfo, fmt.Sprintf("Error context: %s", contextPreview))

	// Get API key
	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("Failed to get project: %v", err))
		s.transitionVariation(ctx, variation, domain.VariationStatusTerminated, "fix failed: could not get project")
		return
	}

	var projectConfig domain.ProjectConfig
	if project.Config != nil {
		json.Unmarshal(project.Config, &projectConfig)
	}
	apiKey := projectConfig.AnthropicAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		logger(domain.LogLevelError, "No API key configured")
		s.transitionVariation(ctx, variation, domain.VariationStatusTerminated, "fix failed: no API key")
		return
	}

	// Build fix prompt
	fixPrompt := fmt.Sprintf(`The previous code generation attempt failed. Here is the log context leading up to the error:

%s

Please fix the issue. Read the relevant files mentioned above, understand what went wrong, and make the necessary corrections.
Focus on fixing the specific error - don't rewrite everything.`, errorContext)

	// Run executor
	exec := executor.New(apiKey, workDir).
		WithEventHandler(func(event executor.Event) {
			switch event.Type {
			case executor.EventToolCall:
				switch event.ToolName {
				case "Write":
					if path, ok := event.ToolInput["file_path"].(string); ok {
						logger(domain.LogLevelMilestone, fmt.Sprintf("Writing: %s", path))
					}
				case "Edit":
					if path, ok := event.ToolInput["file_path"].(string); ok {
						logger(domain.LogLevelMilestone, fmt.Sprintf("Editing: %s", path))
					}
				case "Read":
					if path, ok := event.ToolInput["file_path"].(string); ok {
						logger(domain.LogLevelInfo, fmt.Sprintf("Reading: %s", path))
					}
				}
			case executor.EventAPIResponse:
				logger(domain.LogLevelInfo, fmt.Sprintf("API: +%d in, +%d out tokens", event.InputTokens, event.OutputTokens))
			case executor.EventComplete:
				logger(domain.LogLevelMilestone, "Fix attempt complete")
			}
		})

	result, err := exec.Run(ctx, executor.SystemPrompt(), fixPrompt)
	if err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("Executor error: %v", err))
		s.transitionVariation(ctx, variation, domain.VariationStatusTerminated, "fix failed: executor error")
		return
	}

	logger(domain.LogLevelInfo, fmt.Sprintf("Fix stats: %d rounds, %d tool calls", result.Stats.APIRounds, result.Stats.ToolCalls))
	s.recordVariationRun(ctx, variation, result.Stats, "codegen_fix")

	if !result.Success {
		logger(domain.LogLevelError, fmt.Sprintf("Fix failed: %v", result.Error))
		s.transitionVariation(ctx, variation, domain.VariationStatusTerminated, "fix failed")
		return
	}

	// Get repo config for commit/push
	repo, err := s.db.GetRepositoryByProject(ctx, projectID)
	if err != nil || repo == nil {
		logger(domain.LogLevelError, "Could not get repository config")
		s.transitionVariation(ctx, variation, domain.VariationStatusTerminated, "fix failed: no repository")
		return
	}

	var repoConfig struct {
		AuthToken string `json:"auth_token"`
	}
	if repo.Config != nil {
		json.Unmarshal(repo.Config, &repoConfig)
	}

	// Commit changes (before tests so branch is visible on GitHub even if tests fail)
	logger(domain.LogLevelInfo, "Committing fix...")
	gitClient := git.NewClient(workDir)

	commitMsg := fmt.Sprintf("[MendelBuild] Fix: %s\n\nFixed error in variation '%s'", hop.Name, variation.Name)
	if err := gitClient.CommitAll(ctx, commitMsg); err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("Commit failed: %v", err))
		s.transitionVariation(ctx, variation, domain.VariationStatusTerminated, "fix failed: commit error")
		return
	}

	// Get commit ref
	commitRef, err := gitClient.GetCurrentCommit(ctx)
	if err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("Get commit ref failed: %v", err))
		s.transitionVariation(ctx, variation, domain.VariationStatusTerminated, "fix failed: could not get commit")
		return
	}
	logger(domain.LogLevelMilestone, fmt.Sprintf("Committed: %s", commitRef[:8]))

	// Push (before tests so branch is visible on GitHub even if tests fail)
	logger(domain.LogLevelInfo, "Pushing to remote...")
	if err := gitClient.Push(ctx, repoConfig.AuthToken); err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("Push failed: %v", err))
		s.transitionVariation(ctx, variation, domain.VariationStatusTerminated, "fix failed: push error")
		return
	}
	logger(domain.LogLevelMilestone, "Pushed successfully")

	// Update variation with commit ref (before tests, so we have the ref even if tests fail)
	variation.CommitRef = &commitRef
	variation.UpdatedAt = time.Now()
	if err := s.db.UpdateVariation(ctx, variation); err != nil {
		logger(domain.LogLevelInfo, fmt.Sprintf("Could not save commit ref: %v", err))
	}

	// Re-run tests
	logger(domain.LogLevelMilestone, "Re-running tests...")
	testCfg, err := test.LoadConfig(workDir)
	if err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("Invalid test config: %v", err))
		s.transitionVariation(ctx, variation, domain.VariationStatusTerminated, "fix failed: invalid test config")
		return
	}

	if testCfg == nil {
		logger(domain.LogLevelInfo, "No test config, skipping tests")
	} else {
		testResult := test.RunTestsWithOutput(workDir, testCfg)
		if testResult.Output != "" {
			output := testResult.Output
			if len(output) > 4000 {
				output = output[:2000] + "\n...(truncated)...\n" + output[len(output)-1500:]
			}
			logger(domain.LogLevelInfo, output)
		}
		if !testResult.Passed {
			logger(domain.LogLevelError, fmt.Sprintf("Tests still failing: %s", testResult.Error))
			s.transitionVariation(ctx, variation, domain.VariationStatusTerminated, "fix failed: tests still failing")
			return
		}
		logger(domain.LogLevelMilestone, "Tests passed!")
	}

	// Update variation status to pending (tests passed)
	variation.Status = domain.VariationStatusPending
	variation.UpdatedAt = time.Now()
	if err := s.db.UpdateVariation(ctx, variation); err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("Update variation failed: %v", err))
		return
	}

	s.db.CreateVariationStateTransition(ctx, variation.ID, string(domain.VariationStatusCreating), string(domain.VariationStatusPending), "fix successful")
	logger(domain.LogLevelMilestone, "Fix completed successfully!")
}

// transitionVariation updates variation status and records the transition.
func (s *Server) transitionVariation(ctx context.Context, variation *domain.Variation, newStatus domain.VariationStatus, reason string) {
	oldStatus := variation.Status
	variation.Status = newStatus
	variation.UpdatedAt = time.Now()
	s.db.UpdateVariation(ctx, variation)
	s.db.CreateVariationStateTransition(ctx, variation.ID, string(oldStatus), string(newStatus), reason)
}
