# The Capability Matrix — Design

Status: **draft for review.** No code written. Nothing under `internal/` is
touched by this document.

Companion to [13_live_traffic_experiments.md](13_live_traffic_experiments.md)
and [16_experiment_routing.md](16_experiment_routing.md), which between them
invented eleven new preconditions without noticing that they were the same kind
of thing as the fifteen already in the tree. This document is about that kind of
thing.

The shape, in one sentence: **rows are things Mendel can do for a project,
columns are conditions about the repository or the wider environment, and a row
is available when its conditions hold.** A user who wants live experiments gets
a checklist of what is not yet true, with the ones that are their move
distinguished from the ones that are Mendel's and the ones that are nobody's.

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
for every capability added after them. Each one grew its own gate at the moment
it needed one, in whatever idiom the surrounding file used.

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
that refuses to answer from evidence Mendel has not gone and got. It is a
capability matrix for one capability, and everything in it is right.

**`internal/experiment/datastore.go` is the checking half.** `Capabilities` plus
`RequireForExperiments` is the only place in the tree where adding an invariant
is one struct field and one sentence naming what is missing. The sentences are
the model for every sentence this design asks for: *"postgres cannot apply a
change and undo it without a trace, so it can only be verified against if it is
a non-production datastore Mendel may reset."* That names the finding, names the
consequence, and names the remedy, in one sentence, without the reader opening
anything else.

Neither is being replaced. The work is to widen the first to more than one
capability and the second to more than one subsystem, and to make them the same
mechanism.

There is a third precedent worth naming because it shows where a literal grid
*is* right: `pivotSupportMatrix` in `internal/web` renders platform × artifact
kind as an actual table, including rows for platforms that support nothing,
because "we know about this and cannot deploy to it" is different from silence.
Both of its axes are short and the reader is choosing between cells. Neither is
true here; see D43.

---

## 2. Vocabulary, and a word that is already taken

- **Capability** — something Mendel can do for a project: run a demo, deploy to
  production, serve demos by name over https, run a live experiment. A row.
- **Condition** — something that must be true for a capability to be available.
  A column.
- **Subject** — what a condition is being asked about: this project, this Hop,
  this Variation, this deployment, this channel.
- **Finding** — what evaluating one condition against one subject produced:
  a state, a detail, and — when unsatisfied — the sentence naming what is
  missing.
- **Clause** — one requirement of a capability, satisfied when any one of its
  literals holds. See §4.1.

Following §16's note on overloaded words: "capability" is already used twice in
this codebase for two other things. `experiment.Capabilities` is a datastore
adapter's self-report about what it can do, which is a *source of conditions*
rather than a row in this matrix, and keeps its name — it lives one level down
and the qualified name reads correctly. `internal/web/capabilities_test.go` uses
"capability" for what a signed-in reader can reach on a page, which is an
unrelated meaning inside the package that will hold this code; that file should
be renamed (`reachability_test.go`) rather than left to collide.

"Requirement" is not available: `VariationRequirement` is a specific existing
thing that will become one *source* of conditions, and reusing the word for the
general case would make every sentence about either one ambiguous.

---

## 3. The four questions, settled

The brief named four things that must be settled before the shape is fixed. They
are settled here, in the order they constrain each other. A fifth turned up in
the audit and is settled with them.

### 3.1 Not every cell is "required true"

`RequireForExperiments` needs `StructuralDiff` **and** (`SpeculativeApply`
**or** `Disposable`) **and not** `live.Disposable`. A matrix that can only
express conjunction has two bad options: flatten the disjunction into a single
column that is secretly an `or` — and then the checklist tells someone to make
both true when either would do — or drop one disjunct and narrow the product.

**The answer is a conjunction of disjunctive clauses, one level deep, and no
nesting** (D32). A capability holds when every clause holds; a clause holds when
any of its literals holds. That is exactly enough for the case in hand, and it
is exactly enough to *render*: a clause is one row of the checklist, and a
clause with two literals reads "either X or Y."

An arbitrary boolean tree was rejected. It buys expressiveness nobody has asked
for, and it costs the property that makes the checklist work: with a flat CNF,
every clause is independently displayable and independently falsifiable in a
test. Nested groups have to be rendered as nested groups, and a checklist with
indentation levels is a decision tree, which is a worse thing to hand someone
than a list.

Negation is handled by a literal carrying an expected value rather than by
inventing a positively-phrased mirror condition (D33). "The live datastore is
not disposable" is a real condition with a real sentence attached to it, and
`live.Disposable == false` says so directly. The mirror version — a condition
called `live.IsProduction` — reads better in the abstract and is worse in
practice: it is a second name for the same underlying fact, and two names for
one fact is how they drift apart.

A disjunction also has to say **which disjunct Mendel recommends** (D34). "Make
either of these true" with no further guidance is a worse instruction than
either half of it. In the datastore case the preference is clear and already
written down in the existing comment: a non-production datastore Mendel may
reset is the thing a project is actually asked for; transactional DDL is a
property of the engine that the user cannot go and acquire.

### 3.2 Conditions have dependencies

`gate()` exists because a ladder that shows three things to do at once, two of
which are impossible, is worse than one that shows the next one. A flat
capability × condition matrix loses that, and the loss is not cosmetic: the
certificate step in the domain ladder is *never* the user's move, and a matrix
that showed it as unsatisfied alongside the DNS record it depends on would send
someone to a certificate authority to complain about a record they typed wrongly.

**Conditions form one global directed acyclic graph, and `blocked` is computed
rather than authored** (D35). A condition declares what it depends on; a
condition whose dependencies are unsatisfied is `blocked` regardless of its own
evaluator, and its evaluator is not run — which also saves the DNS lookups and
cluster probes that would have no meaning yet.

Global rather than per-capability, because the dependency is a fact about the
world and not about who is asking. "The certificate is issued" depends on "the
challenge records resolve" whether the asker is named demos, production https,
or something not yet built. A per-capability ordering would let two capabilities
disagree about the shape of reality, and the disagreement would be silent. If a
case ever genuinely needs feature-specific ordering, that is evidence the
condition is two conditions (O14).

Authoring the order by hand — a sort key, or declaration order, which is what
the domain ladder does today — works for one linear ladder and stops working the
moment two capabilities share conditions in different arrangements. It is also
the kind of thing that is correct when written and wrong three commits later,
with nothing to notice.

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

**So a condition carries two scopes: where it is declared, and where it is
satisfied** (D36). Evaluation takes a subject and projects it onto the
satisfaction scope — a Variation subject projects up to its project for a
secret, and down to each of its deployments for an acknowledgement.

This is the single most load-bearing decision in the document, because a
checklist without it asks project-level questions about a Variation and
per-deployment questions about a project, and both readings are wrong in a way
that is hard to see in review and obvious in use.

Projecting downward fans out: one acknowledgement condition against a project
with a demo and a production deployment is two answers, and against a project
with twenty demos is twenty. **A coarser subject aggregates the fan-out into one
row that counts** (D37), which is not an invention — it is what the domain
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

A matrix with only `required` cells cannot express any of these, and the
temptation is to add a `degrades` severity with a sentence saying what is lost.
That is probably wrong, and §7 records why it is an open question rather than a
decision: the honest reading may be that these are not degraded capabilities but
*different capabilities*, and that "Tier 2 experiments with enforcement" and
"Tier 2 experiments without" are two rows rather than one row with an asterisk.

The decision this document does take is the smaller one: **a condition Mendel
cannot currently evaluate is never reported as satisfied** (D38). `unknown`
(there is an evaluator and it has not run) and `unimplemented` (there is no
evaluator) are both distinct from `satisfied` and from `unsatisfied`. This is
the `Known` flag from `DomainObservation`, promoted and given a second sibling,
and the comment there already argues it better than this paragraph does: the
zero value of an observation is indistinguishable from "looked, and found
nothing", which would tell a user to create records they created an hour ago.

The consequence for live experiments today is that the page says, honestly, that
four of its conditions are designed and not built, and that Mendel will not
claim the capability works. That is a better answer than any of the five the
product currently gives.

---

## 4. The model

### 4.1 Nodes, and why there is only one kind

```go
type NodeID string

// A Node is a condition, a capability, or both. See D42.
type Node struct {
    ID    NodeID
    Name  string // Reader-facing, imperative where it is someone's move:
                 // "Create the wildcard A record", not "wildcard_a_record".

    Source Source // probed | observed | declared | asked | derived | composite
    Remedy Remedy // mendel | user | either | elsewhere | unavailable

    DeclaredAt  Scope // Where the question comes from.
    SatisfiedAt Scope // Where the answer lives. Frequently different (D36).

    DependsOn []NodeID // Global DAG (D35).

    // Exactly one of these. A leaf node goes and finds out; a composite node
    // is true when its clauses are.
    Evaluate func(context.Context, Subject) Finding
    Clauses  []Clause
}

type Clause struct {
    AnyOf  []Literal // Holds when any literal holds (D32).
    Prefer NodeID    // Which disjunct to recommend (D34). Empty when AnyOf has one.
}

type Literal struct {
    Node   NodeID
    Expect bool // false where the condition must be untrue (D33).
}
```

**Capabilities and conditions are one type** (D42). The brief describes rows and
columns as different kinds of thing, and the audit says otherwise: "the
deployment channel is validated for production" is a capability the user wants
and asks about directly, *and* a condition live experiments depend on (§16 §1
makes experiments available only where Mendel deploys production). Modelling it
twice means maintaining two definitions of one fact.

A row of the matrix is simply a node with clauses. Cycles are the obvious
hazard; the DAG check that computes `blocked` catches them, and it should be a
test over the catalogue rather than a runtime error.

This is a departure from the brief's framing and is flagged for review as such.
The argument for it is the evidence above rather than tidiness — an abstraction
that unifies two things because they *feel* similar is the failure mode this
whole document is trying to avoid.

### 4.2 Findings

```go
type State string

const (
    Satisfied     State = "satisfied"
    Unsatisfied   State = "unsatisfied"
    Blocked       State = "blocked"       // A dependency is unmet; not startable.
    Deferred      State = "deferred"      // Answerable, but not yet: the deploy URL
                                          // does not exist until the deploy does.
    Offered       State = "offered"       // Mendel may do this and is asking first.
    Unknown       State = "unknown"       // Evaluator exists; has not run.
    Unimplemented State = "unimplemented" // No evaluator (D38).
)

type Finding struct {
    State  State
    Detail string // What was found: "resolves to 34.56.24.112".
    Missing string // The sentence, when unsatisfied (D39). Empty otherwise.

    Outstanding, Total int // Fan-out counts (D37); both zero when singular.
}
```

`Deferred` is not new either — `RequirementStatus.Deferred` already means
exactly this, and `Blocking()` already knows that a deferred requirement is
neither met nor blocking. `Offered` is new, and exists solely for `either`
remedies; §6 argues it into existence.

### 4.3 Assessment, and the one function everything calls

```go
func Assess(ctx context.Context, cat *Catalogue, id NodeID, subj Subject) Assessment

type Assessment struct {
    Available bool
    Headline  string        // "Certificate issued" / "Create the wildcard A record"
    WaitingOn domain.Actor  // Reuses the ribbon's actor vocabulary.
    Steps     []Step        // Ordered by the DAG, gated (D35).
    Missing   []string      // The Missing sentences, in dependency order.
}
```

`Assess` is what handlers call instead of their inline checks, and what the page
renders. `handleStartDemo`'s decline becomes the first entry of `Missing`;
`runChannelProdDeployment`'s becomes the same; the capabilities page renders
`Steps`. That is D39 made mechanical rather than aspirational — there is no
second string to keep in sync because there is no second string.

`Steps` maps onto `DomainStep` closely enough that the existing template and the
existing `domain-ladder.js` should render it with a widened struct rather than a
new one.

### 4.4 Where any of this is stored

**The catalogue is Go; the observations are Postgres; nothing already stored is
stored again** (D41).

This needs reconciling with the standing rule in `CLAUDE.md` against hardcoding
enumerated options, because it looks like a violation and is not. That rule
exists for data that changes without a release — hosting platforms, model rate
cards, the options a user chooses between. A condition is not data of that kind:
adding one means writing an evaluator, which is a code change by definition, and
a condition with no evaluator is `unimplemented` rather than a row someone can
seed. Putting the catalogue in a table would create the possibility of a
database row describing a condition no code can evaluate, which is the one state
D38 exists to make impossible.

The observations are a different matter, and only some of them:

```sql
condition_observations (
  node_id, scope, subject_id,
  state, detail, missing,
  observed_at, expires_at
)
```

This holds results for `probed` and `observed` nodes only — DNS lookups,
certificate state, cluster access probes, hello-world validations — where the
answer costs a network call and has a useful lifetime. `internal/web/domain_observe_cache.go`
is already this table for one capability and should become this table for all of
them.

`asked`, `declared` and `derived` nodes are **not** cached and get no rows. Their
answers are already in `project_env_vars`, `requirement_acknowledgements`,
`variation_requirements`, `project_deployment_channels`, `experiments` and the
repository itself. A second copy would be a second truth, and the failure mode
is a checklist confidently telling a user to enter a secret they entered
yesterday. The matrix is a **view over the state the product already keeps**,
and this is the property that keeps it from becoming its own thing to maintain.

---

## 5. First cut of the actual matrix

Audited from the tree at `d24ef90`. `Source` and `Remedy` are as they would be
under this design; "Today" is how the condition is enforced now, which is the
inconsistency this replaces.

### 5.1 Project and repository

| Condition | Source | Declared / Satisfied | Remedy | Today |
|---|---|---|---|---|
| Repository URL set | asked | project / project | user | `ProjectReadiness.MissingSettings` |
| Push token stored | asked | project / project | user | same |
| Anthropic key present | asked | project / project | user | **narrows only** (§3.5) |
| Encryption key configured | observed | install / install | user | `crypto.GetKey` fails mid-deploy |
| Strategy exists | derived | project / project | user | onboarding ribbon `switch` |
| Objectives approved | derived | project / project | user | onboarding ribbon `switch` |
| Roadmap approved | derived | project / project | user | onboarding ribbon `switch` |

### 5.2 Deployment channel

| Condition | Source | Declared / Satisfied | Remedy | Today |
|---|---|---|---|---|
| A channel is configured | asked | project / project | user | `failDemoWithFix` |
| Channel combo is supported | derived | project / channel | unavailable | `pivotSupportMatrix` (renders well already) |
| Required credentials stored | asked | channel / project | user | background-job failure per credential |
| Demo path validated | probed | channel / channel | mendel | `IsDemoValidated`, four inline checks |
| Production path validated | probed | channel / channel | mendel | `IsProdValidated`, two inline checks |
| Platform issues a hostname | declared | platform / platform | unavailable | `HostnameSource`, read in four places |

Channel validation is the existing **probe** worth keeping in mind as the model
for the cluster-side work: hello-world deploy, health check, teardown. It does
not ask whether the credentials look right, it uses them.

### 5.3 Named demos over https

Every row here exists today, correctly, in `DomainReadiness`. It is listed to
show what a well-formed capability looks like when the whole ladder is present.

| Condition | Source | Declared / Satisfied | Remedy | Today |
|---|---|---|---|---|
| Base domain set | asked | project / project | user | ladder step 1 |
| Static IP reserved | derived | project / project | mendel | ladder step 2 |
| Wildcard A record resolves to it | observed | project / project | user | ladder step 3 |
| Challenge records resolve | observed | project / project | user | ladder step 4, **fan-out by count** |
| Certificate ACTIVE | observed | project / project | elsewhere | ladder step 5 |

### 5.4 A Variation can run anywhere

| Condition | Source | Declared / Satisfied | Remedy | Today |
|---|---|---|---|---|
| Each `secret` requirement has a value | declared | variation / **project** | user | `EvaluateRequirements` |
| Each `acknowledgement` is confirmed | declared | variation / **deployment** | user | `EvaluateRequirements`, deferrable |
| The deploy URL is registrable | derived | deployment / deployment | unavailable | `DeployURLLimitation` |
| Test config present | declared | repo / repo | user | absent means no Docker tests |

The two-scope rows are §3.3's case, and they are the reason a single scope field
is not enough.

### 5.5 A Variation can take live traffic

Declared, in `.mendel/experiment.json`, checked by
`codegen.DeclaredExperiment.Validate`:

| Condition | Source | Remedy | Note |
|---|---|---|---|
| Assignment unit is one of the four | declared | user | |
| Assignment key source and name given | declared | user | |
| Migration has both up and down | declared | user | "an Arm that cannot be withdrawn cannot be run" |
| Migration objects are namespaced | declared | user | `mendel_exp_` |
| No durable writes when unit is `request` | **derived** | user | §13 §5.1; a derivation, not a rule beside it |

Probed, against the datastore, by `experiment.Applier.Admit`:

| Condition | Source | Remedy | Note |
|---|---|---|---|
| An adapter exists for this datastore | probed | unavailable | `ErrUnsupportedDatastore` |
| Adapter can describe structural change | probed | unavailable | |
| Speculative apply **or** disposable | probed | user | **the disjunction** (§3.1) |
| Live datastore is **not** disposable | probed | user | **the negation** (§3.1) |
| Migration passes the deny-list | probed | user | before anything runs |
| Migration is purely additive | probed | user | the affirmative judgment |
| Migration adds something | probed | user | |
| Touched collections exist | probed | user | |
| Touched collections have an identity | probed | unavailable | else the archive cannot be restored |
| Verification store matches production | probed | user | else the proof is about the wrong schema |
| No schema drift since admission | probed | elsewhere | re-checked at apply |

Asked, before traffic, by `Experiment.NotReadyToStart` and `ValidateAllocation`:

| Condition | Source | Remedy | Note |
|---|---|---|---|
| Minimum detectable effect set | asked | user | else no way to tell null from underpowered |
| Planned duration set | asked | user | sets expected spend as well as the stop |
| Stopping rule pre-registered | asked | user | the peeking hazard |
| Dissonance acknowledged | asked | user | typed phrase, `requirement_acknowledgements` shape |
| Allocation totals 100 with one mainline | derived | user | |

### 5.6 Designed and not built

These are the eleven from §13 and §16. Under D38 every one of them is
`unimplemented` on day one, and the live-experiment capability is honestly
unavailable until they are not.

| Condition | Source | Remedy | Origin |
|---|---|---|---|
| Gateway API CRDs and controller installed | probed | **either** | §16 §2.3, D22a |
| A `GatewayClass` is `Accepted` | probed | mendel | §16 §2.3 |
| Channel is experiment-capable (≠ deploy-valid) | derived | either | §16 §2.4 |
| Variation changes exactly one deployable unit | probed | user | §16 D27 |
| Assignment key is edge-extractable | declared | user | §16 D30 |
| Privileged datastore credential available | asked | user | §13 §15 — **narrows only** |
| Projected archive size under ceiling | derived | unavailable | §13 §9 |
| Platform can route by Assignment Unit | declared | unavailable | §13 §6.3 — Cloud Run cannot |

---

## 6. What is most likely to break this if it is built too early

The brief asks for this explicitly, and it is the right question, because the
design above generalises from **one and a bit examples**: one complete ladder
(`DomainReadiness`) and one complete checker (`RequireForExperiments`) whose
scope is a single interface. Everything else in §5 is being *fitted* to a shape
derived from those two, not evidence that the shape is right.

Three of the cluster-side conditions from §16 §8 item 3 stress it in ways
nothing currently in the tree does. Two of them are already handled by decisions
above, deliberately; the third is not, and is honestly open.

**"Install the Gateway API controller" has a runtime-decided actor.** Every
condition in §5.1 through §5.5 knows in advance whose move it is: a DNS record
is always the user's, a static IP is always Mendel's. This one is decided by a
`SelfSubjectAccessReview` at evaluation time — Mendel may be permitted to
install it, in which case it offers to, or may not, in which case it emits a
script for an administrator. An abstraction with `Actor` as a static field on
the condition cannot express it, and the natural workaround — two conditions,
one for each case — is wrong, because they are not two things a user can be
shown; only one of them is ever real. This is why `Remedy` includes `either`
(D40) and why `Offered` is a state (§4.2): D22a says Mendel must say what it is
about to add cluster-wide before adding it, so "Mendel can do this and is asking
first" is a real position on the ladder and not a transient.

**Its scope is the cluster, which is above the project.** Several projects on
one channel share a controller, so one project's administrator satisfying it
satisfies it for the others. `Scope` therefore needs a level above `project`,
and the observation table needs to key on something that is not a project id.
That much D36 already accommodates. What it does not settle is whether one
project's checklist may report a condition as satisfied on evidence gathered
under another project — which is a visibility question as much as a data one
(O15).

**Its probe is not where its remedy happens.** An administrator runs the script
in a terminal Mendel cannot see; Mendel learns the outcome by checking that a
`GatewayClass` reached `Accepted`. Nothing in §5 separates those. The domain
ladder comes closest — the user types records into a provider Mendel cannot see
into — and it is exactly the case the existing comment calls out as the reason
every step there is observed rather than asserted. So the shape survives, but it
survives because one of the two prototypes happened to have already met this
problem. That is thin evidence, and worth saying so.

**And the condition that most threatens the model is the quiet one.** The
privileged datastore credential does not gate anything; it narrows what
enforcement is available. §3.5 declines to invent a `degrades` severity for it
on the grounds that the honest model may be two capabilities rather than one,
and O17 leaves it open. If that resolves the wrong way after the catalogue is
built, it is a change to the type, not to a row — which is the argument for
settling it before §8's step 6 rather than after.

---

## 7. Open questions

**O14 — Is the dependency graph really global?** §3.2 asserts it and gives a
reason, but the evidence is one ladder. The test is whether any capability ever
wants two conditions in the opposite order from another capability. If one does,
the answer is probably that the condition is two conditions with one name, but
that should be established on a real case rather than assumed here.

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

**O17 — Is `degrades` a severity, or a second capability?** §3.5. The candidate
resolution is that "Tier 2 experiments with enforcement" and "Tier 2 experiments
without enforcement" are two rows, one of which requires the privileged
credential — which removes the severity field entirely and makes the API key and
rate-card cases rows too ("cost-bounded generation" versus "round-bounded
generation"). That is cleaner and it may be over-fitting to three examples.
Leaning toward two capabilities; not decided.

**O18 — How stale may an observation be before it is `unknown` again?** The
existing domain cache has an answer for DNS. A cluster access probe, a channel
validation from three months ago, and a certificate state have different useful
lifetimes, and a single TTL will be wrong for most of them.

**O19 — Does the catalogue need to be enumerable from the database for the
debug view?** D41 keeps it in Go. The developer-facing grid (D43) then has to be
rendered from a Go value, which is fine, but it means an operator cannot query
"which projects are blocked on the certificate" in SQL. That may be worth an
exported read-model later; it is not worth inverting D41 for.

**O20 — Does re-expressing `OnboardingLifecycle` belong here at all?** It is in
§5.1 as four derived conditions, and it is the one existing mechanism whose
prose does not decompose cleanly: its `switch` distinguishes "drafting" from
"the draft failed" from "no objectives yet" with three different sentences for
what is arguably one condition. Forcing it into the catalogue may lose something
the ribbon does well. It is listed, not committed.

---

## 8. Decisions

| # | Decision | Rejected alternative | Why |
|---|---|---|---|
| D32 | A capability is a conjunction of disjunctive clauses, one level, no nesting | Conjunction only; arbitrary boolean tree | Conjunction lies about `SpeculativeApply` **or** `Disposable`; a tree renders as a decision tree, which is a worse thing to hand someone than a list |
| D33 | A literal carries the expected value, so a condition can be required false | A positively-phrased mirror condition for each negation | Two names for one fact is how they drift apart |
| D34 | A clause names its preferred disjunct | Present the alternatives unordered | "Make either of these true" with no guidance is worse than either half of it |
| D35 | Conditions form one global DAG; `blocked` is computed, not authored | Per-capability ordering; declaration order | Two capabilities could disagree about reality, silently; authored order is right when written and wrong three commits later |
| D36 | Two scopes per condition: declared-at and satisfied-at | One scope | `requirements.json` is declared per-Variation and satisfied per-project or per-deployment; one field asks project questions about a Variation |
| D37 | A coarser subject aggregates fan-out into one counted row | A row per instance | Already the rule for certificate challenge records: a ladder that grows a rung per zone says the task changed shape when it did not |
| D38 | `unknown` and `unimplemented` are states, never `satisfied` | Treat absent evidence as passing | The `Known` flag exists for exactly this: "looked and found nothing" would send a user to create records they created an hour ago |
| D39 | One `Missing` sentence per condition, rendered by both the decline and the checklist | Separate error strings per call site | If the two can differ they will, and the page stops being the answer to "why can't I" |
| D40 | Closed remedy vocabulary including `either` | `Actor` as a static field | "Install the controller" is Mendel's move or an administrator's depending on a runtime probe (§16 D22a) |
| D41 | Catalogue in Go, observations in Postgres, nothing already stored duplicated | A seeded `capabilities` table, per the platform rule | That rule is for data that changes without a release; a condition's evaluator is code, and a duplicated answer is a second truth |
| D42 | Capabilities and conditions are one node type | Two disjoint types | "Production is validated" is a capability users ask about and a condition experiments depend on; modelling it twice maintains two definitions of one fact |
| D43 | The user surface is a per-capability ladder; the literal grid is a debug view | Show the matrix | `pivotSupportMatrix` earns a grid with two short axes and a reader choosing a cell; forty conditions against six capabilities is a spreadsheet |
| D44 | Re-express the two existing prototypes first, with identical output required | Build for live experiments first | The abstraction must be proven against conditions that exist before carrying ones that do not |

---

## 9. Build order

Steps 1 through 5 touch no code under `internal/experiment`, `internal/codegen`
or the experiment handlers, so they can proceed alongside live-experiment
implementation without collision. Step 6 is the merge point.

1. **The catalogue and `Assess`, as pure functions with no callers.** DAG
   evaluation, clause evaluation, gating, fan-out aggregation, the state
   lattice. Testable entirely on a fixture catalogue. Port
   `RequireForExperiments`'s three literals verbatim as the first clause set,
   since it is already the target shape — this is a transcription, not a
   redesign, and if it needs to be redesigned to fit, the design is wrong.

2. **Re-express `DomainReadiness` as a capability, and require identical
   output.** The existing `domain_readiness_test.go` is the acceptance
   criterion: same steps, same states, same details, same headline. This is the
   de-risking step, because it exercises ordering, gating, observation,
   fan-out-by-count and the "not looked yet" state all at once, against a case
   that is already known to be right. If the abstraction cannot reproduce the
   ladder exactly, stop and revise it before anything else is ported.

3. **Re-express `EvaluateRequirements`.** Proves the two-scope model (D36),
   `Deferred`, and `unavailable` via `DeployURLLimitation`. Its existing tests
   are again the criterion.

4. **Replace the inline handler checks.** `handleStartDemo`,
   `runChannelDemoDeployment`, `runChannelProdDeployment` and the settings
   handler call `Assess` and render `Missing` instead of writing their own
   sentences. Nothing user-visible should change except that the sentences get
   better; the point is that after this there is one source for them.

5. **The capabilities page, and input-request filing.**
   `/p/{id}/capabilities` listing each capability with a one-line verdict, and
   `/p/{id}/capabilities/{node}` rendering the ladder. `yourmove` steps with
   remedy `user` file an input request through the generalised
   `syncDomainRequest`, so they appear under Input Needed like everything else.
   The developer grid (D43) goes behind the existing debug route.

6. **Live-experiment conditions**, including §5.6 as `unimplemented`. This is
   the first time the catalogue carries a capability that is not available, and
   the first time `either` and `Offered` have a real user. Settle O17 before
   this rather than after.

Steps 1 and 2 are the ones that decide whether any of the rest is worth
building, for the same reason §13 §16 put migration non-interference first: they
are the part that can fail, and failing there is cheap.
