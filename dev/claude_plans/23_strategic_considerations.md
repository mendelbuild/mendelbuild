# 23. Strategic Considerations: drafting objectives against a standard

## What prompted this

The first real project drafted through the guided flow — a constituent-polling
tool — produced three objectives that were one sentence of brief cut into three
pieces. All three served the elected official. None was about whether anyone
would adopt it, whether the people being surveyed would answer, or whether it
worked well enough to be used. The person reading them was disappointed, and
right to be.

Three things were true at once, and only the first was obvious:

1. **The objectives predated the prompt that forbids them.** The project was
   drafted at 09:33 PDT on 2026-08-31; 6e77706, which raised objectives above the
   mechanism, was committed at 11:02 the same day. The first objective is now
   quoted verbatim in that prompt as the "too tactical" example, having been
   harvested from this very draft.

2. **The broad thinking had happened and had nowhere to go.** The draft's
   `open_questions` asked whether this was a single-official pilot, how
   respondents would be verified as constituents, and what margin of error would
   count as data-driven. Those are strategy. The prompt routed them to a bucket
   that never becomes an objective, via a line that could not tell **extra
   scope** from **a success condition on scope already asked for**:

   > *"Do not add an objective for something the user never mentioned just
   > because it is good practice; put it in open_questions instead."*

3. **The grader was pulling the other way.** The OKR tuner scored that same
   too-tactical objective **0.75**, praising it as *"specific enough to guide
   development"*. Its rubric asked whether an objective was *"specific enough to
   guide action"* — which rewards naming the mechanism, exactly what the
   strategist forbids. Two prompts written months apart, disagreeing.

## The rule this is built on

The gaps were software quality, growth, and the experience of the people being
surveyed. Naming those three in the prompt would have fixed this project and
broken the next one: every project would answer those three questions whether or
not they were its questions, and the fourth gap would still be missed.

So the system derives the dimensions from the project, then checks its own
coverage against what it derived.

## Two enumerations, neither of them a checklist

A **Strategic Consideration** is something the drafting agent worked out about a
project before writing any objective, and then had to either cover or decline.
Two kinds:

**Failure mode.** *Assume the software works exactly as described. Every feature
is built, nothing is broken. The project is still a failure. List the ways.*

The stipulation is load-bearing. Saying the code works excludes the mechanism,
because the mechanism is what the brief already said — which is how this reaches
adoption, trust, effort and reputation without naming any of them.

**Party.** *Who or what does this system exchange something with, such that
their experience of the exchange decides whether it succeeds?*

Not only the person who logs in: whoever is on the receiving end, whoever
operates it daily, a program or agent that calls it, a supplier whose outage is
indistinguishable from your own, anyone reading its output adversarially.

The second kind absorbs software quality rather than needing a rule for it. For
a party that cannot complain — a program, a scheduled job — a bad experience
*is* latency, error semantics and uptime. Quality arrives through the same
question.

Applied to the project that prompted this, the striking result is that the
constituent does the most work in the system and appears in none of the three
objectives.

## Two passes

| | |
|---|---|
| **Pass one** (`agent.ConsiderationDrawer`) | brief → considerations |
| **Pass two** (`agent.Strategist`) | brief + considerations → objectives + coverage |

Pass one's output is not a draft. Nothing is planned against it and the user
approves none of it; it exists so that pass two has a standard to be measured
against, and so the review screen can show what was weighed rather than only
what was chosen.

Coverage is the forcing function: **every consideration lands in exactly one
place** — an objective's `covers` list, or `uncovered` with a reason. Objectives
are still capped at four and the cap still binds, so declining is a normal
outcome and not a failure. What is not allowed is deciding silently.

### Reference keys

Objectives have no identity until they are written, so pass two cannot answer
"which objective covers this". It answers "which of these does this objective
cover", against short keys (`C1`, `C4`) assigned by position. `coverageFromDraft`
maps those back onto real ids once `ReplaceDraftOKRs` has minted them.

Two things are dropped there on purpose: a key matching no consideration is a
hallucination and names nothing to record, and a consideration mentioned in
neither place is left **unjudged** rather than written down as a decline.

### Unjudged is not uncovered

`covered_by_objective_id` and `uncovered_reason` are both nullable and mutually
exclusive, and both null is a third state. "Mendel did not say" is a gap in its
reasoning the user should see; "Mendel said no, because —" is a judgement they
can argue with. Converting the first into the second would put words in its
mouth on the one screen where a user who would not have thought of the
consideration themselves gets to disagree. Same rule as `FactUnknown` and
`DomainObservation.Known`.

## The grader and the drafter now share a rubric

The tuner's objective half was rewritten as the strategist's own test asked
backwards: outcome versus mechanism, survives-a-change-of-design, plain
language. The line rewarding "specific enough to guide action" is gone, and a
feature list now scores below 0.4 *however clearly it is written* — the rubric
says so explicitly, because a clear feature list is the more dangerous of the
two failures and the one that survives review.

The key result half was already aligned with the strategist and is unchanged.

This matters beyond the score itself. A grade is only usable as a control signal
once it measures the right property, and wiring the old one into anything would
have automated the disagreement.

### Why the grade does not lock anything

The idea this replaced was to use a high grade to quasi-lock an objective
against redrafting. The 0.75 above is the argument against it: under that
scheme, the objective most in need of rewriting is the one that gets protected,
and praised on the way in.

The asymmetry decides it. A wrong grade that *targets* a redraft costs one
unnecessary rewrite, which the user sees and can reject. A wrong grade that
*locks* freezes a bad objective into the plan, invisibly, with everything
downstream planned against it. A per-item grade also cannot see a set-level
property: three individually excellent objectives that all serve the same party.
That judgement is the coverage list's job, not the tuner's.

## Files

| File | What changed |
|---|---|
| `schema/migrations/054_strategic_considerations.*` | new table |
| `internal/domain/types.go` | `StrategicConsideration`, the two kind constants, `Covered`/`Judged` |
| `internal/agent/considerations.go` | pass one: the drawer, its prompt and schema |
| `internal/agent/strategist.go` | pass two takes considerations; coverage rule in both prompts; `KeyedConsideration`, `ConsiderationRef` |
| `internal/agent/types.go` | `DraftedObjective.Covers`, `DraftedStrategy.Uncovered` |
| `internal/agent/okr_tuner.go` | rubric reconciled with the strategist's |
| `internal/db/consideration_queries.go` | replace, set coverage, read |
| `internal/db/onboarding_queries.go` | `ReplaceDraftOKRs` returns objective ids in order |
| `internal/web/handlers_onboarding.go` | `drawConsiderations`, `coverageFromDraft`, `considerationViews` |
| `internal/web/templates/setup_okrs.html` | "Taken into account" block; objectives numbered |

## Cost

One extra Sonnet call per draft, recorded as `consideration_drawer` through
`cost.Recorder` like every other charge. A draft that was one call is now two.

## Degrading

Pass one failing is not fatal. Drafting from the brief alone is what Mendel did
before this existed, and it beats turning a spinner into an error because the
step meant to *broaden* someone's objectives could not run. What is lost is the
coverage check, and the screen shows no considerations rather than an empty list
reading as "nothing to consider".

## Verification

```bash
go test -short ./internal/web/ ./internal/agent/   # coverage mapping, review screen
go test ./schema/...                               # migration against full.sql
```

The coverage mapping tests cover the four cases that come back from a model:
refs that map, refs that do not exist, a consideration claimed and declined at
once (covered wins — it is the more specific claim), and one mentioned nowhere
(stays unjudged).

## What followed, from using it

Four changes came out of the first real draft read on screen.

**Open questions became answerable.** The agent is told to raise a question only
when the answer would change the objectives, and it asks good ones. They were a
bullet list with nothing to answer them with. Each now carries two to four
plausible answers as choices plus a free-text box, and answering redrafts.
Recognising a good answer is far easier than composing one -- the same reason
the objectives are drafted rather than asked for. Answers survive redrafts and
travel as part of the brief, since they are the parts of it the user had not
thought to write down until they were asked. `strategy_open_questions` [055].

**One editor.** There were two, and the second could nest objectives, share a
key result between them, and add or remove rows. The first two were used by
nobody -- zero of each in staging -- and were wired halfway: every other screen
reads root objectives, so anything nested was invisible outside the editor that
made it. The review screen became the editor: before approval it is the review
that sets the project in motion, afterwards the same fields save in place.
Single level for now, with `parent_id` and the junction table left in place so a
richer OKR model stays an addition rather than a migration.

**The grader's critique is acted on before the draft is shown.** The screen had
put a key result in front of someone over Mendel's own verdict that its
done-or-not target could not say whether the work was going well. That asks the
reader to make the edit the grader just described, which is exactly the edit
someone new to writing key results cannot make. Lines below 0.6 -- where the
tuner's guide stops saying "one edit away" -- are redrafted with the grader's own
sentence as the instruction, then graded again. Once: another pass is a loop
with a model in it and a person waiting on it. Whatever survives is shown with
its score, because a critique that survives a rewrite is worth reading.

This is the "invert the signal" decision made concrete. The grade targets a
redraft; it still locks nothing.

**Key result dates align on the cycle end.** The drafter is no longer asked for
a date and the per-row date field is gone. A cycle is not a sequence of
deadlines: what tells you mid-cycle whether the work is going well is that each
key result can be measured weekly, which is already required. A staggered due
date is a milestone, and a milestone is a Hop.

## Not done yet

These were designed alongside the above and deliberately left:

- **Stable identity across a redraft.** `ReplaceDraftOKRs` hard-deletes every
  objective and key result and reinserts with fresh UUIDs, so nothing survives —
  not the user's edits, not tune scores. The review screen has to warn that
  *"anything you typed above is discarded"*, which forces a choice between
  editing and asking for a redraft. This is the prerequisite for the next two.
- **Per-objective and per-key-result feedback.** One textarea for the whole
  draft is the only channel today. Per-item feedback needs something to anchor
  to, hence the above.
- **The redraft as a diff.** *"Rewrote objective 2 — it listed what the user can
  do rather than what those actions were for"*, before and after. This removes
  most of the reason to want locking, since nothing vanishes silently and any
  single change can be rejected. It is also the surface that teaches the
  mechanism/outcome distinction to someone who does not have it yet, by showing
  it applied to their own project.
