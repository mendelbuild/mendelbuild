package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bhs/mendelbuild/internal/codegen/executor"
	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/demo"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// executeMigrationInstructions runs migration instructions via shell.
// Returns nil if instructions are empty or appear to be prose rather than commands.
// In the future, this could use Claude Code for more complex instructions.
func executeMigrationInstructions(ctx context.Context, workDir, instructions string) error {
	if instructions == "" {
		return nil
	}

	// Skip if instructions look like prose rather than commands
	// (e.g., starts with "Run", "Execute", "Apply", etc.)
	trimmed := strings.TrimSpace(instructions)
	proseIndicators := []string{"Run ", "Execute ", "Apply ", "To ", "First ", "Then ", "Note:", "WARNING"}
	for _, indicator := range proseIndicators {
		if strings.HasPrefix(trimmed, indicator) {
			fmt.Printf("[demo] Skipping migration: instructions appear to be prose, not commands\n")
			return nil
		}
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", instructions)
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(output))
	}
	return nil
}

// handleStartDemo starts a demo instance for a variation.
// Uses cloud hosting via .mendel/demo-hosting.yml configuration.
func (s *Server) handleStartDemo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	_, err = s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	// Check if there's already a running or starting demo
	existingDemo, err := s.db.GetRunningDemoByVariation(ctx, variationID)
	if err == nil && existingDemo != nil {
		http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
		return
	}

	// Get the work directory for this variation
	workDir := git.WorkDirForVariation(projectID, variationID.String())

	// Create demo instance with "starting" status immediately
	// All validation happens in the background so errors get proper logging
	demoInstanceID := uuid.New()

	processInfo, _ := json.Marshal(map[string]interface{}{
		"work_dir": workDir,
	})

	demoInstance := &domain.DemoInstance{
		ID:                   demoInstanceID,
		VariationID:          variationID,
		URL:                  "",
		TeardownInstructions: fmt.Sprintf("cd %s/.mendel && docker-compose -f docker-compose.demo.yml down -v", workDir),
		Status:               domain.DemoInstanceStatusStarting,
		ProcessInfo:          processInfo,
	}

	if err := s.db.CreateDemoInstance(ctx, demoInstance); err != nil {
		http.Error(w, "failed to create demo instance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Start the demo in a background goroutine - validation happens there
	go s.runDemoStartup(projectID, variationID, demoInstanceID, workDir)

	// Redirect immediately - user will see "starting" status
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// runDemoStartup runs the Docker-based demo startup process in the background.
// All validation happens here so errors get proper logging and suggested fixes.
func (s *Server) runDemoStartup(projectID string, variationID, demoInstanceID uuid.UUID, workDir string) {
	ctx := context.Background()

	// Helper to log with source tracking
	logInfo := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelInfo, msg, domain.SourceTypeDemo, &demoInstanceID)
	}
	logMilestone := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelMilestone, msg, domain.SourceTypeDemo, &demoInstanceID)
	}
	logError := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelError, msg, domain.SourceTypeDemo, &demoInstanceID)
	}

	// Helper to handle failures with suggested fix
	failDemoWithFix := func(errMsg, suggestedFix string) {
		logError(errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, suggestedFix)
	}

	// Helper to handle failures
	failDemo := func(errMsg string) {
		logError(errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, "")
	}

	// Silence unused variable warning
	_ = failDemo

	logMilestone("Checking demo configuration...")

	// Check if work directory exists - if not, try to recover from remote
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		// Check if the variation has a commit ref (was successfully pushed)
		variation, err := s.db.GetVariation(ctx, variationID)
		if err != nil || variation.CommitRef == nil || *variation.CommitRef == "" {
			failDemoWithFix(
				"Variation work directory not found - code may not be generated yet",
				"The variation's code hasn't been generated. Wait for code generation to complete, or check if there was an error during generation.",
			)
			return
		}

		// Code exists on remote - try to re-clone it
		logMilestone("Re-cloning variation branch from remote...")

		hop, err := s.db.GetHop(ctx, variation.HopID)
		if err != nil {
			failDemoWithFix(
				"Failed to look up hop for variation",
				"Internal error - try refreshing the page.",
			)
			return
		}

		projID, err := uuid.Parse(projectID)
		if err != nil {
			failDemoWithFix("Invalid project ID", "Internal error.")
			return
		}

		repo, err := s.db.GetRepositoryByProject(ctx, projID)
		if err != nil || repo.URL == nil {
			failDemoWithFix(
				"Repository URL not found",
				"Configure the repository URL in project settings.",
			)
			return
		}

		// Parse repo config for auth token
		var repoConfig struct {
			AuthToken string `json:"auth_token"`
		}
		if repo.Config != nil {
			json.Unmarshal(repo.Config, &repoConfig)
		}

		branchName := fmt.Sprintf("mendel/%s/%s", sanitizeBranchName(hop.Name), sanitizeBranchName(variation.Name))
		logInfo(fmt.Sprintf("Cloning branch %s...", branchName))

		gitClient := git.NewClient(workDir)
		if err := gitClient.Clone(ctx, *repo.URL, branchName, repoConfig.AuthToken); err != nil {
			failDemoWithFix(
				fmt.Sprintf("Failed to clone branch: %v", err),
				"The branch may have been deleted from the remote repository. You may need to regenerate this variation.",
			)
			return
		}
		logMilestone("Branch cloned successfully")
	}

	// Check if cloud hosting is configured (demo-hosting.yml)
	hostingCfg, err := demo.LoadHostingConfig(workDir)
	if err != nil {
		failDemo(fmt.Sprintf("Failed to load demo hosting config: %v", err))
		return
	}

	if hostingCfg == nil {
		// No demo-hosting.yml - check if hosting is configured at project level
		projID, _ := uuid.Parse(projectID)
		if project, err := s.db.GetProject(ctx, projID); err == nil && project != nil && project.Config != nil {
			var cfg map[string]interface{}
			if err := json.Unmarshal(project.Config, &cfg); err == nil {
				if platform, ok := cfg["demo_hosting_platform"].(string); ok && platform != "" {
					failDemoWithFix(
						"Demo hosting scripts not found in this variation.",
						fmt.Sprintf("Hosting platform (%s) was configured after this variation was created. The variation needs to be regenerated or rebased on main to include the demo scripts.", platform),
					)
					return
				}
			}
		}
		failDemoWithFix(
			"Demo hosting not configured.",
			"Configure a hosting platform in project settings to enable demos.",
		)
		return
	}

	s.runCloudDemoDeployment(ctx, projectID, variationID, demoInstanceID, workDir, hostingCfg, logMilestone, logInfo, logError, failDemo)
}

// truncateOutput truncates output to maxLen characters, keeping both beginning and end.
// This is important for Docker output where errors appear at the end.
func truncateOutput(output string, maxLen int) string {
	if len(output) <= maxLen {
		return output
	}
	// Keep 20% from start, 80% from end (errors are usually at the end)
	headLen := maxLen / 5
	tailLen := maxLen - headLen - 50 // 50 chars for the truncation notice
	return output[:headLen] + "\n\n... (truncated " + fmt.Sprintf("%d", len(output)-maxLen) + " chars) ...\n\n" + output[len(output)-tailLen:]
}

// lastNChars returns the last n characters of a string.
func lastNChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// createErrorDemoInstance records a failed demo attempt.
func (s *Server) createErrorDemoInstance(ctx context.Context, variationID uuid.UUID, url, errMsg string) {
	demoInstance := &domain.DemoInstance{
		ID:          uuid.New(),
		VariationID: variationID,
		URL:         url,
		Status:      domain.DemoInstanceStatusError,
	}
	demoInstance.ErrorMessage = &errMsg
	s.db.CreateDemoInstance(ctx, demoInstance)
}

// runCloudDemoDeployment deploys a demo using the cloud hosting scripts (demo-hosting.yml).
func (s *Server) runCloudDemoDeployment(
	ctx context.Context,
	projectID string,
	variationID uuid.UUID,
	demoInstanceID uuid.UUID,
	workDir string,
	hostingCfg *demo.HostingConfig,
	logMilestone func(string),
	logInfo func(string),
	logError func(string),
	failDemo func(string),
) {
	logMilestone("Deploying to cloud platform...")

	// Get project credentials
	projID, err := uuid.Parse(projectID)
	if err != nil {
		failDemo("Invalid project ID")
		return
	}

	// Check that all required secrets are present
	creds, err := s.db.ListProjectCredentials(ctx, projID)
	if err != nil {
		failDemo("Failed to load project credentials: " + err.Error())
		return
	}

	credMap := make(map[string]string)
	for _, c := range creds {
		credMap[c.Name] = c.Name // Store name to verify presence
	}

	var missingSecrets []string
	for _, secretName := range hostingCfg.RequiredSecrets {
		if _, ok := credMap[secretName]; !ok {
			missingSecrets = append(missingSecrets, secretName)
		}
	}

	if len(missingSecrets) > 0 {
		failDemo(fmt.Sprintf("Missing required credentials: %s. Add them in Project Settings.", strings.Join(missingSecrets, ", ")))
		return
	}

	// Build environment with decrypted secrets
	env := os.Environ()
	env = append(env, fmt.Sprintf("MENDEL_VARIATION_ID=%s", variationID.String()))

	// Get encryption key once
	key, err := crypto.GetKey()
	if err != nil {
		failDemo("Encryption not configured: " + err.Error())
		return
	}

	for _, secretName := range hostingCfg.RequiredSecrets {
		// Fetch the full credential (ListProjectCredentials doesn't include encrypted_value)
		cred, err := s.db.GetProjectCredential(ctx, projID, secretName)
		if err != nil {
			failDemo(fmt.Sprintf("Failed to load credential %s: %v", secretName, err))
			return
		}

		// Decrypt the credential
		decrypted, err := crypto.Decrypt(cred.EncryptedValue, key)
		if err != nil {
			failDemo(fmt.Sprintf("Failed to decrypt %s: %v", secretName, err))
			return
		}

		env = append(env, fmt.Sprintf("%s=%s", secretName, string(decrypted)))
	}

	// Run the deploy script inside a Docker container with the appropriate CLI tools
	deployScriptPath := workDir + "/.mendel/" + hostingCfg.DeployScript
	if _, err := os.Stat(deployScriptPath); os.IsNotExist(err) {
		failDemo(fmt.Sprintf("Deploy script not found: %s", hostingCfg.DeployScript))
		return
	}

	logInfo(fmt.Sprintf("Running deploy script in %s container", hostingCfg.DeployerImage))

	// Build docker run command:
	// - Mount the entire workDir so scripts can access repo files
	// - Pass environment variables
	// - Run the deploy script
	dockerArgs := []string{
		"run", "--rm",
		"-v", workDir + ":/workspace",
		"-w", "/workspace/.mendel",
	}

	// Add environment variables
	for _, e := range env {
		dockerArgs = append(dockerArgs, "-e", e)
	}

	// Add the image and command
	dockerArgs = append(dockerArgs, hostingCfg.DeployerImage, "bash", "/workspace/.mendel/"+hostingCfg.DeployScript)

	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if outputStr != "" {
		logInfo(truncateOutput(outputStr, 4000))
	}

	if err != nil {
		failDemo(fmt.Sprintf("Deploy script failed: %v", err))
		return
	}

	// Extract URL from output
	var demoURL string
	if hostingCfg.URLFrom == "" || hostingCfg.URLFrom == "stdout" {
		// Look for URL in stdout
		demoURL = extractURLFromOutput(outputStr)
	} else if strings.HasPrefix(hostingCfg.URLFrom, "file:") {
		// Read URL from file
		urlFile := strings.TrimPrefix(hostingCfg.URLFrom, "file:")
		urlFilePath := workDir + "/.mendel/" + urlFile
		urlBytes, err := os.ReadFile(urlFilePath)
		if err != nil {
			failDemo(fmt.Sprintf("Failed to read URL from %s: %v", urlFile, err))
			return
		}
		demoURL = strings.TrimSpace(string(urlBytes))
	}

	if demoURL == "" {
		failDemo("Deploy script did not output a URL. The script must print the deployed URL to stdout.")
		return
	}

	logMilestone(fmt.Sprintf("Demo deployed at %s", demoURL))

	// Build teardown command
	teardownCmd := fmt.Sprintf("cd %s/.mendel && bash %s", workDir, hostingCfg.TeardownScript)

	// Update demo instance
	processInfo, _ := json.Marshal(map[string]interface{}{
		"work_dir":     workDir,
		"deploy_mode":  "cloud",
		"hosting_file": "demo-hosting.yml",
	})

	s.db.Pool.Exec(ctx, `
		UPDATE demo_instances
		SET url = $2, teardown_instructions = $3, status = $4, process_info = $5
		WHERE id = $1
	`, demoInstanceID, demoURL, teardownCmd, domain.DemoInstanceStatusRunning, processInfo)
}

// extractURLFromOutput finds the first https:// URL in the output.
func extractURLFromOutput(output string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "https://") {
			// Take just the URL part (stop at whitespace)
			parts := strings.Fields(line)
			if len(parts) > 0 {
				return parts[0]
			}
		}
		// Also check for http:// in case of local-ish deployments
		if strings.HasPrefix(line, "http://") {
			parts := strings.Fields(line)
			if len(parts) > 0 {
				return parts[0]
			}
		}
	}
	return ""
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// handleStopDemo stops a running demo instance for a variation.
func (s *Server) handleStopDemo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	// Find running demo
	demoInst, err := s.db.GetRunningDemoByVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "no running demo found", http.StatusNotFound)
		return
	}

	// Extract work_dir from process info
	var processInfo map[string]interface{}
	if demoInst.ProcessInfo != nil {
		json.Unmarshal(demoInst.ProcessInfo, &processInfo)
	}
	workDir, _ := processInfo["work_dir"].(string)
	if workDir == "" {
		workDir = git.WorkDirForVariation(projectID, variationID.String())
	}

	// Run teardown instructions for demo process
	cmd := exec.CommandContext(ctx, "sh", "-c", demoInst.TeardownInstructions)
	cmd.Dir = workDir
	if err := cmd.Run(); err != nil {
		// Mark as error but continue
		errMsg := fmt.Sprintf("Teardown failed: %v", err)
		s.db.UpdateDemoInstanceStatus(ctx, demoInst.ID, domain.DemoInstanceStatusError, &errMsg)
	} else {
		// Mark as stopped
		s.db.UpdateDemoInstanceStatus(ctx, demoInst.ID, domain.DemoInstanceStatusStopped, nil)
	}

	// Revert migration if one was applied
	if err := s.revertVariationMigration(ctx, projectID, variationID); err != nil {
		// Log but don't fail - demo is already stopped
		fmt.Printf("[demo] Warning: failed to revert migration: %v\n", err)
	}

	// Redirect to variation detail
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// revertVariationMigration reverts a variation's migration if it was applied and not yet reverted.
func (s *Server) revertVariationMigration(ctx context.Context, projectID string, variationID uuid.UUID) error {
	migration, err := s.db.GetVariationMigration(ctx, variationID)
	if err != nil {
		return nil // No migration exists, nothing to revert
	}

	// Only revert if applied and not already reverted
	if migration.AppliedAt == nil || migration.RevertedAt != nil {
		return nil
	}

	workDir := git.WorkDirForVariation(projectID, variationID.String())
	if err := executeMigrationInstructions(ctx, workDir, migration.DownInstructions); err != nil {
		return fmt.Errorf("execute down_instructions: %w", err)
	}

	if err := s.db.MarkVariationMigrationReverted(ctx, migration.ID); err != nil {
		return fmt.Errorf("mark reverted: %w", err)
	}

	return nil
}

// cleanupVariationWorkDir removes the work directory for a resolved variation.
// This should be called when a variation reaches a terminal state (merged, rejected, pruned).
func (s *Server) cleanupVariationWorkDir(projectID string, variationID uuid.UUID) error {
	workDir := git.WorkDirForVariation(projectID, variationID.String())
	return os.RemoveAll(workDir)
}

// apiGetDemoLogs returns logs for a demo instance as JSON.
func (s *Server) apiGetDemoLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	demoID, err := uuid.Parse(chi.URLParam(r, "demoID"))
	if err != nil {
		http.Error(w, "invalid demo ID", http.StatusBadRequest)
		return
	}

	logs, err := s.db.GetVariationLogsBySource(ctx, domain.SourceTypeDemo, demoID, 500)
	if err != nil {
		http.Error(w, "failed to get logs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}

// apiGetDemoStatus returns the current status of a demo instance.
func (s *Server) apiGetDemoStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	demoID, err := uuid.Parse(chi.URLParam(r, "demoID"))
	if err != nil {
		http.Error(w, "invalid demo ID", http.StatusBadRequest)
		return
	}

	demo, err := s.db.GetDemoInstance(ctx, demoID)
	if err != nil {
		http.Error(w, "demo not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(demo)
}

// handleRetryDemo applies a fix via Claude Code and restarts the demo.
func (s *Server) handleRetryDemo(w http.ResponseWriter, r *http.Request) {
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

	fixPrompt := r.FormValue("fix_prompt")
	if fixPrompt == "" {
		http.Error(w, "fix prompt is required", http.StatusBadRequest)
		return
	}

	// Get work directory for this variation
	workDir := git.WorkDirForVariation(projectID, variationID.String())
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		// Try to re-clone from remote if the variation has a commit ref
		variation, err := s.db.GetVariation(ctx, variationID)
		if err != nil || variation.CommitRef == nil || *variation.CommitRef == "" {
			http.Error(w, "variation work directory not found and code not available on remote", http.StatusBadRequest)
			return
		}

		// Need to re-clone - redirect to restart which handles this
		http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s/restart-demo", projectID, variationID), http.StatusSeeOther)
		return
	}

	// Create a new demo instance for the fix attempt with "starting" status
	fixInstanceID := uuid.New()
	fixInstance := &domain.DemoInstance{
		ID:          fixInstanceID,
		VariationID: variationID,
		URL:         "",
		Status:      domain.DemoInstanceStatusStarting,
	}
	if err := s.db.CreateDemoInstance(ctx, fixInstance); err != nil {
		http.Error(w, "failed to create demo instance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Run the fix and demo startup in background
	go s.runFixAndDemo(projectID, variationID, fixInstanceID, workDir, fixPrompt)

	// Redirect immediately
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// runFixAndDemo runs Claude Code to apply a fix, then starts the demo.
func (s *Server) runFixAndDemo(projectID string, variationID, demoInstanceID uuid.UUID, workDir, fixPrompt string) {
	ctx := context.Background()

	// Helper to log with source tracking (use "demo" so logs appear with the demo)
	logInfo := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelInfo, msg, domain.SourceTypeDemo, &demoInstanceID)
	}
	logMilestone := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelMilestone, msg, domain.SourceTypeDemo, &demoInstanceID)
	}
	logError := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelError, msg, domain.SourceTypeDemo, &demoInstanceID)
	}
	failDemo := func(errMsg string) {
		logError(errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, fixPrompt)
	}

	logMilestone("Applying fix via Claude Code...")
	logInfo(fmt.Sprintf("Prompt: %s", fixPrompt))

	// Run Claude Code with the fix prompt
	cmd := exec.CommandContext(ctx, "claude", "--print", "--dangerously-skip-permissions", fixPrompt)
	cmd.Dir = workDir

	// Start progress monitor in background
	done := make(chan struct{})
	go monitorClaudeProgress(workDir, logInfo, done)

	output, err := cmd.CombinedOutput()
	close(done)

	if len(output) > 0 {
		outStr := string(output)
		if len(outStr) > 4000 {
			outStr = outStr[:4000] + "\n... (truncated)"
		}
		logInfo(outStr)
	}

	if err != nil {
		failDemo(fmt.Sprintf("Claude Code failed: %v", err))
		return
	}

	logMilestone("Fix applied, starting demo...")

	// Load cloud hosting config
	hostingCfg, err := demo.LoadHostingConfig(workDir)
	if err != nil {
		failDemo(fmt.Sprintf("Failed to load demo hosting config: %v", err))
		return
	}
	if hostingCfg == nil {
		failDemo("Demo hosting not configured. Configure a hosting platform in project settings.")
		return
	}

	// Run cloud deployment
	s.runCloudDemoDeployment(ctx, projectID, variationID, demoInstanceID, workDir, hostingCfg, logMilestone, logInfo, logError, failDemo)
}

// handleRestartDemo stops any existing demo and starts fresh without code changes.
func (s *Server) handleRestartDemo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	// Get the work directory
	workDir := git.WorkDirForVariation(projectID, variationID.String())

	// Stop any existing containers first (ignore errors - may not be running)
	demo.DockerComposeDown(workDir, true)

	// Create new demo instance - validation happens in background
	demoInstanceID := uuid.New()
	processInfo, _ := json.Marshal(map[string]interface{}{
		"work_dir": workDir,
	})

	demoInstance := &domain.DemoInstance{
		ID:                   demoInstanceID,
		VariationID:          variationID,
		URL:                  "",
		TeardownInstructions: fmt.Sprintf("cd %s/.mendel && docker-compose -f docker-compose.demo.yml down -v", workDir),
		Status:               domain.DemoInstanceStatusStarting,
		ProcessInfo:          processInfo,
	}

	if err := s.db.CreateDemoInstance(ctx, demoInstance); err != nil {
		http.Error(w, "failed to create demo instance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Start demo in background - validation happens there
	go s.runDemoStartup(projectID, variationID, demoInstanceID, workDir)

	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// monitorClaudeProgress observes file system and git activity while Claude Code runs,
// logging progress updates every 2 seconds until the done channel is closed.
func monitorClaudeProgress(workDir string, logInfo func(string), done <-chan struct{}) {
	startTime := time.Now()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Track state for change detection
	lastCommit := getGitHead(workDir)
	lastModTimes := make(map[string]time.Time)

	for {
		select {
		case <-done:
			elapsed := time.Since(startTime).Round(time.Second)
			logInfo(fmt.Sprintf("Claude Code completed (%s)", elapsed))
			return
		case <-ticker.C:
			elapsed := time.Since(startTime).Round(time.Second)
			var updates []string

			// Check for new commits
			currentCommit := getGitHead(workDir)
			if currentCommit != lastCommit && currentCommit != "" {
				commitMsg := getGitCommitMessage(workDir, currentCommit)
				if commitMsg != "" {
					updates = append(updates, fmt.Sprintf("committed: %s", commitMsg))
				}
				lastCommit = currentCommit
			}

			// Check for recently modified files (ignore .git directory)
			modifiedFiles := getRecentlyModifiedFiles(workDir, lastModTimes)
			if len(modifiedFiles) > 0 {
				if len(modifiedFiles) <= 3 {
					updates = append(updates, fmt.Sprintf("modified: %s", strings.Join(modifiedFiles, ", ")))
				} else {
					updates = append(updates, fmt.Sprintf("modified: %s (+%d more)", strings.Join(modifiedFiles[:3], ", "), len(modifiedFiles)-3))
				}
			}

			// Log progress if there are updates, or every 30s as a heartbeat
			if len(updates) > 0 {
				logInfo(fmt.Sprintf("Claude Code running (%s): %s", elapsed, strings.Join(updates, "; ")))
			} else if elapsed.Seconds() > 0 && int(elapsed.Seconds())%30 == 0 {
				logInfo(fmt.Sprintf("Claude Code still running (%s)...", elapsed))
			}
		}
	}
}

// getGitHead returns the current HEAD commit hash.
func getGitHead(workDir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = workDir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// getGitCommitMessage returns the first line of a commit message.
func getGitCommitMessage(workDir, commitHash string) string {
	cmd := exec.Command("git", "log", "-1", "--format=%s", commitHash)
	cmd.Dir = workDir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	msg := strings.TrimSpace(string(output))
	if len(msg) > 60 {
		msg = msg[:57] + "..."
	}
	return msg
}

// getRecentlyModifiedFiles returns files modified since last check, updating lastModTimes.
func getRecentlyModifiedFiles(workDir string, lastModTimes map[string]time.Time) []string {
	var modified []string

	// Walk top-level files and one level of subdirectories (skip .git)
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return nil
	}

	checkFile := func(path, name string) {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return
		}
		modTime := info.ModTime()
		if lastTime, exists := lastModTimes[path]; !exists || modTime.After(lastTime) {
			if exists { // Only report if we've seen it before (skip initial scan)
				modified = append(modified, name)
			}
			lastModTimes[path] = modTime
		}
	}

	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		path := fmt.Sprintf("%s/%s", workDir, entry.Name())
		if entry.IsDir() {
			// Check one level deep
			subEntries, err := os.ReadDir(path)
			if err != nil {
				continue
			}
			for _, subEntry := range subEntries {
				if !subEntry.IsDir() {
					subPath := fmt.Sprintf("%s/%s", path, subEntry.Name())
					checkFile(subPath, fmt.Sprintf("%s/%s", entry.Name(), subEntry.Name()))
				}
			}
		} else {
			checkFile(path, entry.Name())
		}
	}

	return modified
}

// platformCredentials maps platform IDs to their required credentials.
var platformCredentials = map[string][]struct {
	Name        string
	Description string
}{
	"cloud-run": {
		{Name: "GCP_PROJECT_ID", Description: "Your Google Cloud project ID"},
		{Name: "GCP_SERVICE_ACCOUNT_KEY", Description: "Service account JSON key with Cloud Run deploy permissions"},
	},
	"fly-io": {
		{Name: "FLY_API_TOKEN", Description: "Fly.io API token (from 'fly tokens create deploy')"},
	},
	"railway": {
		{Name: "RAILWAY_TOKEN", Description: "Railway API token (from railway.app/account/tokens)"},
	},
	"vercel": {
		{Name: "VERCEL_TOKEN", Description: "Vercel API token (from vercel.com/account/tokens)"},
		{Name: "VERCEL_ORG_ID", Description: "Vercel organization ID (optional for personal accounts)"},
	},
	"render": {
		{Name: "RENDER_API_KEY", Description: "Render API key (from dashboard.render.com/account/api-keys)"},
	},
}

// handleSelectHostingPlatform handles the platform selection form submission.
func (s *Server) handleSelectHostingPlatform(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	inputRequestID, err := uuid.Parse(chi.URLParam(r, "inputRequestID"))
	if err != nil {
		http.Error(w, "invalid input request ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	platform := r.FormValue("platform")
	if platform == "" {
		http.Error(w, "platform is required", http.StatusBadRequest)
		return
	}

	// Get the InputRequest
	inputRequest, err := s.db.GetInputRequest(ctx, inputRequestID)
	if err != nil {
		http.Error(w, "input request not found", http.StatusNotFound)
		return
	}

	if inputRequest.Kind != domain.InputRequestKindHostingPlatform {
		http.Error(w, "wrong input request kind", http.StatusBadRequest)
		return
	}

	// Resolve the InputRequest
	now := time.Now()
	resolution := platform
	inputRequest.Status = domain.InputRequestStatusResolved
	inputRequest.Resolution = &resolution
	inputRequest.ResolvedAt = &now
	inputRequest.UpdatedAt = now

	if err := s.db.UpdateInputRequest(ctx, inputRequest); err != nil {
		http.Error(w, "failed to update input request", http.StatusInternalServerError)
		return
	}

	// Create credential InputRequests for the selected platform
	creds, ok := platformCredentials[platform]
	if ok && len(creds) > 0 {
		for _, cred := range creds {
			details := fmt.Sprintf("Provide your %s credential.\n\n%s", cred.Name, cred.Description)
			instructions := fmt.Sprintf("Add this credential to project settings with the name: %s", cred.Name)

			credIR := &domain.InputRequest{
				ID:                   uuid.New(),
				ProjectID:            projectID,
				Kind:                 domain.InputRequestKindCredentialRequest,
				Title:                fmt.Sprintf("Provide %s", cred.Name),
				Details:              &details,
				Instructions:         &instructions,
				RequiredCapabilities: []string{cred.Name},
				ObjectivityScore:     0.9,
				ImportanceScore:      0.9,
				Status:               domain.InputRequestStatusNeedsAssignment,
				CreatedAt:            now,
				UpdatedAt:            now,
			}

			if err := s.db.CreateInputRequest(ctx, credIR); err != nil {
				// Log but continue
				fmt.Printf("Failed to create credential InputRequest: %v\n", err)
			}
		}
	}

	// Store the selected platform and set status to "generating"
	project, err := s.db.GetProject(ctx, projectID)
	if err == nil && project != nil {
		var config map[string]interface{}
		if project.Config != nil {
			json.Unmarshal(project.Config, &config)
		}
		if config == nil {
			config = make(map[string]interface{})
		}
		config["demo_hosting_platform"] = platform
		config["demo_script_status"] = "generating"
		configBytes, _ := json.Marshal(config)
		s.db.UpdateProjectConfig(ctx, projectID, configBytes)
	}

	// Trigger script generation in background - commits to main branch
	go s.generateDemoScriptsForMain(projectID, platform)

	// Redirect to inputs page to show the new credential requests
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs", projectID), http.StatusSeeOther)
}

// updateDemoScriptStatus updates the demo_script_status in project config
func (s *Server) updateDemoScriptStatus(projectID uuid.UUID, status string) {
	ctx := context.Background()
	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		return
	}
	var config map[string]interface{}
	if project.Config != nil {
		json.Unmarshal(project.Config, &config)
	}
	if config == nil {
		config = make(map[string]interface{})
	}
	config["demo_script_status"] = status
	configBytes, _ := json.Marshal(config)
	s.db.UpdateProjectConfig(ctx, projectID, configBytes)
}

// generateDemoScriptsForMain generates demo-hosting.yml and deployment scripts,
// committing them to the main branch so all future variations inherit them.
func (s *Server) generateDemoScriptsForMain(projectID uuid.UUID, platform string) {
	ctx := context.Background()

	// Get repository info
	repo, err := s.db.GetRepositoryByProject(ctx, projectID)
	if err != nil || repo.URL == nil {
		fmt.Printf("[demo-scripts] Repository not found for project %s\n", projectID)
		s.updateDemoScriptStatus(projectID, "failed")
		return
	}

	var repoConfig struct {
		AuthToken string `json:"auth_token"`
	}
	if repo.Config != nil {
		json.Unmarshal(repo.Config, &repoConfig)
	}

	// Get API key - check project config first, then env
	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		fmt.Printf("[demo-scripts] Failed to get project: %v\n", err)
		s.updateDemoScriptStatus(projectID, "failed")
		return
	}
	var projectConfig struct {
		AnthropicAPIKey string `json:"anthropic_api_key"`
	}
	if project.Config != nil {
		json.Unmarshal(project.Config, &projectConfig)
	}
	apiKey := projectConfig.AnthropicAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		fmt.Printf("[demo-scripts] No Anthropic API key available\n")
		s.updateDemoScriptStatus(projectID, "failed")
		return
	}

	// Create a temporary work directory for main branch
	workDir := fmt.Sprintf("/work/%s/demo-setup", projectID)
	os.MkdirAll(workDir, 0755)
	defer os.RemoveAll(workDir) // Cleanup after

	// Clone main branch
	gitClient := git.NewClient(workDir)
	if err := gitClient.Clone(ctx, *repo.URL, "main", repoConfig.AuthToken); err != nil {
		fmt.Printf("[demo-scripts] Failed to clone main branch: %v\n", err)
		s.updateDemoScriptStatus(projectID, "failed")
		return
	}

	// Check if demo-hosting.yml exists AND has all required fields
	hostingConfigPath := workDir + "/.mendel/demo-hosting.yml"
	if _, err := os.Stat(hostingConfigPath); err == nil {
		// File exists - check if it has all required fields
		if cfg, err := demo.LoadHostingConfig(workDir); err == nil && cfg != nil && cfg.DeployerImage != "" {
			fmt.Printf("[demo-scripts] demo-hosting.yml already exists and is valid, skipping generation\n")
			s.updateDemoScriptStatus(projectID, "ready")
			return
		}
		// File exists but is missing required fields - will regenerate
		fmt.Printf("[demo-scripts] demo-hosting.yml exists but is missing required fields, regenerating\n")
	}

	// Build the prompt
	prompt := buildDemoScriptPrompt(platform)

	fmt.Printf("[demo-scripts] Generating scripts for platform %s...\n", platform)

	// Run executor (same mechanism as code generation)
	exec := executor.New(apiKey, workDir).
		WithEventHandler(func(event executor.Event) {
			switch event.Type {
			case executor.EventToolCall:
				if event.ToolName == "Write" {
					if path, ok := event.ToolInput["file_path"].(string); ok {
						fmt.Printf("[demo-scripts] Writing: %s\n", path)
					}
				}
			case executor.EventComplete:
				fmt.Printf("[demo-scripts] Generation complete\n")
			}
		})

	result, err := exec.Run(ctx, executor.SystemPrompt(), prompt)
	if err != nil {
		fmt.Printf("[demo-scripts] Executor error: %v\n", err)
		s.updateDemoScriptStatus(projectID, "failed")
		return
	}

	if !result.Success {
		fmt.Printf("[demo-scripts] Generation failed: %v\n", result.Error)
		s.updateDemoScriptStatus(projectID, "failed")
		return
	}

	// Verify the files were created
	if _, err := os.Stat(hostingConfigPath); os.IsNotExist(err) {
		fmt.Printf("[demo-scripts] demo-hosting.yml was not created\n")
		s.updateDemoScriptStatus(projectID, "failed")
		return
	}

	// Commit and push
	if err := gitClient.CommitAll(ctx, fmt.Sprintf("Add demo hosting configuration for %s", platform)); err != nil {
		fmt.Printf("[demo-scripts] Failed to commit: %v\n", err)
		s.updateDemoScriptStatus(projectID, "failed")
		return
	}

	if err := gitClient.Push(ctx, repoConfig.AuthToken); err != nil {
		fmt.Printf("[demo-scripts] Failed to push: %v\n", err)
		s.updateDemoScriptStatus(projectID, "failed")
		return
	}

	fmt.Printf("[demo-scripts] Successfully committed demo scripts to main branch\n")
	s.updateDemoScriptStatus(projectID, "ready")
}

// handleRegenerateDemoScripts forces regeneration of demo scripts for the configured platform.
func (s *Server) handleRegenerateDemoScripts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	// Get the configured platform from project config
	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	var platform string
	if project.Config != nil {
		var cfg map[string]interface{}
		if json.Unmarshal(project.Config, &cfg) == nil {
			if p, ok := cfg["demo_hosting_platform"].(string); ok {
				platform = p
			}
		}
	}

	if platform == "" {
		http.Redirect(w, r, fmt.Sprintf("/p/%s/settings?error=no_platform", projectID), http.StatusSeeOther)
		return
	}

	// Set status to generating and trigger regeneration
	s.updateDemoScriptStatus(projectID, "generating")
	go s.generateDemoScriptsForMain(projectID, platform)

	http.Redirect(w, r, fmt.Sprintf("/p/%s/settings?regenerating=1", projectID), http.StatusSeeOther)
}

// handleConfigureDemoHosting creates an InputRequest for selecting a demo hosting platform.
// AI will suggest options based on project context, and user picks one.
func (s *Server) handleConfigureDemoHosting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	// Check if there's already a pending hosting platform InputRequest
	existingIRs, _ := s.db.GetInputRequestsByProject(ctx, projectID)
	for _, ir := range existingIRs {
		if ir.Kind == domain.InputRequestKindHostingPlatform &&
			ir.Status != domain.InputRequestStatusResolved {
			// Already have a pending one - redirect to it
			http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, ir.ID), http.StatusSeeOther)
			return
		}
	}

	// Get project context for AI to suggest appropriate platforms
	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	// Build context for the InputRequest
	var details strings.Builder
	details.WriteString("Select a hosting platform for running demos of this project.\n\n")
	details.WriteString("**Project:** " + project.Name + "\n")
	details.WriteString("\nMendel will generate deployment scripts for the selected platform ")
	details.WriteString("and prompt you for any required credentials (API keys, project IDs, etc.).")

	detailsStr := details.String()

	// Create the InputRequest
	ir := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        projectID,
		Kind:             domain.InputRequestKindHostingPlatform,
		Title:            "Select Demo Hosting Platform",
		Details:          &detailsStr,
		ObjectivityScore: 0.7, // Somewhat objective - depends on project needs
		ImportanceScore:  0.8, // Important for enabling demos
		Status:           domain.InputRequestStatusNeedsAssignment,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if err := s.db.CreateInputRequest(ctx, ir); err != nil {
		http.Error(w, "failed to create input request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to the InputRequest page
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs/%s", projectID, ir.ID), http.StatusSeeOther)
}

// platformConfig holds configuration for each hosting platform
type platformConfig struct {
	deployerImage string
	instructions  string
}

// buildDemoScriptPrompt creates a prompt for Claude Code to generate demo deployment scripts.
func buildDemoScriptPrompt(platform string) string {
	platforms := map[string]platformConfig{
		"cloud-run": {
			deployerImage: "google/cloud-sdk:slim",
			instructions: `Google Cloud Run deployment:
- Use 'gcloud run deploy' command
- Required env vars: GCP_PROJECT_ID, GCP_SERVICE_ACCOUNT_KEY (JSON)
- Deploy script should authenticate with service account key, build and push to gcr.io, deploy to Cloud Run
- Teardown script should delete the Cloud Run service`,
		},

		"fly-io": {
			deployerImage: "flyio/flyctl:latest",
			instructions: `Fly.io deployment:
- Use 'flyctl' CLI commands (available as 'flyctl' in the container)
- Required env var: FLY_API_TOKEN
- Deploy script should create app if needed, deploy using fly.toml or Dockerfile
- Teardown script should destroy the app`,
		},

		"railway": {
			deployerImage: "node:20-slim",
			instructions: `Railway deployment:
- Use Railway CLI (install with: npm install -g @railway/cli)
- Required env var: RAILWAY_TOKEN
- Deploy script should link project and deploy
- Teardown script should remove the deployment`,
		},

		"vercel": {
			deployerImage: "node:20-slim",
			instructions: `Vercel deployment:
- Use 'vercel' CLI (install with: npm install -g vercel)
- Required env vars: VERCEL_TOKEN, optionally VERCEL_ORG_ID
- Deploy script should deploy using vercel CLI
- Teardown script should remove the deployment`,
		},

		"render": {
			deployerImage: "curlimages/curl:latest",
			instructions: `Render deployment:
- Use Render API with curl (render.com/docs/api)
- Required env var: RENDER_API_KEY
- Deploy script should create service via API and trigger deploy
- Teardown script should delete the service`,
		},
	}

	cfg, ok := platforms[platform]
	if !ok {
		cfg = platformConfig{
			deployerImage: "alpine:latest",
			instructions:  fmt.Sprintf("Generic deployment for platform: %s", platform),
		}
	}

	return fmt.Sprintf(`Generate demo deployment scripts for this project.

Look at the existing Dockerfile, docker-compose files, and project structure to understand how to build and run this application.

Create these files in the .mendel/ directory:

1. demo-hosting.yml - Configuration file with this EXACT structure:
   version: 1
   deployer_image: %s
   required_secrets:
     - <list of env var names needed, based on platform>
   deploy_script: deploy.sh
   teardown_script: teardown.sh
   url_from: stdout

   NOTE: deployer_image specifies the Docker image used to run the scripts.
   This image has the necessary CLI tools pre-installed.

2. deploy.sh - Deployment script that:
   - Uses secrets from environment variables
   - Builds and deploys the application
   - Prints the deployed URL to stdout (CRITICAL - this is how Mendel captures the URL)
   - Has MENDEL_VARIATION_ID env var available for naming resources
   - The script runs inside the deployer container with /workspace mounted

3. teardown.sh - Teardown script that:
   - Cleans up all deployed resources
   - Uses same env vars as deploy.sh
   - Has MENDEL_VARIATION_ID env var available

Platform: %s

%s

Make the scripts executable (chmod +x).
Print ONLY the URL on success (e.g., echo "https://my-app-xyz.run.app").
Handle errors gracefully with clear error messages.
Make scripts idempotent where possible.`, cfg.deployerImage, platform, cfg.instructions)
}
