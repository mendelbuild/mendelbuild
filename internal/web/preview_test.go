package web

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// TestGeneratePreview dumps a gallery of every lifecycle ribbon state to a
// single HTML file, so the component can be reviewed without a database or a
// running server:
//
//	MENDEL_PREVIEW=/tmp/preview.html go test ./internal/web/ -run TestGeneratePreview
//
// It skips unless MENDEL_PREVIEW is set, so it costs nothing in a normal run.
func TestGeneratePreview(t *testing.T) {
	out := os.Getenv("MENDEL_PREVIEW")
	if out == "" {
		t.Skip("set MENDEL_PREVIEW=<path> to generate")
	}

	layout, err := fs.ReadFile(templatesFS, "templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	css := string(layout)
	css = css[strings.Index(css, "<style>")+len("<style>") : strings.Index(css, "</style>")]

	tmpl := partialsTemplate(t)
	var body bytes.Buffer

	section := func(title string) {
		fmt.Fprintf(&body, `<h2 class="preview-section">%s</h2>`, title)
	}
	label := func(s string) {
		fmt.Fprintf(&body, `<div class="preview-label">%s</div>`, s)
	}
	render := func(name string, data interface{}) {
		if err := tmpl.ExecuteTemplate(&body, name, data); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	section("Hop lifecycle")
	for _, st := range []domain.HopStatus{
		domain.HopStatusPending, domain.HopStatusActive, domain.HopStatusSelecting,
		domain.HopStatusCompleted, domain.HopStatusRejected, domain.HopStatusAbandoned,
	} {
		label("hop status: " + string(st))
		render("lifecycle-ribbon", domain.HopLifecycle(&domain.Hop{Status: st}, nil))
	}

	label("hop status: active — all variations eliminated (needs you)")
	render("lifecycle-ribbon", domain.HopLifecycle(&domain.Hop{Status: domain.HopStatusActive},
		[]domain.Variation{{Status: domain.VariationStatusTerminated}, {Status: domain.VariationStatusRejected}}))
	label("hop status: active — building 3 variations")
	render("lifecycle-ribbon", domain.HopLifecycle(&domain.Hop{Status: domain.HopStatusActive},
		[]domain.Variation{{Status: domain.VariationStatusCreating}, {Status: domain.VariationStatusCreating}, {Status: domain.VariationStatusCreating}}))

	section("Variation lifecycle")
	prodHop := &domain.Hop{RequiresProduction: true}
	demoHop := &domain.Hop{RequiresDemo: true}
	for _, st := range []domain.VariationStatus{
		domain.VariationStatusCreating, domain.VariationStatusBlocked, domain.VariationStatusPending,
		domain.VariationStatusMigrating, domain.VariationStatusActive, domain.VariationStatusDraining,
		domain.VariationStatusError, domain.VariationStatusTerminated, domain.VariationStatusPruned,
		domain.VariationStatusRejected, domain.VariationStatusMerged,
	} {
		label("variation status: " + string(st))
		render("lifecycle-ribbon", domain.VariationLifecycle(&domain.Variation{Status: st}, []domain.VariationRevision{}, prodHop))
	}

	// The same statuses on a Hop that only wants a clickable demo. A demo trial
	// never migrates data and never takes real traffic, so the Trial track must
	// not describe it in those terms.
	for _, st := range []domain.VariationStatus{
		domain.VariationStatusActive, domain.VariationStatusDraining,
	} {
		label("variation status: " + string(st) + " — demo-only Hop, so no live-traffic language")
		render("lifecycle-ribbon", domain.VariationLifecycle(&domain.Variation{Status: st}, []domain.VariationRevision{}, demoHop))
	}

	label(`variation status: creating WITH a revision in flight — the case the Refine track exists for`)
	render("lifecycle-ribbon", domain.VariationLifecycle(
		&domain.Variation{Status: domain.VariationStatusCreating},
		[]domain.VariationRevision{
			{ID: uuid.New(), Status: domain.VariationRevisionStatusCompleted},
			{ID: uuid.New(), Status: domain.VariationRevisionStatusInProgress},
		}, prodHop))
	label("variation status: pending, after 2 revisions applied")
	render("lifecycle-ribbon", domain.VariationLifecycle(
		&domain.Variation{Status: domain.VariationStatusPending},
		[]domain.VariationRevision{
			{ID: uuid.New(), Status: domain.VariationRevisionStatusCompleted},
			{ID: uuid.New(), Status: domain.VariationRevisionStatusCompleted},
		}, prodHop))
	label("variation status: pending — hop needs no trial (Trial track dimmed)")
	render("lifecycle-ribbon", domain.VariationLifecycle(
		&domain.Variation{Status: domain.VariationStatusPending}, []domain.VariationRevision{}, &domain.Hop{}))

	section("Decision lifecycle")
	for _, st := range []domain.InputRequestStatus{
		domain.InputRequestStatusNeedsAssignment, domain.InputRequestStatusAssigned,
		domain.InputRequestStatusAccepted, domain.InputRequestStatusResolved,
	} {
		label("decision status: " + string(st))
		render("lifecycle-ribbon", domain.DecisionLifecycle(&domain.InputRequest{
			Status: st, Kind: domain.InputRequestKindVariationSelection}))
	}

	// No <html>/<head>/<body> wrapper: HTML5 infers them, so this renders as a
	// standalone file in a browser and can also be published directly as an
	// Artifact, which supplies its own document skeleton.
	//
	// The styling is Mendel's own, lifted verbatim from layout.html. The point
	// of this page is fidelity to what the app actually renders, so it must not
	// acquire a look of its own.
	page := fmt.Sprintf(`<title>Mendel Lifecycle Components</title>
<style>%s
body { max-width: 960px; }
.preview-intro { color: #666; max-width: 65ch; }
.preview-section {
    margin-top: 40px;
    padding-bottom: 6px;
    border-bottom: 1px solid #ddd;
}
.preview-label {
    font: 600 12px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace;
    color: #999;
    margin: 20px 0 6px;
}
</style>
<h1>Lifecycle ribbon</h1>
<p class="preview-intro">Every state, rendered from the real templates and the
real <code>domain</code> lifecycle mapping — not mockups. The grey monospace
lines are the underlying database status values, shown only to label each
example; they do not appear in the product.</p>
<p class="preview-intro">The roadmap panel that sits above the ribbon on Hop and
Variation pages is not shown here: it is the live roadmap graph, and drawing it
needs the running app.</p>
%s`, css, body.String())

	if err := os.WriteFile(out, []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", out)
}
