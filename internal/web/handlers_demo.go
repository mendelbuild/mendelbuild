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

// generateFixPrompt creates a well-contextualized prompt for Claude Code to fix a Docker-based dev environment issue.
func generateFixPrompt(errMsg string) string {
	return fmt.Sprintf(`I'm trying to run the local development environment using Docker, but encountered an error.

## Error
%s

## Context
The demo runs via Docker Compose from the .mendel/ directory:
- .mendel/docker-compose.yml defines all services (app, database, etc.)
- .mendel/demo.yaml specifies which service to expose and any setup scripts

## What to check
1. Does .mendel/docker-compose.yml exist and define all needed services?
2. Are service health checks configured correctly?
3. Do after_up scripts in .mendel/demo.yaml work? (migrations, seed data)
4. Are environment variables and connection strings correct for Docker networking?
   - Services communicate via service names (e.g., db:5432, not localhost:5432)

## What to do
1. Diagnose the root cause from the error message
2. Fix the Docker Compose or demo.yaml configuration
3. If the main app needs a Dockerfile, create/update it
4. Make sure the fix is committed to this branch

The goal is a working Docker-based local dev environment.`, errMsg)
}

// handleStartDemo starts a demo instance for a variation using Docker.
// Uses .mendel/docker-compose.yml and .mendel/demo.yaml for configuration.
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

	// Check if work directory exists
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		http.Error(w, "variation work directory not found - code may not be generated yet", http.StatusBadRequest)
		return
	}

	// Check for .mendel/docker-compose.yml
	if !demo.HasDockerCompose(workDir) {
		http.Error(w, "demo not configured: .mendel/docker-compose.yml not found", http.StatusBadRequest)
		return
	}

	// Load demo config from .mendel/demo.yaml
	cfg, err := demo.LoadConfig(workDir)
	if err != nil {
		http.Error(w, "demo config error: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Create demo instance with "starting" status immediately
	demoInstanceID := uuid.New()

	processInfo, _ := json.Marshal(map[string]interface{}{
		"work_dir": workDir,
		"service":  cfg.Service,
	})

	demoInstance := &domain.DemoInstance{
		ID:                   demoInstanceID,
		VariationID:          variationID,
		URL:                  "", // Will be set once we know the port
		TeardownInstructions: fmt.Sprintf("cd %s/.mendel && docker-compose down -v", workDir),
		Status:               domain.DemoInstanceStatusStarting,
		ProcessInfo:          processInfo,
	}

	if err := s.db.CreateDemoInstance(ctx, demoInstance); err != nil {
		http.Error(w, "failed to create demo instance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Start the demo in a background goroutine
	go s.runDemoStartup(projectID, variationID, demoInstanceID, workDir, cfg)

	// Redirect immediately - user will see "starting" status
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// runDemoStartup runs the Docker-based demo startup process in the background.
func (s *Server) runDemoStartup(projectID string, variationID, demoInstanceID uuid.UUID, workDir string, cfg *demo.Config) {
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

	s.db.Pool.Exec(ctx, `
		UPDATE demo_instances
		SET url = $2, teardown_instructions = $3, status = $4, process_info = $5
		WHERE id = $1
	`, demoInstanceID, demoURL, fmt.Sprintf("cd %s/.mendel && docker-compose down -v", workDir), domain.DemoInstanceStatusRunning, processInfo)
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
		http.Error(w, "variation work directory not found", http.StatusBadRequest)
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

	output, err := cmd.CombinedOutput()
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
		errMsg := "Docker configuration not found: .mendel/docker-compose.yml"
		logError(errMsg)
		missingDockerPrompt := `The project needs Docker configuration for demos.

Create .mendel/docker-compose.yml that defines all services needed to run the application. Example:

` + "```yaml" + `
# .mendel/docker-compose.yml

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

Also create .mendel/demo.yaml:

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

	teardownCmd := fmt.Sprintf("cd %s/.mendel && docker-compose down -v", workDir)
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
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		http.Error(w, "variation work directory not found", http.StatusBadRequest)
		return
	}

	// Stop any existing containers first (ignore errors - may not be running)
	demo.DockerComposeDown(workDir, true)

	// Check for docker-compose
	if !demo.HasDockerCompose(workDir) {
		http.Error(w, "demo not configured: .mendel/docker-compose.yml not found", http.StatusBadRequest)
		return
	}

	// Load demo config
	cfg, err := demo.LoadConfig(workDir)
	if err != nil {
		http.Error(w, "demo config error: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Create new demo instance
	demoInstanceID := uuid.New()
	processInfo, _ := json.Marshal(map[string]interface{}{
		"work_dir": workDir,
		"service":  cfg.Service,
	})

	demoInstance := &domain.DemoInstance{
		ID:                   demoInstanceID,
		VariationID:          variationID,
		URL:                  "",
		TeardownInstructions: fmt.Sprintf("cd %s/.mendel && docker-compose down -v", workDir),
		Status:               domain.DemoInstanceStatusStarting,
		ProcessInfo:          processInfo,
	}

	if err := s.db.CreateDemoInstance(ctx, demoInstance); err != nil {
		http.Error(w, "failed to create demo instance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Start demo in background
	go s.runDemoStartup(projectID, variationID, demoInstanceID, workDir, cfg)

	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}
