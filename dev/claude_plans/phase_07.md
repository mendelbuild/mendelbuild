# Phase 7: Lifecycle Model and Roadmap Context

## Overview

Information-architecture work on the web UI. The system was legible only to
someone who already knew it: a page showed a Hop's *current word* (`selecting`,
`terminated`) but never its *lifecycle*, so a newcomer could not tell where a
thing was in its life, whether anything was expected of them, or how the Hop in
front of them related to the rest of the roadmap.

This phase adds a presentation-facing lifecycle model in `internal/domain`, a
shared ribbon component that renders it, and an embedded roadmap panel that
places the current Hop in the graph. It also fixes three rendering bugs found on
the way, each of which showed a failure as a success.

Scope was deliberately limited to Hop and Variation detail pages. The shared
review shell for agent-conversation pages, and project-level orientation, are
separate phases.

## Key Design Decisions

### Status enums are storage, not presentation

The rule this phase establishes, stated at the top of `lifecycle.go`:
**templates must never switch on a raw status string.**

`VariationStatus` is one column doing three unrelated jobs — build progress
(`creating`/`pending`/`blocked`), runtime state (`migrating`/`active`/
`draining`), and adjudication outcome (`pruned`/`selected`/`merged`/`rejected`).
A Variation can legitimately be built *and* live *and* unjudged at the same
moment. Any template branching on that column is forced to invent an ordering
that does not exist, which is how the same failure-as-success bug appeared
independently in three templates.

So each entity maps its status(es) into a `Ribbon` in the domain layer, and
templates render only the Ribbon.

### Multi-track, not one stepper

- **Hop** — one track. A Hop really is sequential: Planned → Exploring
  approaches → Comparing results → Decided. `completed`, `rejected`, and
  `abandoned` are three outcomes of the *same* final position, so they share a
  stage and differ in Tone and Label.
- **Variation** — four concurrent tracks: Build, Refine, Trial, Verdict.
- **Decision** — one track: Routing → Assigned → Being worked on → Resolved.
  Uses DESIGN.md's "Decision" vocabulary rather than "Input Request".

### Refine is a track, not a repeat of Build

`handleRequestChange` sets the Variation back to `creating`, so a first build and
a third revision are **indistinguishable from the status column alone**. The
revision records are the only place that difference lives, which is why
`VariationLifecycle` requires them:

```go
refining := st == VariationStatusCreating && rev.inFlight
```

### A trial is not one fixed thing

A Hop that only wants a clickable demo never migrates data and never takes real
traffic. Describing its stages as "Applying migrations" and "Serving live
traffic" would promise work that will never happen. `shapeOfTrial` picks the
stage sequence from what the Hop actually asks for, and "Applying data
migrations" surfaces only while a migration is genuinely running.

### Terminal is not a synonym for success

Three outcomes are distinguished by `Ribbon.StatusLabel()`: `Complete`,
`Failed`, and `Closed` (eliminated, not selected, cancelled — over, but neither
a success nor a failure). Badging a failed build "Complete" is the same class of
bug as painting it green.

### The panel shows the real roadmap, not a summary of it

The first cut was a text strip of neighbouring Hop names. It was replaced by the
actual roadmap graph, embedded and scrolled to the Hop in question, because a
second, reduced drawing would drift from the real one, and the *shape* of the
graph — how much is upstream, how much is still ahead — is most of what the
panel is for.

To make divergence impossible by construction, the roadmap page's inline
renderer moved to `static/js/roadmap-view.js` and both callers use it;
`handleRoadmap` and the panel share `buildRoadmapGraph` for the payload.

### Visual language of the ribbon

The first stepper read as a set of radio buttons ("select one of N") rather than
a progression. Three things now carry direction of travel: arrows rather than
plain rules between stages, arrows coloured only as far as progress has actually
reached, and marks inside the dots (tick, cross) that a radio group would never
have. Colour comes exclusively from `Tone` via CSS custom properties
(`--tone`, `--tone-bg`, `--tone-ink`); no rule keys off a status name.

## Files Created

### Domain
- `internal/domain/lifecycle.go` — shared vocabulary: `Tone`, `StageState`,
  `Actor`, `Stage`, `Track`, `Ribbon`, and `stageSeq`.
- `internal/domain/hop_lifecycle.go` — `HopLifecycle(h, vars)`.
- `internal/domain/variation_lifecycle.go` — `VariationLifecycle(v, revs, h)`.
- `internal/domain/decision_lifecycle.go` — `DecisionLifecycle(ir)`, plus
  `DecisionKindLabel`, `DecisionAsk`, `DecisionImportance`.
- `internal/domain/lifecycle_test.go`

### Web
- `internal/web/lifecycle_view.go` — `MiniRoadmap`, `buildMiniRoadmap`,
  `buildRoadmapGraph`.
- `internal/web/templates/partials.html` — `lifecycle-ribbon`, `roadmap-panel`.
- `internal/web/static/js/roadmap-view.js` — the one roadmap renderer.
- `internal/web/templates_test.go` — first tests in the `web` package.
- `internal/web/preview_test.go` — component gallery generator.

## Files Modified

- `internal/web/handlers.go` — `parsePageTemplate` now includes
  `partials.html`; `handleRoadmap` collapsed onto `buildRoadmapGraph`.
- `internal/web/handlers_hop.go` — `HopDetailView` and `VariationDetailView`
  gain `Ribbon` and `Roadmap`; variation detail now loads revisions.
- `internal/web/templates/layout.html` — tone tokens, ribbon, stepper, and
  roadmap-panel CSS; adds `.status-neutral`.
- `internal/web/templates/hop_detail.html` — panel + ribbon; the 76-line
  narrator replaced by actionable-only branches.
- `internal/web/templates/variation_detail.html` — panel + ribbon; new
  "Requested changes" card.
- `internal/web/templates/roadmap.html` — reduced to a call into `RoadmapView`.
- `internal/web/templates/input_request_selection.html` — status mapping fix.
- `internal/web/static/js/dag.js` — `terminated` moved to the failure palette.

## Database Schema Changes

**None.** This phase is presentation-only; it reads existing columns and adds no
migrations.

## Bugs Fixed

1. `variation_detail.html` and `hop_detail.html` mapped `terminated` (a code or
   test failure) to `status-resolved` — success green.
2. `input_request_selection.html` mapped `rejected` to `status-resolved`,
   identical to `merged`, so a losing Variation looked like the winner — on the
   page where you pick the winner. Everything unmatched fell through to
   `status-error`, so a still-building Variation read as an error.
3. The roadmap DAG coloured `terminated` inert grey, as though it were a clean
   shutdown.

All three are now locked out by `TestNoStatusRendersFailureAsSuccess`, which
greps the templates for the banned mappings.

## Verification

```bash
go build ./...
go vet ./internal/...
go test ./internal/...
MENDEL_TEST_DB_URL="postgres://bhs:@localhost:5432/mendel_test?sslmode=disable" go test ./schema/...
```

Test coverage worth knowing about:

- `lifecycle_test.go` keeps exhaustive enum lists (`allHopStatuses`,
  `allVariationStatuses`, `allDecisionStatuses`, `allDecisionKinds`) so adding a
  status **without teaching the lifecycle about it fails the tests** rather than
  silently rendering "Unrecognized state" in the UI.
- `templates_test.go` parses every page exactly as `parsePageTemplate` does.
  Templates are parsed at request time via `template.Must`, so a syntax error
  does not fail the build — it panics on the first request to that page.
- `TestDetailPagesRender` renders both detail pages end to end through
  `renderPage`. Parsing alone cannot catch a nil dereference or a bad field path.

Component gallery, for reviewing every ribbon state without a database or a
running server:

```bash
MENDEL_PREVIEW=/tmp/preview.html go test ./internal/web/ -run TestGeneratePreview
```

The generated file is self-contained and publishable as-is. It does not include
the roadmap panel, which needs dagre and the running app.

`static/js/roadmap-view.js` was exercised under a throwaway jsdom harness to
confirm it renders, centers on the focused Hop, re-centers when the panel is
expanded, escapes Hop and Variation names, and returns cleanly on an empty
roadmap.

## Follow-ups Not Done

- One shared review shell for the agent-conversation pages (roadmap review,
  variation review, selection, credentials, hosting), which are visually
  distinct today despite sharing a workflow. Backend consolidation behind a
  `ReviewKind` interface should extract the domain effects (`approveRoadmap`,
  `approveVariations`, `mergeWinnerToMain`) into a service layer first —
  otherwise five divergent templates are traded for one god-interface.
- Project home / orientation, breadcrumbs, and a human-readable decision queue.
- `variation_detail.html` still maps demo-instance `stopped` to
  `status-resolved` via a fall-through `{{else}}`.
- Roadmap DAG nodes still colour from raw status rather than `Tone`; threading
  tone through the hops JSON would finish the job the ribbon started.
