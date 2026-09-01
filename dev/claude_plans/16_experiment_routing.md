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

## 2. Who installs the gateway

**Mendel requires a Gateway API implementation; it does not install one.**

This is the question §13 left open, and it is worth being explicit about why the
answer is "require" rather than "install". Installing Envoy Gateway or Istio
means cluster-scoped CRDs, admission webhooks, and an upgrade lifecycle that
outlives any experiment. Mendel would be taking ownership of a component whose
failures look nothing like Mendel's, in a cluster it shares with whatever else
the user runs. Deploying an application into a namespace is a tenant's action.
Installing a gateway controller is an administrator's, and Mendel is not the
administrator.

So it becomes a validated prerequisite, in the same shape as credentials:

- **Channel validation extends.** Today validating `kubernetes`/`gke` deploys a
  hello-world and tears it down. It also checks for a usable `GatewayClass`, and
  — separately — whether experiments are possible at all.
- **Two levels of validated.** A channel can be valid for deploys and not for
  experiments. That distinction has to exist in the data model, or a user with a
  working deploy channel will be told experiments are available and discover
  otherwise at the worst moment.
- **The refusal names what to install**, the way the GKE setup script names what
  to run. Envoy Gateway is a helm install; the message should say so and link it,
  not say "no GatewayClass found".

**Consequence worth stating plainly:** this is the second large prerequisite,
after Kubernetes itself. A user wanting live experiments needs a cluster *and* a
gateway controller in it. §13's §4.2 says the k8s requirement must be raised
early and unprompted; this belongs in the same breath.

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

The assigner is a Mendel-built service, and the important property is that **it
is not a proxy**. It receives a request with no assignment cookie, picks an Arm
by weight, sets `Set-Cookie: mendel_arm=<arm>`, and returns `302` to the same
URL. It never forwards a body, never streams, never holds an upstream
connection. It is a few dozen lines with no data-plane responsibilities, which
is what keeps §6.1's decision intact.

Every subsequent request from that visitor carries the cookie and is routed by
header matching, which *is* core Gateway API — so the portability claim holds on
every conformant implementation, with no Envoy-specific extension anywhere.

### 3.1 What this costs

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

### 3.2 Where the weights live

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
3. **Gateway validation** (§2): extend channel validation, add the
   experiment-capable distinction, write the refusal.
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
| D22 | Require a Gateway API implementation; do not install one | Mendel installs Envoy Gateway | Cluster-scoped CRDs and an upgrade lifecycle are an administrator's to own |
| D23 | Assign once by 302 + cookie, then match on the cookie | Envoy Lua/WASM filter | Header matching is core Gateway API; a Lua filter undoes the portability it claims |
| D24 | Weights in a ConfigMap the assigner reads | Assigner queries Mendel per request | Production traffic must not depend on Mendel being up |
| D25 | Weights not in `backendRefs` | Weighted backendRefs | They pick per request, splitting one visitor across Arms |
| D26 | Kill switch removes the match rules | Set allocation to 100% mainline | Allocation only affects new visitors; the already-assigned keep their cookie |

## 10. Open questions

**O10 — Does the assigner see enough to assign well?** It sees a cookie-less
request and nothing else. Excluding a cohort — logged-out users, a region, a
plan tier — needs an attribute it does not have. Either the app supplies one on
the request, or targeting is limited to "everyone" in the first cut.

**O11 — What happens to a visitor whose Arm is withdrawn mid-session?** Their
cookie names an Arm that no longer routes, so they fall through to mainline,
which is correct. But if the Arm wrote per-Assignment-Unit state they will see
some of it and not the rest, which is exactly the §13 §5 dissonance the user
approved in the abstract. Worth checking that the approval text covers it.

**O12 — One HTTPRoute per experiment, or one per project?** Two experiments on
different paths of one application both want to attach to the same parent
Gateway and hostname. Gateway API merges routes across resources, but the
precedence rules are worth pinning down before relying on them.
