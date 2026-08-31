# Live-Traffic Experiments — Design

Status: **revised after review.** No code written. §13 (Decisions), §14
(Resolved from review) and §15 (Open questions) are the parts most worth your
time.

Everything currently in the tree under `traffic_allocations`,
`traffic_allocation_slices`, and `traffic_allocation_envoy_configs` is dead —
no callers outside `internal/db` — and predates this design. Treat it as
deleted; it is not a starting point.

---

## 1. The question this answers

Not "how do we make every Variation safely reversible under live traffic" —
that is not achievable, and pursuing it produces a system that either lies or
never ships.

The question is: **which Hops and Variations can Mendel run as live experiments
that it can automatically roll back, and which are bigger bets the user must
manage directly?**

That reframing turns an impossible universal into a classification problem, and
it gives the product its shape. A user can *request* a live-traffic experiment;
Mendel may answer that it knows no safe way to run this one, and say precisely
why.

### The asymmetry that sets the default

Wrongly refusing an experiment costs a missed opportunity the user can still
take manually. Wrongly accepting one corrupts production data in ways no
rollback recovers.

**Therefore: default deny.** Mendel admits only what it can affirmatively show
is safe. "Uncertain" resolves to "you manage this one." Every rule below
inherits this default.

---

## 2. Vocabulary

- **Experiment** — potentially multiple Variations from one Hop that are
  competing on live traffic.
- **Arm** — one Variation within an experiment, plus mainline as the control.
  The standard term in A/B testing and clinical trials, and unambiguous inside
  Mendel: nothing else here is an arm.
- **Assignment Unit** — the thing traffic is divided by: user, session, request,
  tenant. Not implied, always declared.
- **Allocation** — the share of Assignment Units each Arm receives.

"10% of traffic" is incoherent without the Assignment Unit. Allocation is over
Assignment Units, never over requests.

Naming Arm and Assignment Unit took a couple of passes, so the reasoning is
worth recording. A bare "Unit" was too vague, but "Experiment Unit" for the
first concept is worse than vague: in experimental design an *experimental unit*
is the entity being randomised — the second concept, not the first — so a reader
with a statistics background would take it to mean exactly the opposite.
"Variant" is out for a different reason: too close to Variation, which Mendel
already uses for something specific.

---

## 3. Tiers

The admission test: *if this runs at X% for N days and then stops, does the
system return to a state indistinguishable — in ways that matter — from never
having run it?*

### Tier 1 — presentation-only

No schema change. No writes to shared state beyond what mainline writes with
identical semantics. No new external side effects.

Copy, layout, styling, client-side interaction, re-ranking a read-only list.
Rollback is *stop routing*; there is nothing to undo.

This is the launch surface, and the case that will impress users: stateless UI
changes measured against conversion.

### Tier 2 — additive schema, per-Assignment-Unit writes

Adds nullable columns or new tables that only this Variation reads. Writes only
to rows keyed by the Assignment Unit being served, so sticky assignment means mainline and
the Variation never interleave conflicting semantics on the same row.

Rollback is: stop routing → archive the added data → run the down migration.

Admissible only with everything in §5 satisfied.

### Tier 3 — everything else

Semantic reinterpretation of existing data. Destructive migration. Mutation of
global or shared rows (counters, inventory, config). New external side effects
(charges, mail, webhooks). Auth or permission changes.

Mendel declines, names the specific reason, and offers demo-level comparison
instead. Still a Hop, still tracked, still deployable — just a bet the user
drives.

---

## 4. Classification, and why it is not the safety story

**Two signals.** Each one independently answers *what is the highest tier this
Variation could be admitted at?* — Tier 1, Tier 2, or Tier 3 and therefore
refused. Each answer is a ceiling, and the Variation is admitted at the **lower
of the two ceilings**. Neither signal can raise the other's answer; either can
lower it.

1. **Declaration** — codegen states what it touched, following the
   `requirements.json` precedent. Cheap, but self-reported by the thing being
   judged, so it can only ever lower the ceiling, never vouch for one.
2. **Static analysis of the diff** — the load-bearing signal. The target is
   *durable state attached to the Variation that is not accounted for by its up
   and down migrations*. Note this includes writes to **existing** tables: a new
   nullable column is the easy case; the risk is the `INSERT INTO orders` that
   looks like mainline's and isn't.

Worked example: codegen declares "presentation only" (ceiling: Tier 1) but the
diff touches a migration file (ceiling: Tier 2). The Variation is admitted at
Tier 2 and must satisfy everything in §5 — a declaration does not get to
override what the diff plainly shows.

**Adversarial audit is not part of this design.** An agent prompted to refute
safety was considered and set aside: slow, expensive, and of marginal value once
these two signals and the enforcement in §4.1 are in place. It stays on the
shelf in case static analysis proves too permissive in practice (see D12).

Re-run on **every revision**. A revision that adds a write turns a Tier 1 into a
Tier 3 and must halt any running experiment.

### 4.1 Enforcement — what makes this shippable

A classifier is a belief about code. These are guarantees regardless of what the
code does, and they are the reason Tier 1 is safe to ship even though the
classifier will sometimes be wrong:

- **Tier 1 Arms get a read-only database role.** If classification was wrong and
  the code writes, it errors instead of corrupting a row.
- **Tier 2 Arms get grants scoped to what their migration added**, plus whatever
  mainline already writes for tables they legitimately share.
- **Egress is restricted by NetworkPolicy**, so an Arm cannot reach Stripe or an
  SMTP host even if it tried.

**Be precise about the limit:** a grant stops "wrote a table it never declared."
It cannot stop "wrote wrong values into a table it legitimately writes." Static
analysis carries the semantic burden; grants carry containment. Neither
substitutes for the other.

This is the main argument for k8s-first — both mechanisms are native there.

### 4.2 This is a platform prerequisite, and it is a big ask

Live experiments need programmable L7 routing and pod-level credential and
egress control. In practice that means Kubernetes (§6). A user on Fly.io or
Cloud Run cannot run one without migrating, and migrating a production
deployment is a far larger undertaking than anything else Mendel asks of anyone.

So it has to be said **early and unprompted** — when a user first expresses
interest in production experiments, or when a Hop is proposed with
`requires_production`, not at the point of refusal after they have built
Variations for it. The honest framing is a prerequisite with a real cost, not a
missing feature Mendel might add later.

Demo-level comparison stays available on every platform, and remains the right
answer for most Hops.

---

## 5. What every live experiment must declare

Six things, all required before admission:

1. **Assignment Unit and its key.** `user | session | request | tenant`, plus how to extract
   the key (cookie name, header, JWT claim, subdomain). App-specific, so it is a
   `.mendel/` declaration — the app tells Mendel what a "user" is rather than
   Mendel assuming.

2. **Schema additions**, if any. Names, types, and which Arm reads them.

3. **Up and down migrations, with a test that exercises the whole cycle.**
   Not "the migration applies" — the full round trip in a throwaway schema:
   `up → write synthetic rows → archive → down → restore → verify`. Then the
   archive path is known-good before it is needed, matching the discipline of
   `go test ./schema/...` requiring real Postgres.

4. **Migration safety, checked mechanically rather than argued.** The hazards
   here are enumerable: `CREATE INDEX` without `CONCURRENTLY`, `ADD COLUMN NOT
   NULL` without a default, volatile defaults forcing a rewrite, type changes,
   renames. Lint them (the `squawk` / `strong_migrations` rule set encodes this
   class). Keep a prose rationale for the human to read; do not let it be the
   gate — an LLM asserting a query is fine is weak evidence.

5. **User-visible dissonance on rollback**, where it exists — a plain
   description of what a person who experienced the Variation will feel when it
   is withdrawn. The Mendel user must **type a short summary character-for-
   character** to approve, so approval cannot be a reflexive click. This reuses
   the `requirement_acknowledgements` shape exactly: keyed by the precise string
   confirmed, recording who and when.

6. **Success criteria**, including the statistics (§8).

### 5.1 The Assignment Unit is a correctness constraint, not a preference

Three things must agree: what the proxy hashes, what durable writes are keyed
by, and the denominator of the success metric. Mismatches are refusable
mechanically:

- Per-**event** assignment is invalid for any Variation writing per-Assignment-Unit durable
  state (the same row gets written by two Arms' logic), and invalid for any
  user-scoped metric like conversion (one person sees both Arms).
- Therefore `assignment_unit: request` implies **no per-Assignment-Unit durable writes are
  admissible** — a derivation, not a separate rule.
- **Tenant** matters for B2B: two people in one org seeing different UIs is its
  own dissonance problem, and it is a different key from user.

---

## 6. Routing

### 6.1 Mendel does not build a proxy

Mendel owns the **control plane** — compute the allocation, render it as
configuration, apply it, observe. The data plane is off-the-shelf. A proxy in a
user's request path means owning HTTP/2, websockets, streaming, retries, TLS,
connection pooling, and being the thing that takes their revenue down at 3am.
This is the same call the codebase already made for deploys: deterministic Go
driving `flyctl`/`gcloud`, not generated scripts.

### 6.2 Target Gateway API, not Istio

Header matching and weighted `backendRefs` are core to `HTTPRoute`, not
extensions. Implemented by Istio, Envoy Gateway, kgateway, Contour, Cilium,
NGINX Gateway Fabric, GKE Gateway, and AWS's controller. Mendel emits one
resource kind and works across every conformant implementation.

### 6.3 Assignment is the platform-specific seam

Gateway API can **match** on a header; it cannot **compute** the bucket. That
step differs per platform, so it sits behind a narrow interface — *given a
request, produce an Arm assignment and route to it* — with per-platform
implementations. Lua stays out of the domain model.

| Platform | Mechanism |
|---|---|
| Kubernetes | Envoy Lua/WASM filter, or a small assigner service in front |
| Fly.io | `fly-replay`: a router app reads the cookie and responds `fly-replay: app=<variation-app>`; Fly Proxy replays internally, ~10ms |
| AWS ALB | Weighted target groups plus header-based rules, no mesh |
| Edge functions | Cloudflare Workers / Vercel middleware / CloudFront, in front of any origin |

**Cloud Run cannot do this.** Its splitting is percentage-of-requests across
revisions, and its session affinity pins a client to an *instance*, best-effort,
broken by autoscaling. No operator-controlled header, cookie, or identity
routing. It gives "10% of requests," not "these Assignment Units."

k8s first. Fly second — already supported, and `fly-replay` is unusually clean.

---

## 7. Concurrency and schema drift

Multiple Arms of one Hop **must** run in parallel; that is the entire model. The
question is only independent experiments touching the same schema, and the
answer must not assume Mendel is the only writer of the user's database.

Mechanism:

- **Record the schemas of the touched tables** — column names and types,
  verbatim — at classification time, scoped to the tables the Variation actually
  touches, so unrelated schema churn does not invalidate everything constantly.
  Stored as the schema itself rather than a hash: they are small, and when one
  fails to match, the reader wants to see *what* changed, which a fingerprint
  cannot tell them.
- **Serialize migration application with a lock held in Mendel's own database.**
  Not the user's: advisory locks are a Postgres and MySQL feature, and a
  Mendel-side lock keyed by (project, datastore) works no matter what the user
  runs. It orders Mendel against itself, which is what it is for.
- **Re-read the touched-table schemas after acquiring the lock.** If they differ
  from classification time, the classification is stale — re-classify rather
  than apply, and show the reader the difference.

Mendel is not the only writer of the user's database, so nothing here prevents
an external DDL change landing between the re-read and the apply. That window is
accepted rather than closed; the re-read shrinks it to the width of one
migration, and the alternative would be a lock the user's datastore may not
offer.

Additive-ness makes conflicts rare but not impossible: two Arms both adding
`users.preferred_theme` with different types collide. Additive does not imply
commutative when names coincide. See Open Question O1.

---

## 8. Statistics

The hazard is **peeking**. An autonomous agent checking daily and stopping at
p<0.05 has a badly inflated false-positive rate — and Mendel is autonomous by
construction, so this is the default failure unless designed against.

Required before an experiment starts, not after:

- A **minimum detectable effect** the user cares about.
- A **duration estimate** derived from expected traffic and the MDE, which also
  sets the expected hosting spend — this ties directly into the existing cost
  model, where every experiment-day is money.
- A **pre-registered stopping rule**: either a fixed horizon, or a sequential
  test designed for continuous monitoring. Not "look each morning."

Results must never be presented as a bare point comparison. An interval, or
nothing.

---

## 9. Data disposition

### On rollback

The down migration drops what was added, destroying the experiment's own data.
Before that: **archive it**, encrypted with the project key, to Mendel-owned
blob storage with a TTL.

What is archived is bounded by *experiment traffic, not table size* — the
non-null rows in added columns, i.e. the Assignment Units that actually participated. So it
is estimable at admission and genuinely small.

Which makes it a gate rather than a hope: **projected archive size is an
admission criterion.** If it exceeds the ceiling, the Variation is not Tier 2 —
it is a bigger bet the user manages. Discovering the size at teardown is too
late to decline.

Three requirements on the archive:

- **Encrypted before upload** with the project key (`crypto.Encrypt`,
  `MENDEL_CREDENTIAL_KEY` already exist). Mendel holds the key, so this is not
  isolation *from* Mendel — but it protects against bucket misconfiguration and
  makes deletion a crypto-shred rather than a promise about object lifecycle.
- **Primary keys included**, not just values, so rows can be correlated back
  later. An orphaned column dump does not answer "was rejecting this a mistake?"
- **Explicit "downloaded and verified" action** permitting early deletion, with
  the TTL clock visible. Silent expiry of the only copy of production data is
  the failure to avoid.

Honest caveat: while it sits there, Mendel holds the user's end-users' data.
Short TTL, and a reason this should not remain the permanent design.

### On promotion

The experiment's up migration **becomes** the mainline migration. It has already
been exercised under live traffic, which is a better provenance than most
migrations get.

### What rollback does not undo

A losing Arm that ran for three days placed real orders and sent real mail. Its
column can be dropped; its side effects are permanent. That is what an
experiment *is* — but the UI must not imply otherwise.

---

## 10. Metrics

### 10.1 Two tiers, and the first needs nothing from the app

**Guardrails — free.** The gateway already labels every request by Arm. Error
rate, status distribution, and latency percentiles per Arm cost nothing and
require zero app cooperation. These drive auto-rollback.

**Business metrics — isolated provider.** The app constructs a **second
`MeterProvider`** with its own `PeriodicExportingMetricReader` and its own OTLP
exporter pointed at Mendel. The app's global provider, readers, and exporters
are untouched. Different provider, different endpoint, no interposition on
anything.

This costs ~10 lines of app code, which for Mendel is nearly free: **codegen
writes the app's code.** The Variation being asked to report a conversion metric
gets its meter setup generated alongside the feature it measures.

It also survives the minimize-dependencies test: what lands in the repo is plain
OTel with a normal metric name configured from an env var. No Mendel SDK, no
Mendel import. The endpoint is injected at deploy time by the machinery that
already exists for requirements. If Mendel disappears, the app exports to
nothing and fails silently.

Rejected: the collector-in-the-middle approach, where the app re-points
`OTEL_EXPORTER_OTLP_ENDPOINT` at Mendel and Mendel forwards everything onward.
One env var instead of code, but it interposes Mendel on *all* of the app's
telemetry — unacceptable for a mature app, and it drags Mendel toward being an
observability platform.

### 10.2 Ingest: Mendel's own OTLP endpoint, no collector

OTLP/HTTP is a protobuf POST to `/v1/metrics`. Mendel's existing Go server
accepts it directly: one more handler, one more table, **no new
infrastructure** — which fits what Mendel is, a Go binary and a Postgres.

The collector's processors turn out to be unnecessary once the app side is an
isolated provider: nothing to filter (that provider emits only
`mendel.experiment.*`), and nothing to enrich (Mendel injects
`OTEL_RESOURCE_ATTRIBUTES` at deploy time, so attribution rides along). The
gateway's guardrail metrics point at the same endpoint.

**Identity comes from the credential, never the payload.** A public OTLP
endpoint that trusts a `mendel.variation` attribute is trivially forgeable —
anyone could poison an experiment. Mendel mints a per-deployment bearer token,
injects it as `OTEL_EXPORTER_OTLP_HEADERS`, and resolves (project, Arm)
server-side. The resource attribute is a cross-check, not the source of truth.

Cardinality protection at ingest: reject metric names outside the contract, cap
attribute values, cap series per Arm. Otherwise one buggy generated app writes
unbounded rows into Mendel's database.

### 10.3 Storage: one shared database

```
(project_id, experiment_id, arm_id, metric_name, bucket_start, count, sum)
```

A contingency table, not a telemetry store. Ten projects × 5 Arms × 5 metrics at
5-minute buckets is single-digit millions of rows per month — unremarkable for
Postgres, rolled up to hourly after 24h if the fine grain is only needed for
guardrail responsiveness. Per-project infrastructure would cost orders of
magnitude more to operate than this data justifies.

If ingest volume ever outgrows the handler, a collector goes in front without
touching the app contract, since the endpoint is an env var.

### 10.4 Accumulate in memory, flush on an interval

Ingest does **not** write to Postgres per OTLP request. Counts accumulate in
memory keyed by `(project, experiment, arm, metric, bucket_start)`
and flush every 60s, upserting into the table above:

```sql
INSERT INTO ... VALUES (...)
ON CONFLICT (project_id, experiment_id, arm_id, metric_name, bucket_start)
DO UPDATE SET count = experiment_metrics.count + EXCLUDED.count,
              sum   = experiment_metrics.sum   + EXCLUDED.sum
```

A 5-minute bucket therefore takes five accumulating upserts rather than one row
per request, and write volume is bounded by *series count*, not traffic.

**Losing the in-flight window on a crash is accepted.** At most 60s of counts
from an experiment that runs for days, which cannot move a conclusion that
needed statistical significance to reach. Durability is not worth a write per
ingest here.

If that trade ever stops holding, the replacement is a write-optimised log that
is truncated on each flush — not a Postgres write per request.

---

## 11. Blast radius

Independent of classification being right:

- Maximum exposure ceiling per experiment.
- Auto-rollback on error-rate or latency regression against the control Arm.
- Time cap and spend cap, the latter wired to the existing cost model.
- A one-action kill switch returning 100% to mainline.

---

## 12. When Mendel declines

The decline is a product surface, not an error. It must name the specific
finding — "this writes to `orders.status`, which mainline also writes with
different semantics" — not "unsupported." The user can then either revise the
approach or take the bet manually, with the Hop and its Variations still tracked
and still comparable at demo level.

---

## 13. Decisions

Recorded with what was rejected, for audit.

| # | Decision | Rejected alternative | Why |
|---|---|---|---|
| D1 | Classification problem, default deny | Universal safe rollback | Not achievable; failure costs are asymmetric |
| D2 | Mendel owns control plane only | Mendel-maintained L7 proxy | Permanent liability in the user's request path |
| D3 | Emit Gateway API `HTTPRoute` | Istio `VirtualService` | Portable across conformant implementations |
| D4 | Enforce tiers via DB grants + NetworkPolicy | Trust the classifier | Classifier will be wrong; containment must not depend on it |
| D5 | Unit of analysis declared per experiment | Hardcode "user" | Unit is a correctness constraint tying routing, writes, and metrics |
| D6 | Migration hazards linted mechanically | LLM performance rationale | Hazards are enumerable; prose is weak evidence |
| D7 | Isolated `MeterProvider` in the app | Collector-in-the-middle | Never interpose on the app's own telemetry |
| D8 | OTLP ingest in Mendel's Go server | Run an OTel Collector | No new infra; processors unnecessary given D7 |
| D9 | One shared metrics database | Per-project infrastructure | Data volume does not justify the operational cost |
| D10 | Identity from bearer token | Trust payload attributes | Otherwise experiment results are forgeable |
| D11 | Archive to Mendel-owned storage, TTL'd | User-owned bucket | Lower setup burden; user bucket has no natural home off GCP |
| D12 | Adversarial audit deferred | Include from the start | Expensive; marginal once static analysis + enforcement exist |
| D13 | Arms of a Hop run in parallel; drift handled by recorded schemas + lock | One experiment at a time | A blanket constraint breaks the core model |
| D14 | Lock held in Mendel's database, not the user's | Advisory lock in the user's datastore | Not every datastore offers one; Mendel only needs to order itself |
| D15 | Record touched-table schemas verbatim | Hash fingerprint | Schemas are small, and a mismatch should show *what* changed |
| D16 | `mendel_exp_` prefix, renamed on promotion | Refuse on name collision | Commutative additions, and the schema documents which objects are experimental |
| D17 | Accumulate in memory, flush every 60s | Postgres write per OTLP ingest | Write volume bounded by series count, not traffic; 60s of loss cannot move a conclusion |
| D18 | Allocation derived from MDE and duration, mainline carries the brunt | Even split across Arms | Minimises exposure while still reaching significance |
| D19 | Mainline deploys during an experiment: carry on and annotate | Invalidate and restart | Unrelated merges are constant; restarting means nothing ever finishes |
| D20 | Tier 1 assigns by a Mendel-set cookie; `user`/`tenant` wait for Tier 2 | Read an app-supplied user key in Tier 1 | No login-transition case to handle; the assigner is a hash and a Set-Cookie |

---

## 14. Resolved from review

**Namespacing (was O1).** Additive objects are created under a `mendel_exp_`
prefix carrying the Variation token, making concurrent additions genuinely
commutative, and **renamed on promotion**. The prefix doubles as documentation:
a human reading the schema can see at a glance which objects belong to a live
experiment and which are permanent.

The consequence to design for: promotion is a rename migration *and* a code
edit, because the merged code must stop referring to the prefixed name. On
Postgres the rename itself is metadata-only and cheap; the code edit is
codegen's job, and it has to be atomic with the rename or the merged code breaks
on the next deploy.

**Archive (was O2).** Not mandatory. Required only where the added data is not
derivable from what mainline already stores, which codegen declares and the user
can waive. Default on, since a waiver is a choice someone made and an omission
is not.

**Allocation (was O3).** The user decides, but Mendel proposes **mainline
carrying the brunt of traffic**, with each Arm given the minimum
allocation that reaches significance within the target duration. This inverts
the usual framing: allocation is *derived* from the MDE and the duration (§8)
rather than picked, and the derivation is what Mendel shows the user.

**Terminology (was O5).** The Variation-in-an-experiment is an **Arm** and the
thing traffic is divided by is an **Assignment Unit**, for the reasons recorded
in §2. "Experiment Unit" was tried and abandoned: it names, in conventional
statistical usage, the concept it was not being applied to.

**Mainline deploys mid-experiment (was O6).** Mainline is the control, and it
will be deployed to while an experiment runs. Mendel **carries on and
annotates**: the result records that the control changed underneath the
comparison, with the commits and the time they landed, so a reader can judge
whether the change plausibly moved the metric.

Not restarting is the right default because unrelated merges are constant and
invalidating on each would mean no experiment ever finishes. The stricter rule —
blocking mainline changes that are *related* to what the experiment touches — is
future work rather than a gap: the static analysis of §4 already computes what a
Variation touches, so overlap with an incoming merge is computable when it is
wanted. Until then the annotation makes the risk visible instead of silent.

**Assignment when the key is absent or changes (was O7).** Resolved by the
cheapest thing that is still honest: **Tier 1 assigns by a cookie Mendel sets on
first contact**, and honours it thereafter no matter what else happens to the
request. The assigner never reads an application key, never knows whether anyone
is logged in, and has no key-transition case to handle. It is a hash and a
`Set-Cookie`.

The consequence is stated rather than hidden: the effective Assignment Unit is
the **browser**, not the person. One human on a laptop and a phone is two
Assignment Units. That is how most experimentation tooling behaves, and for
presentation experiments it is fine.

So Tier 1 offers only `device` (cookie-pinned) and `request` (no stickiness).
`user` and `tenant` require reading a key the application supplies, which is
what drags in the login-transition problem — so they arrive with Tier 2, where
per-Assignment-Unit durable writes make honouring the declared key actually
necessary. Nothing here forecloses them.

**Guardrail baseline (was O4).** Mendel proposes defaults for every statistical
parameter — minimum sample before guardrails may fire, MDE, duration, stopping
rule — and the user may override any of them. Until an Arm reaches
the minimum sample, guardrails cannot fire on a comparison; the absolute
circuit-breakers in §11 still apply, since a hard error-rate ceiling needs no
baseline to be exceeded.

---

## 15. Prerequisites

**Database access (was O8) — resolved.** Mendel wrote the application, so it
knows how the application connects to its own database, and can recover the
connection from the repository and the environment it already injects. Where
that is not discoverable, it asks — through the same `requirements.json`
mechanism that already collects secrets, which is exactly the shape of question
it was built for.

One wrinkle to design around rather than assume away: the credential the
*application* uses is usually not privileged enough to `CREATE ROLE` or `GRANT`,
which is what §4.1's enforcement needs. So the sequence is: discover the app's
connection, check whether it can create the roles Mendel wants, and if it
cannot, request a privileged credential as a `secret` requirement, naming
plainly what it is for. A user who declines gets Tier 1 with static analysis
alone and a clear statement that enforcement is unavailable — not a silent
downgrade.

**Kubernetes (was O9) — in progress.** The `gke` channel exists in
`hosting_platforms` and `deployToGKE` is written, but staging has only a
`fly-io`/`container` channel and no GKE channel has ever been validated, so that
code has never run against a real cluster. Routing, grants and NetworkPolicy all
target a platform nothing currently exercises.

Being handled separately: the pong test project moves from Fly.io to GKE, which
also exercises changing an existing project's deployment channel — itself an
untested path. §16 items 3 and 4 are blocked until that lands.

---

## 16. What I would build first

Deliberately not the whole design. Tier 1 alone is both the safest and the most
demonstrable slice, and it requires solving none of the migration problem:

1. Experiment and Arm domain model, Assignment Unit declaration,
   allocation over Assignment Units.
2. Tier 1 classifier: declaration plus static analysis, default deny.
3. Read-only DB role and NetworkPolicy enforcement for Tier 1 Arms.
4. Gateway API `HTTPRoute` generation, k8s assignment filter, kill switch.
5. OTLP ingest endpoint, token minting, contingency-table storage.
6. Guardrail metrics and auto-rollback.
7. Eval matrix showing Arms with intervals, and the decline surface.

Tier 2 — migrations, archive, recorded schemas, lock — is the second phase, and
the design above exists so that phase does not require reopening phase one.

**Item 5 is the one that can start today.** OTLP ingest, token minting and the
contingency table depend on nothing in §15: no cluster, no database credential,
no routing. Items 3 and 4 are blocked on Kubernetes (§15). Items 1 and 2
can proceed, but item 2's static analysis needs a decision on how Mendel
recognises a presentation-only file in an arbitrary repository — declared paths
in `.mendel/`, cross-checked against a conservative extension allow-list, is the
proposal.
