package codegen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CLIResult contains the result of a Claude CLI invocation.
type CLIResult struct {
	Success      bool   `json:"success"`
	Output       string `json:"output"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalCost    float64 `json:"total_cost,omitempty"`
	Error        string `json:"error,omitempty"`
}

// CLI wraps the Claude CLI subprocess.
type CLI struct {
	workDir string
	apiKey  string
}

// NewCLI creates a new CLI wrapper.
func NewCLI(workDir, apiKey string) *CLI {
	return &CLI{
		workDir: workDir,
		apiKey:  apiKey,
	}
}

// Run executes the Claude CLI with the given prompt.
// It returns the result including token usage.
func (c *CLI) Run(ctx context.Context, prompt string) (*CLIResult, error) {
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

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := &CLIResult{
		Success: err == nil,
		Output:  stdout.String(),
	}

	// Try to parse JSON output for token usage
	if stdout.Len() > 0 {
		parseTokenUsage(stdout.String(), result)
	}

	if err != nil {
		result.Error = stderr.String()
		if result.Error == "" {
			result.Error = err.Error()
		}
		return result, nil // Return result even on error for partial info
	}

	return result, nil
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

	prompt.WriteString("\n## Output\n\n")
	prompt.WriteString("Implement the changes directly. Summarize what was done at the end.\n")

	return prompt.String()
}
