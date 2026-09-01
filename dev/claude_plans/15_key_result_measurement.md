# 15. Measuring Key Results

The Strategy page shows what a project is trying to achieve and the Roadmap
shows what it is doing about it, and nothing connects the two. The intended
answer is a timeline: a shared time axis, one row per Key Result grouped by
Objective, Hop pills on each row, and colour saying which KRs are met, which are
on track, and which are not.

Two things block that today, and neither is the drawing.

## What is missing

**There are no measurements.** `key_result_history` exists in the schema —
`(key_result_id, measured_value, measured_at, source)` — and nothing in the
codebase writes to it. No ingestion, no form, no agent. So there is no "where
are we now" for any KR.

**There is no target to compare against.** `key_results.target_units` is free
text: `"1000 users"`, `"< 200ms p99"`. The schema comment claims Core parses it
into a numeric target, unit, comparator and horizon; no such parser exists, and
the string is only ever stored and displayed.

Both have to be fixed before a single pixel of the timeline is honest. A green
KR that nobody measured is worse than a blank one.

## The target becomes structured, and no parser is written

The question was whether an LLM could do the parsing instead of hand-written
code. It could — but the better answer is that **nothing needs parsing at all**,
because the model is already producing the structure and we are throwing it
away.

The drafting agent's own field description asks for exactly this:

> `TargetUnits ... desc:"The target with its unit and comparison, e.g. '>= 100
> signups', '< 200ms p99', '>= 80%'."`

It is being asked for a comparator, a number and a unit, and told to concatenate
them into one string. Splitting that into three fields on `DraftedKeyResult`
costs three `desc` tags, happens once per KR at generation time, and is checked
by a human at the approval step that already exists.

That leaves one way a target could still arrive as prose: someone typing it into
the OKR editor. So the editor takes the three fields too — a comparator select,
a number, and a unit — and **the structured triple becomes the source of truth,
with the prose form derived for display.** `target_units` stops being stored.

The result is that a target is never parsed, at authoring time or at read time,
and the prose can never drift from the number.

Where an LLM would be the wrong tool is the other placement: parsing a stored
string on every render. That is nondeterministic (the same KR could grade
differently on two page loads), it is a spend line on a page view, and CLAUDE.md
requires every agent call to be recorded — a timeline that costs money to look at
is a bad timeline.

### Shape

```
target_comparator  TEXT   -- at_least | at_most | done
target_value       REAL
target_unit        TEXT   -- "users", "%", "ms p99", "signups per week"
```

Three columns, not four. The unit is display-only — comparison is value against
value — so it can absorb any qualifier (`p99`, `per week`) without a fourth
column that nothing would compute on.

**Three judgement modes, not five operators** [038]. The first cut offered
`>=, <=, >, <, =`, and three of those earn nothing: "more than 1000" and "at
least 1000" differ only on the boundary and nobody writing a Key Result means
the distinction, while "exactly" is almost always "at least" in disguise. Where
it genuinely is not — one launch, one certification — what is meant is *did this
happen*, which is now its own mode. The column is named rather than punctuated
because `done` is not an operator.

Display renders as `at least 1000 users`, `at most 200 ms p99`, `Done`.

### Boolean Key Results are available, and weaker on purpose

A `done` target stores a value of `1` and no unit, so every row keeps a number
and the comparison stays one function. Measurements record `0` or `1`.

It is worth being precise about why this is bad practice rather than just
asserting it: **a boolean carries no early signal.** A number can be compared
against the pace needed to reach it, and says on the Tuesday of week three
whether the work is on course. A checkbox says nothing at all until it flips, by
which time it is too late to act on. That is a property of the target, not an
opinion about it, so it is encoded rather than editorialised:

- `KeyResult.ProgressSignal()` returns false for a boolean, and the timeline
  can therefore only report it as *met* or *not yet* — never *on track*. It
  counts toward the lower bound of progress and never the upper.
- The drafting agent is told to prefer a number and to reach for `done` only
  when there is genuinely nothing to count.
- The OKR tuner is told to score a `done` target lower on measurability for the
  same reason.

### Existing rows

They cannot be given honest values, and per the prototype-stage guidance in
CLAUDE.md the answer is not a compatibility shim. Migration 037 adds the three
columns `NOT NULL` and deletes existing `key_results` along with their
`objective_key_result_pairs` and `funding_source_key_results` rows. Projects
redraft their OKRs through the flow that already exists.

**This is a deliberate data loss and should be stated as one.** It is chosen
because the alternative — nullable columns and a "target not yet structured"
state threaded through the timeline, the tuner and the ask — is permanent
complexity in exchange for preserving prototype rows.

## Where the numbers come from

A recurring **Input Needed** request, not a form somebody has to remember to
visit. This reuses the queue rather than adding a surface, which is what the
queue is for: things only a person can supply.

- **One request per project, covering every KR.** Not one per KR — a project
  with nine KRs would otherwise generate nine items a week and the queue becomes
  noise.
- **At most one open at a time.** When the period rolls over, the existing
  request is updated rather than a second filed. Same shape as the deployment
  credential ask.
- **Each KR row takes a value and an optional "as of" date**, defaulting to now.
  Backdating matters: someone answering on Thursday often knows Monday's number,
  and `measured_at` is a `TIMESTAMPTZ` so recording it truthfully costs nothing.
- **Any row may be left blank.** A skipped KR records nothing; it does not
  record a zero.
- **Never blocks anything**, and is not filed at all when no KR has a target
  date.
- **Weekly, for every Key Result.** Not settable per KR, and this is a product
  position rather than a simplification: a Key Result that can only be measured
  at the end of the quarter cannot tell anyone whether the work is going well
  while there is still time to change it. That is now part of what the drafting
  agent is told a good Key Result looks like.

Resolution writes one `key_result_history` row per value supplied, then resolves
the request. Nothing else in the system waits on it.

### Judgement, only where it disagrees

The status is derived: measured value against target, against time remaining.
A human may override it, and the override records a reason.

Asking "is this on track?" every week produces a reflexive yes, and then two
signals that have to be reconciled. An override is informative precisely when it
contradicts the arithmetic — *"we are at 40% with two weeks left, but the launch
lands Tuesday"* is knowledge the data cannot have. Recording it with its
rationale mirrors what CLAUDE.md already requires of cost estimates: who said
it, and on what basis.

### Staleness, and escalation

Staleness is a first-class state, not an absence.

- The timeline shows **"last measured N days ago"** on every KR row, always.
- Past one cadence period with no measurement, the KR renders **stale** rather
  than carrying a colour it has not earned.
- **At twice the period, the request escalates.** It starts life
  low-importance and non-blocking; at 2× its `importance_score` rises, its tone
  moves from neutral to waiting, and it appears in **Needs you** on the
  Overview.

This is also the answer to a question raised earlier and left open: whether the
queue needs to distinguish blocking from non-blocking work. It does, but the
distinction is a spectrum rather than a switch, and staleness is what moves an
item along it. The queue gains a "holding up work" / "improves the picture"
reading, and a stale measurement ask crosses from one to the other on its own.

## What the timeline can then show

| Reading | Derived from |
| --- | --- |
| Met | latest measurement satisfies the comparator against the target |
| On track | on or ahead of the straight-line pace to target by `target_date` |
| Off track | behind that pace |
| Stale | no measurement within one cadence period |
| Never measured | no history at all |
| Overridden | a human said otherwise, with a reason, on a date |

The pace model is linear and will be wrong for anything that grows in steps.
That is acceptable only if it is legible, so the row says what it assumes —
*"at this pace, 700 of 1000 by 15 Sep"* — which is a falsifiable claim rather
than a colour.

Hops appear as **pills on the Objective group header, not on individual KR
rows**. Some Hops are genuinely Objective-scoped and others serve one Key
Result, and the data only records the Objective link — so attaching them to KR
rows would over-attribute every Objective-scoped Hop to every KR beneath it.
Hanging them off the container is both what the data supports and what is true
of most of them.

They are pills rather than bars: Hops carry no dates, and DESIGN.md section 2.1
sequences them by dependency rather than wall-clock time, so placing them on a
time axis would contradict the model. A pill shows its Hop's own lifecycle
reading, so one blocked on dependencies says so.

## What the timeline draws

One comparison, made visible: **a bar's fill is how much of the goal is done,
and the line down the page is how much of the time is gone.** Fill ahead of the
line is on track. Two unlike quantities are made comparable by sharing a track,
which is the trick the budget meter already uses, so it is a reading people will
have seen before.

The bar runs from the day a Key Result was written to the day it is due.

**The baseline progress is counted from depends on the direction of the target.**
For one that should grow it is zero: you start with no users and no signups, and
"820 of 1000" is a reading anyone recognises. For one that should shrink there is
no such floor — latency does not start at zero and improve upwards — so the
baseline is the first measurement. The consequence is deliberate: a shrinking
target shows no fill until it has been measured twice, because one reading of
260ms against a 200ms goal says nothing about whether the number is falling.

Four things the panel refuses to do, each of which would be a claim the data
cannot support:

- **No fill without a baseline.** An unmeasured Key Result is hatched, not empty:
  empty says *zero* and hatched says *unknown*.
- **No fill for a boolean, ever.** That is the whole reason it is the weaker kind
  of Key Result, and half-filling a bar would invent the signal it lacks.
- **Staleness outranks being on track.** A number a fortnight old supports "was
  on track a fortnight ago", which is not a claim a green bar makes, so a stale
  row is drawn in the waiting tone whatever the arithmetic says.
- **Hops sit in the label column, not over the axis.** A Hop has no date at all;
  laid across a time axis it would read as positioned in time.

## Progress against budget

Once measurements exist, the Costs page can answer the question it currently
cannot: is the spend buying anything.

It should report a **band, not a point**. Turning a measurement into "43%
complete" needs a progress model — a baseline to measure from and an assumption
about the shape of the curve — and neither is available or honest. Counting is:

- **Lower bound**: Key Results actually met, over the total.
- **Upper bound**: those met plus those on track, over the total.

*"Between 3 and 6 of 8 key results — 38% to 75% — against 62% of the budget
spent."* Both ends are derived by counting, which needs no baseline and makes no
claim about how growth is shaped. A KR that is stale counts toward neither
bound, which is the honest treatment: not measuring something is not evidence
that it is going well.

## Work, in order

1. ~~Migration 037: structured target columns; drop the old rows.~~ Done.
2. ~~`DraftedKeyResult` gains the three fields; OKR editor takes them; prose
   derived.~~ Done, and 038 reduced five operators to three judgement modes.
3. ~~`key_result_history` read/write queries.~~ Done.
4. ~~The recurring request: new `InputRequestKind`, the worker that files and
   rolls it, the form that resolves it.~~ Done, migration 040.
5. ~~Staleness and escalation.~~ Done.
6. ~~The timeline on the Strategy page.~~ Done.

All of it is in. The numbers are collected weekly through the queue and drawn on
the Strategy page's Objectives tab.

### What 4 and 5 settled in the building

- **The ask is filed against the strategy**, and there is at most one open per
  strategy ever. When the period rolls over on an unanswered one it is updated
  in place: a project that has ignored the ask for a month should face one
  insistent request, not four polite ones.
- **`measurements_asked_at` lives on the strategy**, not on the request. Asking
  and answering are separate facts, and a request updated in place must not read
  as a fresh one.
- **A strategy is as stale as its least measured Key Result.** Reading the newest
  measurement instead would let one diligently updated goal hide five nobody has
  touched.
- **Escalation is the title and the importance score**, nothing else. The ask
  starts at 0.2 — "Low" in the queue's own words — and goes to 0.8 when overdue.
  A request that shouted from the day it was filed would teach people that the
  queue exaggerates.
- **Answering closes the ask even with rows skipped.** Holding it open until
  every Key Result has a number would let one unmeasurable goal keep it open for
  ever, which is the failure mode that makes queues get ignored.
- **A blank row records nothing, and a zero records a zero.** The form is
  explicit about this, because a Key Result nobody could measure this week must
  not appear as a collapse.
- **The previous reading is shown but never prefilled.** A form that pre-answers
  itself collects last week's number again from anyone who taps through it.

## Verification

```bash
go test ./internal/... ./schema/...
TZ=America/Los_Angeles go test ./internal/db/   # measured_at is an instant
```

The timezone run matters here specifically: `measured_at` is a `TIMESTAMPTZ`
compared against `target_date`, which is a `DATE`. That is exactly the mixture
migration 035 exists to keep honest.
