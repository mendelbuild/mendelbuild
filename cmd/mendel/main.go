package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/codegen"
	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/hosting"
	"github.com/bhs/mendelbuild/internal/web"
	"github.com/google/uuid"
)

const defaultConnString = "postgres://localhost:5432/mendelbuild?sslmode=disable"

// Version and BuildTime are set at build time via ldflags
var Version = "dev"
var BuildTime = ""

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "serve":
		runServer(args)
	case "setup":
		runSetup(args)
	case "load-strategy":
		loadStrategy(args)
	case "migrate":
		runMigrations(args)
	case "propose-roadmap":
		proposeRoadmap(args)
	case "generate":
		runGenerate(args)
	case "assign-owner":
		assignOwner(args)
	case "platforms":
		runPlatforms(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Usage: mendel <command> [args]

Commands:
  serve             Start the MendelBuild server (HTTP API + webapp)
  setup             Initialize Mendel (seed hosting platforms, deployment combos)
  migrate           Run database migrations
  load-strategy     Load a strategy from JSON file
  propose-roadmap   Generate a roadmap proposal for a strategy
  generate          Run code generation for a hop's approved variations
  assign-owner      Assign a user as owner to all projects without an owner
  platforms         Manage hosting platforms (list, refresh)

Environment:
  MENDEL_DB_URL       Postgres connection string (default: postgres://localhost:5432/mendelbuild?sslmode=disable)
  ANTHROPIC_API_KEY   API key for Anthropic Claude (required for propose-roadmap, generate)
  MENDEL_WORK_DIR     Working directory for git clones (default: ~/.mendel/work)

Run 'mendel <command> -h' for more information on a command.`)
}

func getConnString() string {
	if s := os.Getenv("MENDEL_DB_URL"); s != "" {
		return s
	}
	return defaultConnString
}

func runSetup(args []string) {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	fs.Parse(args)

	ctx := context.Background()
	database, err := db.Connect(ctx, getConnString())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	fmt.Println("Running Mendel setup...")

	// Seed hosting platforms
	platformCount, err := hosting.SeedIfEmpty(ctx, database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error seeding hosting platforms: %v\n", err)
		os.Exit(1)
	}
	if platformCount > 0 {
		fmt.Printf("  Seeded %d hosting platforms\n", platformCount)
	} else {
		fmt.Println("  Hosting platforms already seeded")
	}

	// Seed deployment combos
	comboCount, err := hosting.SeedCombosIfEmpty(ctx, database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error seeding deployment combos: %v\n", err)
		os.Exit(1)
	}
	if comboCount > 0 {
		fmt.Printf("  Seeded %d deployment combos\n", comboCount)
	} else {
		fmt.Println("  Deployment combos already seeded")
	}

	fmt.Println("Setup complete.")
}

func runServer(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "HTTP listen address")
	fs.Parse(args)

	ctx := context.Background()
	database, err := db.Connect(ctx, getConnString())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	server := web.NewServer(database, *addr, Version, BuildTime)
	fmt.Printf("Starting server on %s (version: %s)\n", *addr, Version)
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func loadStrategy(args []string) {
	fs := flag.NewFlagSet("load-strategy", flag.ExitOnError)
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: mendel load-strategy <file.json>")
		os.Exit(1)
	}

	filename := fs.Arg(0)
	data, err := os.ReadFile(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		os.Exit(1)
	}

	var input domain.StrategyInput
	if err := json.Unmarshal(data, &input); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing JSON: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	database, err := db.Connect(ctx, getConnString())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	projectID, err := database.LoadStrategy(ctx, &input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading strategy: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Loaded strategy for project %s (ID: %s)\n", input.Project, projectID)
}

func runMigrations(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	down := fs.Int("down", 0, "Number of migrations to revert")
	fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	database, err := db.Connect(ctx, getConnString())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	if *down > 0 {
		if err := database.MigrateDown(ctx, *down); err != nil {
			fmt.Fprintf(os.Stderr, "Error reverting migrations: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := database.Migrate(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error running migrations: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Println("Migrations complete.")
}

func proposeRoadmap(args []string) {
	fs := flag.NewFlagSet("propose-roadmap", flag.ExitOnError)
	strategyID := fs.String("strategy", "", "Strategy UUID")
	fs.Parse(args)

	if *strategyID == "" {
		fmt.Fprintln(os.Stderr, "usage: mendel propose-roadmap -strategy <uuid>")
		os.Exit(1)
	}

	strategyUUID, err := uuid.Parse(*strategyID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid strategy UUID: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	database, err := db.Connect(ctx, getConnString())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	// Load strategy
	strategy, err := database.GetStrategy(ctx, strategyUUID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading strategy: %v\n", err)
		os.Exit(1)
	}

	// Load objectives with key results
	objectives, err := database.GetObjectivesByStrategy(ctx, strategyUUID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading objectives: %v\n", err)
		os.Exit(1)
	}

	var objInfos []agent.ObjectiveInfo
	for _, obj := range objectives {
		krs, err := database.GetKeyResultsByObjective(ctx, obj.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading key results: %v\n", err)
			os.Exit(1)
		}

		var krInfos []agent.KeyResultInfo
		for _, kr := range krs {
			krInfo := agent.KeyResultInfo{
				ID:          kr.ID.String(),
				Description: kr.Description,
				TargetUnits: kr.TargetUnits,
			}
			if kr.TargetDate != nil {
				date := kr.TargetDate.Format("2006-01-02")
				krInfo.TargetDate = &date
			}
			krInfos = append(krInfos, krInfo)
		}

		objInfos = append(objInfos, agent.ObjectiveInfo{
			ID:          obj.ID.String(),
			Description: obj.Description,
			KeyResults:  krInfos,
		})
	}

	// Load funding sources
	funding, err := database.GetFundingSourcesByStrategy(ctx, strategyUUID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading funding: %v\n", err)
		os.Exit(1)
	}

	var fundingEstimates []agent.ResourceEstimate
	for _, f := range funding {
		fundingEstimates = append(fundingEstimates, agent.ResourceEstimate{
			ResourceType: string(f.ResourceType),
			Amount:       f.Amount,
		})
	}

	strategyContext := agent.StrategyContext{
		ID:         strategyUUID.String(),
		Name:       strategy.Name,
		Objectives: objInfos,
		Funding:    fundingEstimates,
	}

	// Create Anthropic client
	client, err := agent.NewClient("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating agent client: %v\n", err)
		os.Exit(1)
	}

	// Generate proposal
	fmt.Println("Generating roadmap proposal...")
	proposer := agent.NewProposer(client)
	roadmap, tokens, err := proposer.ProposeRoadmap(ctx, strategyContext)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating proposal: %v\n", err)
		os.Exit(1)
	}

	// Create decision record
	now := time.Now()
	roadmapJSON, err := json.MarshalIndent(roadmap, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling roadmap: %v\n", err)
		os.Exit(1)
	}
	roadmapStr := string(roadmapJSON)

	inputRequest := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        strategy.ProjectID,
		Kind:             domain.InputRequestKindRoadmapReview,
		Title:            fmt.Sprintf("Roadmap Review: %s", strategy.Name),
		Details:          &roadmapStr,
		ObjectivityScore: 0.3, // Roadmap review is subjective
		ImportanceScore:  0.8, // Roadmaps are important
		Status:           domain.InputRequestStatusNeedsAssignment,
		SubjectType:      strPtr("strategy"),
		SubjectID:        &strategyUUID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := database.CreateInputRequest(ctx, inputRequest); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating input request: %v\n", err)
		os.Exit(1)
	}

	// Create initial agent message
	tokensUsed := tokens
	agentMessage := &domain.InputRequestMessage{
		ID:             uuid.New(),
		InputRequestID: inputRequest.ID,
		Role:           "agent",
		Content:        fmt.Sprintf("Generated initial roadmap proposal with %d hops.", len(roadmap.Hops)),
		TokensUsed:     &tokensUsed,
		CreatedAt:      now,
	}

	if err := database.CreateInputRequestMessage(ctx, agentMessage); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating input request message: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created input request %s\n", inputRequest.ID)
	fmt.Printf("Tokens used: %d\n", tokens)
	fmt.Printf("Proposed %d hops:\n", len(roadmap.Hops))
	for i, hop := range roadmap.Hops {
		fmt.Printf("  %d. %s\n", i+1, hop.Name)
	}
	fmt.Printf("\nView at: http://localhost:8080/p/<project-id>/inputs/%s\n", inputRequest.ID)
}

func strPtr(s string) *string {
	return &s
}

func runGenerate(args []string) {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	inputRequestID := fs.String("input-request", "", "Approved variation_review input request UUID")
	concurrency := fs.Int("concurrency", 2, "Number of parallel generators")
	fs.Parse(args)

	if *inputRequestID == "" {
		fmt.Fprintln(os.Stderr, "usage: mendel generate -input-request <uuid>")
		os.Exit(1)
	}

	inputRequestUUID, err := uuid.Parse(*inputRequestID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid input request UUID: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	database, err := db.Connect(ctx, getConnString())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	// Load input request
	inputRequest, err := database.GetInputRequest(ctx, inputRequestUUID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading input request: %v\n", err)
		os.Exit(1)
	}

	if inputRequest.Kind != domain.InputRequestKindVariationReview {
		fmt.Fprintf(os.Stderr, "Input request is not a variation_review (kind: %s)\n", inputRequest.Kind)
		os.Exit(1)
	}

	if inputRequest.Status != domain.InputRequestStatusResolved || inputRequest.Resolution == nil || *inputRequest.Resolution != "approved" {
		fmt.Fprintln(os.Stderr, "Input request must be approved before generating code")
		os.Exit(1)
	}

	if inputRequest.SubjectID == nil {
		fmt.Fprintln(os.Stderr, "Input request has no hop associated")
		os.Exit(1)
	}

	hopID := *inputRequest.SubjectID

	// Parse variation proposal from input request details
	if inputRequest.Details == nil {
		fmt.Fprintln(os.Stderr, "Input request has no variation proposal")
		os.Exit(1)
	}

	proposal, err := codegen.ParseVariationProposal(*inputRequest.Details)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing variation proposal: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Starting code generation for %d variations...\n", len(proposal.Variations))

	// Run orchestrator
	orchestrator := codegen.NewOrchestrator(database, *concurrency)
	config := codegen.GeneratorConfig{} // Config will be loaded from DB

	result, err := orchestrator.Orchestrate(ctx, hopID, proposal, config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running orchestrator: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nCode generation complete:\n")
	fmt.Printf("  Success: %d\n", result.SuccessCount)
	fmt.Printf("  Failed:  %d\n", result.FailureCount)
	fmt.Printf("  Tokens:  %d\n", result.TotalTokens)

	for _, r := range result.Results {
		status := "SUCCESS"
		if !r.Success {
			status = "FAILED"
		}
		fmt.Printf("\n  %s: %s\n", r.VariationID, status)
		if r.BranchName != "" {
			fmt.Printf("    Branch: %s\n", r.BranchName)
		}
		if r.CommitRef != "" {
			fmt.Printf("    Commit: %s\n", r.CommitRef[:8])
		}
		if r.Error != "" {
			fmt.Printf("    Error: %s\n", r.Error)
		}
	}
}

func assignOwner(args []string) {
	fs := flag.NewFlagSet("assign-owner", flag.ExitOnError)
	email := fs.String("email", "", "Email address of user to assign as owner")
	fs.Parse(args)

	if *email == "" {
		fmt.Fprintln(os.Stderr, "error: -email is required")
		fs.Usage()
		os.Exit(1)
	}

	ctx := context.Background()
	database, err := db.Connect(ctx, getConnString())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	// Find or create user by email
	user, err := database.GetUserByEmail(ctx, *email)
	if err != nil {
		// User doesn't exist, create them
		user = &domain.User{
			ID:        uuid.New(),
			Email:     *email,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := database.CreateUser(ctx, user); err != nil {
			fmt.Fprintf(os.Stderr, "error creating user: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created user: %s (%s)\n", user.Email, user.ID)
	} else {
		fmt.Printf("Found existing user: %s (%s)\n", user.Email, user.ID)
	}

	// Assign as owner to all projects without an owner
	count, err := database.AssignOwnerToUnownedProjects(ctx, user.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error assigning ownership: %v\n", err)
		os.Exit(1)
	}

	if count == 0 {
		fmt.Println("No unowned projects found.")
	} else {
		fmt.Printf("Assigned %s as owner to %d project(s).\n", *email, count)
	}
}

func runPlatforms(args []string) {
	if len(args) < 1 {
		fmt.Println(`Usage: mendel platforms <subcommand>

Subcommands:
  list      List all available hosting platforms
  refresh   Reset to default platforms (updates existing, adds new)
  seed      Seed defaults only if table is empty`)
		os.Exit(1)
	}

	subcmd := args[0]

	ctx := context.Background()
	database, err := db.Connect(ctx, getConnString())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	switch subcmd {
	case "list":
		platforms, err := database.ListHostingPlatforms(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error listing platforms: %v\n", err)
			os.Exit(1)
		}
		if len(platforms) == 0 {
			fmt.Println("No hosting platforms configured. Run 'mendel platforms seed' to add defaults.")
			return
		}
		fmt.Printf("%-15s %-25s %-25s\n", "SLUG", "NAME", "DEPLOYER IMAGE")
		fmt.Printf("%-15s %-25s %-25s\n", "----", "----", "--------------")
		for _, p := range platforms {
			fmt.Printf("%-15s %-25s %-25s\n", p.Slug, p.Name, p.DeployerImage)
		}

	case "refresh":
		count, err := hosting.RefreshAll(ctx, database)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error refreshing platforms: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Refreshed %d hosting platforms.\n", count)

	case "seed":
		count, err := hosting.SeedIfEmpty(ctx, database)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error seeding platforms: %v\n", err)
			os.Exit(1)
		}
		if count == 0 {
			fmt.Println("Platforms table already has data. Use 'mendel platforms refresh' to update.")
		} else {
			fmt.Printf("Seeded %d hosting platforms.\n", count)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}
