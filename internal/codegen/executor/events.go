package executor

import "time"

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
	StartTime    time.Time
	EndTime      time.Time
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int
	ToolCalls    int
	APIRounds    int
}

// Duration returns the total execution time.
func (s Stats) Duration() time.Duration {
	if s.EndTime.IsZero() {
		return time.Since(s.StartTime)
	}
	return s.EndTime.Sub(s.StartTime)
}

// EstimatedCost returns rough cost estimate in USD.
func (s Stats) EstimatedCost() float64 {
	// Sonnet 4 pricing (as of mid-2026, verify current rates)
	inputCost := float64(s.InputTokens) * 0.003 / 1000
	outputCost := float64(s.OutputTokens) * 0.015 / 1000
	// Cache reads are 90% cheaper
	cacheReadCost := float64(s.CacheRead) * 0.0003 / 1000
	return inputCost + outputCost + cacheReadCost
}
