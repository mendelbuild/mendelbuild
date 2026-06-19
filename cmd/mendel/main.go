package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/web"
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

Environment:
  MENDEL_DB_URL     Postgres connection string (default: postgres://localhost:5432/mendelbuild?sslmode=disable)

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
