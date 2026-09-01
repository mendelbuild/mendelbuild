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
target_comparator  TEXT   -- one of >=, <=, >, <, =
target_value       REAL
target_unit        TEXT   -- "users", "%", "ms p99", "signups per week"
```

Three columns, not four. The unit is display-only — comparison is value against
value — so it can absorb any qualifier (`p99`, `per week`) without a fourth
column that nothing would compute on.

Display renders as `≥ 1000 users`, `< 200 ms p99`.

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
- **Weekly by default**, project-settable.

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

Hops appear as **pills on the KR row, not positioned on the axis**. Hops carry
no dates, and DESIGN.md section 2.1 sequences them by dependency rather than
wall-clock time; placing them on a time axis would contradict the model. A pill
shows its Hop's own lifecycle reading, so one blocked on dependencies says so.

## Open

1. **Hop-to-KR linkage.** Hops link to *Objectives* (`params.objective_ids`), so
   pills on a KR row are transitive: a Hop serving Objective O appears on every
   KR of O, even when it moves only one. The alternative is a direct link — a
   `hop_key_results` table plus the roadmap proposer emitting KR ids alongside
   the objective ids it already emits. Transitive is free and slightly wrong;
   direct is a contained change and correct.
2. **Cadence override granularity** — per project, or per KR? A weekly signup
   count and a quarterly retention number do not want the same rhythm.
3. **Budget juxtaposition.** Once "% of objectives complete" exists, showing it
   beside "% of budget spent" is two numbers on one line, and answers the
   question the Costs page currently cannot: is the spend buying anything.

## Work, in order

1. Migration 037: structured target columns; drop the old rows.
2. `DraftedKeyResult` gains the three fields; OKR editor takes them; prose
   derived. Tuner scores measurability against the structured target.
3. `key_result_history` read/write queries.
4. The recurring request: new `InputRequestKind`, the worker that files and
   rolls it, the form that resolves it.
5. Staleness and escalation.
6. The timeline on the Strategy page.

Steps 1–3 are inert on their own: nothing changes for a user until 4. That is
deliberate — it means the data model can land and be lived with before any of it
is drawn.

## Verification

```bash
go test ./internal/... ./schema/...
TZ=America/Los_Angeles go test ./internal/db/   # measured_at is an instant
```

The timezone run matters here specifically: `measured_at` is a `TIMESTAMPTZ`
compared against `target_date`, which is a `DATE`. That is exactly the mixture
migration 035 exists to keep honest.
