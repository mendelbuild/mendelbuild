package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		ribbon := ribbonView(domain.HopLifecycle(&domain.Hop{Status: st}, nil))
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
		ribbon := ribbonView(domain.VariationLifecycle(
			&domain.Variation{Status: st},
			[]domain.VariationRevision{{ID: uuid.New(), Status: domain.VariationRevisionStatusCompleted}},
			&domain.Hop{RequiresDemo: true},
		))
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

	waiting := ribbonView(domain.HopLifecycle(&domain.Hop{Status: domain.HopStatusSelecting}, nil))
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "lifecycle-ribbon", waiting); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Your move") {
		t.Error(`a Hop awaiting selection should render "Your move"`)
	}

	working := ribbonView(domain.HopLifecycle(&domain.Hop{Status: domain.HopStatusPending}, nil))
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
		ribbon := ribbonView(domain.VariationLifecycle(&domain.Variation{Status: c.status}, nil, &domain.Hop{}))
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
	focusVariationID := uuid.New()
	populated := &MiniRoadmap{
		ProjectID:        projectID.String(),
		FocusHopID:       focusID.String(),
		FocusVariationID: focusVariationID.String(),
		HopCount:         3,
		HopsJSON:         template.JS(`[{"ID":"a","Name":"auth-refactor","Status":"completed"}]`),
		EdgesJSON:        template.JS(`[{"from":"a","to":"b"}]`),
	}
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "roadmap-panel", populated); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		focusID.String(),
		// The Variation being viewed drives both the accent fill and the
		// scroll target, so it has to reach the renderer.
		focusVariationID.String(),
		"dimOthers: true",
		"fitFocus: true",
		"auth-refactor", "Open full roadmap", "roadmap-view.js",
	} {
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
			Ribbon:     ribbonView(domain.HopLifecycle(hop, []domain.Variation{variation})),
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
			Ribbon:    ribbonView(domain.VariationLifecycle(&v, revisions, hop)),
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
	body := rec.Body.String()
	dumpRendered(t, page, body)
	return body
}

// dumpRendered writes a rendered page to disk when MENDEL_PAGE_DUMP_DIR is set,
// and does nothing otherwise.
//
// The test suite already constructs every page in every interesting state, so
// this turns the suite into a way to look at all of them at once without a
// database, a repository, or a deployment channel — including the states that
// are awkward to reach by hand, like a run paused at its spend ceiling:
//
//	MENDEL_PAGE_DUMP_DIR=/tmp/pages go test ./internal/web/
//
// Filenames carry the test name, so each state lands in its own file rather
// than the last one winning.
func dumpRendered(t *testing.T, page, body string) {
	t.Helper()
	dir := os.Getenv("MENDEL_PAGE_DUMP_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("page dump dir: %v", err)
	}
	safe := strings.NewReplacer("/", "_", " ", "_", "\\", "_").Replace(t.Name())
	name := filepath.Join(dir, safe+"--"+strings.TrimSuffix(page, ".html")+".html")
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatalf("writing page dump: %v", err)
	}
}

// TestSetupPagesRender renders the two guided-setup screens end to end. These
// are the first pages a new user ever sees, so a nil dereference here is a dead
// end with no way around it — there is no other route into a project.
func TestSetupPagesRender(t *testing.T) {
	projectID := uuid.New()
	now := time.Now()

	t.Run("new_project.html", func(t *testing.T) {
		rec := httptest.NewRecorder()
		data := map[string]interface{}{
			"Title":  "New Project",
			"Form":   newProjectForm{Deadline: "2026-12-31", Budget: "250"},
			"Error":  "",
			"Ribbon": ribbonView(domain.OnboardingLifecycle(domain.OnboardingState{})),
		}
		if err := renderPage(rec, "new_project.html", data); err != nil {
			t.Fatalf("rendering: %v", err)
		}
		body := rec.Body.String()
		for _, want := range []string{"Tell Mendel what you want to build", `name="brief"`, `name="budget_usd"`} {
			if !strings.Contains(body, want) {
				t.Errorf("new project form missing %q", want)
			}
		}
	})

	t.Run("setup_okrs.html", func(t *testing.T) {
		strategyID := uuid.New()
		score := 0.55
		feedback := "Say how many completions counts as success."
		target := now.AddDate(0, 2, 0)

		objective := domain.Objective{
			ID: uuid.New(), StrategyID: strategyID,
			Description: "Someone in their first job can build a budget without help",
			CreatedAt:   now, UpdatedAt: now,
		}
		kr := domain.KeyResult{
			ID: uuid.New(), StrategyID: strategyID,
			Description: "People finish the calculator", TargetUnits: ">= 100 completions",
			TargetDate: &target, TuneScore: &score, TuneFeedback: &feedback,
			CreatedAt: now, UpdatedAt: now,
		}

		state := domain.OnboardingState{HasStrategy: true, HasDraftOKRs: true}
		view := SetupOKRView{
			Project:  &domain.Project{ID: projectID, Name: "adulting-101"},
			Strategy: &domain.Strategy{ID: strategyID, Name: "MVP Launch"},
			Notes: &domain.StrategyDraftNotes{
				Summary:       "A budgeting tool for people in their first job.",
				Assumptions:   []string{"Web, mobile-first, no native app."},
				OpenQuestions: []string{"Is there an existing audience to launch to?"},
				BudgetNote:    "250 dollars is enough for an MVP of this shape.",
			},
			Objectives: []SetupObjectiveView{{Objective: objective, KeyResults: []domain.KeyResult{kr}}},
			Funding: &domain.FundingSource{
				ID: uuid.New(), StrategyID: strategyID, Name: "MVP build",
				AmountUSD: 250, PeriodEnd: &target,
			},
			Ribbon: ribbonView(domain.OnboardingLifecycle(state)),
		}

		body := renderForTest(t, "setup_okrs.html", projectID, view)
		want := []string{
			view.Ribbon.Headline,
			"A budgeting tool for people in their first job.",
			"Is there an existing audience to launch to?",
			objective.Description,
			"&gt;= 100 completions", // html/template escapes the comparison in the value attribute
			feedback,
			"obj_" + objective.ID.String(),
			"kr_" + kr.ID.String() + "_units",
			"$250",
		}
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("setup screen missing %q", w)
			}
		}
	})
}

// TestSetupScreenEditsRoundTrip guards the contract between the review screen's
// field names and the handler that reads them. They are built from UUIDs in two
// different files, so a rename in either one silently drops the user's edits
// rather than failing.
func TestSetupScreenEditsRoundTrip(t *testing.T) {
	projectID := uuid.New()
	objID, krID := uuid.New(), uuid.New()

	view := SetupOKRView{
		Project:  &domain.Project{ID: projectID, Name: "p"},
		Strategy: &domain.Strategy{ID: uuid.New(), Name: "s"},
		Objectives: []SetupObjectiveView{{
			Objective:  domain.Objective{ID: objID, Description: "o"},
			KeyResults: []domain.KeyResult{{ID: krID, Description: "k", TargetUnits: ">= 1"}},
		}},
		Ribbon: ribbonView(domain.OnboardingLifecycle(domain.OnboardingState{HasStrategy: true, HasDraftOKRs: true})),
	}
	body := renderForTest(t, "setup_okrs.html", projectID, view)

	// The names saveOKREdits reads back out of the form.
	for _, name := range []string{
		`name="obj_` + objID.String() + `"`,
		`name="kr_` + krID.String() + `_desc"`,
		`name="kr_` + krID.String() + `_units"`,
		`name="kr_` + krID.String() + `_date"`,
	} {
		if !strings.Contains(body, name) {
			t.Errorf("review form is missing the field %s, so those edits would be dropped on approval", name)
		}
	}
}

// TestSetupScreenDraftStates renders the two states the review screen shows
// while there is no draft to review. The important guarantee is negative: while
// a draft is running or has failed, the approve form must not be on the page.
// Rendering it would let someone approve a strategy with no objectives, or with
// the leftovers of a previous attempt.
func TestSetupScreenDraftStates(t *testing.T) {
	projectID := uuid.New()
	brief := "A trip planner for weekend hikers."

	base := func() SetupOKRView {
		return SetupOKRView{
			Project:     &domain.Project{ID: projectID, Name: "trailkit", Brief: &brief},
			Strategy:    &domain.Strategy{ID: uuid.New(), Name: "Initial strategy"},
			PollSeconds: 3,
		}
	}

	t.Run("drafting", func(t *testing.T) {
		v := base()
		v.Drafting = true
		v.Ribbon = ribbonView(domain.OnboardingLifecycle(domain.OnboardingState{HasStrategy: true, Drafting: true}))

		body := renderForTest(t, "setup_okrs.html", projectID, v)
		for _, want := range []string{"Reading your brief", `class="spinner"`, "window.location.reload"} {
			if !strings.Contains(body, want) {
				t.Errorf("waiting screen missing %q", want)
			}
		}
		if strings.Contains(body, "/setup/okrs/approve") {
			t.Error("the approve form must not render while a draft is still running")
		}
	})

	t.Run("failed", func(t *testing.T) {
		v := base()
		v.DraftFailed = true
		v.DraftError = "the model returned no objectives"
		v.Ribbon = ribbonView(domain.OnboardingLifecycle(domain.OnboardingState{HasStrategy: true, DraftFailed: true}))

		body := renderForTest(t, "setup_okrs.html", projectID, v)
		for _, want := range []string{v.DraftError, "/setup/okrs/redraft", "Try again", brief} {
			if !strings.Contains(body, want) {
				t.Errorf("failure screen missing %q", want)
			}
		}
		if strings.Contains(body, "/setup/okrs/approve") {
			t.Error("the approve form must not render after a failed draft")
		}
		if !strings.Contains(body, v.Ribbon.Headline) {
			t.Error("the ribbon should still say where the project stands")
		}
	})
}

// The comparison table is the most complex page in the app and had no template
// test at all, so a bad field path in it would only ever have been found by
// loading it. It also has to render in two very different states: mid-build,
// when no winner can be picked, and finished, when one must be.
func TestSelectionPageRendersBothStates(t *testing.T) {
	projectID := uuid.New()
	hop := &domain.Hop{ID: uuid.New(), Name: "google-oauth", Status: domain.HopStatusSelecting}

	selection := &SelectionDataView{
		HopID: hop.ID.String(), HopName: hop.Name,
		Criteria: []string{"Simplicity", "Test coverage"},
		Variations: []SelectionVariationView{
			{ID: uuid.New().String(), Name: "oauth-a", Approach: "Signed cookie session.",
				Status: string(domain.VariationStatusPending), CommitRef: "abcdef1234",
				BranchURL: "https://github.com/x/y/tree/oauth-a", DiffURL: "https://github.com/x/y/compare/main...oauth-a",
				DemoURL: "https://oauth-a.fly.dev", FilesChanged: 12, Additions: 340, Deletions: 22},
			{ID: uuid.New().String(), Name: "oauth-b", Approach: "Stateless JWT.",
				Status: string(domain.VariationStatusError)},
		},
	}

	base := func(canSelect bool) *InputRequestDetailView {
		return &InputRequestDetailView{
			InputRequest: &domain.InputRequest{
				ID: uuid.New(), ProjectID: projectID, Title: "Pick a winner for google-oauth",
				Kind: domain.InputRequestKindVariationSelection, Status: domain.InputRequestStatusAssigned,
			},
			Hop: hop, SelectionData: selection, CanSelect: canSelect,
			PendingCount: 1, FailedCount: 1, TotalCount: 2,
		}
	}

	t.Run("ready to choose", func(t *testing.T) {
		body := renderForTest(t, "input_request_selection.html", projectID, base(true))
		for _, want := range []string{
			"oauth-a", "oauth-b", "Simplicity", "Test coverage",
			"Built and awaiting comparison", // the pending one, read through the Ribbon
			"340", "Pick a winner",
			`name="winner"`, // the radio that decides it
		} {
			if !strings.Contains(body, want) {
				t.Errorf("selection page missing %q", want)
			}
		}
		// A failed variation is not a candidate, so it must not be offered as one.
		if n := strings.Count(body, `name="winner"`); n != 1 {
			t.Errorf("expected exactly one selectable variation, got %d radios", n)
		}
	})

	t.Run("still building", func(t *testing.T) {
		body := renderForTest(t, "input_request_selection.html", projectID, base(false))
		if strings.Contains(body, `name="winner"`) {
			t.Error("a winner must not be selectable while variations are still building")
		}
		if !strings.Contains(body, "Waiting for every variation to finish") {
			t.Error("the page should say why no winner can be picked yet")
		}
	})
}

// The Strategy page is the Objectives tab: what the project is for, and what
// pursuing it is costing. Its cost assertions moved to the Costs page when that
// was split out, which left this page with no test at all.
func TestStrategyPageShowsObjectivesAndSpend(t *testing.T) {
	projectID := uuid.New()
	target := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	view := &StrategyView{
		Project:  &domain.Project{ID: projectID, Name: "Pollstar"},
		Strategy: &domain.Strategy{ID: uuid.New(), Name: "Q3 launch"},
		Objectives: []ObjectiveView{
			{
				Objective:  domain.Objective{ID: uuid.New(), Description: "People can sign in"},
				KeyResults: []domain.KeyResult{{Description: "Weekly active users", TargetUnits: "1000 users", TargetDate: &target}},
				HopCount:   2,
			},
			{
				Objective: domain.Objective{ID: uuid.New(), Description: "Nobody is locked out"},
			},
		},
	}

	body := renderPageForTest(t, "strategy.html", map[string]interface{}{
		"ProjectID":   projectID.String(),
		"StrategyTab": "objectives",
		"Strategy":    view,
		"Cost":        &StrategyCostView{ProjectID: projectID.String(), SpentUSD: 41.82, BudgetUSD: 120},
	})

	for _, want := range []string{
		"Q3 launch", "People can sign in", "Weekly active users", "1000 users",
		"2 hops",            // an objective the roadmap is actually pursuing
		"Not planned",       // and one it is not, said plainly rather than left blank
		"$41.82", "of $120", // the budget strip, beside what the money is buying
	} {
		if !strings.Contains(body, want) {
			t.Errorf("strategy page missing %q", want)
		}
	}

	// An objective with no key results cannot be judged met, and saying so is
	// the whole point of showing it.
	if !strings.Contains(body, "no way to tell whether this objective is met") {
		t.Error("an objective without key results should say why that matters")
	}
}

// A project with no strategy must still render: it is the state every project
// starts in, and the page it lands on cannot be the one that errors.
func TestStrategyPageBeforeSetup(t *testing.T) {
	projectID := uuid.New()
	body := renderPageForTest(t, "strategy.html", map[string]interface{}{
		"ProjectID":   projectID.String(),
		"StrategyTab": "objectives",
	})
	if !strings.Contains(body, "No strategy yet") {
		t.Error("a project before setup should say so rather than rendering an empty page")
	}
}
