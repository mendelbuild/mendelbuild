package web

import (
	"encoding/json"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

func renderLogPanel(t *testing.T, panel *LogPanel) string {
	t.Helper()
	tmpl, err := template.New("").Funcs(templateFuncs).ParseFS(
		templatesFS, "templates/layout.html", "templates/partials.html")
	if err != nil {
		t.Fatalf("parsing partials: %v", err)
	}
	var sb strings.Builder
	if err := tmpl.ExecuteTemplate(&sb, "log-tail", panel); err != nil {
		t.Fatalf("rendering log-tail: %v", err)
	}
	return sb.String()
}

// TestLogPanelTimestampsCarryTheDate pins the format. Generation and deploy
// runs straddle midnight, and a bare clock time makes those lines ambiguous.
func TestLogPanelTimestampsCarryTheDate(t *testing.T) {
	at := time.Date(2026, 8, 27, 19, 7, 5, 0, time.Local)
	line := LogLine{LoggedAt: at, Level: "info", Message: "cloning"}

	if got, want := line.Timestamp(), "2026/08/27 19:07:05"; got != want {
		t.Errorf("Timestamp() = %q, want %q", got, want)
	}

	body := renderLogPanel(t, &LogPanel{DOMID: "x", Lines: []LogLine{line}})
	if !strings.Contains(body, "2026/08/27 19:07:05") {
		t.Error("rendered panel should show the full date and time")
	}
}

// TestLogPanelRendersWithoutJavaScript checks that the server-rendered panel is
// complete on its own. The tailer only appends what arrives later, so a page
// with JS disabled must still show the log so far.
func TestLogPanelRendersWithoutJavaScript(t *testing.T) {
	body := renderLogPanel(t, &LogPanel{
		DOMID: "codegen-logs",
		Lines: []LogLine{
			{LoggedAt: time.Now(), Level: "milestone", Message: "Cloning repository"},
			{LoggedAt: time.Now(), Level: "error", Message: "build failed"},
		},
	})
	for _, want := range []string{"Cloning repository", "build failed", "[MILESTONE]", "[ERROR]"} {
		if !strings.Contains(body, want) {
			t.Errorf("panel missing %q", want)
		}
	}
}

// TestLogPanelOnlyPollsWhenLive guards the wasteful case: a finished run is
// already complete on the server render and must not keep polling forever.
func TestLogPanelOnlyPollsWhenLive(t *testing.T) {
	finished := renderLogPanel(t, &LogPanel{
		DOMID: "x", FeedURL: "/api/variations/abc/logs", Status: "pending", Live: false,
	})
	if strings.Contains(finished, `data-log-live="true"`) {
		t.Error("a finished run must not poll")
	}

	live := renderLogPanel(t, &LogPanel{
		DOMID: "x", FeedURL: "/api/variations/abc/logs", Status: "creating", Live: true,
	})
	if !strings.Contains(live, `data-log-live="true"`) {
		t.Error("a running job should poll")
	}
	// The tailer compares this against the feed to decide when the rest of the
	// page has gone stale; without it, a finished job would never settle.
	if !strings.Contains(live, `data-log-status="creating"`) {
		t.Error("panel must publish the status it was rendered with")
	}
	if !strings.Contains(live, `data-log-feed="/api/variations/abc/logs"`) {
		t.Error("panel must publish its feed URL")
	}
}

// TestLogPanelEmptyState covers the panel with nothing in it yet, which is the
// state a just-started run renders in.
func TestLogPanelEmptyState(t *testing.T) {
	body := renderLogPanel(t, &LogPanel{DOMID: "x", Empty: "No logs yet.", Live: true})
	if !strings.Contains(body, "No logs yet.") {
		t.Error("an empty panel should say so")
	}
	// The placeholder must not be pre-hidden, or an empty panel shows blank.
	if strings.Contains(body, `data-log-empty class="empty" style="display: none;"`) {
		t.Error("the empty placeholder should be visible when there are no lines")
	}
}

// TestLogFeedJSONShape pins the wire contract the client depends on. The field
// names live in log-tail.js, which the compiler cannot check.
func TestLogFeedJSONShape(t *testing.T) {
	at := time.Date(2026, 8, 27, 19, 7, 5, 0, time.UTC)
	raw, err := json.Marshal(LogFeed{
		Status: "deploying",
		Logs:   []LogLine{{LoggedAt: at, Level: "info", Message: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct {
		Status string `json:"status"`
		Logs   []struct {
			LoggedAt string `json:"logged_at"`
			Level    string `json:"level"`
			Message  string `json:"message"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("feed does not match the shape log-tail.js expects: %v", err)
	}
	if decoded.Status != "deploying" || len(decoded.Logs) != 1 {
		t.Fatalf("unexpected feed contents: %s", raw)
	}
	if decoded.Logs[0].Message != "hello" || decoded.Logs[0].Level != "info" {
		t.Errorf("unexpected log line: %s", raw)
	}
	// The client parses this with new Date(); RFC3339 is what makes that work.
	if _, err := time.Parse(time.RFC3339, decoded.Logs[0].LoggedAt); err != nil {
		t.Errorf("logged_at must be RFC3339 for the client to parse: %v", err)
	}
}

// TestVariationPageStreamsItsLogs guards a silent-failure mode: the partial
// renders nothing for a nil panel, so a page that forgot to build one would
// lose its log with no error anywhere.
func TestVariationPageStreamsItsLogs(t *testing.T) {
	projectID := uuid.New()
	now := time.Now()
	hop := &domain.Hop{ID: uuid.New(), Name: "rate-limiting", Status: domain.HopStatusActive}
	variation := &domain.Variation{
		ID: uuid.New(), HopID: hop.ID, Name: "token-bucket",
		Status: domain.VariationStatusCreating, CreatedAt: now, UpdatedAt: now,
	}

	view := &VariationDetailView{
		Variation: variation,
		Hop:       hop,
		Ribbon:    domain.VariationLifecycle(variation, nil, hop),
		CodegenPanel: &LogPanel{
			DOMID:   "codegen-logs",
			FeedURL: "/api/variations/" + variation.ID.String() + "/logs",
			Status:  string(variation.Status),
			Live:    true,
			Lines:   []LogLine{{LoggedAt: now, Level: "info", Message: "generating code"}},
		},
	}

	body := renderForTest(t, "variation_detail.html", projectID, view)
	if !strings.Contains(body, "generating code") {
		t.Error("the code generation log should be rendered server-side")
	}
	if !strings.Contains(body, "/static/js/log-tail.js") {
		t.Error("a streaming panel must pull in the tailer")
	}
	// The old page reloaded itself every 3s; that is what this replaces.
	if strings.Contains(body, "location.reload()") {
		t.Error("the variation page must not reload itself wholesale any more")
	}
}
