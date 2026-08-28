package executor

import "testing"

func userTurn(text string) message {
	return message{Role: "user", Content: []contentBlock{{Type: "text", Text: text}}}
}

func assistantTurn(text string) message {
	return message{Role: "assistant", Content: []contentBlock{{Type: "text", Text: text}}}
}

// countBreakpoints reports how many cache breakpoints a request carries. The
// API caps this at four; exceeding it is a request error, not a silent
// degradation, so the count matters as much as the placement.
func countBreakpoints(req apiRequest) int {
	n := 0
	for _, b := range req.System {
		if b.CacheControl != nil {
			n++
		}
	}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				n++
			}
		}
	}
	return n
}

func TestBuildRequestCachesTheStaticPrefix(t *testing.T) {
	e := New("key", "/tmp")
	req := e.buildRequest("you are a coding agent", []message{userTurn("do the thing")})

	if len(req.System) != 1 {
		t.Fatalf("system blocks = %d, want 1", len(req.System))
	}
	// Render order is tools -> system -> messages, so a breakpoint after the
	// system prompt covers the tool definitions too. Those never change within
	// a run and are ~2.5k tokens re-sent every round without this.
	if req.System[0].CacheControl == nil {
		t.Error("system prompt carries no cache breakpoint, so tools are re-sent uncached every round")
	}
	if len(req.Tools) == 0 {
		t.Error("expected tool definitions in the request")
	}
}

// The regression this guards: with no breakpoints the loop is quadratic,
// re-sending the whole conversation at full input price every round.
func TestBuildRequestRollsBreakpointsForward(t *testing.T) {
	e := New("key", "/tmp")

	// Six rounds of user/assistant alternation.
	var messages []message
	for i := 0; i < 6; i++ {
		messages = append(messages, userTurn("tool result"), assistantTurn("thinking"))
	}

	req := e.buildRequest("system", messages)

	var markedUser []int
	for i, m := range req.Messages {
		for _, b := range m.Content {
			if b.CacheControl == nil {
				continue
			}
			if m.Role != "user" {
				t.Errorf("message %d (%s) carries a breakpoint; only user turns should", i, m.Role)
			}
			markedUser = append(markedUser, i)
		}
	}

	if len(markedUser) != rollingCacheBreakpoints {
		t.Fatalf("marked %d user turns, want %d", len(markedUser), rollingCacheBreakpoints)
	}
	// They must be the two most recent user turns: indices 8 and 10 here.
	if markedUser[0] != 8 || markedUser[1] != 10 {
		t.Errorf("breakpoints on user turns %v, want the last two (8, 10)", markedUser)
	}
	if got := countBreakpoints(req); got > 4 {
		t.Errorf("%d breakpoints, over the API limit of 4", got)
	}
}

// Rebuilding across rounds must clear stale breakpoints rather than accumulate
// them, or a long run blows the four-breakpoint limit partway through.
func TestBuildRequestDoesNotAccumulateBreakpoints(t *testing.T) {
	e := New("key", "/tmp")

	var messages []message
	for round := 0; round < 20; round++ {
		messages = append(messages, userTurn("tool result"), assistantTurn("more"))
		req := e.buildRequest("system", messages)
		if got := countBreakpoints(req); got > 4 {
			t.Fatalf("round %d: %d breakpoints, over the API limit of 4", round, got)
		}
	}

	// After the last round only the two most recent user turns stay marked.
	final := e.buildRequest("system", messages)
	if got := countBreakpoints(final); got != 1+rollingCacheBreakpoints {
		t.Errorf("final breakpoint count = %d, want %d (system + rolling)",
			got, 1+rollingCacheBreakpoints)
	}
}

func TestBuildRequestHandlesEmptyAndSystemlessCases(t *testing.T) {
	e := New("key", "/tmp")

	if req := e.buildRequest("", nil); len(req.System) != 0 {
		t.Error("empty system prompt should produce no system blocks")
	}
	// A message with no content blocks must not panic the breakpoint walk.
	req := e.buildRequest("system", []message{{Role: "user"}, userTurn("hi")})
	if countBreakpoints(req) == 0 {
		t.Error("expected breakpoints despite an empty message")
	}
}

// The model default drives both cost and capability; Sonnet 4.6 is superseded
// by Sonnet 5, which is newer and a third cheaper per token.
func TestDefaultModelIsSonnet5(t *testing.T) {
	if got := New("key", "/tmp").model; got != "claude-sonnet-5" {
		t.Errorf("default model = %q, want claude-sonnet-5", got)
	}
}
