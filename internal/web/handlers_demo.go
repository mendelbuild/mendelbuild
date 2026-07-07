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
	"time"

	"github.com/bhs/mendelbuild/internal/demo"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// executeMigrationInstructions runs migration instructions via shell.
// In the future, this could use Claude Code for more complex instructions.
func executeMigrationInstructions(ctx context.Context, instructions string) error {
	if instructions == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", instructions)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(output))
	}
	return nil
}

// handleStartDemo starts a demo instance for a variation using .mendel/demo.yaml.
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

	// Check if there's already a running demo
	existingDemo, err := s.db.GetRunningDemoByVariation(ctx, variationID)
	if err == nil && existingDemo != nil {
		// Already running, redirect to variation detail
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

	// Apply migration if one exists and hasn't been applied yet
	migration, err := s.db.GetVariationMigration(ctx, variationID)
	if err == nil && migration != nil && migration.AppliedAt == nil {
		if err := executeMigrationInstructions(ctx, migration.UpInstructions); err != nil {
			http.Error(w, "failed to apply migration: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.db.MarkVariationMigrationApplied(ctx, migration.ID); err != nil {
			fmt.Printf("[demo] Warning: failed to mark migration as applied: %v\n", err)
		}
	}

	// Build variable substitution map
	vars := map[string]string{
		"VARIATION_ID": variationID.String(),
	}

	var demoURL string
	var port int
	var teardownCmd string

	if cfg.Type == "local" {
		// Local mode: allocate port and start service
		port = demo.AllocatePort(variationID.String(), cfg.Port)
		vars["PORT"] = fmt.Sprintf("%d", port)
		demoURL = fmt.Sprintf("http://localhost:%d", port)
	}

	// Copy env file if specified
	if cfg.EnvFile != "" {
		envSrc := filepath.Join(workDir, cfg.EnvFile)
		envDst := filepath.Join(workDir, ".env")
		if err := copyFile(envSrc, envDst); err != nil {
			http.Error(w, "failed to copy env file: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Run setup commands
	for _, setupCmd := range cfg.Setup {
		cmd := exec.CommandContext(ctx, "sh", "-c", demo.SubstituteVariables(setupCmd, vars))
		cmd.Dir = workDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			errMsg := fmt.Sprintf("setup command failed: %s\n%s", setupCmd, string(output))
			http.Error(w, errMsg, http.StatusInternalServerError)
			return
		}
	}

	// Start the service
	startCmd := demo.SubstituteVariables(cfg.Start, vars)
	cmd := exec.CommandContext(ctx, "sh", "-c", startCmd)
	cmd.Dir = workDir

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Start(); err != nil {
		http.Error(w, "failed to start service: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// For cloud mode, we need to capture output to extract the URL
	// For local mode, we know the URL already
	if cfg.Type == "cloud" {
		// Wait a bit for output (cloud deployments typically print URL quickly)
		time.Sleep(5 * time.Second)
		output := stdout.String()
		demoURL = demo.ExtractURL(output, cfg.URLPattern)
		if demoURL == "" {
			errMsg := "could not extract deployment URL from output"
			s.createErrorDemoInstance(ctx, variationID, "", errMsg)
			http.Error(w, errMsg, http.StatusInternalServerError)
			return
		}
		vars["DEPLOY_URL"] = demoURL
	}

	// Build teardown command
	teardownCmd = demo.SubstituteVariables(cfg.Stop, vars)

	// Wait for health check
	healthURL := demo.SubstituteVariables(cfg.HealthURL, vars)
	if cfg.Type == "cloud" {
		healthURL = demo.SubstituteVariables(cfg.HealthURL, vars)
	}

	healthy := false
	deadline := time.Now().Add(time.Duration(cfg.HealthTimeout) * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(healthURL)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 400 {
			resp.Body.Close()
			healthy = true
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(time.Duration(cfg.HealthInterval) * time.Second)
	}

	if !healthy {
		// Cleanup: run teardown
		teardown := exec.CommandContext(ctx, "sh", "-c", teardownCmd)
		teardown.Dir = workDir
		teardown.Run()

		errMsg := fmt.Sprintf("health check failed after %d seconds", cfg.HealthTimeout)
		s.createErrorDemoInstance(ctx, variationID, demoURL, errMsg)
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}

	// Record the demo instance
	processInfo, _ := json.Marshal(map[string]interface{}{
		"port":     port,
		"pid":      cmd.Process.Pid,
		"work_dir": workDir,
		"type":     cfg.Type,
	})

	demoInstance := &domain.DemoInstance{
		ID:                   uuid.New(),
		VariationID:          variationID,
		URL:                  demoURL,
		TeardownInstructions: teardownCmd,
		StartedAt:            time.Now(),
		Status:               domain.DemoInstanceStatusRunning,
		ProcessInfo:          processInfo,
	}

	if err := s.db.CreateDemoInstance(ctx, demoInstance); err != nil {
		http.Error(w, "failed to record demo instance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to variation detail
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
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
	if err := s.revertVariationMigration(ctx, variationID); err != nil {
		// Log but don't fail - demo is already stopped
		fmt.Printf("[demo] Warning: failed to revert migration: %v\n", err)
	}

	// Redirect to variation detail
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// revertVariationMigration reverts a variation's migration if it was applied and not yet reverted.
func (s *Server) revertVariationMigration(ctx context.Context, variationID uuid.UUID) error {
	migration, err := s.db.GetVariationMigration(ctx, variationID)
	if err != nil {
		return nil // No migration exists, nothing to revert
	}

	// Only revert if applied and not already reverted
	if migration.AppliedAt == nil || migration.RevertedAt != nil {
		return nil
	}

	if err := executeMigrationInstructions(ctx, migration.DownInstructions); err != nil {
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
