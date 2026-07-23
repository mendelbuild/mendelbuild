package codegen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/bhs/mendelbuild/internal/domain"
)

// CLIResult contains the result of a Claude CLI invocation.
type CLIResult struct {
	Success      bool    `json:"success"`
	Output       string  `json:"output"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalCost    float64 `json:"total_cost,omitempty"`
	Error        string  `json:"error,omitempty"`
}

// EventLogger is called with key events during CLI execution.
type EventLogger func(level domain.LogLevel, message string)

// CLI wraps the Claude CLI subprocess.
type CLI struct {
	workDir string
	apiKey  string
	logger  EventLogger
}

// NewCLI creates a new CLI wrapper.
func NewCLI(workDir, apiKey string) *CLI {
	return &CLI{
		workDir: workDir,
		apiKey:  apiKey,
	}
}

// WithLogger sets an event logger for capturing key events.
func (c *CLI) WithLogger(logger EventLogger) *CLI {
	c.logger = logger
	return c
}

func (c *CLI) log(level domain.LogLevel, format string, args ...interface{}) {
	if c.logger != nil {
		c.logger(level, fmt.Sprintf(format, args...))
	}
}

// Run executes the Claude CLI with the given prompt.
// It returns the result including token usage.
func (c *CLI) Run(ctx context.Context, prompt string) (*CLIResult, error) {
	c.log(domain.LogLevelMilestone, "Starting Claude CLI")

	// Build the command
	args := []string{
		"--print", // Non-interactive mode
		"--output-format", "json",
		"--dangerously-skip-permissions", // Allow file operations
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = c.workDir

	// Set up environment - filter out ANTHROPIC_API_KEY since Claude Code
	// should use its own claude.ai authentication, not our API key
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "ANTHROPIC_API_KEY=") {
			env = append(env, e)
		}
	}
	cmd.Env = env

	// Provide prompt via stdin
	cmd.Stdin = strings.NewReader(prompt)

	// Set up pipes for streaming output
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start command: %w", err)
	}

	// Read and process stdout line by line
	var outputLines []string
	scanner := bufio.NewScanner(stdoutPipe)
	// Increase buffer size for long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		outputLines = append(outputLines, line)
		c.processOutputLine(line)
	}

	// Wait for command to complete
	cmdErr := cmd.Wait()

	result := &CLIResult{
		Success: cmdErr == nil,
		Output:  strings.Join(outputLines, "\n"),
	}

	// Try to parse JSON output for token usage
	if len(outputLines) > 0 {
		parseTokenUsage(result.Output, result)
	}

	if cmdErr != nil {
		result.Error = stderr.String()
		if result.Error == "" {
			result.Error = cmdErr.Error()
		}
		c.log(domain.LogLevelError, "CLI failed: %s", result.Error)
		return result, nil // Return result even on error for partial info
	}

	c.log(domain.LogLevelMilestone, "Claude CLI completed (tokens: %d in, %d out)", result.InputTokens, result.OutputTokens)
	return result, nil
}

// processOutputLine parses a JSON output line and logs key events.
func (c *CLI) processOutputLine(line string) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return
	}

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return
	}

	// Check event type
	eventType, _ := event["type"].(string)

	switch eventType {
	case "tool_use":
		// Tool being called
		if name, ok := event["name"].(string); ok {
			switch name {
			case "Read":
				if input, ok := event["input"].(map[string]interface{}); ok {
					if filePath, ok := input["file_path"].(string); ok {
						c.log(domain.LogLevelInfo, "Reading: %s", shortenPath(filePath))
					}
				}
			case "Write":
				if input, ok := event["input"].(map[string]interface{}); ok {
					if filePath, ok := input["file_path"].(string); ok {
						c.log(domain.LogLevelMilestone, "Writing: %s", shortenPath(filePath))
					}
				}
			case "Edit":
				if input, ok := event["input"].(map[string]interface{}); ok {
					if filePath, ok := input["file_path"].(string); ok {
						c.log(domain.LogLevelMilestone, "Editing: %s", shortenPath(filePath))
					}
				}
			case "Bash":
				if input, ok := event["input"].(map[string]interface{}); ok {
					if command, ok := input["command"].(string); ok {
						// Truncate long commands
						if len(command) > 80 {
							command = command[:77] + "..."
						}
						c.log(domain.LogLevelInfo, "Running: %s", command)
					}
				}
			case "Glob", "Grep":
				c.log(domain.LogLevelInfo, "Searching files...")
			default:
				c.log(domain.LogLevelInfo, "Using tool: %s", name)
			}
		}
	case "assistant":
		// Periodic heartbeat for assistant messages
		// We don't log every chunk, just occasionally to show progress
	case "result":
		// Final result
		c.log(domain.LogLevelMilestone, "Generation complete")
	}
}

// shortenPath removes common prefixes to make paths more readable.
func shortenPath(path string) string {
	// Remove /tmp/mendel/<uuid>/ prefix
	if strings.HasPrefix(path, "/tmp/mendel/") {
		parts := strings.SplitN(path, "/", 5)
		if len(parts) >= 5 {
			return parts[4]
		}
	}
	return path
}

// parseTokenUsage attempts to extract token usage from JSON output.
func parseTokenUsage(output string, result *CLIResult) {
	// Claude CLI JSON output may contain usage info
	// Format varies, so we try multiple approaches

	// Look for lines with JSON objects
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(line), &data); err != nil {
			continue
		}

		// Check for usage field
		if usage, ok := data["usage"].(map[string]interface{}); ok {
			if input, ok := usage["input_tokens"].(float64); ok {
				result.InputTokens = int(input)
			}
			if output, ok := usage["output_tokens"].(float64); ok {
				result.OutputTokens = int(output)
			}
		}

		// Check for cost field
		if cost, ok := data["cost"].(float64); ok {
			result.TotalCost = cost
		}
	}
}

// BuildImplementationPrompt constructs the prompt for implementing a variation.
func BuildImplementationPrompt(hopName, variationName, approach string) string {
	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("# Task: Implement the '%s' variation for hop '%s'\n\n", variationName, hopName))
	prompt.WriteString("## Approach\n\n")
	prompt.WriteString(approach)
	prompt.WriteString("\n\n## Instructions\n\n")
	prompt.WriteString("1. Implement the approach described above\n")
	prompt.WriteString("2. Write clean code following existing style and patterns in this repository\n")
	prompt.WriteString("3. Create or modify files as needed\n")
	prompt.WriteString("4. Add appropriate tests for the new functionality\n")
	prompt.WriteString("5. Run tests and fix any failures before committing\n")

	prompt.WriteString("\n## IMPORTANT: `.mendel/` Directory Rules\n\n")
	prompt.WriteString("The `.mendel/` directory is for Mendel configuration ONLY. Allowed files:\n")
	prompt.WriteString("- `docker-compose.demo.yml` - Demo infrastructure\n")
	prompt.WriteString("- `docker-compose.test.yml` - Test infrastructure (REQUIRED)\n")
	prompt.WriteString("- `demo-config.yml` - Demo settings\n")
	prompt.WriteString("- `test-config.yml` - Test settings (REQUIRED)\n")
	prompt.WriteString("- `migration.json` - Migration instructions\n\n")
	prompt.WriteString("**DO NOT create any other files in `.mendel/`** - no documentation, no summaries,\n")
	prompt.WriteString("no ARCHITECTURE.md, no QUICK_START.md, no implementation-summary.md.\n")
	prompt.WriteString("Documentation belongs in the repo root or docs/ folder, not in `.mendel/`.\n")

	prompt.WriteString("\n## Testing Configuration (REQUIRED)\n\n")
	prompt.WriteString("**You MUST create/update `.mendel/test-config.yml` and `.mendel/docker-compose.test.yml`.**\n")
	prompt.WriteString("Tests run INSIDE Docker containers, not on the host. Mendel will run these tests\n")
	prompt.WriteString("after code generation and reject the variation if they fail.\n\n")
	prompt.WriteString("If you add, modify, or remove any tests, update the test configuration accordingly.\n\n")
	prompt.WriteString("### `.mendel/docker-compose.test.yml`\n\n")
	prompt.WriteString("```yaml\n")
	prompt.WriteString("services:\n")
	prompt.WriteString("  app:\n")
	prompt.WriteString("    build:\n")
	prompt.WriteString("      context: ..\n")
	prompt.WriteString("    # Add dependencies if tests need them:\n")
	prompt.WriteString("    # depends_on:\n")
	prompt.WriteString("    #   db:\n")
	prompt.WriteString("    #     condition: service_healthy\n")
	prompt.WriteString("    # environment:\n")
	prompt.WriteString("    #   DATABASE_URL: postgres://postgres:test@db:5432/test\n")
	prompt.WriteString("  # Add services if tests need them:\n")
	prompt.WriteString("  # db:\n")
	prompt.WriteString("  #   image: postgres:15\n")
	prompt.WriteString("  #   environment:\n")
	prompt.WriteString("  #     POSTGRES_PASSWORD: test\n")
	prompt.WriteString("  #     POSTGRES_DB: test\n")
	prompt.WriteString("  #   healthcheck:\n")
	prompt.WriteString("  #     test: [\"CMD-SHELL\", \"pg_isready -U postgres\"]\n")
	prompt.WriteString("  #     interval: 2s\n")
	prompt.WriteString("  #     timeout: 5s\n")
	prompt.WriteString("  #     retries: 10\n")
	prompt.WriteString("```\n\n")
	prompt.WriteString("### `.mendel/test-config.yml`\n\n")
	prompt.WriteString("```yaml\n")
	prompt.WriteString("version: 1\n")
	prompt.WriteString("service: app                    # Container to run tests in\n")
	prompt.WriteString("test_command: npm test          # Command to run inside container\n")
	prompt.WriteString("```\n\n")
	prompt.WriteString("Mendel runs: `docker-compose up` → `exec <service> <test_command>` → check exit code → `down`\n")

	prompt.WriteString("\n## Database/Datastore Migrations\n\n")
	prompt.WriteString("If this variation requires database or datastore schema changes:\n\n")
	prompt.WriteString("### 1. Create a REAL migration in the codebase\n")
	prompt.WriteString("- Look for an existing migration system (Rails migrations, Flyway, Alembic, Knex, Prisma, etc.)\n")
	prompt.WriteString("- If one exists, create a new migration file following the project's conventions\n")
	prompt.WriteString("- If none exists, create one appropriate for the project's stack\n")
	prompt.WriteString("- This migration will be merged to main when the variation is selected\n\n")
	prompt.WriteString("### 2. Write migration tests (CRITICAL)\n")
	prompt.WriteString("Migrations are high-risk. You MUST:\n")
	prompt.WriteString("- Write a test that applies the UP migration and verifies the schema is correct\n")
	prompt.WriteString("- Write a test that applies DOWN migration and verifies it cleanly reverts\n")
	prompt.WriteString("- Test that DOWN migration preserves unrelated data\n\n")
	prompt.WriteString("### 3. Create `.mendel/migration.json` for temporary demo/testing\n")
	prompt.WriteString("```json\n")
	prompt.WriteString("{\n")
	prompt.WriteString("  \"up_instructions\": \"Command to apply the migration\",\n")
	prompt.WriteString("  \"down_instructions\": \"Command to revert the migration\",\n")
	prompt.WriteString("  \"notes\": \"Path to migration files, e.g. 'db/migrations/20240101_add_users.sql'\"\n")
	prompt.WriteString("}\n")
	prompt.WriteString("```\n\n")
	prompt.WriteString("### 4. If no schema changes needed, skip all migration steps\n")

	prompt.WriteString("\n## Demo Configuration (Docker-based)\n\n")
	prompt.WriteString("Mendel runs demos using Docker Compose from the `.mendel/` directory.\n")
	prompt.WriteString("Create these files if they don't exist:\n\n")
	prompt.WriteString("### 1. `.mendel/docker-compose.demo.yml`\n\n")
	prompt.WriteString("This defines all services needed to run the demo:\n\n")
	prompt.WriteString("```yaml\n")
	prompt.WriteString("services:\n")
	prompt.WriteString("  app:\n")
	prompt.WriteString("    build:\n")
	prompt.WriteString("      context: ..\n")
	prompt.WriteString("    ports:\n")
	prompt.WriteString("      - \"3000\"  # Let Docker assign host port\n")
	prompt.WriteString("    depends_on:\n")
	prompt.WriteString("      db:\n")
	prompt.WriteString("        condition: service_healthy\n")
	prompt.WriteString("    environment:\n")
	prompt.WriteString("      DATABASE_URL: postgres://postgres:postgres@db:5432/app\n")
	prompt.WriteString("    healthcheck:\n")
	prompt.WriteString("      test: [\"CMD\", \"wget\", \"-q\", \"--spider\", \"http://localhost:3000/health\"]\n")
	prompt.WriteString("      interval: 5s\n")
	prompt.WriteString("      timeout: 5s\n")
	prompt.WriteString("      retries: 10\n")
	prompt.WriteString("  db:\n")
	prompt.WriteString("    image: postgres:15\n")
	prompt.WriteString("    environment:\n")
	prompt.WriteString("      POSTGRES_PASSWORD: postgres\n")
	prompt.WriteString("      POSTGRES_DB: app\n")
	prompt.WriteString("    healthcheck:\n")
	prompt.WriteString("      test: [\"CMD-SHELL\", \"pg_isready -U postgres\"]\n")
	prompt.WriteString("      interval: 2s\n")
	prompt.WriteString("      timeout: 5s\n")
	prompt.WriteString("      retries: 10\n")
	prompt.WriteString("```\n\n")
	prompt.WriteString("**Important:**\n")
	prompt.WriteString("- Use standard images (postgres, redis, node) not mendel/* images\n")
	prompt.WriteString("- Services communicate via service names (db:5432, not localhost:5432)\n")
	prompt.WriteString("- Expose container ports without fixed host mapping (\"3000\" not \"3000:3000\")\n")
	prompt.WriteString("- Add healthchecks so `docker-compose up --wait` knows when ready\n\n")
	prompt.WriteString("### 2. `.mendel/demo-config.yml`\n\n")
	prompt.WriteString("```yaml\n")
	prompt.WriteString("version: 1\n")
	prompt.WriteString("service: app          # Which docker-compose service to expose\n")
	prompt.WriteString("container_port: 3000  # Port inside the container\n")
	prompt.WriteString("health_path: /health  # Endpoint to check for readiness\n")
	prompt.WriteString("after_up:             # Optional: commands after containers start\n")
	prompt.WriteString("  - \"docker-compose exec app npm run migrate\"\n")
	prompt.WriteString("```\n\n")
	prompt.WriteString("Skip demo configuration if the variation has no deployment/UI impact.\n")

	prompt.WriteString("\n## Output\n\n")
	prompt.WriteString("Implement the changes directly. Provide a brief summary of what was done in your final commit message.\n")

	return prompt.String()
}
