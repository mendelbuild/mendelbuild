package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
)

const (
	anthropicAPIURL = "https://api.anthropic.com/v1/messages"
	defaultModel    = "claude-sonnet-4-6"
	apiVersion      = "2023-06-01"
)

// Client is an Anthropic API client.
type Client struct {
	apiKey     string
	httpClient *http.Client
	model      string
}

// NewClient creates a new Anthropic API client.
// If apiKey is empty, reads from ANTHROPIC_API_KEY environment variable.
func NewClient(apiKey string) (*Client, error) {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
	}
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
		model: defaultModel,
	}, nil
}

// Message represents a conversation message.
type Message struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// OutputFormat specifies the structured output format.
type OutputFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

// OutputConfig specifies output configuration including format.
type OutputConfig struct {
	Format *OutputFormat `json:"format,omitempty"`
}

// Request is an Anthropic API request.
type Request struct {
	Model        string        `json:"model"`
	MaxTokens    int           `json:"max_tokens"`
	System       string        `json:"system,omitempty"`
	Messages     []Message     `json:"messages"`
	OutputConfig *OutputConfig `json:"output_config,omitempty"`
}

// Response is an Anthropic API response.
type Response struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Content      []ContentBlock `json:"content"`
	Model        string         `json:"model"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence,omitempty"`
	Usage        Usage          `json:"usage"`
}

// ContentBlock represents a content block in the response.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Usage contains token usage information.
//
// InputTokens is the uncached remainder only: the full prompt is
// InputTokens + CacheReadInputTokens + CacheCreationInputTokens. Cache tokens
// are priced differently from plain input (a read is a tenth, a write a
// premium), so they are carried separately rather than folded together.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// TotalTokens returns every token billed, in either direction.
func (u Usage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens +
		u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// Tokens converts usage into the shared shape used by the cost ledger.
func (u Usage) Tokens() domain.TokenCounts {
	return domain.TokenCounts{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}

// Spend pairs usage with the model that produced it, which is everything the
// cost ledger needs to price an agent call.
type Spend struct {
	Model  string
	Tokens domain.TokenCounts
}

// Spend describes what this response cost, for the caller to record.
func (r *Response) Spend() Spend {
	if r == nil {
		return Spend{}
	}
	return Spend{Model: r.Model, Tokens: r.Usage.Tokens()}
}

// SendMessage sends a message to the Anthropic API and returns the response.
func (c *Client) SendMessage(ctx context.Context, system string, messages []Message, maxTokens int) (*Response, error) {
	return c.SendMessageWithSchema(ctx, system, messages, maxTokens, nil)
}

// SendMessageWithSchema sends a message with a JSON schema for structured output.
func (c *Client) SendMessageWithSchema(ctx context.Context, system string, messages []Message, maxTokens int, schema json.RawMessage) (*Response, error) {
	if maxTokens == 0 {
		maxTokens = 4096
	}

	req := Request{
		Model:     c.model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  messages,
	}

	if schema != nil {
		req.OutputConfig = &OutputConfig{
			Format: &OutputFormat{
				Type:   "json_schema",
				Schema: schema,
			},
		}
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
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result Response
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &result, nil
}

// GetTextContent extracts the text content from a response.
func (r *Response) GetTextContent() string {
	for _, block := range r.Content {
		if block.Type == "text" {
			return block.Text
		}
	}
	return ""
}
