package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// handleStartDemo starts a demo instance for a variation using .mendel/demo.yaml.
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

	// Load demo config from .mendel/demo.yaml
	cfg, err := demo.LoadConfig(workDir)
	if err != nil {
		http.Error(w, "demo config error: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Create demo instance with "starting" status immediately
	demoInstanceID := uuid.New()
	port := 0
	demoURL := ""
	if cfg.Type == "local" {
		port = demo.AllocatePort(variationID.String(), cfg.Port)
		demoURL = fmt.Sprintf("http://localhost:%d", port)
	}

	processInfo, _ := json.Marshal(map[string]interface{}{
		"port":     port,
		"work_dir": workDir,
		"type":     cfg.Type,
	})

	demoInstance := &domain.DemoInstance{
		ID:                   demoInstanceID,
		VariationID:          variationID,
		URL:                  demoURL,
		TeardownInstructions: "", // Will be set once we know the full command
		Status:               domain.DemoInstanceStatusStarting,
		ProcessInfo:          processInfo,
	}

	if err := s.db.CreateDemoInstance(ctx, demoInstance); err != nil {
		http.Error(w, "failed to create demo instance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Start the demo in a background goroutine
	go s.runDemoStartup(projectID, variationID, demoInstanceID, workDir, cfg, port)

	// Redirect immediately - user will see "starting" status
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// runDemoStartup runs the demo startup process in the background, logging output to variation_logs.
func (s *Server) runDemoStartup(projectID string, variationID, demoInstanceID uuid.UUID, workDir string, cfg *demo.Config, port int) {
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

	// Helper to handle failures
	failDemo := func(errMsg string) {
		logError(errMsg)
		// TODO: Call LLM to suggest a fix
		suggestedFix := fmt.Sprintf("Please investigate and fix the following error in the demo setup:\n\n%s", errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, suggestedFix)
	}

	logMilestone("Starting demo...")

	// Build variable substitution map
	vars := map[string]string{
		"VARIATION_ID": variationID.String(),
	}

	var demoURL string
	if cfg.Type == "local" {
		vars["PORT"] = fmt.Sprintf("%d", port)
		demoURL = fmt.Sprintf("http://localhost:%d", port)
	}

	// Apply migration if one exists and hasn't been applied yet
	migration, err := s.db.GetVariationMigration(ctx, variationID)
	if err == nil && migration != nil && migration.AppliedAt == nil {
		logInfo("Applying migration...")
		if err := executeMigrationInstructions(ctx, workDir, migration.UpInstructions); err != nil {
			failDemo(fmt.Sprintf("Migration failed: %v", err))
			return
		}
		s.db.MarkVariationMigrationApplied(ctx, migration.ID)
		logMilestone("Migration applied")
	}

	// Copy env file if specified
	if cfg.EnvFile != "" {
		logInfo(fmt.Sprintf("Copying %s to .env", cfg.EnvFile))
		envSrc := filepath.Join(workDir, cfg.EnvFile)
		envDst := filepath.Join(workDir, ".env")
		if err := copyFile(envSrc, envDst); err != nil {
			failDemo(fmt.Sprintf("Failed to copy env file: %v", err))
			return
		}
	}

	// Run setup commands
	for i, setupCmd := range cfg.Setup {
		cmdStr := demo.SubstituteVariables(setupCmd, vars)
		logInfo(fmt.Sprintf("Running setup [%d/%d]: %s", i+1, len(cfg.Setup), cmdStr))

		cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
		cmd.Dir = workDir
		output, err := cmd.CombinedOutput()

		if len(output) > 0 {
			// Log output in chunks if very long
			outStr := string(output)
			if len(outStr) > 2000 {
				outStr = outStr[:2000] + "\n... (truncated)"
			}
			logInfo(outStr)
		}

		if err != nil {
			failDemo(fmt.Sprintf("Setup command failed: %s\n\nError: %v\n\nOutput:\n%s", setupCmd, err, string(output)))
			return
		}
	}
	if len(cfg.Setup) > 0 {
		logMilestone("Setup complete")
	}

	// Start the service
	startCmd := demo.SubstituteVariables(cfg.Start, vars)
	logInfo(fmt.Sprintf("Starting service: %s", startCmd))

	cmd := exec.CommandContext(ctx, "sh", "-c", startCmd)
	cmd.Dir = workDir

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Start(); err != nil {
		failDemo(fmt.Sprintf("Failed to start service: %v", err))
		return
	}

	// For cloud mode, capture output to extract URL
	if cfg.Type == "cloud" {
		logInfo("Waiting for deployment URL...")
		time.Sleep(5 * time.Second)
		output := stdout.String()
		if len(output) > 0 {
			logInfo(output)
		}
		demoURL = demo.ExtractURL(output, cfg.URLPattern)
		if demoURL == "" {
			failDemo("Could not extract deployment URL from output")
			return
		}
		vars["DEPLOY_URL"] = demoURL
		logInfo(fmt.Sprintf("Deployment URL: %s", demoURL))
	}

	// Build teardown command
	teardownCmd := demo.SubstituteVariables(cfg.Stop, vars)

	// Wait for health check
	healthURL := demo.SubstituteVariables(cfg.HealthURL, vars)
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
		// Cleanup: run teardown
		logInfo("Health check failed, running teardown...")
		teardown := exec.CommandContext(ctx, "sh", "-c", teardownCmd)
		teardown.Dir = workDir
		teardown.Run()

		failDemo(fmt.Sprintf("Health check failed after %d seconds (%d attempts)", cfg.HealthTimeout, attempts))
		return
	}

	logMilestone(fmt.Sprintf("Demo running at %s", demoURL))

	// Update demo instance to running status with full info
	processInfo, _ := json.Marshal(map[string]interface{}{
		"port":     port,
		"pid":      cmd.Process.Pid,
		"work_dir": workDir,
		"type":     cfg.Type,
	})

	// Update the demo instance with URL, teardown, and running status
	s.db.Pool.Exec(ctx, `
		UPDATE demo_instances
		SET url = $2, teardown_instructions = $3, status = $4, process_info = $5
		WHERE id = $1
	`, demoInstanceID, demoURL, teardownCmd, domain.DemoInstanceStatusRunning, processInfo)
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
	// Use --print to get output without interactive mode
	cmd := exec.CommandContext(ctx, "claude", "--print", fixPrompt)
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

	// Now load demo config and start the demo
	cfg, err := demo.LoadConfig(workDir)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to load demo config: %v", err)
		logError(errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, "Check that .mendel/demo.yaml exists and is valid")
		return
	}

	// Continue with normal demo startup (reusing the same demo instance)
	s.continueDemoStartup(ctx, projectID, variationID, demoInstanceID, workDir, cfg)
}

// continueDemoStartup continues the demo startup process after a fix has been applied.
// This is similar to runDemoStartup but reuses an existing demo instance.
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

	failDemo := func(errMsg string) {
		logError(errMsg)
		suggestedFix := fmt.Sprintf("Please investigate and fix the following error:\n\n%s", errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, suggestedFix)
	}

	// Build variable substitution map
	vars := map[string]string{
		"VARIATION_ID": variationID.String(),
	}

	var demoURL string
	var port int
	if cfg.Type == "local" {
		port = demo.AllocatePort(variationID.String(), cfg.Port)
		vars["PORT"] = fmt.Sprintf("%d", port)
		demoURL = fmt.Sprintf("http://localhost:%d", port)
	}

	// Copy env file if specified
	if cfg.EnvFile != "" {
		logInfo(fmt.Sprintf("Copying %s to .env", cfg.EnvFile))
		envSrc := filepath.Join(workDir, cfg.EnvFile)
		envDst := filepath.Join(workDir, ".env")
		if err := copyFile(envSrc, envDst); err != nil {
			failDemo(fmt.Sprintf("Failed to copy env file: %v", err))
			return
		}
	}

	// Run setup commands
	for i, setupCmd := range cfg.Setup {
		cmdStr := demo.SubstituteVariables(setupCmd, vars)
		logInfo(fmt.Sprintf("Running setup [%d/%d]: %s", i+1, len(cfg.Setup), cmdStr))

		cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
		cmd.Dir = workDir
		output, err := cmd.CombinedOutput()

		if len(output) > 0 {
			outStr := string(output)
			if len(outStr) > 2000 {
				outStr = outStr[:2000] + "\n... (truncated)"
			}
			logInfo(outStr)
		}

		if err != nil {
			failDemo(fmt.Sprintf("Setup command failed: %s\n\nError: %v\n\nOutput:\n%s", setupCmd, err, string(output)))
			return
		}
	}
	if len(cfg.Setup) > 0 {
		logMilestone("Setup complete")
	}

	// Start the service
	startCmd := demo.SubstituteVariables(cfg.Start, vars)
	logInfo(fmt.Sprintf("Starting service: %s", startCmd))

	cmd := exec.CommandContext(ctx, "sh", "-c", startCmd)
	cmd.Dir = workDir

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Start(); err != nil {
		failDemo(fmt.Sprintf("Failed to start service: %v", err))
		return
	}

	// For cloud mode, capture output to extract URL
	if cfg.Type == "cloud" {
		logInfo("Waiting for deployment URL...")
		time.Sleep(5 * time.Second)
		output := stdout.String()
		if len(output) > 0 {
			logInfo(output)
		}
		demoURL = demo.ExtractURL(output, cfg.URLPattern)
		if demoURL == "" {
			failDemo("Could not extract deployment URL from output")
			return
		}
		vars["DEPLOY_URL"] = demoURL
		logInfo(fmt.Sprintf("Deployment URL: %s", demoURL))
	}

	// Build teardown command
	teardownCmd := demo.SubstituteVariables(cfg.Stop, vars)

	// Wait for health check
	healthURL := demo.SubstituteVariables(cfg.HealthURL, vars)
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
		logInfo("Health check failed, running teardown...")
		teardown := exec.CommandContext(ctx, "sh", "-c", teardownCmd)
		teardown.Dir = workDir
		teardown.Run()

		failDemo(fmt.Sprintf("Health check failed after %d seconds (%d attempts)", cfg.HealthTimeout, attempts))
		return
	}

	logMilestone(fmt.Sprintf("Demo running at %s", demoURL))

	// Update demo instance to running status
	processInfo, _ := json.Marshal(map[string]interface{}{
		"port":     port,
		"pid":      cmd.Process.Pid,
		"work_dir": workDir,
		"type":     cfg.Type,
	})

	s.db.Pool.Exec(ctx, `
		UPDATE demo_instances
		SET url = $2, teardown_instructions = $3, status = $4, process_info = $5
		WHERE id = $1
	`, demoInstanceID, demoURL, teardownCmd, domain.DemoInstanceStatusRunning, processInfo)
}
