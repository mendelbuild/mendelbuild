# Live-Traffic Experiments — What Building It Corrected

Companion to [13_live_traffic_experiments.md](13_live_traffic_experiments.md)
(the design) and [16_experiment_routing.md](16_experiment_routing.md) (the
routing plan). Those say what was intended. This says what happened when it was
built against a real GKE cluster, because six things the design assumed were
wrong, and every one of them failed *silently* — applied cleanly, reported
healthy, and did nothing.

Two visitors now see different code at `app.pong.mendel.build`, each staying
with the version they were given. That is the headline. The rest of this
document is the corrections, because they are the part that will not be
rediscovered from reading the code.

---

## 1. What was built

§16 §8's order of work, items 1 through 5, plus the lifecycle and the UI that
make them reachable.

| | |
|---|---|
| **Schema and domain model** | Migrations 047 and 048. Five tables from §16 §7, plus `hops.live_experiment` |
| **The `.mendel/` declaration** | `experiment.json`, parsed in `internal/codegen/experiment_declaration.go` |
| **Gateway validation and install** | Mendel installs Envoy Gateway itself, or names the one command that does |
| **Assignment** | Deleted. The gateway does it — see §3.2 |
| **Arm deployment and routing** | `ExperimentDeployment.Manifest()`, a pure function |
| **Lifecycle** | Start, stop, and the production hostname takeover |
| **Readiness** | A Functional Area with conditions, per [17](17_functional_area_matrix.md) |

Landed alongside, from the session that wrote [17](17_functional_area_matrix.md):
Mendel now provisions the verification datastore itself rather than asking for a
connection string. Nothing in this document depends on which way that went — the
migration half of §13 is still unexercised either way, because pong has no
database and its experiments change no schema.

`internal/experiment` — which could already admit a migration, prove it
additive against a real database, apply it under a lock, archive it and roll it
back — had no caller and no persistence when this started. §16 §7 said so
plainly: "a proven library with no caller". It has both now.

---

## 2. The recurring failure, stated once

Every correction below is an instance of one shape, and it is worth naming
before the list because the list is otherwise six unrelated anecdotes.

**Mendel reported that its own action completed, rather than what became of the
world.** "I applied the manifest" became *deployed*. "The last action returned"
became *the current state*. "The API server accepted this object" became *this
object does something*.

The platform half of that has its own entry in `CLAUDE.md` now — *Check What
the Platform Supports Before Building On It* — because GKE accepts
configuration it does not implement, and `kubectl apply --dry-run=server`
proves storage, not behaviour. The product half is the same mistake one layer
up, and the domain ladder already had the correction: **a record counts once it
resolves, not once it is reported**, with `Fact` keeping *unknown* distinct
from *absent*.

---

## 3. What the build corrected in the design

### 3.1 The deployed GatewayClass cannot match a cookie (O23)

The design routes each Arm by matching its cookie. GKE's
`gke-l7-global-external-managed` matches headers `Exact` only, and an `Exact`
match on a `Cookie` header cannot pick one cookie out of the several a visitor
carries. Google's [GatewayClass capabilities
table](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/gatewayclass-capabilities)
is explicit; only the multi-cluster `-mc` variants support
`RegularExpression`.

A server-side dry run against the real cluster **accepted** the regex. The CRD
schema permits the field and the controller ignores it.

**Correction: two gateways.** GKE's stays at the edge, holding the reserved
address, terminating TLS and carrying the Certificate Manager map — none of
which Envoy Gateway can be given, since it reads certificates from Kubernetes
Secrets and Certificate Manager will not export a private key. Envoy Gateway
sits behind it and does the matching. Everything already working keeps working;
the cost is one in-cluster hop.

This revived §2 of the routing plan, which had been argued away on the grounds
that GKE's controller is managed rather than installed. True, and beside the
point: the managed controller cannot do what the design needs.

### 3.2 The assigner was unnecessary, and broken

The design deployed a small service to assign visitors: receive a cookie-less
request, pick an Arm, set a cookie, redirect.

Gateway API can set a `Set-Cookie` per *weighted backend*, so a cookie-less
visitor is assigned by the same response that serves them. Verified on the real
cluster: twelve requests, twelve cookies naming the arm that served them, no
mismatches.

That deleted a Deployment, a Service, a ConfigMap, an extra round trip, a
redirect that could not carry a POST, and an image Mendel would have had to
build and publish into every user's registry — which was itself blocking a
first end-to-end run.

**It also deleted something that did not work.** `Assign("")` returned mainline,
and on the device path a first-time visitor has no key *by definition*. Every
new visitor would have gone to mainline and no experiment would ever have
enrolled anybody. Its unit tests passed because every one supplied a session
cookie — the single case the device path does not have.

### 3.3 A second route for the production host would never serve

The first implementation rendered the experiment's own `HTTPRoute` for
`app.pong.mendel.build`, alongside the one the ordinary deploy created. Gateway
API ranks matches by path specificity, then method, then header count, and
breaks the remaining tie with the **older** route — which the production one
always is.

**Correction: repoint, do not add.** The experiment patches the existing route
to point at the experiment gateway, and points it back on stop. One object, no
precedence to reason about, and a stop that restores exactly what was there.

### 3.4 A Service cannot select pods in another namespace

The repoint targeted a Service in `mendel-apps` selecting the Envoy proxy pods
by label. Envoy runs its proxies in `envoy-gateway-system`, so it could never
have endpoints.

Every signal said success: the manifest applied, `ResolvedRefs: True` because
the Service *existed*, and the site kept serving 200 because the load balancer
had not propagated. Another minute and production would have started failing.

**Correction:** the production route references Envoy's own proxy Service across
the boundary, permitted by a `ReferenceGrant`. The proxy's name is discovered
from the cluster rather than written down, because Envoy chooses it.

### 3.5 The edge gateway must be told how to check the proxy

That alone still served 503. GKE's default health check probes the backend on
its traffic port with no `Host` header, and Envoy answers 404 to a request
matching no route — correct of Envoy, fatal here. The GCP backend sat
`UNHEALTHY`.

**Correction:** a `HealthCheckPolicy` pointing at Envoy's readiness endpoint on
port 19003. GKE-specific and unavoidably so — the check belongs to the load
balancer GKE provisions, and Gateway API has no portable way to describe one.

### 3.6 A flag cannot name the namespace for a manifest that spans two

`kubectl apply -n mendel-apps` refuses an object declaring a different
namespace, so the apply died on the `ReferenceGrant` — after the arms, the
gateway and the route had all applied, leaving the experiment half-built.

**Correction:** every object names its own namespace and the flag is gone.

---

## 4. Three parsing bugs, one cause

`kubectl auth can-i` was used to predict whether Mendel could install the
controller. It produced two bugs — stderr folded into the verdict, then the
verdict read from the wrong end of `no - requires one of [...]` — and a third
appeared later when an error message was matched to tolerate a failed patch.

All three were the same mistake: **predicting an external tool's output instead
of running it and looking**. The yes path had been tested against the real tool;
the no path had not, because as an administrator `can-i` answers "yes" and never
shows what a refusal looks like.

**Correction:** `can-i` is gone. `kubectl apply --server-side --dry-run=server`
sends the real objects through the real admission chain and persists nothing;
the exit code is the answer. It also has no *model* of the operation — the
`can-i` version asked about six resources and the wrong verb, and could only ever
see RBAC, while the thing that actually blocked the install was an Autopilot
`ValidatingAdmissionPolicy` no permission question would have revealed.

The patch that failed on an error-string match now reads the object first and
emits a `remove` only when there is something to remove.

---

## 5. Schema

**047** — `experiments`, `experiment_arms`, `arm_admissions`, `arm_archives`,
`experiment_events`. Two rules the design argues for are constraints rather than
conventions: an experiment cannot leave draft without a minimum detectable
effect, a duration and a stopping rule; and exactly one mainline per experiment,
as a partial unique index.

**048** — `experiment_arms.declared_migration_{up,down}` and
`hops.live_experiment`. The migration lives on the Arm because a verdict needs
the user's datastore to reach, so it must survive the gap between code
generation writing it and admission ruling on it.

---

## 6. States

```
draft ──▶ starting ──▶ running ──▶ stopping ──▶ stopped
             │                        │
             └── (failure) ───────────┴──▶ back to draft/stopped,
                                           with an EventFailed recorded
```

`starting` and `stopping` are real states, not decoration. Both take minutes —
one builds an image per Arm, the other waits on a load balancer — and without
them a page could only show what was true before the button was pressed.
Pressing Stop and being returned to a page still offering Stop is
indistinguishable from the button not working, which is how it was read.

Ordering follows from what is reversible. Building and applying happen first and
serve nobody, so they are safely repeatable; the repoint is last and is the only
step that changes what a visitor sees. Stopping inverts it: the route goes back
before anything is deleted, because an experiment being stopped is usually being
stopped for a reason.

---

## 7. Verification

Everything below was run against the real `pong-autopilot` cluster, on a demo
hostname under the existing wildcard wherever production could have been
affected.

- **Cookie matching.** Mendel's own `cookieMatch()` output, applied to a real
  Envoy Gateway: one cookie among several routes correctly with and without
  spaces; `mendel_arm=ab` does not match arm `a`; no cookie reaches the fallback.
- **Gateway-side assignment.** Twelve requests, twelve cookies naming the arm
  that served them, zero mismatches.
- **The whole chain.** Twenty-four fresh visitors split 9/9/6 across mainline
  and two arms, weights 34/33/33.
- **Different code per arm.** Three requests to one URL differing only by
  cookie: mainline `#111`, one arm `#00008b`, the other `#3e2411`.
- **Health check.** `UNHEALTHY` until the policy, `HEALTHY` after.
- **Teardown.** Both namespaces, verified by finding two orphaned pairs that the
  earlier version had left behind.

---

## 8. What is not built

- **Per-user assignment (D47).** What exists is the device path: Mendel's cookie
  identifies a browser. Two people sharing one see the same arm; one person on
  two devices sees two. Pong has Google OAuth and a session, so it is a good
  first subject.
- **Arms get no `EnvFrom`.** Both arm pods log `Google OAuth: NOT configured`,
  so they differ from mainline in a way nobody chose and sign-in is broken on
  them. This blocks D47 directly.
- **No arm build provenance.** No commit, image or built-at column, so "this arm
  is running code from before your last change" is undetectable, and nothing
  rebuilds an arm when its Variation changes.
- **No way to see which arm you are on.** The cookie is in the response and the
  product never mentions it. The first user of this resorted to changing
  background colours to tell the arms apart, which is the clearest possible
  statement of the gap.
- **Measurement.** OTLP ingest, contingency tables, guardrails, auto-rollback —
  §13 §16's third step, untouched. An experiment runs and sticks; nothing reads
  the result.
