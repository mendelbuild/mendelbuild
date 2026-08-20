package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	anthropicAPIURL = "https://api.anthropic.com/v1/messages"
	defaultModel    = "claude-sonnet-4-20250514"
	maxTokens       = 16384
	maxRounds       = 50
)

// Executor runs a tool loop with the Anthropic API.
type Executor struct {
	apiKey       string
	model        string
	workDir      string
	tools        *ToolExecutor
	eventHandler EventHandler
	stats        Stats
}

// New creates a new executor.
func New(apiKey, workDir string) *Executor {
	return &Executor{
		apiKey:  apiKey,
		model:   defaultModel,
		workDir: workDir,
		tools:   NewToolExecutor(workDir),
	}
}

// WithModel sets the model to use.
func (e *Executor) WithModel(model string) *Executor {
	e.model = model
	return e
}

// WithEventHandler sets the event handler for progress events.
func (e *Executor) WithEventHandler(handler EventHandler) *Executor {
	e.eventHandler = handler
	return e
}

// emit sends an event to the handler if set.
func (e *Executor) emit(event Event) {
	event.Timestamp = time.Now()
	if e.eventHandler != nil {
		e.eventHandler(event)
	}
}

// Stats returns the current execution statistics.
func (e *Executor) Stats() Stats {
	return e.stats
}

// Result holds the final result of an execution.
type Result struct {
	Success bool
	Output  string
	Stats   Stats
	Error   error
}

// Run executes the prompt with tool loop until completion.
func (e *Executor) Run(ctx context.Context, systemPrompt, userPrompt string) (*Result, error) {
	e.stats = Stats{StartTime: time.Now()}
	e.emit(Event{Type: EventStart})

	messages := []message{
		{Role: "user", Content: []contentBlock{{Type: "text", Text: userPrompt}}},
	}

	var lastText string

	for round := 0; round < maxRounds; round++ {
		e.stats.APIRounds++
		e.emit(Event{Type: EventAPICall})

		resp, err := e.callAPI(ctx, systemPrompt, messages)
		if err != nil {
			e.stats.EndTime = time.Now()
			e.emit(Event{Type: EventError, Error: err})
			return &Result{Success: false, Stats: e.stats, Error: err}, err
		}

		// Update stats
		e.stats.InputTokens += resp.Usage.InputTokens
		e.stats.OutputTokens += resp.Usage.OutputTokens
		e.stats.CacheRead += resp.Usage.CacheReadInputTokens
		e.stats.CacheWrite += resp.Usage.CacheCreationInputTokens

		e.emit(Event{
			Type:         EventAPIResponse,
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			CacheRead:    resp.Usage.CacheReadInputTokens,
		})

		// Process response content
		var assistantContent []contentBlock
		var toolUses []toolUse

		for _, block := range resp.Content {
			assistantContent = append(assistantContent, block)

			switch block.Type {
			case "text":
				lastText = block.Text
				e.emit(Event{Type: EventText, Text: block.Text})

			case "tool_use":
				toolUses = append(toolUses, toolUse{
					ID:    block.ID,
					Name:  block.Name,
					Input: block.Input,
				})
			}
		}

		// Add assistant response to messages
		messages = append(messages, message{Role: "assistant", Content: assistantContent})

		// If no tool use, we're done
		if len(toolUses) == 0 {
			e.stats.EndTime = time.Now()
			e.emit(Event{Type: EventComplete})
			return &Result{
				Success: true,
				Output:  lastText,
				Stats:   e.stats,
			}, nil
		}

		// Execute tools and collect results
		var toolResults []contentBlock
		for _, tu := range toolUses {
			e.stats.ToolCalls++
			e.emit(Event{
				Type:      EventToolCall,
				ToolName:  tu.Name,
				ToolInput: tu.Input,
			})

			result, err := e.tools.Execute(ctx, tu.Name, tu.Input)

			var resultContent string
			var isError bool
			if err != nil {
				resultContent = fmt.Sprintf("Error: %v", err)
				isError = true
				e.emit(Event{Type: EventToolResult, ToolName: tu.Name, ToolError: resultContent})
			} else {
				resultContent = result
				e.emit(Event{Type: EventToolResult, ToolName: tu.Name, ToolResult: truncate(result, 500)})
			}

			toolResults = append(toolResults, contentBlock{
				Type:      "tool_result",
				ToolUseID: tu.ID,
				Content:   resultContent,
				IsError:   isError,
			})
		}

		// Add tool results to messages
		messages = append(messages, message{Role: "user", Content: toolResults})
	}

	e.stats.EndTime = time.Now()
	err := fmt.Errorf("exceeded maximum rounds (%d)", maxRounds)
	e.emit(Event{Type: EventError, Error: err})
	return &Result{Success: false, Output: lastText, Stats: e.stats, Error: err}, err
}

// API types

type message struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text,omitempty"`
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	Content   string                 `json:"content,omitempty"`
	IsError   bool                   `json:"is_error,omitempty"`
}

type toolUse struct {
	ID    string
	Name  string
	Input map[string]interface{}
}

type apiRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
	Tools     []ToolDef `json:"tools,omitempty"`
}

type apiResponse struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
	Usage   struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	StopReason string `json:"stop_reason"`
}

type apiError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e *Executor) callAPI(ctx context.Context, systemPrompt string, messages []message) (*apiResponse, error) {
	req := apiRequest{
		Model:     e.model,
		MaxTokens: maxTokens,
		System:    systemPrompt,
		Messages:  messages,
		Tools:     Tools(),
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", e.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		var apiErr apiError
		json.Unmarshal(respBody, &apiErr)
		return nil, fmt.Errorf("API error %d: %s - %s", resp.StatusCode, apiErr.Error.Type, apiErr.Error.Message)
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &apiResp, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// SystemPrompt returns a coding-focused system prompt.
func SystemPrompt() string {
	return `You are an expert software engineer implementing code changes in a repository.

You have access to these tools:
- Read: Read file contents
- Write: Create or overwrite files
- Edit: Replace specific strings in files (old_string must be unique)
- Bash: Run shell commands
- Glob: Find files matching patterns
- Grep: Search for patterns in files

Guidelines:
- Read existing files before modifying them to understand context
- Use Edit for surgical changes, Write for new files or complete rewrites
- Run tests after making changes to verify correctness
- Keep changes focused on the task at hand
- Follow existing code style and patterns in the repository
- Create necessary directories before writing files

When you're done, provide a brief summary of the changes made.`
}
