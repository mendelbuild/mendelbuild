# The Functional Area Matrix — Design

Status: **draft, revised after review.** No code written. Nothing under
`internal/` is touched by this document.

Companion to [13_live_traffic_experiments.md](13_live_traffic_experiments.md)
and [16_experiment_routing.md](16_experiment_routing.md), which between them
invented eleven new preconditions without noticing that they were the same kind
of thing as the thirty-odd already in the tree. This document is about that kind
of thing.

The shape, in one sentence: **rows are Functional Areas — things Mendel can do
for a project — columns are Functional Area Conditions, and a row is available
when every condition marked required for that row is true.** A user who wants
live experiments gets a checklist of what is not yet true, with the ones that
are their move distinguished from the ones that are Mendel's and the ones that
are nobody's.

---

## 1. Why this is worth building, stated as the failure it fixes

Ask the product today: *why can't I run a live experiment?*

The answer is spread across `internal/experiment/datastore.go`
(`ErrUnsupportedDatastore`, and only if you call the right function),
`internal/codegen/experiment_declaration.go` (`Validate`, which returns one
reason and stops), `internal/domain/experiment.go` (`NotReadyToStart`, which
also returns one reason and stops), four places in `handlers_demo.go` that
return `http.StatusBadRequest` with a sentence, and two plan documents
describing conditions that have no code at all. There is no page. There is no
list. There is no way to learn the second reason without fixing the first.

That is the whole problem, and it is not really about experiments. It is the
same problem for demos, for production deploys, for named demos over https, and
for every functional area added after them. Each one grew its own gate at the
moment it needed one, in whatever idiom the surrounding file used.

### 1.1 What the audit actually found

The brief for this work estimated "roughly fifteen such invariants, enforced
through six different mechanisms." An audit of the tree found **more than
forty**, through **seven** mechanisms. The undercount is itself informative:
these things are hard to see because no two of them look alike.

The seven mechanisms:

| Mechanism | Example | Shape |
|---|---|---|
| Typed capability struct + a `Require` function | `experiment.RequireForExperiments` | Extensible; one field, one sentence |
| Ordered ladder with per-step state | `domain.DomainReadiness` | Ordered, gated, observed |
| Status list with a `Blocking()` predicate | `domain.EvaluateRequirements` | Per-item, two scopes, deferrable |
| Boolean struct + a list of missing names | `domain.ProjectReadiness` | Flat, no detail, no remedy |
| Lifecycle ribbon assembled by a `switch` | `domain.OnboardingLifecycle` | Prose per state; not enumerable |
| Inline check in an HTTP handler | `if !channel.IsDemoValidated()` | Invisible until you hit it |
| Failure inside a background job | `failDemo("Missing credential " + name)` | Discovered after a deploy is burnt |

The last two are the ones that hurt. A condition enforced only by an inline
handler check cannot be listed, cannot be shown in advance, and cannot be linked
to. A condition enforced only by a background job's failure costs the user a
deploy to discover.

### 1.2 The two prototypes this widens

Two of those seven are not ad hoc, and this design is deliberately a
generalisation of exactly those two rather than a new abstraction over all
seven.

**`internal/domain/domain_readiness.go` is the checklist half.** It already has
ordered steps, per-step state (`done` / `waiting` / `yourmove` / `blocked` /
`checking`), a `gate()` helper that keeps a step from claiming to be the user's
move before it is startable, a headline naming who is holding things up, fan-out
handled by counting rather than by growing a rung, and an `observed()` wrapper
that refuses to answer from evidence Mendel has not gone and got. It is this
matrix for one functional area, and everything in it is right.

**`internal/experiment/datastore.go` is the checking half.** `Capabilities` plus
`RequireForExperiments` is the only place in the tree where adding an invariant
is one struct field and one sentence naming what is missing. The sentences are
the model for every sentence this design asks for: *"postgres cannot apply a
change and undo it without a trace, so it can only be verified against if it is
a non-production datastore Mendel may reset."* That names the finding, names the
consequence, and names the remedy, in one sentence, without the reader opening
anything else.

Neither is being replaced. The work is to widen the first to more than one
functional area and the second to more than one subsystem, and to make them the
same mechanism.

---

## 2. Vocabulary

Non-core vocabulary is spelled out in full here, even where that is verbose
(D32). Mendel's core words — Hop, Variation, Arm, Assignment Unit — have earned
short names by being used constantly and defined once. Everything invented by
this document has not, and a short name that collides with an existing one costs
more than the characters it saves.

- **Functional Area** — something Mendel can do for a project: run a demo,
  deploy to production, serve deployments by name over https, run a live
  experiment. A row of the matrix.

- **Functional Area Condition** — something that must be true for a functional
  area to be available. A column. Written in full throughout; "condition" alone
  is not used as a term of art.

The two words this document previously coined and no longer uses:

- **Clause** is gone. It existed only to carry a disjunction, and §3.1 now puts
  the disjunction inside a single condition where it belongs. A clause was never
  distinguishable from a condition, which is the correct objection to it.

- **Subject** and **Finding** are gone as vocabulary. They were implementation
  names promoted to terms by accident. What a condition is evaluated *about* — a
  project, a Hop, a Variation, a deployment — needs no coined word, and `Subject`
  is in any case already taken by `Ribbon.Subject`, where it means the name of
  the thing a ribbon narrates.

Two words that are kept but qualified, because the bare forms are vague:

- **Evidence Source** — how Mendel knows: probed, observed, declared, asked, or
  derived. "Source" alone could mean the repository.
- **Declaration Scope** and **Satisfaction Scope** — see §3.3. They are
  frequently different, and a single word for both is the bug that section is
  about.

Following §16's note on overloaded words: "capability" was the first candidate
for the row concept and is rejected because the codebase already uses it twice.
`experiment.Capabilities` is a datastore adapter's self-report about what it can
do, which is an *input to* conditions rather than a row, and keeps its name.
`internal/web/capabilities_test.go` uses "capability" for what a signed-in reader
can reach on a page, an unrelated meaning inside the package that will hold this
code; that file should be renamed (`reachability_test.go`) rather than left to
collide.

---

## 3. The four questions, settled

The brief named four things that must be settled before the shape is fixed. A
fifth turned up in the audit and is settled with them. The first is settled
differently from the first draft of this document, and the correction is the
most useful thing in it.

### 3.1 Every marked cell is required, and the one real disjunction is not a matrix feature

The first draft of this document argued that the matrix must express
disjunction, because `RequireForExperiments` needs `StructuralDiff` **and**
(`SpeculativeApply` **or** `Disposable`) **and not** `live.Disposable`. It
proposed a conjunction of disjunctive clauses to carry that.

**That was wrong, and the review's framing is right: a functional area names a
set of conditions, and all of them must be true** (D33). The matrix is a
selection — each cell is *required* or *not applicable* — and there is no
operator in it beyond `and`.

What the first draft got wrong was the level at which the disjunction lives. The
three booleans above are not three conditions. They are three *inputs* to one
condition, which is properly stated as:

> **Mendel can learn what a migration does without risking production data.**

That is one thing a person can want, one thing that can be true or false, and
one thing with one remedy. Whether it is satisfied by transactional DDL or by a
throwaway datastore Mendel may reset is an internal matter of how the condition
is evaluated (D34). The raw `Capabilities` fields stop being columns and go back
to being what they are: an adapter's self-report, consumed by an evaluator.

The negation dissolves the same way. `live.Disposable == false` is not a column
either; it is part of the definition of the condition **"the migration will run
against production, not against the throwaway copy."** Stated that way it is a
positive, singular fact with its own sentence, and the first draft's objection to
positively-phrased conditions — that two names for one fact drift apart —
evaporates, because there is now only one name. The `Disposable` boolean is not
a name for anything the user can see.

**The test for whether a condition is secretly hiding a disjunction it should
not:** would the two routes produce two different asks, shown to the same person
at the same time? If yes, they are two conditions and the functional area is
under-specified. If no, one condition is right.

The datastore case passes cleanly. Transactional DDL is a property of the engine
that the user cannot go and acquire — where it holds, the condition is simply
satisfied and nothing is asked. Where it does not, there is exactly one ask:
give Mendel a non-production datastore it may reset. The checklist never says
"make both true", because it never had two things to offer.

Worth recording honestly: this was the *only* genuine disjunction the audit
found, in more than forty conditions. Building a general disjunction mechanism
for it would have been the exact failure mode §6 warns about — an abstraction
sized for one example.

### 3.2 Conditions have dependencies

`gate()` exists because a ladder that shows three things to do at once, two of
which are impossible, is worse than one that shows the next one. A flat
functional-area × condition matrix loses that, and the loss is not cosmetic: the
certificate step in the domain ladder is *never* the user's move, and a matrix
that showed it as unsatisfied alongside the DNS record it depends on would send
someone to a certificate authority to complain about a record they typed
wrongly.

**Conditions form one global directed acyclic graph, and `blocked` is computed
rather than authored** (D35). A condition declares what it depends on; a
condition whose dependencies are unsatisfied is `blocked` regardless of its own
evaluator, and its evaluator is not run — which also saves the DNS lookups and
cluster probes that would have no meaning yet.

The graph is between conditions, not between a functional area and its
conditions. That distinction is what lets §4.2's table be a table: the row says
*which* conditions apply, the graph says *in what order* they can be worked on,
and two rows sharing a condition inherit the same ordering for free.

Global rather than per-functional-area, because the dependency is a fact about
the world and not about who is asking. "The certificate is issued" depends on
"the challenge records resolve" whether the asker is named demos, production
https, or something not yet built. A per-row ordering would let two rows
disagree about the shape of reality, and the disagreement would be silent. If a
case ever genuinely needs row-specific ordering, that is evidence the condition
is two conditions (O14).

Authoring the order by hand — a sort key, or declaration order, which is what
the domain ladder does today — works for one linear ladder and stops working the
moment two rows share conditions in different arrangements. It is also the kind
of thing that is correct when written and wrong three commits later, with
nothing to notice.

### 3.3 Conditions have different scopes, sometimes within one condition

A domain is project-scoped. A live experiment is per-Hop. An additive migration
is per-Variation. That much a single scope field handles.

`requirements.json` does not fit in a single scope field, and it is not an
exotic case — it is the mechanism most of the product's user-facing asks already
run through. A requirement is **declared** per-Variation, because what the code
needs is a property of the code. A `secret` is **satisfied** per-project,
because an OAuth client ID is entered once and serves every Variation and
production too. An `acknowledgement` is **satisfied** per-deployment, because
the demo's redirect URI and production's are different strings and both must be
registered.

**So a condition carries a Declaration Scope and a Satisfaction Scope** (D36).
Evaluation projects what is being asked about onto the satisfaction scope — a
Variation projects up to its project for a secret, and down to each of its
deployments for an acknowledgement.

This is the single most load-bearing decision in the document, because a
checklist without it asks project-level questions about a Variation and
per-deployment questions about a project, and both readings are wrong in a way
that is hard to see in review and obvious in use.

Projecting downward fans out: one acknowledgement condition against a project
with a demo and a production deployment is two answers, and against a project
with twenty demos is twenty. **A coarser question aggregates the fan-out into
one row that counts** (D37), which is not an invention — it is what the domain
ladder already does for certificate challenge records, and the comment there
gives the reason: they are created in the same sitting, in the same tool, and a
ladder that grows a rung per zone tells the reader the shape of the task changed
when it did not. `"2 of 3 created. Still to create: _acme-challenge.demos"` is
the right output, and it generalises.

### 3.4 What happens when a condition is absent

The most valuable column, and today the least consistent. The audit found five
different behaviours for an absent condition: decline by name
(`ErrUnsupportedDatastore`), file an input request (`syncDomainRequest`), render
an empty state, return a `400` from a handler, and fail inside a background job
after a deploy has already started. A sixth — do nothing at all, because the
condition exists only in a plan document — covers the eleven from §13 and §16.

Two decisions make this uniform.

**Every condition carries the sentence naming what is missing, and the declining
code path and the checklist render the same one** (D39). Not "the same wording",
the same string, from the same function. This is the mechanical guarantee behind
the whole product claim: if the two can differ, they will, and the page will
stop being the answer to "why can't I."

The sentence is hand-written per condition, in the register
`RequireForExperiments` already uses: what was found, what it means, what would
change it. There is no generated version of this that is worth reading.

**The remedy vocabulary is closed, and one of its members is conditional**
(D40):

| Remedy | Meaning | Surfaces as |
|---|---|---|
| `mendel` | Mendel does it, unprompted | `waiting`; no ask |
| `user` | A person must act | `yourmove` + an input request |
| `either` | Mendel if permitted, otherwise a script for someone who is | `offered` or `yourmove`, decided at runtime |
| `elsewhere` | Something external is working; nobody can hurry it | `waiting` |
| `unavailable` | Not achievable on this platform or datastore; the remedy is to revisit an earlier choice | `blocked`, naming the choice |

`either` is the member that matters, and §6 explains why it is the one most
likely to break an abstraction designed without it.

`unavailable` deserves its own note. It covers `ErrUnsupportedDatastore`, it
covers Cloud Run being unable to route by Assignment Unit at all (§13 §6.3), and
it covers `DeployURLLimitation` — a deployment reached at a bare IP over http
cannot register an OAuth redirect URI anywhere, no matter what the user does.
The existing `DeployURLLimitation` comment is the model: it names the
limitation, explains why the obvious escape does not work, and points at the
choice that would lift it (set a domain you control). A checklist row that says
"impossible" without that third part is a dead end.

### 3.5 The fifth question, from the audit: absence that narrows rather than denies

Three conditions in the tree today do not block anything when absent. They make
the thing that happens worse:

- **No Anthropic API key.** `ProjectReadiness.IsReady()` deliberately excludes
  `HasAPIKey`, and `MissingSettings()` does not list it.
- **No rate card for the model.** `runBudget.Apply` falls back from a spend
  ceiling to a 50-round cap, and says so in the log.
- **No privileged datastore credential** (§13 §15). Arms run as the application,
  enforcement is unavailable, and the design is explicit that this is "not a
  silent downgrade."

The first draft proposed a `degrades` severity on the cell for these, and left
the alternative — that they are separate functional areas — as an open question.
**Drawing the matrix answered it, and there is no severity field** (D38).

§4.2 shows it: *Enforce Arm containment* shares almost no conditions with *Run a
live experiment with schema changes*. It needs the datastore adapter and the
privileged credential and nothing else, while the experiment row needs the
migration column set and the whole routing column set and not the privileged
credential. Two rows that overlap in one cell out of twenty are not one row with
an asterisk on it. The same reading applies to the other two: "cost-bounded
generation" and "round-bounded generation" are different things Mendel does, and
the rate card is required for one of them.

This is the clearest case in the document of the table paying for itself. The
question was unanswerable in prose and obvious once the cells were filled in.

The remaining decision here is the smaller one: **a condition Mendel cannot
currently evaluate is never reported as satisfied**. `unknown` (there is an
evaluator and it has not run) and `unimplemented` (there is no evaluator) are
both distinct from satisfied and from unsatisfied. This is the `Known` flag from
`DomainObservation`, promoted and given a second sibling, and the comment there
already argues it better than this paragraph does: the zero value of an
observation is indistinguishable from "looked, and found nothing", which would
tell a user to create records they created an hour ago.

The consequence for live experiments today is that the page says, honestly, that
several of its conditions are designed and not built, and that Mendel will not
claim the functional area works. That is a better answer than any of the five
the product currently gives.

---

## 4. The matrix

### 4.1 The rows

Seven functional areas, all of which exist as things a user wants and asks
about. The last three are not available today.

| # | Functional Area | Short name below |
|---|---|---|
| 1 | Write code for a Variation | Code |
| 2 | Run a demo of a Variation | Demo |
| 3 | Deploy to production | Prod |
| 4 | Serve deployments by name over https | Named |
| 5 | Run a live experiment, presentation only | Exp1 |
| 6 | Run a live experiment with schema changes | Exp2 |
| 7 | Enforce Arm containment | Enforce |

### 4.2 The shared conditions

**A condition used by exactly one functional area does not earn a place in the
table** — it is a list under that row, and §5 has those. What a table is for is
the sharing, so this is the sharing. Transposed so that seven columns fit:
conditions down the side, functional areas across.

| Functional Area Condition | Evidence | Remedy | Code | Demo | Prod | Named | Exp1 | Exp2 | Enforce |
|---|---|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| Repository URL is set | asked | user | ● | ● | ● | | ● | ● | |
| A push token is stored | asked | user | ● | ● | ● | | ● | ● | |
| The encryption key is configured | observed | user | | ● | ● | | ● | ● | ● |
| A deployment channel is configured | asked | user | | ● | ● | ● | ● | ● | |
| The channel's combination is supported | derived | unavailable | | ● | ● | | ● | ● | |
| The channel's credentials are stored | asked | user | | ● | ● | ● | ● | ● | |
| The demo path is validated | probed | mendel | | ● | | | | | |
| The production path is validated | probed | mendel | | | ● | | ● | ● | |
| Every `secret` requirement has a value | declared | user | | ● | ● | | ● | ● | |
| Every `acknowledgement` is confirmed | declared | user | | ● | ● | | ● | ● | |
| The deployment's URL is registrable | derived | unavailable | | ● | ● | | | | |
| A base domain is set | asked | user | | | | ● | ○ | ○ | |
| The certificate is issued | observed | elsewhere | | | | ● | ○ | ○ | |
| The platform can route by Assignment Unit | declared | unavailable | | | | | ● | ● | |
| A Gateway API controller is installed | probed | **either** | | | | | ● | ● | |
| A `GatewayClass` is `Accepted` | probed | mendel | | | | | ● | ● | |
| The Assignment Unit and key are declared | declared | user | | | | | ● | ● | |
| The key is edge-extractable | declared | user | | | | | ● | ● | |
| The Variation changes one deployable unit | probed | user | | | | | ● | ● | |
| An effect size, duration and stopping rule are set | asked | user | | | | | ● | ● | |
| The withdrawal dissonance is acknowledged | asked | user | | | | | ● | ● | |
| An adapter exists for the datastore | probed | unavailable | | | | | | ● | ● |
| A privileged datastore credential is available | asked | user | | | | | | | ● |

● required ○ required only under some assignment mechanisms, see O21

Three things this table says that the prose could not.

**The five columns down the left third are the reason to build this at all.**
Repository, encryption key, channel, credentials, secrets and acknowledgements
are required by five or six rows each and are today checked in five or six
unrelated places, in different words, at different moments. Those are the
duplicate implementations the matrix removes.

**Exp1 and Exp2 differ by one block, and Enforce is not part of either.** The
first two share every routing condition and diverge only where migrations
appear — which is §13's own conclusion (Tier 1 "falls out of Tier 2 rather than
preceding it") arrived at independently. Enforce shares one cell with them and
is otherwise disjoint, which is §3.5's answer.

**The `○` cells are a finding, not a formatting choice.** Nothing in §13 or §16
says a live experiment requires a domain the user controls, and drawing the
table made the question unavoidable. Pulling on it turned out to expose
something about §16 rather than about this document: the requirement is not
cookies, it is whether the edge has to *validate* what it routes on, and there
are assignment mechanisms where it does not. O21 has the argument; the
amendment belongs in §16 D23.

### 4.3 A functional area may be a condition of another

*The production path is validated* is a row people ask about directly and a
condition three other rows depend on — §16 §1 makes experiments available only
where Mendel deploys production. **A functional area may therefore appear as a
condition of another functional area** (D41), rather than the same fact being
defined twice.

Cycles are the obvious hazard; the same graph check that computes `blocked`
catches them, and it belongs in a test over the catalogue rather than at runtime.

### 4.4 Where any of this is stored

**The catalogue is Go; the observations are Postgres; nothing already stored is
stored again** (D42).

This needs reconciling with the standing rule in `CLAUDE.md` against hardcoding
enumerated options, because it looks like a violation and is not. That rule
exists for data that changes without a release — hosting platforms, model rate
cards, the options a user chooses between. A functional area condition is not
data of that kind: adding one means writing an evaluator, which is a code change
by definition, and a condition with no evaluator is `unimplemented` rather than a
row someone can seed. Putting the catalogue in a table would create the
possibility of a database row describing a condition no code can evaluate, which
is the one state D38 exists to make impossible.

The observations are a different matter, and only some of them:

```sql
condition_observations (
  condition_id, scope, subject_id,
  state, detail, missing,
  observed_at, expires_at
)
```

This holds results for `probed` and `observed` conditions only — DNS lookups,
certificate state, cluster access probes, hello-world validations — where the
answer costs a network call and has a useful lifetime.
`internal/web/domain_observe_cache.go` is already this table for one functional
area and should become this table for all of them.

`asked`, `declared` and `derived` conditions are **not** cached and get no rows.
Their answers are already in `project_env_vars`, `requirement_acknowledgements`,
`variation_requirements`, `project_deployment_channels`, `experiments` and the
repository itself. A second copy would be a second truth, and the failure mode
is a checklist confidently telling a user to enter a secret they entered
yesterday. The matrix is a **view over the state the product already keeps**,
and this is the property that keeps it from becoming its own thing to maintain.

---

## 5. The single-row conditions

The table above is the shared conditions. These are the rest, listed under the
one row that needs each. Audited from the tree at `d24ef90`; "Today" is how the
condition is enforced now, which is the inconsistency this replaces.

### Code

| Condition | Evidence | Remedy | Today |
|---|---|---|---|
| An Anthropic API key is present | asked | user | Excluded from `IsReady()` on purpose |
| A rate card exists for the model | observed | user | Falls back to a 50-round cap (own row, §3.5) |
| A strategy exists | derived | user | onboarding ribbon `switch` |
| Objectives are approved | derived | user | onboarding ribbon `switch` |
| A roadmap is approved | derived | user | onboarding ribbon `switch` |

### Named

Every row here exists today, correctly, in `DomainReadiness`, and is listed to
show what a well-formed functional area looks like when the whole ladder is
present.

| Condition | Evidence | Remedy | Today |
|---|---|---|---|
| A static IP is reserved | derived | mendel | ladder step 2 |
| The wildcard A record resolves to it | observed | user | ladder step 3 |
| The challenge records resolve | observed | user | ladder step 4, **fan-out by count** |

### Exp1 and Exp2

| Condition | Evidence | Remedy | Row | Source |
|---|---|---|---|---|
| The allocation totals 100 with one mainline | derived | user | both | `ValidateAllocation` |
| No durable writes when the unit is `request` | derived | user | Exp2 | §13 §5.1 — a derivation, not a rule beside it |
| The migration has both an up and a down | declared | user | Exp2 | "an Arm that cannot be withdrawn cannot be run" |
| The migration's objects are namespaced | declared | user | Exp2 | `mendel_exp_` |
| Mendel can learn what the migration does without risking production data | probed | user | Exp2 | §3.1 — the one condition with two routes |
| The migration passes the deny-list | probed | user | Exp2 | before anything runs |
| The migration is purely additive | probed | user | Exp2 | the affirmative judgment |
| The migration adds something | probed | user | Exp2 | |
| The touched collections exist | probed | user | Exp2 | |
| The touched collections have an identity | probed | unavailable | Exp2 | else the archive cannot be restored |
| The migration will run against production, not the throwaway copy | probed | user | Exp2 | §3.1 — the former negation |
| The verification datastore matches production | probed | user | Exp2 | else the proof is about the wrong schema |
| No schema drift since admission | probed | elsewhere | Exp2 | re-checked at apply |
| The projected archive size is under the ceiling | derived | unavailable | Exp2 | §13 §9 |

### Everywhere

One condition applies to every row and is left out of the table because a column
of solid dots carries no information: **the project exists and the reader may
see it.** Worth stating once so that nobody adds it as a cell.

---

## 6. What is most likely to break this if it is built too early

The design above generalises from **one and a bit examples**: one complete ladder
(`DomainReadiness`) and one complete checker (`RequireForExperiments`) whose
scope is a single interface. Everything else in §4 and §5 is being *fitted* to a
shape derived from those two, not evidence that the shape is right.

Three of the cluster-side conditions from §16 §8 item 3 stress it in ways
nothing currently in the tree does. Two of them are already handled by decisions
above, deliberately; the third is not, and is honestly open.

**"Install the Gateway API controller" has a runtime-decided actor.** Every
other condition in §4 and §5 knows in advance whose move it is: a DNS record is
always the user's, a static IP is always Mendel's. This one is decided by a
`SelfSubjectAccessReview` at evaluation time — Mendel may be permitted to
install it, in which case it offers to, or may not, in which case it emits a
script for an administrator. An abstraction with a static actor field cannot
express it, and the natural workaround — two conditions, one per case — is
wrong, because they are not two things a user can be shown; only one of them is
ever real. This is why the remedy vocabulary includes `either` (D40) and why
`offered` is a state: D22a says Mendel must say what it is about to add
cluster-wide before adding it, so "Mendel can do this and is asking first" is a
real rung on the ladder and not a transient.

**Its scope is the cluster, which is above the project.** Several projects on
one channel share a controller, so one project's administrator satisfying it
satisfies it for the others. The scope vocabulary therefore needs a level above
`project`, and the observation table needs to key on something that is not a
project id. That much D36 already accommodates. What it does not settle is
whether one project's checklist may report a condition as satisfied on evidence
gathered under another project — a visibility question as much as a data one
(O15).

**Its probe is not where its remedy happens.** An administrator runs the script
in a terminal Mendel cannot see; Mendel learns the outcome by checking that a
`GatewayClass` reached `Accepted`. Nothing else in §5 separates those. The
domain ladder comes closest — the user types records into a provider Mendel
cannot see into — and it is exactly the case the existing comment calls out as
the reason every step there is observed rather than asserted. So the shape
survives, but it survives because one of the two prototypes happened to have
already met this problem. That is thin evidence, and worth saying so.

**And one thing that would have broken it has already been avoided.** The first
draft built a general disjunction mechanism into the matrix to carry a single
example. §3.1 removed it. The lesson is worth keeping in view for the next
condition that does not fit: the first question is whether the condition is
stated at the wrong level, not whether the matrix needs a new operator.

---

## 7. Open questions

**O14 — Is the dependency graph really global?** §3.2 asserts it and gives a
reason, but the evidence is one ladder. The test is whether any functional area
ever wants two conditions in the opposite order from another. If one does, the
answer is probably that the condition is two conditions with one name, but that
should be established on a real case rather than assumed here.

**O15 — Who may see a cluster-scoped observation?** A condition satisfied by one
project's administrator is satisfied for every project on that channel. Reading
it back into another project's checklist is correct and useful, and it also
leaks the existence and state of shared infrastructure across a project boundary
that is otherwise respected everywhere in the codebase. This needs a position
before the first cluster-scoped condition ships.

**O16 — What is the fan-out ceiling?** D37 aggregates and counts, which is right
for three DNS records. A project with twenty demos and a per-deployment
acknowledgement produces "3 of 20 confirmed", and the remaining seventeen have
no obvious presentation. The domain ladder's precedent does not reach this far.

**O17 — resolved.** Whether "absence that narrows" is a cell severity or a
separate functional area. Drawing §4.2 settled it: separate rows, no severity
field. Recorded here because the question was open in the previous draft and
someone reading the two side by side should see why it closed.

**O18 — How stale may an observation be before it is `unknown` again?** The
existing domain cache has an answer for DNS. A cluster access probe, a channel
validation from three months ago, and a certificate state have different useful
lifetimes, and a single TTL will be wrong for most of them.

**O19 — Does the catalogue need to be enumerable from the database?** D42 keeps
it in Go, so the developer-facing grid renders from a Go value — fine — but an
operator cannot ask in SQL which projects are blocked on the certificate. That
may be worth an exported read-model later; it is not worth inverting D42 for.

**O20 — Does re-expressing `OnboardingLifecycle` belong here at all?** Its four
derived conditions are in §5, and it is the one existing mechanism whose prose
does not decompose cleanly: its `switch` distinguishes "drafting" from "the
draft failed" from "no objectives yet" with three different sentences for what is
arguably one condition. Forcing it into the catalogue may lose something the
ribbon does well. Listed, not committed.

**O21 — Does a live experiment require a domain the user controls? — restated.**
The `○` cells in §4.2. The first draft of this question assumed the answer
followed from cookies: assignment works by a cookie the assigner sets (§16 D23),
a cookie is scoped to a host, and a bare LoadBalancer IP over http is a poor
host to scope anything to.

That framing was too narrow, and the review's question — *do we need a cookie at
all, or could an existing L7 field be mapped declaratively to Arms?* — is what
exposes it. Three mechanisms are available, and they do not all imply a domain:

- **Map an existing field directly.** Route on a value already in the request:
  a tenant subdomain, an authenticated user id header, a JWT claim. No cookie,
  no assigner, no redirect. Bucketing is done by matching the value's shape —
  a hex prefix, say — rather than by hashing it.

- **Have the application emit a bucket.** Codegen adds a middleware that hashes
  the Assignment Unit key with a salt Mendel injects as an environment variable
  and sets a header carrying a number from 0 to 99. The edge matches that value
  exactly. This is §13 §10.1's argument applied to routing rather than to
  metrics: the cost is a few lines of the app's own code, which Mendel writes
  anyway, and what lands in the repository is a middleware and an env var with
  no Mendel import in sight.

- **Mint identity at the edge**, which is §16 D23 as it stands.

Only the third *requires* a cookie, and it requires one because it is the only
mechanism that works for a visitor Mendel knows nothing about. That is the
launch surface: §13 §3 puts presentation experiments on possibly-logged-out
traffic first, and a landing page has no user id header to map. **So the third
mechanism cannot be retired**, and the first two cannot serve Tier 1 alone.

But the first two are strictly better wherever they apply, and where they apply
is exactly Tier 2: a Variation writing per-Assignment-Unit durable state is
writing rows for someone the application has already identified, so the key is
in flight by construction. They delete the assigner Deployment, the Service, the
allocation ConfigMap, the extra round trip, the `?_ma=1` loop-breaker and the
"a first-ever POST cannot be redirected" caveat — all of §16 §3.4. They also
make §16 D28 nearly free, since the value propagated to each internal hop is the
same value the edge matched on.

**Which restates the condition properly.** The requirement was never cookies. It
is: **does the edge have to validate what it routes on?** §16 D29 says the edge
must overwrite the identity header from the validated session, because a caller
that sets its own identity header picks its own Arm. Validating a session at the
edge means reading a credential in flight, which means TLS, which means a
certificate, which means a domain — no certificate authority will issue for a
bare address.

It only escapes that where the routed value is a *bucket* rather than an
identity. §16 D31 already draws this line for client-supplied values: an opaque
token is fine because assignment is not an authorisation decision. A bucket
number is the same kind of thing — spoofable, and harmless to spoof beyond
self-selection into a cohort.

So the answer is conditional, and the condition is worth writing into the matrix
in place of the `○` cells:

| Mechanism | Needs a domain | Works for anonymous traffic |
|---|---|---|
| Application-emitted bucket header | no | only if the app mints an anonymous id, which is a cookie again |
| Mapped identity field, validated at the edge | **yes** | no |
| Mapped opaque token, unvalidated | no | no |
| Mendel-set cookie at the edge (§16 D23) | yes, in practice | **yes** |

Two things still argue for the cookie beyond anonymous traffic, and they are the
reason this is an open question rather than a decision.

**A mapped field carries no salt.** Assignment is supposed to be a function of
the key, the allocation *and* a salt, so that successive experiments do not draw
the same cohort every time. Prefix-matching a user id has no salt to turn, and
the same tenth of the population is the guinea pig forever. Partially fixable by
giving each experiment a disjoint slice of the value space rather than the same
one — two hex characters give 256 slices to hand out — which is enough for a
long time and is not the same as independence. The application-emitted bucket
does not have this problem at all, since Mendel injects the salt.

**A mapped field may not be uniform.** A UUIDv4 prefix is; a sequential integer
id is emphatically not, and neither is a tenant slug or an email. So direct
mapping needs Mendel to establish that the field is uniformly distributed before
trusting a prefix match to allocate anything — which is a new admission check
with no equivalent today. The application-emitted bucket avoids this too, since
a hash is uniform whatever it is fed.

Both objections point the same way: **the application-emitted bucket is the
strong version of the idea**, and direct field mapping is the convenient version
that needs two guards direct mapping cannot easily provide.

There is one conformance cost to weigh. Gateway API's exact header match is Core
support; regular-expression matching is implementation-specific. Direct
prefix-matching therefore needs a regex and steps down a conformance level,
which is the axis §16 §6.2 used to reject the Lua filter, so it should not be
adopted without noticing. Matching an enumerated bucket value is exact, and
stays Core — a hundred match rules is unlovely and Mendel generates them.

**What this does not change.** It is a §16 decision, not this document's, and it
is being implemented in parallel. Recorded here because it is the condition the
matrix asked about; the amendment belongs in §16 D23 and is not made here.

---

## 8. Decisions

Continuing the numbering from §13 (D1–D20) and §16 (D21–D31).

| # | Decision | Rejected alternative | Why |
|---|---|---|---|
| D32 | Non-core vocabulary is spelled out in full: **Functional Area**, **Functional Area Condition** | "Capability" and "Condition" | "Capability" already means two other things in this tree; a short name earns itself by constant use, which invented vocabulary has not had |
| D33 | A functional area names a set of conditions and all of them must be true; a cell is required or not applicable | A conjunction of disjunctive clauses; an arbitrary boolean tree | The only disjunction in forty conditions was a condition stated at the wrong level (D34); an operator built for one example is an abstraction sized for one example |
| D34 | A condition may have more than one route to satisfaction, internal to its evaluator | Two columns joined by `or`; a `Clause` type between rows and conditions | "Mendel can learn what a migration does without risking production data" is one thing a person wants; a clause was never distinguishable from a condition |
| D35 | Conditions form one global DAG; `blocked` is computed, not authored | Per-row ordering; declaration order | Two rows could disagree about reality, silently; authored order is right when written and wrong three commits later |
| D36 | Every condition carries a Declaration Scope and a Satisfaction Scope | One scope | `requirements.json` is declared per-Variation and satisfied per-project or per-deployment; one field asks project questions about a Variation |
| D37 | A coarser question aggregates fan-out into one counted row | A row per instance | Already the rule for certificate challenge records: a ladder that grows a rung per zone says the task changed shape when it did not |
| D38 | `unknown` and `unimplemented` are states, never satisfied; there is no `degrades` severity | Treat absent evidence as passing; mark narrowing conditions as degrading | The `Known` flag exists for exactly this reason; and §4.2 shows narrowing conditions belong to rows of their own |
| D39 | One sentence per condition, rendered by both the decline and the checklist | Separate error strings per call site | If the two can differ they will, and the page stops being the answer to "why can't I" |
| D40 | Closed remedy vocabulary including `either` | A static actor field | "Install the controller" is Mendel's move or an administrator's depending on a runtime probe (§16 D22a) |
| D41 | A functional area may itself be a condition of another | Define the same fact once as a row and again as a column | "Production is validated" is a row users ask about and a condition three rows depend on |
| D42 | Catalogue in Go, observations in Postgres, nothing already stored duplicated | A seeded table, per the platform rule | That rule is for data that changes without a release; a condition's evaluator is code, and a duplicated answer is a second truth |
| D43 | Users get a per-functional-area ladder; the literal grid is a developer view | Show users the matrix | `pivotSupportMatrix` earns a grid with two short axes and a reader choosing a cell; forty conditions against seven rows is a spreadsheet |
| D44 | Re-express the two existing prototypes first, with identical output required | Build for live experiments first | The abstraction must be proven against conditions that exist before carrying ones that do not |

---

## 9. Build order

Steps 1 through 5 touch no code under `internal/experiment`, `internal/codegen`
or the experiment handlers, so they can proceed alongside live-experiment
implementation without collision. Step 6 is the merge point.

1. **The catalogue and the evaluator, as pure functions with no callers.** Graph
   evaluation, gating, fan-out aggregation, the state lattice. Testable entirely
   on a fixture catalogue. Port `RequireForExperiments` as the first two
   conditions — §3.1's pair — since restating it is the first real exercise of
   D34, and if it needs the matrix to grow an operator to fit, the design is
   wrong.

2. **Re-express `DomainReadiness` as a functional area, and require identical
   output.** The existing `domain_readiness_test.go` is the acceptance
   criterion: same steps, same states, same details, same headline. This is the
   de-risking step, because it exercises ordering, gating, observation,
   fan-out-by-count and the "not looked yet" state at once, against a case
   already known to be right. If the abstraction cannot reproduce the ladder
   exactly, stop and revise it before anything else is ported.

3. **Re-express `EvaluateRequirements`.** Proves the two-scope model (D36),
   `deferred`, and `unavailable` via `DeployURLLimitation`. Its existing tests
   are again the criterion.

4. **Replace the inline handler checks.** `handleStartDemo`,
   `runChannelDemoDeployment`, `runChannelProdDeployment` and the settings
   handler evaluate the functional area and render its missing sentences instead
   of writing their own. Nothing user-visible should change except that the
   sentences get better; the point is that after this there is one source for
   them.

5. **The page, and input-request filing.** `/p/{id}/functional-areas` listing
   each row with a one-line verdict, and `/p/{id}/functional-areas/{slug}`
   rendering the ladder. `yourmove` steps with remedy `user` file an input
   request through the generalised `syncDomainRequest`, so they appear under
   Input Needed like everything else. The developer grid (D43) goes behind the
   existing debug route.

6. **The experiment rows**, including everything designed and not built as
   `unimplemented`. This is the first time the catalogue carries a functional
   area that is not available, and the first time `either` and `offered` have a
   real user. Settle O21 before this rather than after: it changes what a user is
   told when they first ask about experiments, which is a conversation §13 §4.2
   says must happen early and unprompted.

Steps 1 and 2 are the ones that decide whether any of the rest is worth
building, for the same reason §13 §16 put migration non-interference first: they
are the part that can fail, and failing there is cheap.
