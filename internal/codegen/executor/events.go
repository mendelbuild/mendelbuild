package executor

import (
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
)

// EventType identifies the kind of progress event.
type EventType string

const (
	EventStart       EventType = "start"
	EventToolCall    EventType = "tool_call"
	EventToolResult  EventType = "tool_result"
	EventThinking    EventType = "thinking"
	EventText        EventType = "text"
	EventComplete    EventType = "complete"
	EventError       EventType = "error"
	EventAPICall     EventType = "api_call"
	EventAPIResponse EventType = "api_response"
)

// Event represents a progress event during code generation.
type Event struct {
	Type      EventType
	Timestamp time.Time

	// Tool events
	ToolName  string
	ToolInput map[string]interface{}
	ToolResult string
	ToolError  string

	// API events
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int

	// Text events
	Text string

	// Error events
	Error error
}

// EventHandler receives progress events during execution.
type EventHandler func(Event)

// Stats tracks cumulative statistics for an execution.
type Stats struct {
	StartTime time.Time
	EndTime   time.Time

	// Model that produced these counts, so the caller can price them against
	// the right rate card. An agentic run is cache-heavy, and cache tokens are
	// priced off the input rate, which differs ~10x across models.
	Model string

	// InputTokens is the uncached remainder only: the full prompt is
	// InputTokens + CacheRead + CacheWrite.
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int

	ToolCalls int
	APIRounds int
}

// Tokens returns the counts in the shared domain shape used by the cost ledger.
func (s Stats) Tokens() domain.TokenCounts {
	return domain.TokenCounts{
		InputTokens:      s.InputTokens,
		OutputTokens:     s.OutputTokens,
		CacheReadTokens:  s.CacheRead,
		CacheWriteTokens: s.CacheWrite,
	}
}

// Duration returns the total execution time.
func (s Stats) Duration() time.Duration {
	if s.EndTime.IsZero() {
		return time.Since(s.StartTime)
	}
	return s.EndTime.Sub(s.StartTime)
}
