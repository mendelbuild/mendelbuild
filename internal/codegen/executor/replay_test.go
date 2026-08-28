package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

func thinkingText(s string) *string { return &s }

// The regression: models that reason before answering return thinking blocks,
// and the whole assistant turn must be echoed back unchanged on the next round.
// The executor dropped them, and the API rejected the conversation with
// "messages.1.content.0.thinking.thinking: Field required".
//
// The trap is that the field is required yet routinely empty -- these models
// omit the reasoning text by default and return only a signature -- so a plain
// string with omitempty silently reproduces the bug.
func TestThinkingBlocksSurviveTheRoundTrip(t *testing.T) {
	// Exactly what the API returns: empty thinking text, real signature.
	const fromAPI = `[{"type":"thinking","thinking":"","signature":"sig-abc"},{"type":"text","text":"hello"}]`

	var blocks []contentBlock
	if err := json.Unmarshal([]byte(fromAPI), &blocks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, `"thinking":""`) {
		t.Errorf("empty thinking field was dropped, which is the exact 400 the API returns.\ngot: %s", got)
	}
	if !strings.Contains(got, `"signature":"sig-abc"`) {
		t.Errorf("signature lost; the API verifies it.\ngot: %s", got)
	}
	if got != fromAPI {
		t.Errorf("assistant turn must echo back unchanged.\n got: %s\nwant: %s", got, fromAPI)
	}
}

// A block that has no thinking must not sprout an empty one.
func TestNonThinkingBlocksCarryNoThinkingField(t *testing.T) {
	for _, b := range []contentBlock{
		{Type: "text", Text: "hi"},
		{Type: "tool_use", ID: "t1", Name: "Read", Input: map[string]interface{}{"file_path": "/x"}},
		{Type: "tool_result", ToolUseID: "t1", Content: "contents"},
	} {
		out, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal %s: %v", b.Type, err)
		}
		if strings.Contains(string(out), "thinking") {
			t.Errorf("%s block emitted a thinking field: %s", b.Type, out)
		}
	}
}

// Redacted reasoning carries its payload in `data`, which likewise has to
// survive or the same rejection follows.
func TestRedactedThinkingRetainsItsPayload(t *testing.T) {
	const fromAPI = `[{"type":"redacted_thinking","data":"encrypted-payload"}]`
	var blocks []contentBlock
	if err := json.Unmarshal([]byte(fromAPI), &blocks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, _ := json.Marshal(blocks)
	if string(out) != fromAPI {
		t.Errorf("redacted_thinking altered.\n got: %s\nwant: %s", out, fromAPI)
	}
}

// Assembling a request must not strip reasoning from earlier assistant turns,
// and the cache breakpoint walk must leave assistant blocks alone.
func TestBuildRequestPreservesEarlierThinking(t *testing.T) {
	e := New("key", "/tmp")

	messages := []message{
		{Role: "user", Content: []contentBlock{{Type: "text", Text: "do the thing"}}},
		{Role: "assistant", Content: []contentBlock{
			{Type: "thinking", Thinking: thinkingText(""), Signature: "sig-1"},
			{Type: "tool_use", ID: "t1", Name: "Bash", Input: map[string]interface{}{"command": "ls"}},
		}},
		{Role: "user", Content: []contentBlock{{Type: "tool_result", ToolUseID: "t1", Content: "a.go"}}},
	}

	req := e.buildRequest("system", messages)
	out, err := json.Marshal(req.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, `"thinking":""`) || !strings.Contains(got, `"signature":"sig-1"`) {
		t.Errorf("request dropped the earlier turn's reasoning.\ngot: %s", got)
	}
	// Breakpoints belong on user turns; an assistant turn carrying one would
	// mean the walk had reached into the reasoning blocks.
	for _, m := range req.Messages {
		if m.Role != "assistant" {
			continue
		}
		for _, b := range m.Content {
			if b.CacheControl != nil {
				t.Error("assistant turn carries a cache breakpoint")
			}
		}
	}
}
