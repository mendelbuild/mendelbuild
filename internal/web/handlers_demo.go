package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// handleStartDemo starts a demo instance for a variation.
// In the future, this will use Claude Code to read MENDEL.md and deploy.
// For now, it provides a placeholder that records the demo request.
func (s *Server) handleStartDemo(w http.ResponseWriter, r *http.Request) {
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

	// Check if there's already a running demo
	existingDemo, err := s.db.GetRunningDemoByVariation(ctx, variationID)
	if err == nil && existingDemo != nil {
		// Already running, redirect to variation detail
		http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
		return
	}

	// Get the hop for this variation
	hop, err := s.db.GetHop(ctx, variation.HopID)
	if err != nil {
		http.Error(w, "hop not found", http.StatusNotFound)
		return
	}

	// Get repository info
	strategy, err := s.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		http.Error(w, "strategy not found", http.StatusNotFound)
		return
	}

	repo, err := s.db.GetRepositoryByProject(ctx, strategy.ProjectID)
	if err != nil {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}

	// For now, we implement a simple localhost demo approach
	// In the future, this will read MENDEL.md and use Claude Code

	// Determine a port based on variation name hash (simple approach)
	port := 3000 + (int(variationID[0]) % 100)
	url := fmt.Sprintf("http://localhost:%d", port)

	// Get the working directory for this variation
	workDir := ""
	if repo.Config != nil {
		var config map[string]interface{}
		if err := json.Unmarshal(repo.Config, &config); err == nil {
			if wd, ok := config["work_dir"].(string); ok {
				workDir = wd
			}
		}
	}

	if workDir == "" {
		http.Error(w, "repository work_dir not configured - please set up repository in project settings", http.StatusBadRequest)
		return
	}

	// Build variation branch path
	branchPath := fmt.Sprintf("%s/variations/%s", workDir, variation.Name)

	// Try to start a simple dev server (npm run dev)
	// This is a basic implementation - MENDEL.md will make this configurable
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf(
		"cd %s && git checkout %s 2>/dev/null; PORT=%d npm run dev &",
		branchPath, variation.Name, port,
	))

	if err := cmd.Start(); err != nil {
		// Failed to start - create error demo instance
		demoInstance := &domain.DemoInstance{
			ID:                   uuid.New(),
			VariationID:          variationID,
			URL:                  url,
			TeardownInstructions: fmt.Sprintf("lsof -ti:%d | xargs kill -9", port),
			Status:               domain.DemoInstanceStatusError,
		}
		errMsg := fmt.Sprintf("Failed to start demo: %v", err)
		demoInstance.ErrorMessage = &errMsg
		s.db.CreateDemoInstance(ctx, demoInstance)

		http.Error(w, "failed to start demo: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Record the demo instance
	processInfo, _ := json.Marshal(map[string]interface{}{
		"port": port,
		"pid":  cmd.Process.Pid,
	})

	demoInstance := &domain.DemoInstance{
		ID:                   uuid.New(),
		VariationID:          variationID,
		URL:                  url,
		TeardownInstructions: fmt.Sprintf("lsof -ti:%d | xargs kill -9", port),
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
	demo, err := s.db.GetRunningDemoByVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "no running demo found", http.StatusNotFound)
		return
	}

	// Run teardown instructions
	cmd := exec.CommandContext(ctx, "sh", "-c", demo.TeardownInstructions)
	if err := cmd.Run(); err != nil {
		// Mark as error but continue
		errMsg := fmt.Sprintf("Teardown failed: %v", err)
		s.db.UpdateDemoInstanceStatus(ctx, demo.ID, domain.DemoInstanceStatusError, &errMsg)
	} else {
		// Mark as stopped
		s.db.UpdateDemoInstanceStatus(ctx, demo.ID, domain.DemoInstanceStatusStopped, nil)
	}

	// Redirect to variation detail
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}
