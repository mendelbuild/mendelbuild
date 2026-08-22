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

// generateMissingDockerPrompt creates a prompt for setting up Docker configuration from scratch.
func generateMissingDockerPrompt() string {
	return `The project needs Docker configuration for demos. Create two files in the .mendel/ directory.

## Requirements

### .mendel/docker-compose.demo.yml

This file must:
1. Define a service that runs the application
2. Include any dependencies (database, cache, etc.) the app needs
3. Use healthchecks so "docker-compose up --wait" knows when services are ready
4. Expose the app's port WITHOUT a fixed host mapping (e.g., "3000" not "3000:3000") — Mendel assigns the host port dynamically

If the project already has a docker-compose.yml or Dockerfile, use/reference those. If not, create what's needed.

Services should communicate via Docker networking (e.g., db:5432, not localhost:5432).

### .mendel/demo-config.yml

This file tells Mendel which service to expose:

` + "```yaml" + `
version: 1
service: <name of the docker-compose service to expose>
container_port: <port the app listens on inside the container>
health_path: <HTTP path to check, e.g., "/", "/health", "/api/health">

# Optional: commands to run after containers start (migrations, seed data)
after_up:
  - "docker-compose exec -T <service> <command>"
` + "```" + `

## Instructions

1. Examine the project to understand its stack (look at package.json, requirements.txt, go.mod, Dockerfile, existing docker-compose.yml, etc.)
2. Create .mendel/docker-compose.demo.yml appropriate for this specific project
3. Create .mendel/demo-config.yml pointing to the right service and port
4. Commit the changes`
}

// generateFixPrompt creates a well-contextualized prompt for Claude Code to fix a Docker-based dev environment issue.
func generateFixPrompt(errMsg string) string {
	return fmt.Sprintf(`I'm trying to run the local development environment using Docker, but encountered an error.

## Error
%s

## Context
The demo runs via Docker Compose from the .mendel/ directory:
- .mendel/docker-compose.demo.yml defines all services (app, database, etc.)
- .mendel/demo-config.yml specifies which service to expose and any setup scripts

## What to check
1. Does .mendel/docker-compose.demo.yml exist and define all needed services?
2. Are service health checks configured correctly?
3. Do after_up scripts in .mendel/demo-config.yml work? (migrations, seed data)
4. Are environment variables and connection strings correct for Docker networking?
   - Services communicate via service names (e.g., db:5432, not localhost:5432)

## What to do
1. Diagnose the root cause from the error message
2. Fix the Docker Compose or demo-config.yml configuration
3. If the main app needs a Dockerfile, create/update it
4. Make sure the fix is committed to this branch

The goal is a working Docker-based local dev environment.`, errMsg)
}

// handleStartDemo starts a demo instance for a variation using Docker.
// Uses .mendel/docker-compose.demo.yml and .mendel/demo-config.yml for configuration.
// The actual startup runs in a background goroutine with output logged to variation_logs.
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

	// Helper to handle failures - tears down containers before recording error
	failDemo := func(errMsg string) {
		logError(errMsg)
		logInfo("Tearing down containers...")
		if output, err := demo.DockerComposeDown(workDir, true); err != nil {
			logInfo(fmt.Sprintf("Teardown warning: %v\n%s", err, output))
		}
		suggestedFix := generateFixPrompt(errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, suggestedFix)
	}

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
	if hostingCfg, err := demo.LoadHostingConfig(workDir); err == nil && hostingCfg != nil {
		s.runCloudDemoDeployment(ctx, projectID, variationID, demoInstanceID, workDir, hostingCfg, logMilestone, logInfo, logError, failDemo)
		return
	}

	// Check for .mendel/docker-compose.demo.yml (local Docker demo)
	if !demo.HasDockerCompose(workDir) {
		failDemoWithFix(
			"Docker configuration not found: .mendel/docker-compose.demo.yml",
			generateMissingDockerPrompt(),
		)
		return
	}

	// Load demo config from .mendel/demo-config.yml
	cfg, err := demo.LoadConfig(workDir)
	if err != nil {
		failDemoWithFix(
			fmt.Sprintf("Failed to load demo config: %v", err),
			generateFixPrompt(fmt.Sprintf("Demo config error: %v", err)),
		)
		return
	}

	logMilestone("Starting Docker containers...")

	// Run docker-compose up
	logInfo("Running: docker-compose up -d --build --wait")
	output, err := demo.DockerComposeUp(workDir)
	if output != "" {
		logInfo(truncateOutput(output, 6000))
	}
	if err != nil {
		failDemo(fmt.Sprintf("docker-compose up failed: %v\n\nOutput tail:\n%s", err, lastNChars(output, 2000)))
		return
	}
	logMilestone("Containers started")

	// Run after_up scripts (migrations, seed data, etc.)
	for i, script := range cfg.AfterUp {
		logInfo(fmt.Sprintf("Running after_up [%d/%d]: %s", i+1, len(cfg.AfterUp), script))
		output, err := demo.RunScript(workDir, script)
		if output != "" {
			logInfo(truncateOutput(output, 2000))
		}
		if err != nil {
			failDemo(fmt.Sprintf("after_up script failed: %s\n\nError: %v", script, err))
			return
		}
	}
	if len(cfg.AfterUp) > 0 {
		logMilestone("Setup scripts complete")
	}

	// Get the exposed port for the service
	logInfo(fmt.Sprintf("Getting port for service '%s'...", cfg.Service))
	port, err := demo.GetServicePort(workDir, cfg.Service, cfg.ContainerPort)
	if err != nil {
		failDemo(fmt.Sprintf("Failed to get service port: %v", err))
		return
	}
	demoURL := fmt.Sprintf("http://localhost:%d", port)
	logInfo(fmt.Sprintf("Service available at %s", demoURL))

	// Wait for health check
	healthURL := fmt.Sprintf("http://localhost:%d%s", port, cfg.HealthPath)
	logInfo(fmt.Sprintf("Waiting for health check: %s", healthURL))

	healthy := false
	deadline := time.Now().Add(time.Duration(cfg.HealthTimeout) * time.Second)
	attempts := 0
	for time.Now().Before(deadline) {
		attempts++
		resp, err := http.Get(healthURL)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 400 {
			resp.Body.Close()
			healthy = true
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		if attempts%5 == 0 {
			logInfo(fmt.Sprintf("Health check attempt %d...", attempts))
		}
		time.Sleep(time.Duration(cfg.HealthInterval) * time.Second)
	}

	if !healthy {
		failDemo(fmt.Sprintf("Health check failed after %d seconds (%d attempts)", cfg.HealthTimeout, attempts))
		return
	}

	logMilestone(fmt.Sprintf("Demo running at %s", demoURL))

	// Update demo instance to running status
	processInfo, _ := json.Marshal(map[string]interface{}{
		"work_dir": workDir,
		"service":  cfg.Service,
		"port":     port,
	})

	s.db.Pool.Exec(ctx, `
		UPDATE demo_instances
		SET url = $2, teardown_instructions = $3, status = $4, process_info = $5
		WHERE id = $1
	`, demoInstanceID, demoURL, fmt.Sprintf("cd %s/.mendel && docker-compose -f docker-compose.demo.yml down -v", workDir), domain.DemoInstanceStatusRunning, processInfo)
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

	for _, c := range creds {
		// Check if this credential is required
		isRequired := false
		for _, secretName := range hostingCfg.RequiredSecrets {
			if c.Name == secretName {
				isRequired = true
				break
			}
		}
		if !isRequired {
			continue
		}

		// Decrypt the credential
		key, err := crypto.GetKey()
		if err != nil {
			failDemo("Encryption not configured: " + err.Error())
			return
		}

		decrypted, err := crypto.Decrypt(c.EncryptedValue, key)
		if err != nil {
			failDemo(fmt.Sprintf("Failed to decrypt %s: %v", c.Name, err))
			return
		}

		env = append(env, fmt.Sprintf("%s=%s", c.Name, string(decrypted)))
	}

	// Run the deploy script
	deployScriptPath := workDir + "/.mendel/" + hostingCfg.DeployScript
	if _, err := os.Stat(deployScriptPath); os.IsNotExist(err) {
		failDemo(fmt.Sprintf("Deploy script not found: %s", hostingCfg.DeployScript))
		return
	}

	logInfo(fmt.Sprintf("Running deploy script: %s", hostingCfg.DeployScript))

	cmd := exec.CommandContext(ctx, "bash", deployScriptPath)
	cmd.Dir = workDir + "/.mendel"
	cmd.Env = env

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

	logMilestone("Applying fix via Claude Code...")
	logInfo(fmt.Sprintf("Prompt: %s", fixPrompt))

	// Run Claude Code with the fix prompt
	// --print: non-interactive output mode
	// --dangerously-skip-permissions: allow file writes without approval
	cmd := exec.CommandContext(ctx, "claude", "--print", "--dangerously-skip-permissions", fixPrompt)
	cmd.Dir = workDir

	// Start progress monitor in background
	done := make(chan struct{})
	go monitorClaudeProgress(workDir, logInfo, done)

	output, err := cmd.CombinedOutput()
	close(done) // Stop the monitor

	if len(output) > 0 {
		// Log output in chunks if very long
		outStr := string(output)
		if len(outStr) > 4000 {
			outStr = outStr[:4000] + "\n... (truncated)"
		}
		logInfo(outStr)
	}

	if err != nil {
		errMsg := fmt.Sprintf("Claude Code failed: %v", err)
		logError(errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, fixPrompt)
		return
	}

	logMilestone("Fix applied, starting demo...")

	// Check for docker-compose
	if !demo.HasDockerCompose(workDir) {
		errMsg := "Docker configuration not found: .mendel/docker-compose.demo.yml"
		logError(errMsg)
		missingDockerPrompt := `The project needs Docker configuration for demos.

Create .mendel/docker-compose.demo.yml that defines all services needed to run the application. Example:

` + "```yaml" + `
# .mendel/docker-compose.demo.yml

# Include the project's existing docker-compose if it has one
# include:
#   - path: ../docker-compose.yml
#     project_directory: ..

services:
  web:
    build:
      context: ..
      dockerfile: Dockerfile
    ports:
      - "3000"  # Mendel will allocate a host port dynamically
    environment:
      - DATABASE_URL=postgresql://dev:dev@db:5432/app
    depends_on:
      db:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:3000/health"]
      interval: 2s
      timeout: 5s
      retries: 30

  db:
    image: postgres:15
    environment:
      POSTGRES_DB: app
      POSTGRES_USER: dev
      POSTGRES_PASSWORD: dev
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U dev -d app"]
      interval: 2s
      timeout: 5s
      retries: 30
` + "```" + `

Also create .mendel/demo-config.yml:

` + "```yaml" + `
version: 1
service: web
container_port: 3000
health_path: /health
after_up:
  - "docker-compose exec -T web npm run db:migrate"
  - "docker-compose exec -T web npm run db:seed"
` + "```" + `

Adapt to match the project's actual stack.`
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, missingDockerPrompt)
		return
	}

	// Load demo config
	cfg, err := demo.LoadConfig(workDir)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to load demo config: %v", err)
		logError(errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, generateFixPrompt(errMsg))
		return
	}

	// Continue with Docker-based demo startup
	s.continueDemoStartup(ctx, projectID, variationID, demoInstanceID, workDir, cfg)
}

// continueDemoStartup continues the Docker-based demo startup after a fix has been applied.
func (s *Server) continueDemoStartup(ctx context.Context, projectID string, variationID, demoInstanceID uuid.UUID, workDir string, cfg *demo.Config) {
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

	// Helper to handle failures - tears down containers before recording error
	failDemo := func(errMsg string) {
		logError(errMsg)
		logInfo("Tearing down containers...")
		if output, err := demo.DockerComposeDown(workDir, true); err != nil {
			logInfo(fmt.Sprintf("Teardown warning: %v\n%s", err, output))
		}
		suggestedFix := generateFixPrompt(errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, suggestedFix)
	}

	logMilestone("Starting Docker containers...")

	// Run docker-compose up
	logInfo("Running: docker-compose up -d --build --wait")
	output, err := demo.DockerComposeUp(workDir)
	if output != "" {
		logInfo(truncateOutput(output, 6000))
	}
	if err != nil {
		failDemo(fmt.Sprintf("docker-compose up failed: %v\n\nOutput tail:\n%s", err, lastNChars(output, 2000)))
		return
	}
	logMilestone("Containers started")

	// Run after_up scripts (migrations, seed data, etc.)
	for i, script := range cfg.AfterUp {
		logInfo(fmt.Sprintf("Running after_up [%d/%d]: %s", i+1, len(cfg.AfterUp), script))
		output, err := demo.RunScript(workDir, script)
		if output != "" {
			logInfo(truncateOutput(output, 2000))
		}
		if err != nil {
			failDemo(fmt.Sprintf("after_up script failed: %s\n\nError: %v", script, err))
			return
		}
	}
	if len(cfg.AfterUp) > 0 {
		logMilestone("Setup scripts complete")
	}

	// Get the exposed port for the service
	logInfo(fmt.Sprintf("Getting port for service '%s'...", cfg.Service))
	port, err := demo.GetServicePort(workDir, cfg.Service, cfg.ContainerPort)
	if err != nil {
		failDemo(fmt.Sprintf("Failed to get service port: %v", err))
		return
	}
	demoURL := fmt.Sprintf("http://localhost:%d", port)
	logInfo(fmt.Sprintf("Service available at %s", demoURL))

	// Wait for health check
	healthURL := fmt.Sprintf("http://localhost:%d%s", port, cfg.HealthPath)
	logInfo(fmt.Sprintf("Waiting for health check: %s", healthURL))

	healthy := false
	deadline := time.Now().Add(time.Duration(cfg.HealthTimeout) * time.Second)
	attempts := 0
	for time.Now().Before(deadline) {
		attempts++
		resp, err := http.Get(healthURL)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 400 {
			resp.Body.Close()
			healthy = true
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		if attempts%5 == 0 {
			logInfo(fmt.Sprintf("Health check attempt %d...", attempts))
		}
		time.Sleep(time.Duration(cfg.HealthInterval) * time.Second)
	}

	if !healthy {
		failDemo(fmt.Sprintf("Health check failed after %d seconds (%d attempts)", cfg.HealthTimeout, attempts))
		return
	}

	logMilestone(fmt.Sprintf("Demo running at %s", demoURL))

	// Update demo instance to running status
	processInfo, _ := json.Marshal(map[string]interface{}{
		"work_dir": workDir,
		"service":  cfg.Service,
		"port":     port,
	})

	teardownCmd := fmt.Sprintf("cd %s/.mendel && docker-compose -f docker-compose.demo.yml down -v", workDir)
	s.db.Pool.Exec(ctx, `
		UPDATE demo_instances
		SET url = $2, teardown_instructions = $3, status = $4, process_info = $5
		WHERE id = $1
	`, demoInstanceID, demoURL, teardownCmd, domain.DemoInstanceStatusRunning, processInfo)
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

	// Store the selected platform in project config for later use
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
		configBytes, _ := json.Marshal(config)
		s.db.UpdateProjectConfig(ctx, projectID, configBytes)
	}

	// Redirect to inputs page to show the new credential requests
	http.Redirect(w, r, fmt.Sprintf("/p/%s/inputs", projectID), http.StatusSeeOther)
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

// handleGenerateDemoScripts uses AI to generate demo-hosting.yml and deployment scripts.
func (s *Server) handleGenerateDemoScripts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	// Get selected platform from project config
	project, err := s.db.GetProject(ctx, projectUUID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	var cfg map[string]interface{}
	if project.Config != nil {
		json.Unmarshal(project.Config, &cfg)
	}
	platform, _ := cfg["demo_hosting_platform"].(string)
	if platform == "" {
		http.Error(w, "no hosting platform selected", http.StatusBadRequest)
		return
	}

	// Get work directory
	workDir := git.WorkDirForVariation(projectID, variationID.String())

	// Create a demo instance to track progress (reusing the demo log system)
	demoInstanceID := uuid.New()
	processInfo, _ := json.Marshal(map[string]interface{}{
		"work_dir": workDir,
		"phase":    "generating_scripts",
	})

	demoInstance := &domain.DemoInstance{
		ID:          demoInstanceID,
		VariationID: variationID,
		Status:      domain.DemoInstanceStatusStarting,
		ProcessInfo: processInfo,
	}

	if err := s.db.CreateDemoInstance(ctx, demoInstance); err != nil {
		http.Error(w, "failed to create demo instance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Run script generation in background
	go s.runDemoScriptGeneration(ctx, projectID, variationID, demoInstanceID, workDir, platform)

	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// runDemoScriptGeneration generates demo-hosting.yml and deployment scripts using Claude Code.
func (s *Server) runDemoScriptGeneration(ctx context.Context, projectID string, variationID uuid.UUID, demoInstanceID uuid.UUID, workDir string, platform string) {
	// Helper to log progress
	logMilestone := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelMilestone, msg, domain.SourceTypeDemo, &demoInstanceID)
	}

	logInfo := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelInfo, msg, domain.SourceTypeDemo, &demoInstanceID)
	}

	logError := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelError, msg, domain.SourceTypeDemo, &demoInstanceID)
	}

	logMilestone(fmt.Sprintf("Generating demo scripts for %s platform...", platform))

	// Build the prompt for Claude Code
	prompt := buildDemoScriptPrompt(platform)

	logInfo("Running Claude Code to generate deployment scripts...")

	// Run Claude Code
	cmd := exec.CommandContext(ctx, "claude", "--print", "--dangerously-skip-permissions", prompt)
	cmd.Dir = workDir

	done := make(chan struct{})
	go monitorClaudeProgress(workDir, logInfo, done)

	output, err := cmd.CombinedOutput()
	close(done)

	if err != nil {
		errMsg := fmt.Sprintf("Claude Code failed: %v", err)
		if len(output) > 0 {
			errMsg += "\n" + string(output)
		}
		logError(errMsg)
		s.db.Pool.Exec(ctx, `
			UPDATE demo_instances SET status = $2, error_message = $3 WHERE id = $1
		`, demoInstanceID, domain.DemoInstanceStatusError, errMsg)
		return
	}

	logMilestone("Demo scripts generated successfully")

	// Verify the files were created
	hostingConfigPath := workDir + "/.mendel/demo-hosting.yml"
	if _, err := os.Stat(hostingConfigPath); os.IsNotExist(err) {
		errMsg := "demo-hosting.yml was not created"
		logError(errMsg)
		s.db.Pool.Exec(ctx, `
			UPDATE demo_instances SET status = $2, error_message = $3 WHERE id = $1
		`, demoInstanceID, domain.DemoInstanceStatusError, errMsg)
		return
	}

	// Get repo info for pushing
	projectUUID, _ := uuid.Parse(projectID)
	repo, err := s.db.GetRepositoryByProject(ctx, projectUUID)
	if err != nil || repo.URL == nil {
		logError("Repository not configured - cannot push scripts")
		s.db.Pool.Exec(ctx, `
			UPDATE demo_instances SET status = $2, error_message = $3 WHERE id = $1
		`, demoInstanceID, domain.DemoInstanceStatusError, "Repository not configured")
		return
	}

	var repoConfig struct {
		AuthToken string `json:"auth_token"`
	}
	if repo.Config != nil {
		json.Unmarshal(repo.Config, &repoConfig)
	}

	// Commit and push the scripts
	logInfo("Committing demo scripts...")
	gitClient := git.NewClient(workDir)
	if err := gitClient.CommitAll(ctx, "Add demo deployment scripts for "+platform); err != nil {
		logError("Failed to commit scripts: " + err.Error())
		s.db.Pool.Exec(ctx, `
			UPDATE demo_instances SET status = $2, error_message = $3 WHERE id = $1
		`, demoInstanceID, domain.DemoInstanceStatusError, "Failed to commit: "+err.Error())
		return
	}

	// Get current branch and push
	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		logError("Failed to get variation: " + err.Error())
		return
	}

	hop, err := s.db.GetHop(ctx, variation.HopID)
	if err != nil {
		logError("Failed to get hop: " + err.Error())
		return
	}

	branchName := fmt.Sprintf("mendel/%s/%s", sanitizeBranchName(hop.Name), sanitizeBranchName(variation.Name))
	logInfo(fmt.Sprintf("Pushing to branch %s...", branchName))

	if err := gitClient.Push(ctx, repoConfig.AuthToken); err != nil {
		logError("Failed to push scripts: " + err.Error())
		s.db.Pool.Exec(ctx, `
			UPDATE demo_instances SET status = $2, error_message = $3 WHERE id = $1
		`, demoInstanceID, domain.DemoInstanceStatusError, "Failed to push: "+err.Error())
		return
	}

	logMilestone("Demo scripts committed and pushed")

	// Mark as stopped (ready for user to start demo)
	s.db.Pool.Exec(ctx, `
		UPDATE demo_instances SET status = $2 WHERE id = $1
	`, demoInstanceID, domain.DemoInstanceStatusStopped)
}

// buildDemoScriptPrompt creates a prompt for Claude Code to generate demo deployment scripts.
func buildDemoScriptPrompt(platform string) string {
	platformInstructions := map[string]string{
		"cloud-run": `Google Cloud Run deployment:
- Use 'gcloud run deploy' command
- Required env vars: GCP_PROJECT_ID, GCP_SERVICE_ACCOUNT_KEY (JSON)
- Deploy script should authenticate with service account key, build and push to gcr.io, deploy to Cloud Run
- Teardown script should delete the Cloud Run service`,

		"fly-io": `Fly.io deployment:
- Use 'flyctl' CLI commands
- Required env var: FLY_API_TOKEN
- Deploy script should create app if needed, deploy using fly.toml or Dockerfile
- Teardown script should destroy the app`,

		"railway": `Railway deployment:
- Use Railway CLI or API
- Required env var: RAILWAY_TOKEN
- Deploy script should link project and deploy
- Teardown script should remove the deployment`,

		"vercel": `Vercel deployment:
- Use 'vercel' CLI
- Required env vars: VERCEL_TOKEN, optionally VERCEL_ORG_ID
- Deploy script should deploy using vercel CLI
- Teardown script should remove the deployment`,

		"render": `Render deployment:
- Use Render API (render.com/docs/api)
- Required env var: RENDER_API_KEY
- Deploy script should create service via API and trigger deploy
- Teardown script should delete the service`,
	}

	instructions, ok := platformInstructions[platform]
	if !ok {
		instructions = fmt.Sprintf("Generic deployment for platform: %s", platform)
	}

	return fmt.Sprintf(`Generate demo deployment scripts for this project.

Look at the existing Dockerfile, docker-compose files, and project structure to understand how to build and run this application.

Create these files in the .mendel/ directory:

1. demo-hosting.yml - Configuration file with this structure:
   version: 1
   required_secrets:
     - <list of env var names needed, based on platform>
   deploy_script: deploy.sh
   teardown_script: teardown.sh
   url_from: stdout

2. deploy.sh - Deployment script that:
   - Uses secrets from environment variables
   - Builds and deploys the application
   - Prints the deployed URL to stdout (CRITICAL - this is how Mendel captures the URL)
   - Has MENDEL_VARIATION_ID env var available for naming resources

3. teardown.sh - Teardown script that:
   - Cleans up all deployed resources
   - Uses same env vars as deploy.sh
   - Has MENDEL_VARIATION_ID env var available

Platform: %s

%s

Make the scripts executable (chmod +x).
Print ONLY the URL on success (e.g., echo "https://my-app-xyz.run.app").
Handle errors gracefully with clear error messages.
Make scripts idempotent where possible.

Commit the files when done.`, platform, instructions)
}
