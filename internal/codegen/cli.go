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

	// Set up environment
	env := os.Environ()
	if c.apiKey != "" {
		env = append(env, fmt.Sprintf("ANTHROPIC_API_KEY=%s", c.apiKey))
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
func BuildImplementationPrompt(hopName, variationName, approach, testCommand string) string {
	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("# Task: Implement the '%s' variation for hop '%s'\n\n", variationName, hopName))
	prompt.WriteString("## Approach\n\n")
	prompt.WriteString(approach)
	prompt.WriteString("\n\n## Instructions\n\n")
	prompt.WriteString("1. Implement the approach described above\n")
	prompt.WriteString("2. Write clean, well-documented code\n")
	prompt.WriteString("3. Follow existing code style and patterns in this repository\n")
	prompt.WriteString("4. Create or modify files as needed\n")
	prompt.WriteString("5. Add appropriate tests for the new functionality\n")

	if testCommand != "" {
		prompt.WriteString(fmt.Sprintf("\n## Testing\n\nAfter implementation, run: `%s`\n", testCommand))
		prompt.WriteString("Fix any failing tests before considering the task complete.\n")
	}

	prompt.WriteString("\n## Database/Datastore Migrations\n\n")
	prompt.WriteString("If this variation requires database or datastore schema changes:\n\n")
	prompt.WriteString("### 1. Create a REAL migration in the codebase\n")
	prompt.WriteString("- Look for an existing migration system (Rails migrations, Flyway, Alembic, Knex, Prisma, etc.)\n")
	prompt.WriteString("- If one exists, create a new migration file following the project's conventions\n")
	prompt.WriteString("- If none exists, create one appropriate for the project's stack\n")
	prompt.WriteString("- This migration will be merged to main when the variation is selected\n\n")
	prompt.WriteString("### 2. Create `.mendel/migration.json` for temporary demo/testing\n")
	prompt.WriteString("This file contains instructions to apply/revert the schema changes for demo purposes:\n")
	prompt.WriteString("```json\n")
	prompt.WriteString("{\n")
	prompt.WriteString("  \"up_instructions\": \"Instructions to apply the migration for demo. Reference MENDEL.md or specify commands.\",\n")
	prompt.WriteString("  \"down_instructions\": \"Instructions to cleanly revert. Must NOT affect unrelated data.\",\n")
	prompt.WriteString("  \"notes\": \"Where to find the real migration files, e.g. 'db/migrations/20240101_add_users_table.sql' or 'prisma/migrations/...'\"\n")
	prompt.WriteString("}\n")
	prompt.WriteString("```\n")
	prompt.WriteString("- The temporary migration is reverted when the variation ends (win or lose)\n")
	prompt.WriteString("- The real migration runs through the normal deployment process after merge\n\n")
	prompt.WriteString("### 3. If no schema changes needed, skip both steps\n")

	prompt.WriteString("\n## Output\n\n")
	prompt.WriteString("Implement the changes directly. Summarize what was done at the end.\n")

	return prompt.String()
}
