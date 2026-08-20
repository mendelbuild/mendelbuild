package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ToolDef defines a tool for the Anthropic API.
type ToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// Tools returns the tool definitions for the API.
func Tools() []ToolDef {
	return []ToolDef{
		{
			Name:        "Read",
			Description: "Read the contents of a file. Returns the file contents with line numbers.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Absolute path to the file to read",
					},
					"offset": map[string]interface{}{
						"type":        "integer",
						"description": "Line number to start reading from (0-indexed). Optional.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of lines to read. Optional, defaults to entire file.",
					},
				},
				"required": []string{"file_path"},
			},
		},
		{
			Name:        "Write",
			Description: "Write content to a file, creating it if it doesn't exist or overwriting if it does.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Absolute path to the file to write",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "Content to write to the file",
					},
				},
				"required": []string{"file_path", "content"},
			},
		},
		{
			Name:        "Edit",
			Description: "Replace a specific string in a file with new content. The old_string must match exactly and uniquely.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Absolute path to the file to edit",
					},
					"old_string": map[string]interface{}{
						"type":        "string",
						"description": "The exact string to find and replace",
					},
					"new_string": map[string]interface{}{
						"type":        "string",
						"description": "The string to replace it with",
					},
				},
				"required": []string{"file_path", "old_string", "new_string"},
			},
		},
		{
			Name:        "Bash",
			Description: "Execute a bash command and return its output. Use for running tests, git commands, etc.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "The bash command to execute",
					},
					"timeout": map[string]interface{}{
						"type":        "integer",
						"description": "Timeout in seconds. Optional, defaults to 120.",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "Glob",
			Description: "Find files matching a glob pattern.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern": map[string]interface{}{
						"type":        "string",
						"description": "Glob pattern to match files (e.g., 'src/**/*.ts')",
					},
				},
				"required": []string{"pattern"},
			},
		},
		{
			Name:        "Grep",
			Description: "Search for a pattern in files.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern": map[string]interface{}{
						"type":        "string",
						"description": "The regex pattern to search for",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Directory or file to search in. Defaults to current directory.",
					},
					"include": map[string]interface{}{
						"type":        "string",
						"description": "File pattern to include (e.g., '*.ts'). Optional.",
					},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

// ToolExecutor executes tools in a working directory.
type ToolExecutor struct {
	workDir string
}

// NewToolExecutor creates a new tool executor.
func NewToolExecutor(workDir string) *ToolExecutor {
	return &ToolExecutor{workDir: workDir}
}

// Execute runs a tool and returns the result.
func (e *ToolExecutor) Execute(ctx context.Context, name string, input map[string]interface{}) (string, error) {
	switch name {
	case "Read":
		return e.read(input)
	case "Write":
		return e.write(input)
	case "Edit":
		return e.edit(input)
	case "Bash":
		return e.bash(ctx, input)
	case "Glob":
		return e.glob(input)
	case "Grep":
		return e.grep(ctx, input)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (e *ToolExecutor) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(e.workDir, path)
}

func (e *ToolExecutor) read(input map[string]interface{}) (string, error) {
	path, _ := input["file_path"].(string)
	if path == "" {
		return "", fmt.Errorf("file_path is required")
	}
	path = e.resolvePath(path)

	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(content), "\n")
	offset := 0
	limit := len(lines)

	if o, ok := input["offset"].(float64); ok {
		offset = int(o)
	}
	if l, ok := input["limit"].(float64); ok {
		limit = int(l)
	}

	if offset > len(lines) {
		offset = len(lines)
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}

	var result strings.Builder
	for i := offset; i < end; i++ {
		result.WriteString(fmt.Sprintf("%d\t%s\n", i+1, lines[i]))
	}
	return result.String(), nil
}

func (e *ToolExecutor) write(input map[string]interface{}) (string, error) {
	path, _ := input["file_path"].(string)
	content, _ := input["content"].(string)
	if path == "" {
		return "", fmt.Errorf("file_path is required")
	}
	path = e.resolvePath(path)

	// Create parent directories if needed
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path), nil
}

func (e *ToolExecutor) edit(input map[string]interface{}) (string, error) {
	path, _ := input["file_path"].(string)
	oldStr, _ := input["old_string"].(string)
	newStr, _ := input["new_string"].(string)
	if path == "" || oldStr == "" {
		return "", fmt.Errorf("file_path and old_string are required")
	}
	path = e.resolvePath(path)

	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	count := strings.Count(string(content), oldStr)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in file")
	}
	if count > 1 {
		return "", fmt.Errorf("old_string found %d times, must be unique", count)
	}

	newContent := strings.Replace(string(content), oldStr, newStr, 1)
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully edited %s", path), nil
}

func (e *ToolExecutor) bash(ctx context.Context, input map[string]interface{}) (string, error) {
	command, _ := input["command"].(string)
	if command == "" {
		return "", fmt.Errorf("command is required")
	}

	timeout := 120 * time.Second
	if t, ok := input["timeout"].(float64); ok {
		timeout = time.Duration(t) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = e.workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := stdout.String()
	if stderr.Len() > 0 {
		result += "\nSTDERR:\n" + stderr.String()
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return result, fmt.Errorf("command timed out after %v", timeout)
		}
		return result, fmt.Errorf("command failed: %w", err)
	}

	return result, nil
}

func (e *ToolExecutor) glob(input map[string]interface{}) (string, error) {
	pattern, _ := input["pattern"].(string)
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	// Use find for recursive glob support
	cmd := exec.Command("find", e.workDir, "-type", "f", "-path", "*"+pattern+"*")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Run()

	// Also try filepath.Glob for simple patterns
	fullPattern := filepath.Join(e.workDir, pattern)
	matches, _ := filepath.Glob(fullPattern)

	var result []string
	for _, m := range matches {
		rel, _ := filepath.Rel(e.workDir, m)
		result = append(result, rel)
	}

	// Combine and dedupe
	seen := make(map[string]bool)
	for _, r := range result {
		seen[r] = true
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line != "" {
			rel, _ := filepath.Rel(e.workDir, line)
			seen[rel] = true
		}
	}

	var files []string
	for f := range seen {
		files = append(files, f)
	}

	if len(files) == 0 {
		return "No files found matching pattern", nil
	}

	return strings.Join(files, "\n"), nil
}

func (e *ToolExecutor) grep(ctx context.Context, input map[string]interface{}) (string, error) {
	pattern, _ := input["pattern"].(string)
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	path := e.workDir
	if p, ok := input["path"].(string); ok && p != "" {
		path = e.resolvePath(p)
	}

	args := []string{"-rn", pattern, path}
	if include, ok := input["include"].(string); ok && include != "" {
		args = []string{"-rn", "--include=" + include, pattern, path}
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "grep", args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Run() // Ignore error - grep returns 1 for no matches

	result := stdout.String()
	if result == "" {
		return "No matches found", nil
	}

	// Limit output
	lines := strings.Split(result, "\n")
	if len(lines) > 100 {
		result = strings.Join(lines[:100], "\n") + fmt.Sprintf("\n... (%d more lines)", len(lines)-100)
	}

	return result, nil
}

// ToJSON converts tool input to JSON for logging.
func ToJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
