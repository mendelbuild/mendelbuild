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
	"github.com/bhs/mendelbuild/internal/web"
	"github.com/google/uuid"
)

const defaultConnString = "postgres://localhost:5432/mendelbuild?sslmode=disable"

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
	case "load-strategy":
		loadStrategy(args)
	case "migrate":
		runMigrations(args)
	case "propose-roadmap":
		proposeRoadmap(args)
	case "generate":
		runGenerate(args)
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
  load-strategy     Load a strategy from JSON file
  migrate           Run database migrations
  propose-roadmap   Generate a roadmap proposal for a strategy
  generate          Run code generation for a hop's approved variations

Environment:
  MENDEL_DB_URL       Postgres connection string (default: postgres://localhost:5432/mendelbuild?sslmode=disable)
  ANTHROPIC_API_KEY   API key for Anthropic Claude (required for propose-roadmap, generate)
  MENDEL_WORK_DIR     Working directory for git clones (default: /tmp/mendel)

Run 'mendel <command> -h' for more information on a command.`)
}

func getConnString() string {
	if s := os.Getenv("MENDEL_DB_URL"); s != "" {
		return s
	}
	return defaultConnString
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

	server := web.NewServer(database, *addr)
	fmt.Printf("Starting server on %s\n", *addr)
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

	decision := &domain.Decision{
		ID:               uuid.New(),
		Kind:             domain.DecisionKindRoadmapReview,
		Title:            fmt.Sprintf("Roadmap Review: %s", strategy.Name),
		Details:          &roadmapStr,
		ObjectivityScore: 0.3, // Roadmap review is subjective
		ImportanceScore:  0.8, // Roadmaps are important
		Status:           domain.DecisionStatusNeedsAssignment,
		SubjectType:      strPtr("strategy"),
		SubjectID:        &strategyUUID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := database.CreateDecision(ctx, decision); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating decision: %v\n", err)
		os.Exit(1)
	}

	// Create initial agent message
	tokensUsed := tokens
	agentMessage := &domain.DecisionMessage{
		ID:         uuid.New(),
		DecisionID: decision.ID,
		Role:       "agent",
		Content:    fmt.Sprintf("Generated initial roadmap proposal with %d hops.", len(roadmap.Hops)),
		TokensUsed: &tokensUsed,
		CreatedAt:  now,
	}

	if err := database.CreateDecisionMessage(ctx, agentMessage); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating decision message: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created decision %s\n", decision.ID)
	fmt.Printf("Tokens used: %d\n", tokens)
	fmt.Printf("Proposed %d hops:\n", len(roadmap.Hops))
	for i, hop := range roadmap.Hops {
		fmt.Printf("  %d. %s\n", i+1, hop.Name)
	}
	fmt.Printf("\nView at: http://localhost:8080/p/<project-id>/decisions/%s\n", decision.ID)
}

func strPtr(s string) *string {
	return &s
}

func runGenerate(args []string) {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	decisionID := fs.String("decision", "", "Approved variation_review decision UUID")
	concurrency := fs.Int("concurrency", 2, "Number of parallel generators")
	fs.Parse(args)

	if *decisionID == "" {
		fmt.Fprintln(os.Stderr, "usage: mendel generate -decision <uuid>")
		os.Exit(1)
	}

	decisionUUID, err := uuid.Parse(*decisionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid decision UUID: %v\n", err)
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

	// Load decision
	decision, err := database.GetDecision(ctx, decisionUUID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading decision: %v\n", err)
		os.Exit(1)
	}

	if decision.Kind != domain.DecisionKindVariationReview {
		fmt.Fprintf(os.Stderr, "Decision is not a variation_review (kind: %s)\n", decision.Kind)
		os.Exit(1)
	}

	if decision.Status != domain.DecisionStatusResolved || decision.Resolution == nil || *decision.Resolution != "approved" {
		fmt.Fprintln(os.Stderr, "Decision must be approved before generating code")
		os.Exit(1)
	}

	if decision.SubjectID == nil {
		fmt.Fprintln(os.Stderr, "Decision has no hop associated")
		os.Exit(1)
	}

	hopID := *decision.SubjectID

	// Parse variation proposal from decision details
	if decision.Details == nil {
		fmt.Fprintln(os.Stderr, "Decision has no variation proposal")
		os.Exit(1)
	}

	proposal, err := codegen.ParseVariationProposal(*decision.Details)
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
