package codegen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/bhs/mendelbuild/internal/codegen/executor"
	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/demo"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
	"github.com/bhs/mendelbuild/internal/test"
	"github.com/google/uuid"
)

// GeneratorConfig holds configuration for the generator.
type GeneratorConfig struct {
	ProjectID     string
	RepositoryURL string
	MainBranch    string
	AuthToken     string
	APIKey        string
}

// Generator handles code generation for a single variation.
type Generator struct {
	db     *db.DB
	config GeneratorConfig
}

// NewGenerator creates a new Generator.
func NewGenerator(database *db.DB, config GeneratorConfig) *Generator {
	return &Generator{
		db:     database,
		config: config,
	}
}

// createEventHandler creates an executor event handler that logs to the database.
func (g *Generator) createEventHandler(ctx context.Context, variationID uuid.UUID, logger func(domain.LogLevel, string)) executor.EventHandler {
	return func(event executor.Event) {
		switch event.Type {
		case executor.EventToolCall:
			switch event.ToolName {
			case "Read":
				if path, ok := event.ToolInput["file_path"].(string); ok {
					logger(domain.LogLevelInfo, fmt.Sprintf("Reading: %s", shortenPath(path)))
				}
			case "Write":
				if path, ok := event.ToolInput["file_path"].(string); ok {
					logger(domain.LogLevelMilestone, fmt.Sprintf("Writing: %s", shortenPath(path)))
				}
			case "Edit":
				if path, ok := event.ToolInput["file_path"].(string); ok {
					logger(domain.LogLevelMilestone, fmt.Sprintf("Editing: %s", shortenPath(path)))
				}
			case "Bash":
				if cmd, ok := event.ToolInput["command"].(string); ok {
					if len(cmd) > 80 {
						cmd = cmd[:77] + "..."
					}
					logger(domain.LogLevelInfo, fmt.Sprintf("Running: %s", cmd))
				}
			case "Glob", "Grep":
				logger(domain.LogLevelInfo, "Searching files...")
			}
		case executor.EventAPIResponse:
			logger(domain.LogLevelInfo, fmt.Sprintf("API: +%d in, +%d out tokens", event.InputTokens, event.OutputTokens))
		case executor.EventComplete:
			logger(domain.LogLevelMilestone, "Code generation complete")
		case executor.EventError:
			logger(domain.LogLevelError, fmt.Sprintf("Error: %v", event.Error))
		}
	}
}


// GenerateResult contains the result of code generation.
type GenerateResult struct {
	VariationID   uuid.UUID
	Success       bool
	CommitRef     string
	BranchName    string
	TokensUsed    int
	TestsPassed   bool
	Error         string
}

// Generate runs the full code generation workflow for a variation.
func (g *Generator) Generate(ctx context.Context, variation *domain.Variation, hopName string) (*GenerateResult, error) {
	result := &GenerateResult{
		VariationID: variation.ID,
	}

	// Create a logger that writes to the database
	logger := func(level domain.LogLevel, message string) {
		g.db.CreateVariationLog(ctx, variation.ID, level, message)
	}

	logger(domain.LogLevelMilestone, fmt.Sprintf("Starting code generation for variation '%s'", variation.Name))

	// 1. Check for pending revision (user feedback) - determines clone strategy
	var pendingRevision *domain.VariationRevision
	pendingRevision, _ = g.db.GetPendingVariationRevision(ctx, variation.ID)

	isRevision := pendingRevision != nil
	if isRevision {
		g.db.UpdateVariationRevisionStatus(ctx, pendingRevision.ID, domain.VariationRevisionStatusInProgress, nil)
		logger(domain.LogLevelMilestone, fmt.Sprintf("Processing revision: %s", pendingRevision.Feedback))
	}

	// 2. Set up work directory
	workDir := git.WorkDirForVariation(g.config.ProjectID, variation.ID.String())
	gitClient := git.NewClient(workDir)
	branchName := fmt.Sprintf("mendel/%s/%s", hopName, variation.Name)
	result.BranchName = branchName

	// Infrastructure failures use "error" status (retryable)
	// Code/test failures use "terminated" status (not retryable)

	if isRevision {
		// For revisions: clone existing branch (or use existing work dir)
		if _, err := os.Stat(workDir); err == nil {
			// Work directory exists - pull latest
			logger(domain.LogLevelInfo, "Using existing work directory, pulling latest")
			// Already on the branch, just make sure it's up to date
		} else {
			// Clone the existing variation branch
			logger(domain.LogLevelInfo, fmt.Sprintf("Cloning existing branch %s", branchName))
			if err := gitClient.Clone(ctx, g.config.RepositoryURL, branchName, g.config.AuthToken); err != nil {
				result.Error = fmt.Sprintf("clone failed: %v", err)
				logger(domain.LogLevelError, result.Error)
				g.db.UpdateVariationRevisionStatus(ctx, pendingRevision.ID, domain.VariationRevisionStatusFailed, &result.Error)
				g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusError, result.Error)
				return result, nil
			}
		}
	} else {
		// For fresh generation: clean up and clone from main
		if _, err := os.Stat(workDir); err == nil {
			logger(domain.LogLevelInfo, "Cleaning up existing work directory from previous attempt")
			if err := os.RemoveAll(workDir); err != nil {
				result.Error = fmt.Sprintf("failed to clean up work directory: %v", err)
				logger(domain.LogLevelError, result.Error)
				g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusError, result.Error)
				return result, nil
			}
		}

		logger(domain.LogLevelInfo, fmt.Sprintf("Cloning repository to %s", workDir))
		if err := gitClient.Clone(ctx, g.config.RepositoryURL, g.config.MainBranch, g.config.AuthToken); err != nil {
			result.Error = fmt.Sprintf("clone failed: %v", err)
			logger(domain.LogLevelError, result.Error)
			g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusError, result.Error)
			return result, nil
		}
		logger(domain.LogLevelMilestone, "Repository cloned successfully")

		// Create branch
		logger(domain.LogLevelInfo, fmt.Sprintf("Creating branch: %s", branchName))
		if err := gitClient.CreateBranch(ctx, branchName); err != nil {
			result.Error = fmt.Sprintf("create branch failed: %v", err)
			logger(domain.LogLevelError, result.Error)
			g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusError, result.Error)
			return result, nil
		}
	}

	// 4. Run code generation via API tool loop
	exec := executor.New(g.config.APIKey, workDir).
		WithEventHandler(g.createEventHandler(ctx, variation.ID, logger))

	var prompt string
	if pendingRevision != nil {
		prompt = BuildRevisionPrompt(hopName, variation.Name, variation.Approach, pendingRevision.Feedback)
	} else {
		prompt = BuildImplementationPrompt(hopName, variation.Name, variation.Approach)
	}
	execResult, err := exec.Run(ctx, executor.SystemPrompt(), prompt)
	if err != nil {
		result.Error = fmt.Sprintf("executor error: %v", err)
		logger(domain.LogLevelError, result.Error)
		if pendingRevision != nil {
			g.db.UpdateVariationRevisionStatus(ctx, pendingRevision.ID, domain.VariationRevisionStatusFailed, &result.Error)
		}
		g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusError, result.Error)
		return result, nil
	}

	result.TokensUsed = execResult.Stats.InputTokens + execResult.Stats.OutputTokens
	logger(domain.LogLevelInfo, fmt.Sprintf("API stats: %d rounds, %d tool calls, %d input tokens, %d output tokens",
		execResult.Stats.APIRounds, execResult.Stats.ToolCalls, execResult.Stats.InputTokens, execResult.Stats.OutputTokens))

	// Save token usage to database (accumulates across runs)
	g.db.AddVariationTokens(ctx, variation.ID, execResult.Stats.InputTokens, execResult.Stats.OutputTokens)

	if !execResult.Success {
		result.Error = fmt.Sprintf("code generation failed: %v", execResult.Error)
		logger(domain.LogLevelError, result.Error)
		if pendingRevision != nil {
			g.db.UpdateVariationRevisionStatus(ctx, pendingRevision.ID, domain.VariationRevisionStatusFailed, &result.Error)
		}
		g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusTerminated, result.Error)
		return result, nil
	}

	// 4. Check for migration instructions
	if err := g.saveMigrationInstructions(ctx, workDir, variation.ID, logger); err != nil {
		// Log but don't fail - migrations are optional
		logger(domain.LogLevelInfo, fmt.Sprintf("No migration instructions: %v", err))
	}

	// 5. Commit changes (before tests so branch is visible on GitHub even if tests fail)
	logger(domain.LogLevelInfo, "Committing changes")
	commitMsg := fmt.Sprintf("[MendelBuild] %s: %s\n\nGenerated by MendelBuild for hop '%s'",
		variation.Name, truncate(variation.Approach, 50), hopName)

	if err := gitClient.CommitAll(ctx, commitMsg); err != nil {
		result.Error = fmt.Sprintf("commit failed: %v", err)
		logger(domain.LogLevelError, result.Error)
		g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusError, result.Error)
		return result, nil
	}

	// 6. Get commit SHA
	commitRef, err := gitClient.GetCurrentCommit(ctx)
	if err != nil {
		result.Error = fmt.Sprintf("get commit ref failed: %v", err)
		logger(domain.LogLevelError, result.Error)
		g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusError, result.Error)
		return result, nil
	}
	result.CommitRef = commitRef
	logger(domain.LogLevelMilestone, fmt.Sprintf("Committed: %s", commitRef[:8]))

	// 7. Push to remote (before tests so branch is visible on GitHub even if tests fail)
	logger(domain.LogLevelInfo, "Pushing to remote")
	if err := gitClient.Push(ctx, g.config.AuthToken); err != nil {
		result.Error = fmt.Sprintf("push failed: %v", err)
		logger(domain.LogLevelError, result.Error)
		g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusError, result.Error)
		return result, nil
	}
	logger(domain.LogLevelMilestone, fmt.Sprintf("Pushed branch: %s", branchName))

	// 8. Compute diff stats vs main branch
	diffStats, err := gitClient.GetDiffStats(ctx, g.config.MainBranch)
	if err != nil {
		logger(domain.LogLevelInfo, fmt.Sprintf("Could not compute diff stats: %v", err))
	} else {
		logger(domain.LogLevelInfo, fmt.Sprintf("Diff stats: %d files, +%d/-%d lines", diffStats.FilesChanged, diffStats.Additions, diffStats.Deletions))
		if err := g.db.UpdateVariationDiffStats(ctx, variation.ID, diffStats.FilesChanged, diffStats.Additions, diffStats.Deletions); err != nil {
			logger(domain.LogLevelInfo, fmt.Sprintf("Could not save diff stats: %v", err))
		}
	}

	// Update variation with commit info (before tests, so we have the ref even if tests fail)
	variation.CommitRef = &commitRef
	variation.UpdatedAt = time.Now()
	if err := g.db.UpdateVariation(ctx, variation); err != nil {
		logger(domain.LogLevelInfo, fmt.Sprintf("Could not save commit ref: %v", err))
	}

	// 9. Run tests
	testsPassed, testErr := g.runTests(ctx, workDir, logger)
	result.TestsPassed = testsPassed

	if !testsPassed {
		// Tests failed - code issue, not retryable
		reason := "tests failed"
		if testErr != "" {
			reason = fmt.Sprintf("tests failed: %s", testErr)
		}
		result.Error = reason
		logger(domain.LogLevelError, reason)
		g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusTerminated, reason)
		return result, nil
	}

	// 10. Verify demo works (if demo config exists)
	if demo.HasDockerCompose(workDir) {
		demoPassed, demoErr := g.verifyDemo(ctx, workDir, exec, logger)
		if !demoPassed {
			reason := "demo verification failed"
			if demoErr != "" {
				reason = fmt.Sprintf("demo verification failed: %s", demoErr)
			}
			result.Error = reason
			logger(domain.LogLevelError, reason)
			g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusTerminated, reason)
			return result, nil
		}
	}

	// 11. Update variation status to pending (tests passed)
	variation.Status = domain.VariationStatusPending
	variation.UpdatedAt = time.Now()
	if err := g.db.UpdateVariation(ctx, variation); err != nil {
		result.Error = fmt.Sprintf("update variation failed: %v", err)
		logger(domain.LogLevelError, result.Error)
		return result, nil
	}

	// 12. Record state transition
	g.transitionState(ctx, variation.ID, domain.VariationStatusCreating, domain.VariationStatusPending, "code generation successful")

	// 13. Mark revision as completed if one was being processed
	if pendingRevision != nil {
		g.db.UpdateVariationRevisionStatus(ctx, pendingRevision.ID, domain.VariationRevisionStatusCompleted, nil)
		logger(domain.LogLevelMilestone, "Revision completed successfully!")
	}

	logger(domain.LogLevelMilestone, "Code generation completed successfully!")
	result.Success = true
	return result, nil
}

// runTests executes tests in Docker and returns whether they passed.
// If no test config exists, creates a default one that passes.
func (g *Generator) runTests(ctx context.Context, workDir string, logger func(domain.LogLevel, string)) (bool, string) {
	// Ensure test config exists
	testCfg, err := test.LoadConfig(workDir)
	if err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("Invalid test config: %v", err))
		return false, err.Error()
	}

	// Create default test config if none exists
	if testCfg == nil {
		logger(domain.LogLevelInfo, "No test config found, creating default (tests will pass)")
		if err := test.CreateDefaultConfig(workDir); err != nil {
			logger(domain.LogLevelError, fmt.Sprintf("Failed to create default test config: %v", err))
			return false, err.Error()
		}
		testCfg, _ = test.LoadConfig(workDir)
	}

	// Ensure docker-compose.test.yml exists
	if !test.HasTestCompose(workDir) {
		logger(domain.LogLevelInfo, "No docker-compose.test.yml found, creating default")
		if err := test.CreateDefaultCompose(workDir); err != nil {
			logger(domain.LogLevelError, fmt.Sprintf("Failed to create default docker-compose.test.yml: %v", err))
			return false, err.Error()
		}
	}

	// Run Docker-based tests
	logger(domain.LogLevelMilestone, "Running tests in Docker...")
	logger(domain.LogLevelInfo, fmt.Sprintf("Test command: %s (in container '%s')", testCfg.TestCommand, testCfg.Service))

	testResult := test.RunTestsWithOutput(workDir, testCfg)
	if testResult.Output != "" {
		// Truncate long output
		output := testResult.Output
		if len(output) > 4000 {
			output = output[:2000] + "\n...(truncated)...\n" + output[len(output)-1500:]
		}
		logger(domain.LogLevelInfo, output)
	}

	if testResult.Passed {
		logger(domain.LogLevelMilestone, "Tests passed")
		return true, ""
	}
	return false, testResult.Error
}

// verifyDemo starts the demo, checks health, and lets the executor fix issues if needed.
// Returns (passed, errorMessage).
func (g *Generator) verifyDemo(ctx context.Context, workDir string, exec *executor.Executor, logger func(domain.LogLevel, string)) (bool, string) {
	const maxAttempts = 3

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		logger(domain.LogLevelMilestone, fmt.Sprintf("Verifying demo (attempt %d/%d)...", attempt, maxAttempts))

		// Load demo config
		cfg, err := demo.LoadConfig(workDir)
		if err != nil {
			logger(domain.LogLevelError, fmt.Sprintf("Failed to load demo config: %v", err))
			return false, err.Error()
		}

		// Start demo containers
		logger(domain.LogLevelInfo, "Starting demo containers...")
		output, err := demo.DockerComposeUp(workDir)
		if err != nil {
			errMsg := fmt.Sprintf("docker-compose up failed: %v\n%s", err, truncateOutput(output, 2000))
			logger(domain.LogLevelError, errMsg)

			// Tear down before retry
			demo.DockerComposeDown(workDir, true)

			if attempt < maxAttempts {
				// Let Claude Code fix the issue
				if !g.fixDemoIssue(ctx, workDir, exec, errMsg, logger) {
					return false, "Claude Code failed to fix demo issue"
				}
				continue
			}
			return false, errMsg
		}

		// Get the exposed port
		port, err := demo.GetServicePort(workDir, cfg.Service, cfg.ContainerPort)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to get service port: %v", err)
			logger(domain.LogLevelError, errMsg)
			demo.DockerComposeDown(workDir, true)

			if attempt < maxAttempts {
				if !g.fixDemoIssue(ctx, workDir, exec, errMsg, logger) {
					return false, "Claude Code failed to fix demo issue"
				}
				continue
			}
			return false, errMsg
		}

		// Wait for health check
		healthURL := fmt.Sprintf("http://localhost:%d%s", port, cfg.HealthPath)
		logger(domain.LogLevelInfo, fmt.Sprintf("Waiting for health check: %s", healthURL))

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

		// Tear down containers (we're just verifying, not running the demo)
		demo.DockerComposeDown(workDir, true)

		if healthy {
			logger(domain.LogLevelMilestone, "Demo verification passed")
			return true, ""
		}

		errMsg := fmt.Sprintf("Health check failed after %d seconds", cfg.HealthTimeout)
		logger(domain.LogLevelError, errMsg)

		if attempt < maxAttempts {
			if !g.fixDemoIssue(ctx, workDir, exec, errMsg, logger) {
				return false, "Claude Code failed to fix demo issue"
			}
			continue
		}
		return false, errMsg
	}

	return false, "demo verification failed after max attempts"
}

// fixDemoIssue runs the executor to fix a demo issue.
func (g *Generator) fixDemoIssue(ctx context.Context, workDir string, exec *executor.Executor, errMsg string, logger func(domain.LogLevel, string)) bool {
	logger(domain.LogLevelInfo, "Asking Claude to fix demo issue...")

	prompt := fmt.Sprintf(`The demo failed to start. Fix the issue and try again.

## Error
%s

## What to check
1. Does .mendel/docker-compose.demo.yml exist and define all needed services?
2. Are service health checks configured correctly?
3. Does the Dockerfile build successfully?
4. Are environment variables and connection strings correct for Docker networking?
   - Services communicate via service names (e.g., db:5432, not localhost:5432)

## What to do
1. Diagnose the root cause from the error message
2. Fix the Docker Compose, Dockerfile, or application configuration
3. Make sure the fix addresses the actual error, not just symptoms`, errMsg)

	result, err := exec.Run(ctx, executor.SystemPrompt(), prompt)
	if err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("Executor error: %v", err))
		return false
	}

	if !result.Success {
		logger(domain.LogLevelError, fmt.Sprintf("Fix attempt failed: %v", result.Error))
		return false
	}

	logger(domain.LogLevelMilestone, "Applied fix, retrying demo...")
	return true
}

// truncateOutput truncates output keeping end (where errors usually are).
func truncateOutput(output string, maxLen int) string {
	if len(output) <= maxLen {
		return output
	}
	return "..." + output[len(output)-maxLen:]
}

// MigrationInstructions is the structure written to .mendel/migration.json
type MigrationInstructions struct {
	UpInstructions   string `json:"up_instructions"`
	DownInstructions string `json:"down_instructions"`
	Notes            string `json:"notes"` // Where to find migration files in user's repo
}

// saveMigrationInstructions reads .mendel/migration.json if it exists and saves to DB.
func (g *Generator) saveMigrationInstructions(ctx context.Context, workDir string, variationID uuid.UUID, logger func(domain.LogLevel, string)) error {
	migrationPath := filepath.Join(workDir, ".mendel", "migration.json")

	data, err := os.ReadFile(migrationPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no migration file (this is fine)")
		}
		return fmt.Errorf("read migration file: %w", err)
	}

	var instructions MigrationInstructions
	if err := json.Unmarshal(data, &instructions); err != nil {
		return fmt.Errorf("parse migration file: %w", err)
	}

	if instructions.UpInstructions == "" || instructions.DownInstructions == "" {
		return fmt.Errorf("migration file missing up_instructions or down_instructions")
	}

	// Save migration instructions to variation_migrations table
	var notesPtr *string
	if instructions.Notes != "" {
		notesPtr = &instructions.Notes
	}

	migration := &domain.VariationMigration{
		ID:               uuid.New(),
		VariationID:      variationID,
		UpInstructions:   instructions.UpInstructions,
		DownInstructions: instructions.DownInstructions,
		Notes:            notesPtr,
		CreatedAt:        time.Now(),
	}

	if err := g.db.CreateVariationMigration(ctx, migration); err != nil {
		return fmt.Errorf("save migration: %w", err)
	}

	logger(domain.LogLevelMilestone, "Saved migration instructions")
	if instructions.Notes != "" {
		logger(domain.LogLevelInfo, fmt.Sprintf("Migration files: %s", instructions.Notes))
	}
	return nil
}

// transitionState records a state transition in the database.
func (g *Generator) transitionState(ctx context.Context, variationID uuid.UUID, from, to domain.VariationStatus, reason string) {
	g.db.CreateVariationStateTransition(ctx, variationID, string(from), string(to), reason)

	// Also update the variation status
	v, err := g.db.GetVariation(ctx, variationID)
	if err == nil {
		v.Status = to
		v.UpdatedAt = time.Now()
		g.db.UpdateVariation(ctx, v)
	}
}

// truncate truncates a string to the given length.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// ParseRepoConfig parses repository config JSON into a RepoConfig struct.
func ParseRepoConfig(config json.RawMessage) (*domain.RepoConfig, error) {
	if config == nil {
		return &domain.RepoConfig{}, nil
	}
	var rc domain.RepoConfig
	if err := json.Unmarshal(config, &rc); err != nil {
		return nil, err
	}
	return &rc, nil
}
