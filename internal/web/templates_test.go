package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// TestTemplatesParse parses every embedded template the same way the handlers
// do. Templates are parsed at request time via template.Must, so a syntax error
// does not fail the build — it panics on the first request to that page. This
// catches it at test time instead.
func TestTemplatesParse(t *testing.T) {
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		t.Fatalf("reading embedded templates: %v", err)
	}

	found := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".html") {
			continue
		}
		found++

		switch name {
		case "layout.html", "partials.html":
			// Not pages; parsed as part of every page below.
			continue
		case "login.html":
			// Rendered standalone, without the layout.
			t.Run(name, func(t *testing.T) {
				if _, err := template.New("").ParseFS(templatesFS, "templates/"+name); err != nil {
					t.Errorf("parse: %v", err)
				}
			})
		default:
			t.Run(name, func(t *testing.T) {
				// Parsed exactly as parsePageTemplate does it, so the shared
				// partials are covered too.
				_, err := template.New("").Funcs(templateFuncs).ParseFS(
					templatesFS,
					"templates/layout.html",
					"templates/partials.html",
					"templates/"+name,
				)
				if err != nil {
					t.Errorf("parse: %v", err)
				}
			})
		}
	}

	if found == 0 {
		t.Fatal("no templates found in the embedded FS")
	}
}

// TestNoStatusRendersFailureAsSuccess guards a bug class that recurred in three
// separate templates: mapping a failed or losing Variation to status-resolved,
// which is success green. `terminated` is a code/test failure and `rejected`
// means another Variation won, so neither may share the winner's styling.
func TestNoStatusRendersFailureAsSuccess(t *testing.T) {
	banned := []struct {
		file    string
		snippet string
		why     string
	}{
		{"variation_detail.html", `"terminated"}}status-resolved`, "terminated is a code/test failure, not a success"},
		{"hop_detail.html", `"terminated"}}status-resolved`, "terminated is a code/test failure, not a success"},
		{"input_request_selection.html", `"rejected"}}status-resolved`, "a rejected Variation lost; it must not look like the winner"},
	}

	for _, b := range banned {
		data, err := fs.ReadFile(templatesFS, "templates/"+b.file)
		if err != nil {
			t.Fatalf("reading %s: %v", b.file, err)
		}
		if strings.Contains(string(data), b.snippet) {
			t.Errorf("%s: found %q — %s", b.file, b.snippet, b.why)
		}
	}
}

// partialsTemplate parses just the shared partials, for executing them in
// isolation.
func partialsTemplate(t *testing.T) *template.Template {
	t.Helper()
	tmpl, err := template.New("").Funcs(templateFuncs).ParseFS(
		templatesFS, "templates/layout.html", "templates/partials.html")
	if err != nil {
		t.Fatalf("parsing partials: %v", err)
	}
	return tmpl
}

// TestRibbonExecutes renders the lifecycle ribbon for every Hop and Variation
// status. Parsing proves only that the syntax is valid; a bad field reference
// fails at execution time, on a real request.
func TestRibbonExecutes(t *testing.T) {
	tmpl := partialsTemplate(t)

	hopStatuses := []domain.HopStatus{
		domain.HopStatusPending, domain.HopStatusActive, domain.HopStatusSelecting,
		domain.HopStatusCompleted, domain.HopStatusRejected, domain.HopStatusAbandoned,
	}
	for _, st := range hopStatuses {
		ribbon := domain.HopLifecycle(&domain.Hop{Status: st}, nil)
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "lifecycle-ribbon", ribbon); err != nil {
			t.Fatalf("hop %q: %v", st, err)
		}
		if !strings.Contains(buf.String(), ribbon.Headline) {
			t.Errorf("hop %q: headline %q missing from output", st, ribbon.Headline)
		}
	}

	variationStatuses := []domain.VariationStatus{
		domain.VariationStatusCreating, domain.VariationStatusPending, domain.VariationStatusBlocked,
		domain.VariationStatusMigrating, domain.VariationStatusActive, domain.VariationStatusDraining,
		domain.VariationStatusError, domain.VariationStatusTerminated, domain.VariationStatusPruned,
		domain.VariationStatusSelected, domain.VariationStatusMerged, domain.VariationStatusRejected,
	}
	for _, st := range variationStatuses {
		ribbon := domain.VariationLifecycle(
			&domain.Variation{Status: st},
			[]domain.VariationRevision{{ID: uuid.New(), Status: domain.VariationRevisionStatusCompleted}},
			&domain.Hop{RequiresDemo: true},
		)
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "lifecycle-ribbon", ribbon); err != nil {
			t.Fatalf("variation %q: %v", st, err)
		}
		if !strings.Contains(buf.String(), ribbon.Headline) {
			t.Errorf("variation %q: headline %q missing from output", st, ribbon.Headline)
		}
	}
}

// TestRibbonShowsWhoseMoveItIs checks the single most useful fact on the page.
func TestRibbonShowsWhoseMoveItIs(t *testing.T) {
	tmpl := partialsTemplate(t)

	waiting := domain.HopLifecycle(&domain.Hop{Status: domain.HopStatusSelecting}, nil)
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "lifecycle-ribbon", waiting); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Your move") {
		t.Error(`a Hop awaiting selection should render "Your move"`)
	}

	working := domain.HopLifecycle(&domain.Hop{Status: domain.HopStatusPending}, nil)
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "lifecycle-ribbon", working); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Your move") {
		t.Error("a Hop waiting on dependencies must not claim it is your move")
	}
}

// TestRibbonBadgeDistinguishesOutcomes guards the mirror image of the
// failure-as-success bug: a Variation whose build failed and one that was
// merged are both terminal, and labelling them alike would erase the only
// distinction that matters.
func TestRibbonBadgeDistinguishesOutcomes(t *testing.T) {
	tmpl := partialsTemplate(t)

	cases := []struct {
		status  domain.VariationStatus
		want    string
		notWant string
	}{
		{domain.VariationStatusTerminated, "Failed", "Complete"},
		{domain.VariationStatusMerged, "Complete", "Failed"},
		{domain.VariationStatusRejected, "Closed", "Complete"},
		{domain.VariationStatusPruned, "Closed", "Complete"},
	}
	for _, c := range cases {
		ribbon := domain.VariationLifecycle(&domain.Variation{Status: c.status}, nil, &domain.Hop{})
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "lifecycle-ribbon", ribbon); err != nil {
			t.Fatalf("%s: %v", c.status, err)
		}
		out := buf.String()
		if !strings.Contains(out, c.want) {
			t.Errorf("%s: badge should read %q", c.status, c.want)
		}
		if strings.Contains(out, c.notWant) {
			t.Errorf("%s: badge must not read %q", c.status, c.notWant)
		}
	}
}

// TestRoadmapPanelExecutes covers the nil case explicitly. buildMiniRoadmap
// returns nil when the graph cannot be loaded, and the panel is contextual, so
// that must render as nothing rather than panicking the page.
func TestRoadmapPanelExecutes(t *testing.T) {
	tmpl := partialsTemplate(t)

	var nilPanel *MiniRoadmap
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "roadmap-panel", nilPanel); err != nil {
		t.Fatalf("nil panel must render harmlessly, got: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "" {
		t.Errorf("nil panel should render nothing, got %q", buf.String())
	}

	// A one-Hop roadmap adds no context, so it should render nothing.
	lonely := &MiniRoadmap{ProjectID: uuid.New().String(), HopCount: 1}
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "roadmap-panel", lonely); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "" {
		t.Errorf("a single-Hop roadmap should render no panel, got %q", buf.String())
	}

	projectID := uuid.New()
	focusID := uuid.New()
	populated := &MiniRoadmap{
		ProjectID:  projectID.String(),
		FocusHopID: focusID.String(),
		HopCount:   3,
		HopsJSON:   template.JS(`[{"ID":"a","Name":"auth-refactor","Status":"completed"}]`),
		EdgesJSON:  template.JS(`[{"from":"a","to":"b"}]`),
	}
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "roadmap-panel", populated); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{focusID.String(), "auth-refactor", "Open full roadmap", "roadmap-view.js"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel output missing %q", want)
		}
	}
	if !strings.Contains(out, "/p/"+projectID.String()+"/roadmap") {
		t.Error("panel should link to the full roadmap")
	}
	// html/template escapes strings in a script context; template.JS is what
	// keeps the payload parseable as JSON. See CLAUDE.md.
	if strings.Contains(out, "&#34;") {
		t.Error("roadmap payload was HTML-escaped; it must be template.JS")
	}
}

// TestDetailPagesRender renders the two pages that carry the ribbon and the
// roadmap panel, end to end through renderPage. This is what catches a nil
// dereference or a bad field path in the page itself, which parsing cannot.
func TestDetailPagesRender(t *testing.T) {
	projectID := uuid.New()
	now := time.Now()

	hop := &domain.Hop{
		ID: uuid.New(), StrategyID: uuid.New(),
		Name: "rate-limiting", Commentary: "Protect the API from bursts.",
		Status: domain.HopStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	variation := domain.Variation{
		ID: uuid.New(), HopID: hop.ID,
		Name: "token-bucket", Approach: "Per-key token bucket in Redis.",
		Status: domain.VariationStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	roadmap := &MiniRoadmap{
		ProjectID:  projectID.String(),
		FocusHopID: hop.ID.String(),
		HopCount:   2,
		HopsJSON:   template.JS(`[{"ID":"a","Name":"auth-refactor","Status":"completed"}]`),
		EdgesJSON:  template.JS(`[]`),
	}

	t.Run("hop_detail.html", func(t *testing.T) {
		view := &HopDetailView{
			Hop:        hop,
			Strategy:   &domain.Strategy{ID: hop.StrategyID, Name: "Q3 reliability"},
			Project:    &domain.Project{ID: projectID, Name: "Demo"},
			Variations: []VariationWithLogs{{Variation: variation}},
			Ribbon:     domain.HopLifecycle(hop, []domain.Variation{variation}),
			Roadmap:    roadmap,
		}
		body := renderForTest(t, "hop_detail.html", projectID, view)
		for _, want := range []string{view.Ribbon.Headline, "auth-refactor", "rate-limiting", "token-bucket"} {
			if !strings.Contains(body, want) {
				t.Errorf("hop detail missing %q", want)
			}
		}
	})

	t.Run("variation_detail.html", func(t *testing.T) {
		revisions := []domain.VariationRevision{
			{ID: uuid.New(), VariationID: variation.ID, Feedback: "Use a sliding window instead.",
				Status: domain.VariationRevisionStatusCompleted, CreatedAt: now},
		}
		v := variation
		view := &VariationDetailView{
			Variation: &v,
			Hop:       hop,
			Revisions: revisions,
			Ribbon:    domain.VariationLifecycle(&v, revisions, hop),
			Roadmap:   roadmap,
		}
		body := renderForTest(t, "variation_detail.html", projectID, view)
		for _, want := range []string{view.Ribbon.Headline, "auth-refactor", "Use a sliding window instead."} {
			if !strings.Contains(body, want) {
				t.Errorf("variation detail missing %q", want)
			}
		}
	})
}

func renderForTest(t *testing.T, page string, projectID uuid.UUID, view interface{}) string {
	t.Helper()
	rec := httptest.NewRecorder()
	data := map[string]interface{}{
		"Title":     "test",
		"ProjectID": projectID.String(),
		"View":      view,
	}
	if err := renderPage(rec, page, data); err != nil {
		t.Fatalf("rendering %s: %v", page, err)
	}
	return rec.Body.String()
}
