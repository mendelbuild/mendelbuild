# 14. UI redesign: a design system and an information architecture

The app worked and read as unfinished. This is the plan for a full rev across
the project dashboard, Hop, Variation, roadmap, settings, and deployment pages:
one design system, one page spine, and an information architecture that answers
"what should I do right now?" on the page you land on.

The lifecycle ribbons were the first half of this work and they were right. This
document is mostly about finishing what they started.

## Where things stood

Measured across `internal/web/templates/` before any change:

| | count |
| --- | --- |
| inline `style="…"` attributes | 683 |
| hardcoded colour literals (~40 distinct) | ~700 |
| templates branching on a raw status string | 47 |
| renderings of deployment-channel state, in three different visual treatments | 3 |
| page renders that forgot to add the signed-in user | 5 |

The interesting number is 47.

`internal/domain/lifecycle.go` opens by stating the rule the package exists to
enforce: *templates must never switch on a raw status string*. Every entity
already maps to a plain-English `Ribbon` carrying a `Tone`. But 47 raw-status
branches survived alongside the ribbons — 20 on the Variation page alone — so
each page showed a good ribbon **and** a pile of hand-coloured chrome restating
it, in colours chosen per-site. That is the mechanism behind "wonky": not that
any one page was ugly, but that two systems were describing the same state and
only one of them had been thought through.

Four more problems, all downstream of having no shared vocabulary:

1. **No component set.** The same amber warning box was hand-rolled eleven
   times. Five different button colours appeared inside a single card on the
   Variation page, because colour was picked by eye at each site rather than by
   role.
2. **The project dashboard was not a dashboard.** `/p/{id}` redirected to
   `/strategy`, a 346-line page carrying an onboarding ribbon, a deployment
   card, a budget burn bar, a cost-by-model table, OKRs, a hops table, a
   propose-roadmap form and an input list. Meanwhile `/roadmap` was 35 lines of
   graph and `/inputs` separately listed exactly the things demanding attention.
   The answer to "what needs me?" was split across three pages and sat below a
   cost table.
3. **No consistent action region.** The Hop page put actions in a sidebar card;
   the Variation page had three button rows inside its Details card plus more
   inside the Demo panel; the deployment page scattered validate and deploy
   across two cards. There was no one place to look for "what can I do here".
4. **Settings was split arbitrarily.** Cloud deployment credentials lived on
   `/settings`; the deployment channel that consumes them lived on
   `/deployment`.

Plus the small things that read as unfinished: both detail pages led with a raw
enum labelled "Internal status"; the nav had no active state and no breadcrumb;
back-links were hand-written prose.

## The design system

Three layers, in `internal/web/static/css/`, loaded in dependency order.

### `tokens.css` — the vocabulary

Every colour, space, and size decision made once. A token names a **meaning**,
not an appearance: `--tone-waiting` is "blocked on the user", and it happens to
be amber. Naming it `--amber` would let a template pick amber because amber
looked nice, which is how the app arrived at forty hex values saying nothing
consistent.

**The tone rule is the load-bearing decision.** The six tones are not a palette;
they are `domain.Tone` mirrored one-for-one:

```
ToneNeutral  ToneProgress  ToneWaiting  ToneSuccess  ToneWarning  ToneFailure
```

Go decides which tone a thing is. CSS decides only what that tone looks like.
One vocabulary runs from the lifecycle functions through to the pixel, and
adding a seventh colour without a seventh `domain.Tone` means a template is
choosing colour by eye again.

Each tone carries three values that travel as a unit — the line, the fill, and
the ink that goes *on* that fill. Putting `--tone-N-ink` on the page background,
or `--ink-1` on a tone fill, is a contrast bug by construction.

Mendel green is chrome, not a tone. A green button means "the main thing to
press", never "success" — keeping those distinct is why `--brand` sits outside
the tone set.

Beyond tone: a 4px space scale (eight values, and that is the budget), six type
sizes, three ink weights plus inverse, four surfaces, two elevations, one focus
ring.

### `components.css` — the application

Every visual pattern the app is allowed to use, ~30 components: card, callout,
badge, button, metric, meter, table, key-value list, tabs, field, choice, empty
state, log panel, disclosure, modal, banner, breadcrumb, page header, ribbon,
roadmap panel, plus layout primitives (`.stack`, `.row`, `.split`) that exist so
no template ever writes `display:flex` inline.

Two rules worth stating because they were the actual failures:

- **Buttons are chosen by role, never by colour.** `primary` / `secondary` /
  `danger` / `ghost`. Exactly one primary per view: the thing the page wants you
  to do.
- **Callouts and badges take a tone, and the tone comes from Go.** One `.callout`
  replaces the eleven hand-rolled boxes.

### `legacy.css` — the debt, made visible, then paid

Rules that pages still depended on but the component library replaced, put in one
file so the design system could land in a single piece without rewriting every
page in the same commit. The file only ever shrank: converting a page meant
deleting the rules it was the last user of, so its line count was an honest
measure of what was left — 186 lines, then 153, then none.

**It has been deleted.** The handful of rules that turned out to be real
components rather than debt — the draft-review blocks on the setup screens, the
tuner's quality score — moved into `components.css` under their own heading.

## The page spine

Every page gets the same shape, so a reader who learns one page has learned all
of them:

```
breadcrumb                 where am I, and how do I get back
title (+ kind eyebrow)     what is this
ribbon                     where does it stand, whose move, what happens next
                           — and the page's ONE primary action, in .ribbon-foot
substance                  what this page is actually for
disclosure                 raw statuses, history, per-model costs, estimator notes
```

Two consequences worth being explicit about:

- **The primary action lives in the ribbon foot**, beside the sentence explaining
  why it is being offered. Contextual actions still sit with the content they act
  on; what ends is a page having three competing places to look.
- **The disclosure is the consistent home for anything a newcomer should not have
  to read.** "Internal status" moves there. It stays available — it is genuinely
  useful for debugging — but it stops being the first row of the first card.

## The information architecture

Target route map:

| Route | Is | Was |
| --- | --- | --- |
| `/p/{id}` | **Overview** — what needs you now, roadmap thumbnail, deploy state, budget one-liner | redirect to `/strategy` |
| `/p/{id}/roadmap` | graph **and** hop table together | graph only; the table was on `/strategy` |
| `/p/{id}/strategy` | OKRs and objectives narrative | everything |
| `/p/{id}/costs` | budget, burn, cost by model, where the money went | a card dominating `/strategy` |
| `/p/{id}/inputs` | decision queue | unchanged |
| `/p/{id}/settings` | tabs: Repository · Credentials · Deployment · Members | split across `/settings` and `/deployment` |

Rationale, briefly: the roadmap graph and the hops table are the same
information drawn two ways and belong on one page; cost is a thing you go and
look at, not a thing that should greet you; and the deployment channel and the
credentials it consumes have to be in the same place or neither page can be
understood alone.

The navigation gains an active state, "Input Needed" becomes "Decisions" (the
word `DESIGN.md` uses, and the one that explains itself), and Hop and Variation
pages resolve to the Roadmap section because that is what contains them.

## State inventory

What each page has to render. Every state below appears on `/styleguide`, built
from the real lifecycle functions, so this table and the app cannot disagree.

**Hop** — one track, four positions. `pending`; `active` in five distinguishable
situations (no variations yet, building, blocked, ready, out of candidates);
`selecting`; and three terminal outcomes that share a stage and differ in tone
(`completed`, `rejected`, `abandoned`), plus the unknown-status degradation.

**Variation** — four concurrent tracks, because a Variation can be built and live
and unjudged at the same moment. Twelve statuses, and two that the status column
cannot tell apart on its own: a first build and a revision in flight are both
`creating`, distinguishable only from the revision rows. The Trial track changes
shape with the Hop: a demo Hop promises "Demo available", a production Hop
promises "Serving live traffic", and neither should promise the other's work.

**Decision** — four statuses, and ten kinds whose *ask* differs. The ask is the
entire point of the page, so all ten are inventoried.

**Getting started** — seven states from "nothing yet" to "ready to explore",
including the two that a naive implementation collapses: Mendel drafting OKRs
and a draft that failed both otherwise read as "this project has no objectives".

## Enforcement

Four lint tests in `internal/web/template_lint_test.go`, passing repo-wide. They
are what make this a rev rather than a repaint:

1. **No `style="` attribute in any template.** Absolute, deliberately: "just
   this once, for a width" is how the last 683 accumulated. A computed length
   has a way out — put it in a data attribute and let a script apply it, as the
   budget meter does.
2. **No colour literal.** Colours live in `tokens.css` and are referenced by
   name. A page-specific `<style>` block is still allowed, but it must express
   itself in tokens like everything else.
3. **No `eq .Something.Status "…"` branch.** The rule `lifecycle.go` opens by
   stating, finally checkable.
4. **Every class used must exist in a stylesheet.** A class matching nothing
   renders as unstyled markup, which looks like a layout bug and gets diagnosed
   as one. This is also what stops a seventh tone being invented: `badge-danger`
   is a class nobody defined, so it fails here. It found a real one on its first
   run — `.stage-label` was in the ribbon markup and styled nowhere.

## Phases

- **P0 — Audit and state inventory.** Done; this document.
- **P1 — Tokens, components, styleguide.** Done. `tokens.css`, `components.css`,
  `legacy.css`, `partials.html` rewritten with zero inline styles, `/styleguide`,
  and the `renderPageFor` chrome helper.
- **P2 — Shared partials.** Mostly folded into P1: ribbon, roadmap panel, log
  tail and requirements panel are converted.
- **P3 — Page conversion.** Done, in four commits: Variation and Hop; the IA
  move; settings and deployment; the queue, OKR editor and setup screens; the
  four decision pages.
- **P4 — The IA move.** Done. Overview page, settings tabs, costs page,
  breadcrumbs, nav active state stamped by `navSection`.
- **P5 — Lint tests on, `legacy.css` deleted.** Done.

### Where it ended up

| | before | after |
| --- | --- | --- |
| inline `style="…"` attributes | 683 | **0** |
| colour literals in templates | ~700 | **0** |
| raw-status branches in templates | 47 | **0** |
| `legacy.css` | 186 lines | **deleted** |

## What moved into the domain

The recurring shape of this work was that a template had been left to decide
something it had no business deciding, and the fix was to move the decision into
`internal/domain/`, where it is a few lines of Go with a test:

- **`status_view.go`** — the statuses that are not lifecycles. A demo, a
  revision, a deployment, a validation leg, a membership role, a message author:
  each maps to a word and a `Tone`. Hop and Variation badges read from the
  `Ribbon` itself, so a Variation cannot describe itself one way in a list and
  another on its own page.
- **`VariationActions`** — what may be done to a Variation right now, as
  booleans with no URLs in them. The Variation page had ten branches on the
  status string deciding which of five differently-coloured buttons to draw, and
  two of them disagreed about `blocked`.
- **`VariationLifecycle`, corrected** — a run that stops at its spend ceiling
  sits in `blocked`, so the ribbon had been telling the reader to go and resolve
  a credential that did not exist.
- **`HostingDeployment.InFlight`** — nil-safe, so a project that has never
  deployed is simply not deploying rather than a special case in the markup.
- **`DecisionResolution`** — approved and rejected are both resolutions and are
  not the same news. The queue had been colouring a rejection with the success
  palette.

## Changes in P1

New:

- `internal/web/static/css/tokens.css`
- `internal/web/static/css/components.css`
- `internal/web/static/css/legacy.css`
- `internal/web/handlers_styleguide.go`, `internal/web/templates/styleguide.html`
- `internal/web/styleguide_test.go`

Modified:

- `internal/web/templates/layout.html` — 539 lines to 54. The `<style>` block
  became three stylesheets; the nav gained an active state and a `.page`
  wrapper; the settings warning became a `.banner`.
- `internal/web/templates/partials.html` — rewritten onto component classes;
  zero inline styles, zero colour literals.
- `internal/web/handlers.go` — `renderPageFor`, `navSection`, CSS in the embed.
- `internal/web/logtail.go` — `MaxHeight string` became `Tall bool`, and the
  bracketed level label moved from a template branch to `LogLine.LevelLabel`.
- All 14 page renders route through `renderPageFor`.

Two bugs fell out of the chrome helper: five pages (Hop, Variation, decisions,
one OKR route, and the settings error path) never added the signed-in user, so
they showed no account and no way to log out. Deriving chrome centrally means a
new page cannot be born missing it.

## Notable fixes that fell out

Looking at rendered pages turned up things no test was asking about:

- Five pages never added the signed-in user, so they showed no account and no
  way to log out. `renderPageFor` derives it centrally.
- The OKR editor carried its own stylesheet redefining `.btn-primary` in blue,
  so the one page where you edit your objectives had different buttons from
  everywhere else.
- Required deployment credentials were read from a `deploy-config.yml` fetched
  out of the user's repository — a file `.mendel/`'s rules do not permit, and one
  that could disagree with what the chosen channel actually needed. They now come
  from `hosting.RequiredCredentialsForCombo`, the same source that gates a demo.
- Two modals and three visibility toggles ran on JavaScript that set
  `style.display`. The modals are `:target` now, so their open state is in the
  URL and survives a reload; the toggles use the `hidden` attribute.
- The comparison table shipped two near-identical copies of a 100-line script,
  one per branch of an `{{if}}`, each building grade badges from hardcoded hex.
  One copy now lives in `static/js/selection.js` and uses badge classes.

## Verification

```bash
go build ./... && go test ./internal/...
```

To review the design system without a database:

```bash
MENDEL_STYLEGUIDE_OUT=/tmp/sg/index.html go test ./internal/web/ -run Styleguide
```

To look at every page in every state the suite constructs — including the ones
that are awkward to reach by hand, like a run paused at its spend ceiling or a
channel that failed validation:

```bash
MENDEL_PAGE_DUMP_DIR=/tmp/pages go test ./internal/web/
```

Or, in a running server, visit `/styleguide`. Every component appears in every
tone, and every lifecycle state is rendered from the real domain functions — so
a status added without a sensible ribbon shows up there looking wrong, next to
all the ones that look right.
