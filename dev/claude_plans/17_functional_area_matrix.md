# The Functional Area Matrix — Design

Status: **built.** Four functional areas, twenty-two conditions, both ladders and
every deploy gate going through one mechanism. §9 records what each step
corrected. §4.2 is generated from the catalogue and checked by a test, so this
document cannot drift from the code it describes. What remains is listed in §5
and is additions rather than architecture.

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

### 3.5 The fifth question: every cell is true or false, and never neither

Three conditions in the tree do not block anything when absent. They make the
thing that happens worse:

- **No Anthropic API key** on the project. `ProjectReadiness.IsReady()`
  deliberately excludes `HasAPIKey`, and `MissingSettings()` does not list it.
- **No rate card for the model.** `runBudget.Apply` falls back from a spend
  ceiling to a 50-round cap, and says so in the log.
- **No privileged datastore credential** (§13 §15). Arms run as the application,
  enforcement is unavailable, and the design is explicit that this is "not a
  silent downgrade."

And a fourth shape turned up later, in `experiment_readiness.go`: a verification
datastore is required of an experiment that changes the schema and irrelevant to
one that does not. The obvious readings are a severity on the cell for the first
three and a conditional cell for the fourth, and **both are wrong** (D33, D38).

**A condition is a total predicate. If a situation exists where it is neither
true nor false, the condition is stated at the wrong level.**

The tell is always the same: a condition that goes undefined names a *mechanism*
rather than an *outcome*. "A verification datastore exists" is a mechanism, and
it has nothing to say about an experiment that changes no schema. State the
outcome instead —

> **Schema changes can be proved additive without touching production.**

— and it is true of an experiment with no migration, because there is nothing to
prove; true of one with a migration and somewhere to verify it; false only when
there is a migration and nowhere. Three states, no fourth, nothing conditional.

The code already reaches this on its own for the case it can answer.
`experiment_readiness.go` returns `StepDone` with *"Not needed: nothing in this
experiment changes the schema"* when `SchemaChanges` is `FactFalse` — a step
marked done because the requirement is vacuous, which is exactly the total
predicate. Its `Advisory` flag fires only in the remaining case,
`FactUnknown`, where Mendel does not yet know whether a migration is coming. And
"not known yet" is not a severity and not applicability: it is the `unknown`
state this design already has, gated on a dependency it already models — *it is
settled whether this experiment changes the schema*.

Restating the other three the same way disposes of the severity too:

| Mechanism, undefined somewhere | Outcome, total |
|---|---|
| A rate card exists for the model | A generation run is bounded before it starts |
| An adapter exists for the datastore | The datastore supports what this experiment does |
| A project-level Anthropic key is set | An API key is available to generate with |

Each right-hand form is true or false in every situation, including the ones
that made the left-hand form go quiet.

**What is left over is a warning, and a warning is not a cell.** One case does
not restate: production answering over http means the assignment cookie cannot
be `Secure`, so it can be rewritten in transit and a participant could choose
their own Arm. That is real, it is permanent, and it is a poor reason to refuse
to run an experiment — `experiment_readiness.go` marks it `Advisory`
unconditionally and is right to. It is not a gate, so it does not belong in a
grid of gates. Warnings are a separate, short list beside the matrix, and the
matrix stays a conjunction of predicates that are each simply true or false.

This is the same lesson as §3.1, arrived at from the other end. There the fix
for an apparent disjunction was to state the condition at the level of what is
wanted rather than how it is supplied. Here the fix for an apparent third truth
value is the same move. Two of the four questions this section set out to answer
turn out to have one answer.

## 4. The matrix

### 4.1 The rows

Six functional areas, all of which exist as things a user wants and asks about.
The last two are not available today.

| # | Functional Area | Short name below |
|---|---|---|
| 1 | Write code for a Variation | Code |
| 2 | Run a demo of a Variation | Demo |
| 3 | Deploy to production | Prod |
| 4 | Serve deployments by name over https | Named |
| 5 | Run a live experiment | Experiment |
| 6 | Enforce Arm containment | Enforce |

**One experiment row, not two.** An earlier draft had *Exp1* for a
presentation-only experiment and *Exp2* for one with a migration, which was
wrong twice over. The numbering implied a pair of comparable things when the
second is the first plus a schema change, and the split only existed because
the migration conditions appeared to be inapplicable to Exp1 — which §3.5 shows
is an artefact of stating them as mechanisms. As totals they are simply true of
an experiment with no migration, and the two rows are one.

That also puts the matrix back in agreement with §13 §16 — *"Tier 1 falls out of
Tier 2 rather than preceding it: an experiment that declares no migration is a
Tier 1 experiment, and needs strictly less"* — and with `experiment_readiness.go`,
which has always had one ladder. It matters beyond tidiness: two rows implies the
user picks which kind of experiment they are running, and one row implies Mendel
derives it from what the Variation declared. §13 is explicit that the tier is
classified, never chosen.

### 4.2 The matrix

**Generated from the catalogue.** This table is rendered by
`Catalogue.MatrixMarkdown` and checked by `TestThePlanMatchesTheCatalogue`, so a
condition added in Go without this document updating fails the suite. Regenerate
with:

```bash
MENDEL_UPDATE_MATRIX=1 go test ./internal/domain/ -run TestThePlanMatchesTheCatalogue
```

That is the same discipline `schema/full.sql` is under, and it is here for the
same reason: a document describing code it is not connected to drifts, and drift
in *this* document means a reader reasoning about a matrix that no longer
exists. The design is about gates saying one thing and pages saying another; a
plan saying a third would be the joke writing itself.

● required ◌ required, and no evaluator written yet ○ a warning (§4.2.1), which
does not gate

A `◌` is not a gap in the design. It is a condition this document names, the
catalogue carries, and nobody has implemented -- and `Assess` reports it as
`unimplemented`, which is neither satisfied nor failed, so an area holding one
is unavailable and says why. The mark exists because the alternative is that a
deferred condition renders identically to a finished one, and this table is what
a reader trusts when deciding whether an area is covered.

<!-- BEGIN GENERATED MATRIX -->
| Functional Area Condition | Evidence | Remedy | Declared | Satisfied | demo | experiment-admission | live-experiments | named-demos | production |
|---|---|---|---|---|:-:|:-:|:-:|:-:|:-:|
| That combination is one Mendel can deploy | derived | unavailable | project | channel | ● |  |  |  | ● |
| Choose how this project deploys | asked | user | project | project | ● |  |  |  | ● |
| Store the credentials that channel needs | asked | user | channel | project | ● |  |  |  | ● |
| The deployment has a URL a provider will accept | derived | unavailable | deployment | deployment | ● |  |  |  | ● |
| Configure the encryption key | observed | user | installation | installation | ● |  |  |  | ● |
| Store a token Mendel can push with | asked | user | project | project | ● |  |  |  | ● |
| Give Mendel a repository to write into | asked | user | project | project | ● |  |  |  | ● |
| Confirm the setup steps done elsewhere | declared | user | variation | deployment | ● |  |  |  | ● |
| Supply the values this code needs | declared | user | variation | project | ● |  |  |  | ● |
| Traffic adds up, with exactly one mainline | derived | user | experiment | experiment |  | ● |  |  |  |
| That key can be read at the edge | declared | user | experiment | experiment |  | ● |  |  |  |
| The experiment says what one participant is | declared | user | experiment | experiment |  | ● |  |  |  |
| Whatever it changes is purely additive | probed | user | variation | experiment |  | ◌ |  |  |  |
| The Variation changes one deployable unit | probed | user | variation | experiment |  | ◌ |  |  |  |
| Any schema change it declares can be undone | declared | user | variation | experiment |  | ● |  |  |  |
| Any schema change it declares stays in its own namespace | declared | user | variation | experiment |  | ● |  |  |  |
| The platform can route by assignment unit | declared | unavailable | channel | channel |  | ◌ |  |  |  |
| The projected archive fits | derived | unavailable | experiment | experiment |  | ◌ |  |  |  |
| Effect size, duration and stopping rule are set | asked | user | experiment | experiment |  | ● |  |  |  |
| Whatever it touches has an identity | probed | unavailable | variation | experiment |  | ◌ |  |  |  |
| The verification datastore agrees with production | probed | user | project | experiment |  | ◌ |  |  |  |
| The withdrawal dissonance is acknowledged | asked | user | experiment | experiment |  | ● |  |  |  |
| Prove the demo path works | probed | mendel | channel | channel | ● |  |  |  |  |
| Prove the production path works | probed | mendel | channel | channel |  |  |  |  | ● |
| A controller that can match an experiment cookie | probed | either | channel | channel |  |  | ● |  |  |
| Cluster can route per experiment arm | probed | either | channel | channel |  |  | ● |  |  |
| Give Mendel a domain you control | asked | user | project | project |  |  |  | ● |  |
| Certificate issued | observed | elsewhere | project | project |  |  |  | ● |  |
| Create the certificate record | observed | user | project | project |  |  |  | ● |  |
| Mendel reserves an address | derived | mendel | project | project |  |  |  | ● |  |
| Create the wildcard A record | observed | user | project | project |  |  |  | ● |  |
| Arms can be given the environment production runs with | observed | user | variation | project |  |  | ● |  |  |
| A non-production datastore to verify against | asked | user | project | project |  |  | ● |  |  |
| That datastore is reachable | probed | user | project | project |  |  | ● |  |  |
| Production answers at a name | observed | user | project | project |  |  | ● |  |  |
| That name serves https | observed | user | project | project |  |  | ○ |  |  |
<!-- END GENERATED MATRIX -->

Two rows of the six this document describes are not in it. **Code** — write code
for a Variation — and **Enforce** — enforce Arm containment — are designed and
not built, so they have no conditions in the catalogue and correctly do not
appear. That absence is the table doing its job: an earlier draft of this
section listed all six as though they existed.

Every marked cell is required and every one of them is true or false; there is
no operator in the table beyond *and*, and no third truth value in a cell (D33,
D51).


Three things this table says that the prose could not.

**The nine rows at the top are the reason to build this at all.** Repository,
push token, encryption key, channel, combination, credentials, secrets,
acknowledgements and the deploy URL are required by *demo* and *production*
alike, and each was checked in its own words in its own place until step 4.
Those are the duplicate implementations the matrix removed, and they are the
only reason a table beats a checklist per area.

**Demo and production differ by one cell each.** Which validation they need, and
nothing else — a thing a table makes obvious and separate checklists never
would.

**The experiment row shares nothing with the other three.** Not one cell. That
is worth noticing rather than assuming: it says the cluster-side conditions are
genuinely a different set from the deploy-side ones, and it is the evidence that
*Enforce*, when it is built, belongs as a fourth row rather than as a severity
on this one — the same reading §3.5 arrived at from the other direction.

**The domain cells that are not on the experiment row are a finding.** An
earlier draft marked base domain and certificate as required by the experiment
row. They are not: what a live experiment needs is *production answering at a
name*, which is its own condition and appears above. Nothing in §13 or §16 said
an experiment requires a domain the user controls, and drawing the table made
the question unavoidable.

The reason offered first was cookies: assignment worked by a cookie, a cookie is
scoped to a host. That turned out to be wrong twice over — a host-only cookie on
a bare address works, and §16 §3.6 now routes on a value needing no validation
at all. What survives is unrelated to assignment: Mendel runs **one Gateway per
namespace**, and the hostname is how one deployment's traffic is told from
another's. Without a hostname there is no `HTTPRoute` emitted, so there is
nothing to attach Arm matching to, whatever it would have matched on. O21
records how the question travelled.

### 4.2.1 Warnings, which are not conditions

Not everything worth telling a user gates anything. §3.5 keeps these out of the
grid so that every cell stays a gate, and lists them beside it:

| Warning | Area | What is lost |
|---|---|---|
| Production answers over http, so the assignment cookie cannot be `Secure` | Experiment | It can be rewritten in transit, and a participant could choose their own Arm |
| No rate card for the model, so the run is bounded at 50 rounds rather than by spend | Code | Rounds are a poor proxy for cost; the bound holds, its relation to money does not |
| No privileged datastore credential | Enforce | Arms run as the application, so containment is by classification alone |

The third is the one to watch: it is a warning on *Enforce* only because Enforce
is a row of its own. Were enforcement folded into the experiment row it would be
a gate, and §13 §15's insistence that this is "not a silent downgrade" would
have nowhere to live.


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

## 5. The conditions that are not built yet

§4.2 is now generated, and it lists every condition that exists. What is left to
say is what does not — the conditions this design named and the catalogue does
not carry, so that a reader can tell an omission from a decision.

**Two functional areas have no conditions at all.** *Write code for a Variation*
would carry the Anthropic key, the bounded generation run, and the three
onboarding states — a strategy, approved objectives, an approved roadmap — which
are today a `switch` in `OnboardingLifecycle` producing prose per state rather
than an enumerable list (O20 asks whether forcing them into the catalogue loses
something that ribbon does well). *Enforce Arm containment* would carry the
privileged datastore credential from §13 §15, and is blocked on the enforcement
work rather than on anything here.

**The per-experiment conditions are their own area now.** They were listed here
as a hole in the experiment row; they are `experiment-admission` in §4.2, with a
subject one step finer than a project (`ScopeExperiment`, between hop and
variation — an experiment belongs to a hop and its Arms are Variations).

Seven of the thirteen have evaluators. Six do not, and they are **declared in the
catalogue with a nil evaluator rather than left in this list**, which is the
change that matters: a nil evaluator reports as `unimplemented`, renders as `◌`
in the generated table, and makes the area unavailable with a stated reason. A
condition deferred in prose is one somebody has to remember. A condition deferred
as a nil evaluator is one a test renders every time.

| Condition | Evidence | Why not yet |
|---|---|---|
| The platform can route by Assignment Unit | declared | No platform record carries this, and a hardcoded list of platforms in Go is forbidden outright |
| The Variation changes one deployable unit | probed | Needs a real deployment to look at |
| Whatever it changes is purely additive | probed | Settled by applying the migration and diffing, which an evaluator cannot do |
| Whatever it touches exists and has an identity | probed | Same |
| The verification datastore agrees with production | probed | Same |
| The projected archive size is under the ceiling | derived | §13 §9's ceiling does not exist in code |

The four `probed` ones were not merely unwritten but **architecturally
impossible**: `Evaluate` is `func(Observations) Finding`, and that purity is what
lets `TestEveryConditionIsATotalPredicate` interrogate every condition against
synthetic situations and what makes the page free to render. §20's adapter is
what dissolves the conflict — an `admit` phase runs as a Job, records its result
in `adapter_invocations`, and the condition becomes a pure read of a stored fact,
exactly as `probeFacts` reads a probe.

**`Applier.Admit` keeps refusing regardless.** A catalogue row is a forecast; the
check against the live datastore at the moment of action is what is actually
safe. The area mirrors those checks so a reader can see them coming, and does not
replace them.

**Everywhere.** One condition applies to every row and is left out of the table
because a column of solid dots carries no information: **the project exists and
the reader may see it.** Worth stating once so that nobody adds it as a cell.


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

**And the controller condition stopped being hypothetical while this was being
written.** §16 O23 settled: the GatewayClass Mendel deploys,
`gke-l7-global-external-managed`, matches headers only exactly, and a cookie
arrives inside a `Cookie` header full of other cookies. Two of the three
assignment mechanisms cannot run on it, and the remedy under consideration is
installing Envoy Gateway — which is exactly the `either` condition above, now
load-bearing for the whole functional area rather than a cluster-side nicety.
The argument in this section was written before that was known and turns out to
have been about a real case rather than an anticipated one.

It also produced a condition this document did not have and would not have
thought of. "A `GatewayClass` is `Accepted`" was the validation step, and it is
**not sufficient**: a server-side dry run accepts an `HTTPRoute` carrying a
regular-expression header match, because the CRD schema permits it, and the
controller then declines to honour it at runtime. Accepted is a fact about the
schema; capable is a fact about the implementation. Different conditions,
different evidence, and both are now in §4.2.

That is the sharpest available argument for D38. A matrix reporting `satisfied`
on the strength of a dry run would have been confidently wrong in the same
silent way GKE has already been wrong twice — `ingressClassName: gce` naming a
class nothing provided, and a `certmap` annotation ignored outright, both
accepted, both silent. Three of one kind is a rule: **on this platform,
acceptance is not evidence of support.** Any condition whose evaluator asks a
cluster to validate something has to say which of the two it established, and
the `Missing` sentence D39 requires is where that distinction has to survive.

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

**O17 — resolved, and it took two goes.** Whether "absence that narrows" is a
cell severity or a separate functional area.

Drawing §4.2 appeared to settle it — separate rows, no severity — and D38 was
written on that basis. Then `DomainStep` grew an `Advisory` flag for a step
*"worth doing and does not have to be done"*, which is the severity D38
declined, arrived at independently in the one prototype §9 step 2 requires
reproducing exactly.

Reading how it is used showed the flag carrying two different things.
`https.Advisory = true` is unconditional, and is a genuine warning.
`store.Advisory = !needed` fires only when `obs.SchemaChanges` is `FactUnknown`
— the `FactFalse` case returns `StepDone` with *"Not needed: nothing in this
experiment changes the schema"* — so it is not applicability either, but "Mendel
does not know yet", which is the `unknown` state gated on a dependency.

Neither reading survived review. D51 supersedes both: a condition that can go
undefined names a mechanism, and restating it as an outcome makes it total. That
disposes of the conditional case, disposes of two of the three narrowing cases,
collapses Exp1 and Exp2 into one row (§4.1), and leaves exactly one warning
(D52) — which is not a cell at all.

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

**O25 — Can `Assess` take a subject finer than a project? — yes, and it did.**
The per-experiment conditions are `experiment-admission`, whose subject is one
experiment carried on `Observations.Admission`. Evaluators keep the signature
that makes them testable in bulk; only what is in the struct changed.

The framing was the expensive part. Asking one question about all thirteen made
them look like one blocked unit, when seven were only ever waiting for somebody
to write them and six are waiting on different things — four on §20's adapter,
one on a platform capability record, one on a ceiling that does not exist. The
scope rung answered itself once the schema was read: `experiments` carries a
`hop_id` and its Arms are Variations, so `ScopeExperiment` goes between the two.

**O24 — What happens to an input request when its condition is satisfied some
other way?** Step 5 stopped short of filing input requests for `yourmove` steps
because of this. A request asking for a base domain, answered by someone setting
one in project settings instead, leaves a request that is no longer about
anything — and the existing `syncDomainRequest` resolves that by reconciling on
every render, which works for one condition and wants thinking about before it
is the rule for forty.

**O21 — Does a live experiment require a domain the user controls? — resolved:
yes, and every reason first given for it was wrong.** The argument is kept in
full because the route is more useful than the answer: it produced §16 D45–D49
along the way, and it is a worked example of a cell that survives having all of
its stated justifications removed. The first draft assumed it followed from
cookies: assignment works by a cookie the assigner sets (§16 D23),
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

Which sorts the mechanisms:

| Mechanism | Needs a domain | Works for anonymous traffic |
|---|---|---|
| Application-emitted bucket | no | only if the app mints an anonymous id, which is a cookie again |
| Mapped identity field, validated at the edge | **yes** | no |
| Mapped opaque token, unvalidated | no | no |
| Mendel-set cookie at the edge (§16 D23) | no — see the correction below | **yes** |

Only the second needs a domain, and it is the one mechanism §16 did not adopt.

Two things argue against the cheapest version of this — mapping an existing
field directly — and they are what pushed the answer toward the application
minting a bucket rather than the edge reading one.

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

A conformance argument was offered here and does not survive. It ran: exact
header matching is Core support, regular-expression matching is
implementation-specific, so an enumerated bucket stays Core where a prefix match
does not. It is wrong, and §16 O23 is why — a bucket travels in a cookie, so the
edge matches the `Cookie` header, which carries every cookie the visitor holds,
and an exact match on a whole header value cannot find one entry in a list. Both
mechanisms need the same regular-expression match, and both stand or fall with
O23. The case for the application-emitted bucket rests on the salt and the
uniformity, which are enough.

**Where this landed.** §16 §3.6 and §3.7 take the application-emitted bucket for
`user`, `session` and `tenant`, and weighted `backendRefs` for `request`; D23 is
narrowed to `device` rather than withdrawn, because minting identity at the edge
is still the only mechanism that works for a visitor nobody has identified —
which is Tier 1, the launch surface. D45–D49 record it.

**And then the answer came back yes anyway, on a reason nobody had offered.**
Both cookie arguments do fall: a host-only cookie on a bare address works, the
`mendel_arm` cookie is no more spoofable than a bucket, and the TLS requirement
attaches to edge-validated identity, which no adopted mechanism uses. What none
of that reached is Mendel's own topology. There is **one Gateway per namespace**
(`gatewayName = "mendel"`), and the hostname is how one deployment's traffic is
told from another's on it. `k8sManifestFor` emits no `HTTPRoute` at all without
a hostname, and `ExperimentDeployment.Validate` says so directly: *"an
experiment needs a hostname: the routes are matched on it."*

So the cells are required, the assignment argument was a red herring in both
directions, and lifting the requirement would mean revisiting the shared Gateway
— one address and one certificate serving every deployment, which is what a
single wildcard record and a single reserved address require.

Two lessons worth keeping, since this is the most instructive thing in the
document. **A condition can be right while every reason given for it is wrong**,
which is an argument for D39's insistence that the sentence naming what is
missing be written per condition and kept true, rather than inferred from
whichever rationale was fashionable when the cell was filled in. And a matrix
that had merely recorded `●` here would have been correct and useless: it was
being made to *defend* the cell that found the real reason, and that is worth
remembering when §9 step 5 decides how much of the reasoning the page shows.

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
| D38 | `unknown` and `unimplemented` are states, never satisfied | Treat absent evidence as passing | The `Known` flag exists for exactly this reason: "looked and found nothing" would send a user to create records they created an hour ago |
| D51 | A condition is a **total predicate** — true or false in every situation. A cell has no third value, conditional or otherwise; a condition that goes undefined names a mechanism and must be restated as an outcome | A `degrades` severity for the narrowing cases; a conditional cell for the schema case | Both encode in the grid something belonging in the condition's own definition. "A verification datastore exists" is silent about an experiment with no migration; "schema changes can be proved additive without touching production" is true of it. The same move as D33 |
| D52 | A thing worth saying that gates nothing is a **warning**, listed beside the matrix and never in it | A non-blocking cell state | A grid of gates whose cells are sometimes not gates cannot be read at a glance, and exactly one warning does not restate as a total |
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

1. **The catalogue and the evaluator, as pure functions with no callers.** —
   **done**, `internal/domain/functional_area.go`. Graph evaluation, gating,
   fan-out counts, the state lattice, and a `BuildCatalogue` that refuses a
   cycle, a dangling dependency, or an area gated by nothing.

   Two of this design's rules are now tests over the shipped catalogue rather
   than sentences in a document nobody rereads.
   `TestEveryConditionIsATotalPredicate` asks every condition about seven shapes
   of observation and fails any that cannot answer (D51).
   `TestUnsatisfiedConditionsSayWhatIsMissing` fails any condition that is
   unsatisfied and says nothing about what would change it (D39).

2. **Re-express `DomainReadiness` as a functional area, and require identical
   output.** — **done.** All seven tests in `domain_readiness_test.go` pass
   unchanged, and `TestTheLadderIsTheAssessment` now holds the two together so
   they cannot drift apart later.

   It reproduced, and cost two corrections, both to things this document had
   stated wrongly:

   **Evaluate first, then gate.** The obvious order — refuse to run a condition
   whose dependencies are unmet — reports an already-satisfied condition as
   blocked, which is a plain lie about something that is true. So a condition is
   asked first and only *demoted* to blocked when it comes back unsatisfied,
   which is what `gate()` always did. The states meaning "Mendel does not know"
   are not demoted either, or an unchecked step would be called blocked and
   claim knowledge in the other direction. The saving the original order was
   after — not paying for a lookup that cannot matter — belongs to whoever
   gathers the observations, since evaluators are pure functions over a struct
   already in hand.

   **Four of five dependencies were condition-to-condition; one was not.** The
   certificate records cannot be created until Mendel has requested a
   certificate, and "Mendel has requested one" is a fact about the request rather
   than a condition anyone needs listed. That needed an explicit `Ready`
   predicate beside `DependsOn`. It is the first evidence about D35 and it is
   mixed: the graph carried four fifths of the ordering, and the remaining case
   did not want to be a condition. Watch whether `Ready` stays rare — if the next
   two areas need it, the likelier reading is that those preconditions *are*
   conditions and the catalogue is under-populated (O14).

3. **Re-express `EvaluateRequirements`.** — **done**,
   `internal/domain/functional_area_deploy.go`, along with the rest of the
   conditions the *Demo* and *Production* rows need. `EvaluateRequirements`
   itself is unchanged and still does the per-requirement judging; what is new
   is the two conditions that aggregate it, and the nine others beside them.

   D36 now has work to do rather than being documentation. `BuildCatalogue`
   refuses a condition satisfied at one scope that depends on one satisfied at a
   finer scope: a project-scoped answer resting on a per-deployment one has as
   many answers as there are deployments, and picking one silently is how a
   project gets reported ready on the strength of its healthiest deployment.

   The rest landed as designed. A `secret` is declared per Variation and
   satisfied per project; an `acknowledgement` is declared identically and
   satisfied per deployment; `Deferred` is satisfied rather than outstanding,
   because production's redirect URI is unknowable until production exists;
   `DeployURLLimitation` is `unavailable`, since no value the reader supplies
   fixes a bare IP over http.

   **And the totality test earned itself immediately.** The channel-validation
   conditions read `IsDemoValidated()` off `Observations.Channel` without
   checking it for nil, which panics for any project with no channel — the
   commonest state a new project is in.
   `TestEveryConditionIsATotalPredicate` caught it on the first run, before any
   caller existed. That is the rule from §3.5 doing exactly what it was written
   for: a condition that cannot answer about some situation is not merely
   inelegant, it is a crash waiting for that situation.

4. **Replace the inline handler checks.** — **done.** Seven hand-written gates
   across `handlers_demo.go`, `handlers_prod_deploy.go` and
   `handlers_settings.go` now evaluate the functional area and render
   `Assessment.Missing`. `TestARefusalAndTheChecklistQuoteTheSameSentences`
   holds a refusal and the page together, which is D39 asserted rather than
   documented.

   It turned out to be less a refactor of the checks than of where the *facts*
   come from. `gatherObservations` was project-scoped and the page's alone;
   giving it a variation makes it the single place a project, a variation and a
   deployment are observed together, and the deploy paths now read their
   requirements back out of it rather than looking them up again — a second
   lookup can disagree with the gate that just allowed the deploy. The predicted
   deploy URL, which decides what an acknowledgement resolves to, had been
   computed at three call sites and had to agree with a fourth copy inside the
   deploy; it is derived once now, from `deployAppName`.

   Two things changed beyond the wording, both deliberate:

   **Seven gates became four, because most of them were duplicates of a check
   further down.** `runDemoStartup` and the retry-with-fix path now judge only
   whether a channel exists, which is what stops them calling the deploy at all;
   `runChannelDemoDeployment` is the choke point all three demo paths pass
   through, so the gate belongs there and not at each of them.

   **Production is assessed before its `hosting_deployments` row exists.** The
   two old checks sat either side of that row: an unvalidated channel refused
   without one, unmet requirements recorded a *failed deployment that had never
   begun*. Neither is an attempt worth keeping, and `handleRedeploy` assesses
   too, so the reader still gets the reason synchronously rather than from a log
   line.

   Held until now because the three files it lands in were being actively edited
   by the routing work; step 5 went first instead, needing only new files.

5. **The page.** — **done**, `/p/{id}/available` and
   `/p/{id}/available/{areaID}`, a tab beside Deployment and Domain. It lists
   every area with a one-line verdict and renders each one's checklist through
   the same `ladder` markup the Domain tab uses, so it reads as part of the app
   rather than beside it.

   Two things it does that the design asked for and one it does not.
   `TestTheChecklistShowsEveryStepTheAssessmentFound` fails if the page drops a
   step the assessment reports — a checklist a reader can work through and still
   not proceed is worse than none. `TestThePageQuotesTheSentenceARefusalWouldUse`
   fails if a `Missing` sentence does not reach the page, which is D39 asserted
   rather than asserted-about.

   **Input-request filing is not built.** Generalising `syncDomainRequest` means
   deciding what happens to a request when its condition becomes satisfied by
   something other than the request being answered, and that question is worth
   its own pass rather than a paragraph here (O24).

6. **The Experiment row.** — **done.** `experiment_readiness.go` was a second
   ladder with its own observation struct, its own state vocabulary aliased onto
   the domain ladder's, and its own blocker list; `ExperimentReadiness` is now
   one assessment of an `experiment` area. `either` has its real user in the
   controller install, and nothing authors `Advisory` any more.

   The two decisions this ladder prompted were tested by folding it in, and both
   held. **The warning mechanism (D52) did not exist and does now**: an area
   declares `Warns` beside `Requires`, an unsatisfied warning does not make the
   area unavailable, and https is the one member. **The datastore steps became
   totals (D51)** and needed no flag: an experiment that changes no schema has
   nothing to prove.

   Three things fell out.

   **`Scope`'s zero value was a real scope.** Five domain conditions shipped
   with scopes nobody had written, which happened to be the coarsest and so
   passed the coherence check vacuously. The zero value is invalid now, and the
   check caught all five on the first build. A field that is wrong by omission
   and right by accident is worse than one that fails.

   **An area can be unavailable purely because nothing has been checked**, and it
   still owes the reader a reason. An evaluator has nothing to say about the case
   where Mendel did not look, so the reason is derived from the state in
   `Assess` rather than left to each evaluator to remember — which makes
   "unavailable implies a stated reason" structural instead of a convention.

   **One rung stopped vanishing.** "That datastore is reachable" was omitted
   entirely for a presentation-only experiment; it now renders as done, saying it
   is not needed — which is what the datastore step one line above it already
   did, and what the challenge-records step's own comment argues for: a ladder
   that changes length tells the reader the shape of the task changed when it did
   not. That argument does not care which direction the length moves in. One test
   assertion changed with it, deliberately.

7. **The plan's table, generated.** — **done.** `Catalogue.MatrixMarkdown`
   renders §4.2 and `TestThePlanMatchesTheCatalogue` fails when it is stale,
   with `MENDEL_UPDATE_MATRIX=1` to rewrite it. Same discipline as
   `schema/full.sql` against the migrations, and here for the same reason: this
   document is about gates and pages saying different things, and a plan saying
   a third would be the joke writing itself.

   Generating it immediately corrected two claims. The document said the
   experiment row required a base domain and a certificate; it requires
   *production answering at a name*, which is its own condition. And it listed
   six areas as though all six existed, where two have no conditions at all —
   §5 now says which and why.

8. **The per-experiment area.** — **done.** `experiment-admission`, thirteen
   conditions with a subject one rung finer than a project. Seven have
   evaluators; six are declared with none, so they report as `unimplemented`,
   render as `◌`, and hold the area unavailable until somebody writes them.

   The old `experiment` area is `live-experiments`, because one name was
   answering two questions — whether a project is *equipped* to run experiments,
   and whether a *particular* experiment may start — and that conflation is what
   made O25 read as one blocked unit rather than as two pieces of very different
   size. Not `experiment-capable`: `experiment.Capabilities` already means
   something else two files away, which is the collision the naming rule exists
   to catch.

   The matrix grew a third mark for it. `◌` is a required condition with no
   evaluator, and it exists because the alternative renders a deliberate hole
   identically to a finished cell — in the one table a reader consults to decide
   whether an area is covered.

9. **What is left**, and it is additions rather than architecture. The *Code*
   and *Enforce* rows have no conditions yet; six admission conditions have no
   evaluator, four of them waiting on §20's `admit` phase; and `yourmove` steps
   still do not file input requests (O24). §5 has the list.


Steps 1 and 2 are the ones that decide whether any of the rest is worth
building, for the same reason §13 §16 put migration non-interference first: they
are the part that can fail, and failing there is cheap.
