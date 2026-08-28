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

// DefaultModel is the model code generation runs on. Exported because cost
// calibration must know which model its history describes: a figure from a
// different model, or from before prompt caching, does not predict this one.
const DefaultModel = "claude-sonnet-5"

const (
	anthropicAPIURL = "https://api.anthropic.com/v1/messages"
	defaultModel    = DefaultModel
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
	e.stats = Stats{StartTime: time.Now(), Model: e.model}
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

	// Thinking blocks arrive on models that reason before answering, and must
	// be echoed back unchanged on the next round or the API rejects the whole
	// conversation. The executor does not read them; it only has to not lose
	// them.
	//
	// Thinking is a pointer because the field is required on a thinking block
	// and is routinely the empty string: these models omit the reasoning text
	// by default and return only the signature. A plain string with omitempty
	// would drop it again and reproduce the original failure --
	// "messages.1.content.0.thinking.thinking: Field required" -- so the
	// pointer is what distinguishes "absent" from "present and empty".
	Thinking  *string `json:"thinking,omitempty"`
	Signature string  `json:"signature,omitempty"`

	// Data carries a redacted_thinking block's payload, which likewise has to
	// survive the round trip.
	Data string `json:"data,omitempty"`

	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// cacheControl marks a prompt prefix as cacheable up to and including the block
// it sits on. Default TTL is five minutes, which suits an agentic loop whose
// rounds are seconds apart; the one-hour TTL costs twice as much to write and
// would buy nothing here.
type cacheControl struct {
	Type string `json:"type"`
}

func ephemeral() *cacheControl { return &cacheControl{Type: "ephemeral"} }

// systemBlock is the system prompt in block form, which is what allows a cache
// breakpoint to be placed after it.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type toolUse struct {
	ID    string
	Name  string
	Input map[string]interface{}
}

type apiRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    []systemBlock `json:"system,omitempty"`
	Messages  []message     `json:"messages"`
	Tools     []ToolDef     `json:"tools,omitempty"`
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

// buildRequest assembles one round's request, placing prompt cache breakpoints.
//
// Without these the loop is quadratic: the API only caches where it is told to,
// so every round re-sent the tools, the system prompt and the entire growing
// conversation at full input price. On a 50-round run that is most of the bill.
//
// Two kinds of breakpoint, three in total against a limit of four:
//
//   - One after the system prompt. Render order is tools -> system -> messages,
//     so a breakpoint there covers the tool definitions too. That prefix never
//     changes within a run, so it is written once and read thereafter.
//
//   - Two rolling ones, on the last two user turns. The older is what this
//     round reads from; the newer extends the cache for the next round. Keeping
//     the pair means each round hits a cache written one round earlier rather
//     than racing its own write.
func (e *Executor) buildRequest(systemPrompt string, messages []message) apiRequest {
	tools := Tools()

	req := apiRequest{
		Model:     e.model,
		MaxTokens: maxTokens,
		Messages:  messages,
		Tools:     tools,
	}

	if systemPrompt != "" {
		req.System = []systemBlock{{
			Type:         "text",
			Text:         systemPrompt,
			CacheControl: ephemeral(),
		}}
	}

	markRollingCachePoints(req.Messages)
	return req
}

// rollingCacheBreakpoints is how many of the most recent user turns carry a
// cache breakpoint. Two, so a round reads a cache written on the previous round
// while writing the one the next round will read.
const rollingCacheBreakpoints = 2

// markRollingCachePoints puts a breakpoint on the final block of each of the
// last few user turns, and clears any left on older ones.
//
// Breakpoints go on user turns because those are what end a round: the prefix
// they close covers the assistant turn and tool results before them. Marking
// assistant turns instead would leave each round's tool output outside the
// cached prefix, which is the bulk of what grows.
func markRollingCachePoints(messages []message) {
	marked := 0
	for i := len(messages) - 1; i >= 0; i-- {
		blocks := messages[i].Content
		if len(blocks) == 0 {
			continue
		}
		last := &blocks[len(blocks)-1]

		if messages[i].Role == "user" && marked < rollingCacheBreakpoints {
			last.CacheControl = ephemeral()
			marked++
			continue
		}
		// Older breakpoints are cleared: leaving them would blow the
		// four-breakpoint limit as the conversation grows.
		last.CacheControl = nil
	}
}

func (e *Executor) callAPI(ctx context.Context, systemPrompt string, messages []message) (*apiResponse, error) {
	req := e.buildRequest(systemPrompt, messages)

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
- Bash: Run shell commands (for mkdir, ls, etc.)
- Glob: Find files matching patterns
- Grep: Search for patterns in files

Guidelines:
- Read existing source files before modifying them to understand context
- Use Edit for surgical changes, Write for new files or complete rewrites
- Keep changes focused on the task at hand
- Follow existing code style and patterns in the repository
- Create necessary directories before writing files
- NEVER read .git/ directory contents - focus only on source code files
- Work efficiently - don't re-read the same files repeatedly

IMPORTANT - when to stop:
- Stop when you've written the code and any simple unit tests
- Do NOT try to run tests yourself or verify they pass
- Do NOT try to verify external integrations (APIs, databases, etc.)
- Mendel will run tests in Docker after you're done

When you're done implementing, stop. Provide a brief summary of the changes made.`
}
