package codegen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bhs/mendelbuild/internal/codegen/executor"
	"github.com/bhs/mendelbuild/internal/cost"
	"github.com/bhs/mendelbuild/internal/db"
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
	ArtifactKind  string // e.g., "container", "kubernetes", "static", "source_deploy"
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

	// A run paused at its spend ceiling keeps its work directory, so continuing
	// means carrying on against the half-finished code rather than starting
	// over from a clean clone. If that directory has since gone, there is
	// nothing to continue and this falls back to a fresh generation.
	resuming := variation.PausedForBudget()

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

	if resuming {
		// Nothing to continue if the working directory has gone -- a machine
		// restart, or a cleanup between runs -- so fall back to a fresh start
		// rather than resuming against code that is not there.
		if _, err := os.Stat(workDir); err != nil {
			logger(domain.LogLevelInfo,
				"The paused run's work directory is gone, so this starts fresh instead of continuing")
			resuming = false
		}
		// Lift the pause now: this run supersedes it either way, and leaving
		// the marker set would make a later run think it too was a resume.
		if err := g.db.ClearVariationBudgetPause(ctx, variation.ID); err != nil {
			logger(domain.LogLevelError, fmt.Sprintf("could not clear the spend pause: %v", err))
		}
	}

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
	} else if resuming {
		logger(domain.LogLevelMilestone, fmt.Sprintf(
			"Continuing the run paused at $%.2f; the work directory is intact",
			*variation.BudgetPausedUSD))
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

	// 4. Run code generation via API tool loop, bounded by spend rather than by
	// a fixed number of rounds. Rounds correlate poorly with cost, and the old
	// fixed cap was cutting off runs that were making progress.
	budget := budgetForRun(ctx, g.db, variation.HopID, executor.DefaultModel)
	logger(domain.LogLevelInfo, "Budget: "+budget.Describe())

	exec := budget.Apply(executor.New(g.config.APIKey, workDir).
		WithEventHandler(g.createEventHandler(ctx, variation.ID, logger)))

	var prompt string
	switch {
	case pendingRevision != nil:
		prompt = BuildRevisionPrompt(hopName, variation.Name, variation.Approach, pendingRevision.Feedback)
	case resuming:
		prompt = BuildResumePrompt(hopName, variation.Name, variation.Approach, g.config.ArtifactKind)
	default:
		prompt = BuildImplementationPrompt(hopName, variation.Name, variation.Approach,
			g.config.ArtifactKind, g.hopWantsExperiment(ctx, variation.HopID))
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

	if execResult.StoppedForBudget {
		g.recordSpend(ctx, variation, execResult.Stats, logger)
		g.pauseForBudget(ctx, variation, execResult, budget, logger)
		result.Error = execResult.Error.Error()
		return result, nil
	}

	result.TokensUsed = execResult.Stats.Tokens().Total()
	logger(domain.LogLevelInfo, fmt.Sprintf(
		"API stats: %d rounds, %d tool calls, %d in / %d out / %d cache-read / %d cache-write tokens",
		execResult.Stats.APIRounds, execResult.Stats.ToolCalls,
		execResult.Stats.InputTokens, execResult.Stats.OutputTokens,
		execResult.Stats.CacheRead, execResult.Stats.CacheWrite))

	// Price this run and append it to the cost ledger. Cache tokens are carried
	// through: on a long agentic run they are most of the prompt, and counting
	// only input/output undercounts what was actually bought.
	g.recordSpend(ctx, variation, execResult.Stats, logger)

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

	// 4b. Check for declared requirements
	if err := g.saveRequirements(ctx, workDir, variation.ID, logger); err != nil {
		// Log but don't fail - most variations need nothing to run.
		logger(domain.LogLevelInfo, fmt.Sprintf("No declared requirements: %v", err))
	}

	// 4c. Check for a live-experiment declaration. Most variations have none;
	// one that does is asking for live traffic and saying what that costs the
	// schema, which is the upstream half internal/experiment never had.
	if err := g.saveExperimentDeclaration(ctx, workDir, variation, logger); err != nil {
		logger(domain.LogLevelInfo, fmt.Sprintf("No live experiment: %v", err))
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

	// 10. Update variation status to pending (tests passed)
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

// DeclaredRequirement is one entry in .mendel/requirements.json.
type DeclaredRequirement struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
	ConsoleURL   string `json:"console_url"`
}

// DeclaredRequirements is the structure written to .mendel/requirements.json.
type DeclaredRequirements struct {
	Requirements []DeclaredRequirement `json:"requirements"`
}

// saveRequirements reads .mendel/requirements.json if it exists and saves the
// variation's requirements to the DB.
//
// The declaration lives in the repo, next to the code that needs it, for the
// same reason migration.json does: what the code requires is a property of the
// code, and code generation is the only thing that knows it.
func (g *Generator) saveRequirements(ctx context.Context, workDir string, variationID uuid.UUID, logger func(domain.LogLevel, string)) error {
	path := filepath.Join(workDir, ".mendel", "requirements.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// The file is committed to the variation's branch, so on a revision
			// it is still there unless the code no longer needs it. Absent
			// means nothing is required, which must clear what a previous run
			// declared — otherwise a revision that drops the OAuth flow leaves
			// a demo blocked on a secret nothing reads.
			if err := g.db.ReplaceVariationRequirements(ctx, variationID, nil); err != nil {
				return fmt.Errorf("clear requirements: %w", err)
			}
			return fmt.Errorf("no requirements file (this is fine)")
		}
		return fmt.Errorf("read requirements file: %w", err)
	}

	var declared DeclaredRequirements
	if err := json.Unmarshal(data, &declared); err != nil {
		return fmt.Errorf("parse requirements file: %w", err)
	}

	reqs := make([]domain.VariationRequirement, 0, len(declared.Requirements))
	for _, d := range declared.Requirements {
		req, err := d.toDomain(variationID)
		if err != nil {
			// One malformed entry must not discard the well-formed ones: a
			// missing secret is a blocked deploy the user can act on, while a
			// silently dropped requirement is a demo that fails mysteriously.
			logger(domain.LogLevelError, fmt.Sprintf("Ignoring requirement %q: %v", d.Name, err))
			continue
		}
		reqs = append(reqs, *req)
	}

	if err := g.db.ReplaceVariationRequirements(ctx, variationID, reqs); err != nil {
		return fmt.Errorf("save requirements: %w", err)
	}

	if len(reqs) == 0 {
		return nil
	}
	logger(domain.LogLevelMilestone, fmt.Sprintf("Declared %d requirement(s) to run this variation", len(reqs)))
	for _, req := range reqs {
		logger(domain.LogLevelInfo, fmt.Sprintf("  %s: %s", req.Kind, req.Name))
	}
	return nil
}

// toDomain validates a declared requirement and converts it. The checks mirror
// the table's constraints so a bad declaration is reported against the entry
// that caused it rather than surfacing as a database error.
func (d DeclaredRequirement) toDomain(variationID uuid.UUID) (*domain.VariationRequirement, error) {
	kind := domain.RequirementKind(d.Kind)
	if kind != domain.RequirementKindSecret && kind != domain.RequirementKindAcknowledgement {
		return nil, fmt.Errorf("unknown kind %q", d.Kind)
	}
	if d.Name == "" {
		return nil, fmt.Errorf("missing name")
	}
	if kind == domain.RequirementKindAcknowledgement && d.Instructions == "" {
		return nil, fmt.Errorf("acknowledgement has no instructions, so there is nothing to act on")
	}

	req := &domain.VariationRequirement{
		VariationID: variationID,
		Kind:        kind,
		Name:        d.Name,
	}
	if d.Description != "" {
		req.Description = &d.Description
	}
	if d.Instructions != "" {
		req.Instructions = &d.Instructions
	}
	if d.ConsoleURL != "" {
		req.ConsoleURL = &d.ConsoleURL
	}
	return req, nil
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

// recordSpend prices an executor run and files it against the Variation's Hop.
//
// Failures here are logged, never fatal: losing a ledger row is bad, but
// failing a completed code generation because its receipt could not be written
// would be worse.
func (g *Generator) recordSpend(ctx context.Context, variation *domain.Variation, stats executor.Stats, logger func(domain.LogLevel, string)) {
	tokens := stats.Tokens()
	if tokens.IsZero() {
		return
	}

	projectID, strategyID, err := g.db.ResolveHopAttribution(ctx, variation.HopID)
	if err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("could not attribute spend: %v", err))
		return
	}

	hopID := variation.HopID
	variationID := variation.ID
	entry, err := cost.NewRecorder(g.db).RecordModelUsage(ctx, cost.Attribution{
		ProjectID:   projectID,
		StrategyID:  &strategyID,
		HopID:       &hopID,
		VariationID: &variationID,
	}, "codegen", stats.Model, tokens)
	if err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("could not record spend: %v", err))
		return
	}
	if entry != nil {
		logger(domain.LogLevelInfo, fmt.Sprintf("Cost: $%.4f (%s)", entry.AmountUSD, stats.Model))
	}
}

// pauseForBudget parks a run that reached its spend ceiling.
//
// The variation goes to 'blocked' rather than 'error' because nothing went
// wrong: the code written so far is intact in the work directory, and the only
// open question is whether finishing it is worth more money. That decision
// belongs to a person, so the run stops and says what it would take.
func (g *Generator) pauseForBudget(
	ctx context.Context,
	variation *domain.Variation,
	execResult *executor.Result,
	budget runBudget,
	logger func(domain.LogLevel, string),
) {
	if err := g.db.PauseVariationForBudget(ctx, variation.ID, execResult.SpendUSD, budget.LimitUSD); err != nil {
		logger(domain.LogLevelError, fmt.Sprintf("could not record the spend pause: %v", err))
	}

	logger(domain.LogLevelMilestone, fmt.Sprintf(
		"Paused at its spend ceiling: $%.2f of $%.2f after %d rounds and %d tool calls. "+
			"The work so far is kept. Review the log and continue it if the remaining work is worth more.",
		execResult.SpendUSD, budget.LimitUSD,
		execResult.Stats.APIRounds, execResult.Stats.ToolCalls))

	g.transitionState(ctx, variation.ID, domain.VariationStatusCreating,
		domain.VariationStatusBlocked, "reached its spend ceiling")
}
