package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// LogTimeFormat is the timestamp shown on every log line. The date matters
// because generation and deploy runs routinely straddle midnight, and a bare
// clock time makes those lines ambiguous.
const LogTimeFormat = "2006/01/02 15:04:05"

// LogLine is the wire format for one tailed log line. Both variation logs and
// hosting deployment logs narrow to this so the client only has to know one
// shape.
type LogLine struct {
	LoggedAt time.Time `json:"logged_at"`
	Level    string    `json:"level"`
	Message  string    `json:"message"`
}

// Timestamp renders the line for server-side rendering, matching what the
// client-side tailer produces for lines it appends.
func (l LogLine) Timestamp() string { return l.LoggedAt.Format(LogTimeFormat) }

// LogFeed is what a tailing client polls: the owning object's status plus the
// log so far. Status travels with the logs because a change in it means the
// rest of the page is now stale and the client should reload.
type LogFeed struct {
	Status string    `json:"status"`
	Logs   []LogLine `json:"logs"`
}

// LogPanel is the view model for the "log-tail" partial.
type LogPanel struct {
	// DOMID must be unique on the page; a page may show several panels.
	DOMID string
	// FeedURL is polled for updates. Empty renders a static panel.
	FeedURL string
	// Status is the owning object's status at render time. The tailer reloads
	// the page when the feed reports something different.
	Status string
	// Live reports whether the underlying work is still running. Only live
	// panels poll; a finished run is already complete on the server render.
	Live bool
	// MaxHeight bounds the scroll area, e.g. "300px".
	MaxHeight string
	// Empty is shown when there are no lines yet.
	Empty string
	Lines []LogLine
}

// logLinesFromVariation narrows variation logs to the wire format.
func logLinesFromVariation(logs []domain.VariationLog) []LogLine {
	out := make([]LogLine, 0, len(logs))
	for _, l := range logs {
		out = append(out, LogLine{LoggedAt: l.LoggedAt, Level: string(l.Level), Message: l.Message})
	}
	return out
}

// logLinesFromDeployment narrows hosting deployment logs to the wire format.
func logLinesFromDeployment(logs []domain.HostingDeploymentLog) []LogLine {
	out := make([]LogLine, 0, len(logs))
	for _, l := range logs {
		out = append(out, LogLine{LoggedAt: l.LoggedAt, Level: string(l.Level), Message: l.Message})
	}
	return out
}

func writeLogFeed(w http.ResponseWriter, status string, lines []LogLine) {
	if lines == nil {
		lines = []LogLine{}
	}
	w.Header().Set("Content-Type", "application/json")
	// These are polled every few seconds; a cached response would freeze the tail.
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(LogFeed{Status: status, Logs: lines})
}

// apiVariationLogs feeds the code generation log on the variation page.
func (s *Server) apiVariationLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	variation, err := s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	logs, err := s.db.GetVariationLogsByType(ctx, variationID, domain.SourceTypeCodegen, 500)
	if err != nil {
		http.Error(w, "failed to get logs", http.StatusInternalServerError)
		return
	}

	writeLogFeed(w, string(variation.Status), logLinesFromVariation(logs))
}

// apiDemoLogs feeds the demo log on the variation page.
func (s *Server) apiDemoLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	demoID, err := uuid.Parse(chi.URLParam(r, "demoID"))
	if err != nil {
		http.Error(w, "invalid demo ID", http.StatusBadRequest)
		return
	}

	demo, err := s.db.GetDemoInstance(ctx, demoID)
	if err != nil {
		http.Error(w, "demo not found", http.StatusNotFound)
		return
	}

	logs, err := s.db.GetVariationLogsBySource(ctx, domain.SourceTypeDemo, demoID, 500)
	if err != nil {
		http.Error(w, "failed to get logs", http.StatusInternalServerError)
		return
	}

	writeLogFeed(w, string(demo.Status), logLinesFromVariation(logs))
}

// apiDeploymentLogs feeds the production deploy log on the deployment page.
func (s *Server) apiDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	deploymentID, err := uuid.Parse(chi.URLParam(r, "deploymentID"))
	if err != nil {
		http.Error(w, "invalid deployment ID", http.StatusBadRequest)
		return
	}

	deployment, err := s.db.GetHostingDeployment(ctx, deploymentID)
	if err != nil {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}

	logs, err := s.db.GetHostingDeploymentLogs(ctx, deploymentID)
	if err != nil {
		http.Error(w, "failed to get logs", http.StatusInternalServerError)
		return
	}

	writeLogFeed(w, string(deployment.Status), logLinesFromDeployment(logs))
}
