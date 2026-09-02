# Experiment Routing — Implementation Plan

Status: **draft for review.** No code written.

Companion to [13_live_traffic_experiments.md](13_live_traffic_experiments.md),
which settled *what* to build and why. That document records decisions — Gateway
API rather than Istio, Mendel owns a control plane and not a data plane,
assignment is a per-platform seam. It does not say what Mendel emits, who
installs what, or how an allocation reaches a running request. This one does.

The decisions in §13 are assumed, not revisited. Where this plan needs one §13
did not make, it is called out and added to §9 below.

---

## 1. Scope, and the boundary that makes it tractable

**Experiments are available only for projects whose production Mendel deploys.**

The alternative is reaching into ingress a user configured themselves, and that
is a different and much worse product: Mendel would be editing the routing of an
application it did not deploy, in a cluster whose conventions it does not know,
with no safe way to put it back.

Since `runChannelProdDeployment` already deploys into a namespace of Mendel's
own (`hosting.Namespace`, `mendel-apps`), the projects that can run experiments
are exactly the ones already inside that boundary. Everything below assumes it.

A project deployed some other way gets the §13 decline: demo-level comparison,
and a bigger bet the user manages.

---

## 2. How many Envoys, and who installs them

### 2.1 One per Gateway, not one per pod

Worth settling first, because it changes how large the ask is. Gateway API's
`Gateway` is an **ingress point**, not a sidecar. Creating one causes the
controller to provision a single Envoy Deployment plus a LoadBalancer Service,
and all traffic for the routes attached to that Gateway flows through it. There
is no Envoy alongside each application pod, and no east-west interception.

That is the distinction between an ingress gateway and a service mesh. A mesh
puts a proxy next to every workload for mTLS and east-west policy, and it is a
far heavier thing to adopt. Splitting *inbound* traffic across Arms of one
application needs none of that.

So the footprint is: **one Envoy for the experiment's Gateway.** Not one per
Arm, not one per deployable unit, not one per pod.

### 2.2 Two installs, at different levels

They are worth separating because only one of them is an administrator's:

| Layer | Scope | Who | Frequency |
|---|---|---|---|
| Gateway API CRDs, the controller, a `GatewayClass` | cluster | administrator | once per cluster |
| A `Gateway` resource, and the Envoy it provisions | namespace | **Mendel** | once per project |

Mendel owning the `Gateway` matters. It means Mendel gets its own Envoy for
experiment traffic rather than sharing whatever the user's other workloads use,
so an experiment cannot disturb unrelated ingress, and teardown removes an Envoy
Mendel created. Only the layer above it — cluster-scoped CRDs and a controller
with its own upgrade lifecycle — is genuinely an administrator's to own.

### 2.3 Who installs the controller

Mendel **asks the cluster whether it may**, rather than assuming either way.

Kubernetes answers this question directly through `SelfSubjectAccessReview` —
the API behind `kubectl auth can-i`. At channel validation Mendel checks whether
its credential can create `customresourcedefinitions`, `clusterroles` and a
namespace. That is exact, costs one API call, and sidesteps reasoning about how
GKE's IAM roles combine with Kubernetes RBAC, which is a union of two systems
and not reliably predictable from the role name alone.

**If the answer is yes**, Mendel offers to install the controller, and says what
it is about to add cluster-wide before doing it. Installing CRDs into someone's
cluster is not a thing to do silently, even when permitted.

**If the answer is no**, Mendel emits the exact commands for an administrator to
run — the same shape as the `SetupScript` that `hosting.DefaultPlatforms`
already uses for credentials, where Mendel writes a script the user pastes and
Mendel then validates the result. That pattern exists and works; a gateway
install is the same problem with a different script.

Either way Mendel validates the outcome rather than trusting it: a usable
`GatewayClass` must be `Accepted` before the channel is experiment-capable.

### 2.4 What this still costs the user

Even at its best this is the second large prerequisite after Kubernetes itself.
A cluster with no Gateway API controller needs one installed, and if Mendel's
credential cannot do it, a human with cluster-admin has to. §13's §4.2 says the
Kubernetes requirement must be raised early and unprompted; this belongs in the
same conversation, not discovered later.

**A channel can therefore be valid for deploys and not valid for experiments.**
That distinction has to exist in the data model, or a user with a working deploy
channel will be told experiments are available and find out otherwise at the
worst moment.

---

## 3. How a request reaches an Arm

The problem §13 identified: Gateway API can **match** on a header but cannot
**compute** which Arm a visitor belongs to. Its answer was "Envoy Lua/WASM
filter, or a small assigner service in front", which is two options and a
contradiction — a Lua filter is an Envoy extension, not Gateway API, so it
undoes §6.2's portability in the same breath that claims it.

**The resolution: assign once, then route on the cookie with plain Gateway API
matching.** Assignment is only needed for a request that has no cookie yet.

```
                    ┌─────────────────────────────────────┐
   request ────────▶│ HTTPRoute                           │
                    │                                     │
                    │  Cookie: mendel_arm=a  ──▶ Service arm-a
                    │  Cookie: mendel_arm=b  ──▶ Service arm-b
                    │  Cookie: mendel_arm=0  ──▶ Service mainline
                    │  (no match)            ──▶ Service assigner
                    └─────────────────────────────────────┘
```

The assigner is a Mendel-built component, and the important property is that **it
is not a proxy**. It receives a request with no assignment cookie, picks an Arm
by weight, sets `Set-Cookie: mendel_arm=<arm>`, and returns `302` to the same
URL. It never forwards a body, never streams, never holds an upstream
connection. It is a few dozen lines with no data-plane responsibilities, which
is what keeps §6.1's decision intact.

Every subsequent request from that visitor carries the cookie and is routed by
header matching, which *is* core Gateway API — so the portability claim holds on
every conformant implementation, with no Envoy-specific extension anywhere.

### 3.1 Reaching the Arm, not just knowing it

Everything above routes traffic **at the edge**. Once a request is inside the
cluster, an Arm's code calls its collaborators by their ordinary names, and
those resolve to mainline. So Arm b's unit A talks to mainline B.

When a Variation changes only A that is not a flaw, it is correct: B is
byte-identical in both Arms, and there is nothing to isolate. When a Variation
changes A *and* B, and A calls B, it is broken — new A talking to old B is
neither Arm, and whatever the experiment measures is a mixture nobody designed.

The earlier rule here was "a Variation may change exactly one deployable unit",
enforced by counting touched directories in the diff. Counting is the wrong
test, and it is wrong in both directions. Two units entered independently from
the edge — a web frontend and an API the browser calls directly — are both
routed by the same visitor cookie, so changing both is perfectly safe and the
count forbids it. Meanwhile one unit that calls itself through a queue is
counted as one and is not obviously safe at all.

**The constraint is a call edge between two changed units**, not how many
changed. Two conditions, and both must hold:

1. **Every changed unit can extract the Assignment Unit key and recompute the
   Arm** (D28, applied per unit). A unit blind to the key cannot know which Arm
   it is serving, so it serves mainline.
2. **No changed unit is reached from another changed unit by an internal call**,
   unless east-west routing exists to carry that call to the right Arm.

Condition 1 is what makes an Arm *knowable*; condition 2 is what makes it
*reachable*. Extraction alone is not enough, because the edge cannot see an
internal call at all — that is §3.2's subject, and until a mesh or internal
Gateway is in place, condition 2 is satisfied only by the changed units being
entered independently from the edge.

For a monolith this is no narrowing whatever: one deployable unit, no internal
call edges between changed units, condition 2 vacuous.

### 3.2 What lifting that restriction actually takes

Two things are needed: the Arm identity must reach each hop, and each hop must
route on it. They are usually bundled under "get a service mesh", but they are
different problems with different owners, and the first one is where this gets
decided.

**Propagating the Arm is the wrong shape.** A sidecar sees an inbound request
and an outbound request as unrelated connections and cannot correlate them;
Istio is explicit that applications must forward trace headers themselves. So
carrying an Arm the way trace context is carried makes experiment correctness
depend on getting distributed context propagation right in someone else's
application — notoriously subtle, and in places not even well defined. A unit
that batches fifty users' work into one call downstream has no single Arm for
that call, and whatever context happens to be active when the batch flushes is
arbitrary. The failure is silent and produces a *wrong* Arm.

**Propagate the identity instead, and recompute.** Arm assignment is a pure
function of the Assignment Unit key, the allocation and the salt. Give every hop
the key and every hop derives the same Arm independently. There is no causal
chain to preserve, nothing to lose across an async boundary — only a value that
is present or absent.

That changes the character of the requirement in three ways:

- **The key is already in flight.** User and tenant identity move between
  deployable units as a matter of business necessity, since every hop that authorizes
  anything needs them. Trace context is infrastructure Mendel would be asking a
  team to add; identity is data they already pass.
- **A missing key fails safe and legibly.** No key means mainline — a known
  default — rather than an arbitrary Arm. Mendel can also observe internal calls
  arriving without one, which turns a silent bias into a visible one.
- **It needs no new declaration.** §13 §5 already requires the application to
  say which cookie, header or claim carries the Assignment Unit key. This is
  that same declaration, applied at every hop rather than only the first.

Batching is still not solved — fifty users in one call have no single key — but
it degrades to mainline and is detectable, rather than silently attributing the
batch to whichever request was on the stack.

**What this does not replace: east-west routing.** Something must still
intercept internal calls and choose a backend, which is a mesh sidecar or an
internal Gateway. What changes is the mesh's job: mechanical matching on a
header that is already present, which is the part meshes do reliably, rather
than correlating requests, which they cannot do at all.

**The identity header becomes a routing input, and inputs from clients are
spoofable.** A caller setting the identity header itself could choose its Arm.
The edge must *overwrite* it from the validated session rather than pass through
what arrived, or participants self-select and the comparison quietly stops
meaning anything.

### 3.3 When the key has to be available

Routing needs the Assignment Unit key. So a Variation can only take live traffic
if **the key is derivable by code that is identical in every Arm** — which in a
request flow means derivable before any of the divergent code runs.

Ordering is the practical form, but independence is the actual requirement. If
the key were extracted inside divergent code, two Arms could extract different
keys, and the assignment would depend on the Arm it is supposed to select.

**Eligibility is therefore a property of the (Variation, Assignment Unit) pair,
not of the Variation alone.** The Google sign-in Variation already in the test
project is the clean example. Keyed on `user` it is ineligible: the user id does
not exist until the authentication code has run, and the authentication code is
precisely what varies. Keyed on `device` it is fine: the cookie is there before
any application code executes. Same Variation, opposite answers — so any refusal
has to name the unit it is refusing for, or it will be wrong half the time.

#### The rule: the edge extracts, or the experiment does not run

Deciding "does divergent code run before extraction" in an arbitrary codebase is
a reachability question across middleware, dynamic dispatch and async
boundaries. It is not something to infer from a diff or ask a model to
adjudicate; at the limit it is undecidable, and well before that limit it is
unreliable.

So make it structural instead. **Mendel requires the key to be extractable at
the edge** — its own assignment cookie, a JWT claim, a session the gateway can
validate — before any application code runs at all. Then "before divergent code"
holds by construction, for every Variation, with nothing to analyse, nothing to
declare beyond which cookie or claim carries it (§13 §5 already requires that),
and no per-language tooling.

A key that only application code can compute makes the experiment ineligible,
and Mendel says so. That is the conservative default, and it is the same shape
as every other decline here.

#### A decline is a diagnosis, not a verdict

Before refusing, Mendel should consider whether **the client could be changed to
put the key where the edge can see it** — and say so when it could.

This matters more than it first appears:

- **Mendel writes the client too.** Making a web or mobile client send a stable
  identifier on its requests is a small additive change of exactly the kind
  Mendel generates. The remedy for "this cannot be experimented on" is often
  itself a Hop, and Mendel is in a position to propose it rather than leave the
  user to work it out.
- **For a native client it is the only mechanism.** There is no cookie for the
  edge to set, so a client-sent header is not an expansion of the options — it
  is the entire option. Without it mobile traffic cannot participate at all.

Two hazards to carry into that:

- **A client-supplied value may be trusted for bucketing, never for identity.**
  An opaque install or device token is fine: assignment is not an authorisation
  decision. A client-asserted *user* id is not fine, and D29 stands — where a
  validated session exists, the edge overwrites what arrived.
- **Mobile rollout biases the population.** A header added today reaches users
  over weeks, and older versions never send it. Experimenting only on clients
  that send the key means experimenting on the subset that upgrades promptly,
  which is not a random subset. Either wait for adoption to be broad enough to
  be uninteresting, or record the restriction as a stated limit on what the
  result generalises to. Web clients mostly escape this; a cached SPA does not.

### 3.4 What this costs

- **One extra round trip on a visitor's first request.** Acceptable; it happens
  once per Assignment Unit per experiment.
- **A 302 on the first request.** Fine for `GET`. A first-ever request that is a
  `POST` cannot be redirected without losing the body, so the assigner sends
  those to mainline unassigned rather than mangling them. Rare — a first contact
  is nearly always a navigation — and failing to mainline is the safe direction.
- **Cookie-less clients never enter an experiment.** They always fall through to
  the assigner and always redirect. To avoid an infinite loop for a client that
  refuses cookies, the assigner marks the redirect (`?_ma=1`) and sends a second
  cookie-less arrival straight to mainline.

### 3.5 Where the weights live

In a `ConfigMap` the assigner reads, written by Mendel — **not** fetched from
Mendel's API at request time. The user's production traffic must not depend on
Mendel being reachable. If Mendel is down, the last-written allocation stands.

The weights are deliberately *not* in the HTTPRoute's `backendRefs`. Weighted
backendRefs pick per request, which would split a visitor across Arms on every
navigation — the opposite of what an Assignment Unit means (§13 §5.1).

---

## 4. What Mendel emits

Per experiment, in `mendel-apps`:

| Resource | Per | Notes |
|---|---|---|
| `Deployment` | Arm | The Variation's image, `PORT` from `hosting.ContainerPort` |
| `Service` | Arm | ClusterIP, selecting that Arm's pods |
| `Gateway` | project | Provisions the experiment's own Envoy (§2.1) |
| `Deployment` + `Service` | experiment | The assigner |
| `ConfigMap` | experiment | Allocation the assigner reads |
| `HTTPRoute` | experiment | Cookie matches per Arm, fallback to assigner |
| `Secret` | Arm | The Arm's own datastore credential (§5) |
| `NetworkPolicy` | Arm | Egress restriction (§5) |

Mainline keeps the Deployment and Service it already has; it becomes one more
`backendRef`, matched by `mendel_arm=0`.

`deployToGKE` grows an Arm-aware sibling rather than being overloaded: it
currently assumes one Deployment named for one app, and an experiment needs N
named for Arms plus the routing around them.

---

## 5. Enforcement, concretely

§13 §4.1 argues that containment must not depend on the classifier being right.
That was an argument; this is the mechanism.

**Datastore credential per Arm.** Mendel creates a Postgres role per Arm and
injects a distinct connection string into that Arm's pods, so the Arm connects
as itself rather than as the application:

- Tier 1: `GRANT SELECT` on everything the app can read, and nothing else. A
  Tier 1 Arm that tries to write gets an error instead of a corrupted row.
- Tier 2: the Tier 1 grants, plus `INSERT`/`UPDATE` on exactly the objects its
  admitted migration added — which `Admission.Delta.Added` already names — plus
  whatever mainline writes on collections it legitimately shares.

This is where §13 §15's privileged-credential question becomes load-bearing: the
application's own credential almost certainly cannot `CREATE ROLE`. If Mendel
has no privileged credential, Arms run as the application and enforcement is
unavailable — which is said out loud, not silently skipped.

**Egress.** A default-deny `NetworkPolicy` in `mendel-apps` allowing DNS, the
datastore, and nothing else, so an Arm cannot reach Stripe or an SMTP host even
if its code tries. This is the mechanism that makes "no new external side
effects" a fact rather than a hope.

---

## 6. Changing and stopping

**Reallocation** rewrites the ConfigMap. Existing visitors keep their Arm, since
their cookie decides; only newly-assigned visitors see the new weights. That is
the correct behaviour for an experiment in flight and needs no explanation to
anyone who has run one.

**The kill switch must override cookies**, and this is the detail most easily
got wrong. Setting the allocation to 100% mainline only changes what *new*
visitors get; everyone already assigned keeps arriving at a broken Arm with
their cookie. So stopping an experiment means **removing the per-Arm match rules
from the HTTPRoute**, leaving mainline as the only backend. Cookies then match
nothing and fall through. Teardown of Deployments and Services follows, and the
migration rollback (§13 §9) after that.

Order matters: routing first, then workloads, then schema. Dropping a column
while an Arm is still serving is how you turn a withdrawal into an incident.

---

## 7. Mendel's own schema

None of this exists yet. `internal/experiment` is a proven library with no
caller and no persistence.

```
experiments          project, hop, assignment unit + key, status, timing,
                     MDE / duration / stopping rule (§13 §8)
experiment_arms      experiment, variation (null for mainline control),
                     allocation weight, deployment identifiers
arm_admissions       the Admission: migration, recorded Delta, recorded
                     shapes, verdict, when
arm_archives         pointer to the stored dump, size, TTL, downloaded-at
experiment_events    allocation changes, guardrail firings, mainline deploys
                     landing mid-experiment (§13 §14, "carry on and annotate")
```

`experiment_events` is what makes the annotation in §13's D19 possible; without
it "the control changed underneath the comparison" has nowhere to be recorded.

---

## 8. Order of work

1. **Schema and domain model** (§7), and wire `internal/experiment` to it. It is
   currently proven and unused; this is what makes it reachable.
2. **The `.mendel/` declaration** so codegen produces an experiment migration at
   all — nothing upstream generates one today.
3. **Gateway validation and install** (§2): the `SelfSubjectAccessReview`
   probe, the install path when permitted, the admin script when not, the
   `GatewayClass` acceptance check, and the experiment-capable distinction on
   the channel.
4. **The assigner**, with its ConfigMap contract. Testable on its own: given
   weights and a cookie, which Arm.
5. **Arm deployment and HTTPRoute generation** (§4), as pure functions first —
   the same shape as `k8sManifestFor`, so the resources can be tested without a
   cluster.
6. **Enforcement** (§5): roles, grants, NetworkPolicy.
7. **Kill switch and teardown** (§6), in that order.

1 and 2 are unblocked today. 3 onward wait on the pong project moving to a GKE
channel, which §13 §15 records as the remaining staging move.

---

## 9. Decisions this plan adds

| # | Decision | Rejected alternative | Why |
|---|---|---|---|
| D21 | Experiments only for Mendel-deployed production | Reach into user-configured ingress | Mendel would be editing routing it did not create, with no safe way back |
| D22 | Mendel owns the `Gateway`; the controller is installed by whoever may | Mendel always installs, or never installs | The two layers have different scopes and different owners |
| D22a | Detect the right with `SelfSubjectAccessReview`, install if permitted, else emit a script | Infer from the IAM role name | IAM and RBAC combine as a union; asking the cluster is exact |
| D23 | Assign once by 302 + cookie, then match on the cookie | Envoy Lua/WASM filter | Header matching is core Gateway API; a Lua filter undoes the portability it claims |
| D24 | Weights in a ConfigMap the assigner reads | Assigner queries Mendel per request | Production traffic must not depend on Mendel being up |
| D25 | Weights not in `backendRefs` | Weighted backendRefs | They pick per request, splitting one visitor across Arms |
| D26 | Kill switch removes the match rules | Set allocation to 100% mainline | Allocation only affects new visitors; the already-assigned keep their cookie |
| D27 | A Variation may change any number of deployable units, provided each can extract the Assignment Unit key and no changed unit calls another without east-west routing | Restrict a Variation to one | Counting is wrong in both directions: two edge-entered units are safe, one self-calling unit is not. The real constraint is the call edge (O13, §3.1) |
| D28 | Propagate the Assignment Unit key and recompute the Arm at each hop | Propagate the Arm as trace baggage | Identity is already in flight and needs no correlation; a lost key means mainline, a lost context means a wrong Arm |
| D29 | The edge overwrites the identity header from the validated session | Trust what the client sent | Otherwise participants can select their own Arm |
| D30 | Require the Assignment Unit key to be edge-extractable | Analyse whether extraction precedes divergent code | Reachability in an arbitrary codebase is not decidable from a diff; the structural rule needs no analysis |
| D31 | A decline names the client change that would lift it | Refuse and stop | Mendel writes the client, so the remedy is often a Hop it can propose — and for native clients it is the only mechanism |

## 10. Open questions

**O10 — Does the assigner see enough to assign well?** It sees a cookie-less
request and nothing else. Excluding a cohort — logged-out users, a region, a
plan tier — needs an attribute it does not have. Either the app supplies one on
the request, or targeting is limited to "everyone" in the first cut.

**O11 — What happens to a visitor whose Arm is withdrawn mid-session? —
resolved.** Their cookie names an Arm that no longer routes, so they fall
through to mainline, which is correct. If the Arm wrote per-Assignment-Unit
state they then see some of it and not the rest.

This needs no new mechanism, but it does change the wording of one that exists.
§13 §5 item 5 has the Mendel user type a summary of the dissonance
character-for-character, and that summary is written **about rollback**. D26
reaches the same state sooner: the kill switch works by removing the match
rules, so everyone already assigned falls through immediately — no rollback, no
migration reversed, and it happens on the emergency path where nobody is going
to re-read a form.

So the acknowledgement is phrased about **the Arm ceasing to serve**, not about
rollback, and it is taken once at admission. Rollback, kill switch and an
allocation change that withdraws an Arm all produce it, and the one a user is
most likely to reach in a hurry is the one they would otherwise not have been
shown.

To be explicit about who is being asked: this is the **Mendel user**
acknowledging what a visitor will experience. Visitors are not told they are in
an experiment and are asked for nothing. The word "consent" does not belong
anywhere near this — it names a different thing, owed to a different person.

**O13 — How does Mendel identify a deployable unit in an arbitrary repo? —
resolved, and D27 relaxed with it.**

A **deployable unit** is the smallest thing that can be deployed on its own. It
is defined by the deployment, not by the architecture: a serverless function is
a deployable unit and is very small; an everything-binary monolith is one
deployable unit and is very large. A repo of Dockerfiles has several; a monorepo
with one deployed entrypoint has one.

Counting them was the wrong test, and wrong in both directions — see §3.1. A
Variation may change **any number** of deployable units, provided each can
extract the Assignment Unit key (D28 applied per unit) and no changed unit is
reached from another changed unit by an internal call.

The second half is the one worth being explicit about, because extraction alone
reads like enough and is not. Extraction makes an Arm *knowable*; it does not
make it *reachable*. The edge cannot see an internal call, so Arm b's A calling
B still lands on mainline B however well B can read the key. Until east-west
routing exists (§3.2), multi-unit Variations are admissible exactly when the
changed units are entered independently from the edge — which is a real and
common shape, a frontend and a browser-called API being the obvious one.

This removes the need to infer a boundary from directory layout: what Mendel
must establish is extraction per unit and the absence of a call edge between
changed units.

**A note on the word.** "Service" is one of
the most overloaded words in the vocabulary — it means a process, a bounded
context, a Kubernetes `Service`, a SaaS product, and a team, often in the same
paragraph. Where a more specific word exists, use it: **deployable unit** for
the thing being deployed, `Service` in backticks for the Kubernetes resource,
**application** for what the user is building. Ambiguity in this area costs
correctness, since a Kubernetes `Service` and a deployable unit are neither the
same thing nor reliably one-to-one.

It survives in this document in exactly three places, all of them earned: twice
in "service mesh", which is the name of the thing, and once inside a quotation
from §13 that is reproduced verbatim.

**O12 — One HTTPRoute per experiment, or one per project?** Two experiments on
different paths of one application both want to attach to the same parent
Gateway and hostname. Gateway API merges routes across resources, but the
precedence rules are worth pinning down before relying on them.
