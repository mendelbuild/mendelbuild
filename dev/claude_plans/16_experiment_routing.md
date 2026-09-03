# Experiment Routing — Implementation Plan

Status: **draft, amended.** No code written. §§3, 4, 6, 8 were amended after
review to add the assignment mechanisms in §3.6 and §3.7; D45–D49 record what
changed and D23 is narrowed rather than withdrawn. Subsection numbers are
unchanged, because [17_functional_area_matrix.md](17_functional_area_matrix.md)
cites several of them.

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

**The resolution: compute the Arm wherever the identity already is, and leave
the edge doing nothing but matching** (D45). The edge is the one place that
cannot compute, so the answer is never to make it — not by extending it with
Lua, and not by putting a service in front of it unless there is no alternative.

Where the identity already is depends on what an Assignment Unit is, so there
are three mechanisms behind the one seam §13 §6.3 established, chosen by the
declared unit rather than configured:

| Assignment Unit | Where the Arm is computed | What the edge matches |
|---|---|---|
| `request` | Nowhere; every request is independent | Nothing — weighted `backendRefs` (§3.7) |
| `device` | A Mendel-built assigner at the edge | The `mendel_arm` cookie it set (below) |
| `user`, `session`, `tenant` | The application, which knows who this is | A bucket the client carries (§3.6) |

The rest of this section is the `device` path, which is the one §13 D20 names
for Tier 1 and the only one that works for a visitor nobody has identified yet.
It is unchanged. The other two are §3.6 and §3.7, and between them they carry
every experiment where the participant is a person rather than a browser.

The `device` path, then:

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

**So a Variation may change exactly one deployable unit**, where a deployable
unit is what O13 defines and, for live traffic specifically, must also be a
single pod that can be deployed in isolation *and* sits behind the Gateway,
where an `HTTPRoute` can route to it. Something not behind the Gateway cannot be
routed per-Arm at all.

There is a case this rejects that is genuinely safe, and it is written down here
rather than argued each time it comes up. Two units entered *independently from
the edge* — a frontend and an API the browser calls directly — are each routed
by the same visitor, with no call edge between them, so no east-west routing is
needed and the mixing above cannot happen. Admitting it would need two things
that do not exist yet:

- **Cookie scope across hosts.** The assigner sets `mendel_arm` on the host it
  was reached at. A second unit on another subdomain never receives it unless
  the cookie is deliberately scoped to the parent domain, which is a decision
  with its own blast radius and has not been taken.
- **A 302 is wrong for a non-navigational request.** Assignment works by
  redirecting a cookie-less request to the same URL. That is fine for a page
  load and wrong for an XHR or a POST with a body, which is exactly how a
  browser-called API is first reached.

Both are tractable and neither is free, so the first cut stays at one unit. This
is expected to bite: it is a real narrowing for anyone whose application is more
than one pod, and it will need revisiting rather than defending.

For a monolith this is no narrowing whatever: it is one deployable unit, so
there is nothing to mix.

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

### 3.4 What the `device` path costs

Every item here is a cost of minting identity at the edge. §3.6 pays none of
them, which is most of the argument for it.

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

### 3.5 Where the weights live on the `device` path

In a `ConfigMap` the assigner reads, written by Mendel — **not** fetched from
Mendel's API at request time. The user's production traffic must not depend on
Mendel being reachable. If Mendel is down, the last-written allocation stands.

The weights are deliberately *not* in the HTTPRoute's `backendRefs`. Weighted
backendRefs pick per request, which would split a visitor across Arms on every
navigation — the opposite of what an Assignment Unit means (§13 §5.1).

That reasoning holds wherever assignment is sticky, which is everywhere except
`assignment_unit: request` — where splitting per request is not a hazard but the
definition. §3.7 takes that carve-out. And on the §3.6 path there is no
ConfigMap at all, because the allocation is expressible in the match rules
themselves.

### 3.6 Assignment where the information already is

The `device` path exists because a first-time visitor carries nothing Mendel can
route on, so Mendel has to mint something. That is the right answer for exactly
that case and the wrong one for every other, and §13 §14 already concedes the
cost in the course of accepting it: the effective Assignment Unit becomes **the
browser, not the person**, and `user` and `tenant` "require reading a key the
application supplies, which is what drags in the login-transition problem."

This is that reading, and the login-transition problem turns out to dissolve
rather than need solving.

**The application mints a bucket** (D47). Codegen adds middleware that computes
`hash(assignment key, salt) mod 100` and sets it as a cookie on the response;
the client sends it back on every subsequent request; the edge matches the value
exactly and routes. The salt is per experiment, injected as an environment
variable by the same machinery that already injects requirement secrets and the
OTLP endpoint.

The constraint that shapes this is easy to miss: **the value has to be on the
inbound request**, because the edge decides routing before anything is reached.
So the application cannot compute the bucket for the request being routed — it
computes it for the *next* one. That is not a limitation, it is the mechanism:
the value is minted once on whatever request first knows who the visitor is, and
is simply present thereafter.

### How the client comes to know its bucket

Worth setting out plainly, because "the client carries a value" invites the
question of how it got it and what happens when it has not.

**The middleware sets it on any response where the key is known and the cookie
is absent or does not match what it should be.** Not specifically at login —
login is merely the usual first such response. Stating it as a condition rather
than an event means there is no code path that has to remember to do it, and no
enrolment step to get wrong.

**Losing the cookie costs nothing, because it is a cache and not the source of
truth.** The value is `hash(key, salt)`, which the application can recompute at
any moment from data it already has. A visitor who clears their cookies is
handed the same bucket back on their next authenticated response. A visitor on a
second device gets that same bucket without ever having had it on the first.
This is the property the `device` path cannot have at all: an edge-minted
assignment is a random draw pinned to one browser, so clearing it re-draws, and
a laptop and a phone are two participants (§13 §14 says so in as many words).
Here the browser stores a value; the identity determines it.

**A first authenticated request enrols one request late.** The login `POST`
itself, and anything before the cookie exists, are served mainline. For Tier 2
that is exactly right — durable writes happen after enrolment — and for metrics
the participant counts from enrolment. It is a request, not a session.

**A client that does not handle cookies for itself needs the client change D31
already describes.** A browser does this without being asked. A native mobile
client does not: it has to store what the response gave it and send it back,
which is a few lines in a client Mendel also wrote, and is the same remedy D31
names for the same reason. Where that has not been done, requests arrive without
a bucket and are served mainline — the safe direction, and observable rather
than silent.

**And it is still a cookie.** What §3.4 stops paying for is not cookies; it is
*minting identity at the edge* — the redirect, the extra round trip, the service
in the request path, and the fact that what gets minted is a fresh random draw
rather than a function of who the visitor actually is. The cookie itself was
never the expensive part.

Which is also what dissolves the login transition. Before a visitor is
identified there is no bucket, no match, and they are served mainline. At login
the application knows the key, mints the bucket, and every request after that is
routed. There is no ambiguous window where two identities disagree, because the
value only ever comes into existence once there is one identity to compute it
from. A visitor who saw mainline and then an Arm saw exactly what an experiment
enrolling on login should show them.

**What it deletes.** The assigner `Deployment` and `Service`, the allocation
`ConfigMap`, the extra round trip, the `?_ma=1` loop-breaker, and the "a
first-ever POST cannot be redirected" caveat — all of §3.4 and the middle rows
of §4's table. What replaces them is a hash and a `Set-Cookie` in the
application's own middleware, which is the same trade §13 §10.1 made for
metrics and for the same reason: **Mendel writes the app's code**, so a few
lines there are nearly free, and what lands in the repository is a hash, a
cookie and an env var with no Mendel import anywhere in it.

**What it gains beyond deleting things.**

- **A salt, which the `device` path has nowhere to put.** Assignment is meant to
  be a function of the key, the allocation *and* a salt, so that successive
  experiments do not draw the same cohort every time. A per-experiment salt
  makes each experiment's cohort independent of the last, which is a validity
  property §13 §8 cares about a great deal and which nothing else here provides.
- **Uniformity by construction.** A hash is uniform whatever it is fed —
  sequential integer ids, tenant slugs, email addresses. See below for why that
  matters.
- ~~**Core conformance.**~~ **Withdrawn.** This was claimed on the grounds that
  matching an enumerated bucket value is an `Exact` header match. It is not, and
  O23 is why: the bucket arrives inside the `Cookie` header alongside every
  other cookie the visitor holds, and Gateway API's `Exact` match compares whole
  header values rather than finding one entry in a list. So the bucket needs a
  `RegularExpression` match exactly as the Arm cookie does, and this path stands
  or falls with O23 on the same terms as the `device` path.

  Gateway API v1 has no cookie matcher at all — an `HTTPRouteMatch` offers
  `path`, `headers`, `queryParams` and `method`, and the header match types are
  `Exact` and `RegularExpression` — which is why a cookie is reached through the
  `Cookie` header in the first place, and why `Exact` could only isolate a bucket
  in an application whose visitors hold exactly one cookie.

  It could only have been otherwise by carrying the bucket in a header of its
  own, and that is unavailable where it matters most: **a browser will not send
  a custom header on a top-level navigation**, so a page load — the launch
  surface — cannot carry one however the application is written. An SPA's
  `fetch` can, and a native client Mendel writes can (D31), which is worth
  remembering if O23 comes back badly. It is not a general answer, and cookies
  are unavoidable for page loads rather than merely convenient.
- **D28 gets simpler.** That decision propagates the key and recomputes the Arm
  at each hop, to avoid a lost context producing a *wrong* Arm. A bucket has the
  same safety property with nothing to recompute: it is stable, it is
  deterministic, and where it is absent the hop serves mainline.

**One bucket per running experiment** (D48), carried in its own cookie named for
the experiment, because the salt differs per experiment and a single scalar
cannot answer for two of them. The salt is fixed for the life of an experiment;
rotating it mid-flight would re-bucket everyone at once and destroy the
stickiness the whole arrangement exists to provide.

**The mechanism this rejects, and why it is worth naming.** The obvious cheaper
version is to skip the application entirely and match an existing field —
prefix-match a user id header, say, so that `^[0-3]` is a quarter of traffic.
It needs no app change at all, and it fails on three counts. It has no salt to
turn, so the same tenth of the population is the guinea pig in every experiment
forever. It assumes the field is uniformly distributed, which a UUIDv4 is and a
sequential id emphatically is not, so it would need a new admission check
establishing uniformity before it could allocate anything. And prefix-matching
needs a regular-expression match, which Gateway API supports as
implementation-specific rather than Core. Since experiments only run where
Mendel deploys production (D21), and Mendel wrote that application, "needs no
app change" is worth much less here than it would be elsewhere.

The one case where direct mapping is exact, needs no regex and no uniformity
argument is a **tenant subdomain** with few enough tenants to enumerate in the
route. That is deferred rather than rejected; it is a narrow special case of an
`AssignmentKeySubdomain` declaration the domain model already has.

**And a correction to something this document implied.** The `device` path was
described as needing a domain, on the grounds that a cookie is scoped to a host.
A host-only cookie on a bare address works, so it does not — the requirement
comes from somewhere else entirely. Where the edge has to *validate* what it
routes on, as D29 requires for an identity header, it is reading a credential in
flight, which needs TLS, which needs a certificate, which needs a domain nobody
issues for a bare address. Where the routed value is a **bucket**, there is
nothing to validate: spoofing it selects a cohort and nothing else, which is the
same line D31 already draws for client-supplied tokens. So *this* requirement
attaches to edge-validated identity, and to no other mechanism here.

**A hostname is still required, for an unrelated reason, and the two were being
conflated.** §2.1's Gateway is one per namespace — `gatewayName = "mendel"` —
and the hostname is how one deployment's traffic is told from another's on it.
`k8sManifestFor` returns after the Deployment and Service when there is no
hostname, emitting no `HTTPRoute` at all, and `ExperimentDeployment.Validate`
refuses outright: *"an experiment needs a hostname: the routes are matched on
it."* An experiment without one has nothing to attach Arm matching to,
whatever it would have matched on.

That is a property of Mendel's deployment model rather than of assignment, and
lifting it means revisiting the shared Gateway — one address and one certificate
serving every deployment, which is what a single wildcard record and a single
reserved address require. Not a change this amendment intends.

So there are two requirements where this document previously described one. The
cookie does not need a domain and the TLS requirement attaches only to
edge-validated identity, both of which stand. And a hostname is needed
regardless, because the Gateway is shared. Keeping them apart matters: the first
is a constraint on what a mechanism may route on, the second is a constraint on
Mendel's own topology, and only the second is what a user has to go and satisfy.

Nor is a spoofable bucket a new exposure. The `mendel_arm` cookie D23 sets is
equally editable by whoever holds it, so a visitor determined to pick their Arm
could already do so; what either of them buys an attacker is a seat in a cohort
of their choosing, for themselves and their own devices, which moves no
comparison anybody would notice. D29 is untouched by this and remains about the
internal identity header of §3.2, where the value being carried is an identity
and consequences follow from it.

### 3.7 `request` assignment routes by weight, and needs none of this

`assignment_unit: request` means every request is independent, so there is
nothing to keep sticky and nothing to remember between requests. Weighted
`backendRefs` do exactly that and are Core Gateway API (D46).

D25 rejected weighted backendRefs because they pick per request, splitting one
visitor across Arms on every navigation. That is a fatal objection for every
sticky unit and not an objection at all here: splitting per request is not a
hazard of this unit, it is its definition. §13 §5.1 already establishes that
`request` admits no per-Assignment-Unit durable writes, which is the thing
stickiness was protecting.

So the cheapest experiment Mendel can run needs no assigner, no cookie, no
header, and no application change — only an `HTTPRoute` with weights on it.

---

## 4. What Mendel emits

Per experiment, in `mendel-apps`:

| Resource | Per | Notes |
|---|---|---|
| `Deployment` | Arm | The Variation's image, `PORT` from `hosting.ContainerPort` |
| `Service` | Arm | ClusterIP, selecting that Arm's pods |
| `Gateway` | project | Provisions the experiment's own Envoy (§2.1) |
| `Deployment` + `Service` | experiment | The assigner — **`device` path only** |
| `ConfigMap` | experiment | Allocation the assigner reads — **`device` path only** |
| `HTTPRoute` | experiment | Match rules per Arm; what is matched depends on the path |
| `Secret` | Arm | The Arm's own datastore credential (§5) |
| `NetworkPolicy` | Arm | Egress restriction (§5) |

The two rows marked `device` path only are absent from every other experiment,
which is most of them: §3.6 needs neither, and §3.7 needs no routing state
beyond the weights on the `backendRefs` themselves.

What the `HTTPRoute` matches, per path:

| Path | Match rules | Where the allocation lives |
|---|---|---|
| `request` | none; weighted `backendRefs` | the weights, in the route |
| `device` | `mendel_arm=<slug>` per Arm, fallback to the assigner | the assigner's `ConfigMap` |
| bucket | the bucket values allocated to each Arm, matched exactly | **the match rules themselves** |

The bucket path putting the allocation in the route rather than in a ConfigMap
still satisfies D24 — production traffic depends on the cluster, not on Mendel
being reachable — and is one fewer object to keep in step with the route beside it.

Mainline keeps the Deployment and Service it already has; it becomes one more
`backendRef`, matched by `mendel_arm=0` on the `device` path and by the
unallocated bucket values on the bucket path.

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

**Reallocation** rewrites the ConfigMap on the `device` path. Existing visitors
keep their Arm, since their cookie decides; only newly-assigned visitors see the
new weights. That is the correct behaviour for an experiment in flight and needs
no explanation to anyone who has run one.

On the bucket path there is no ConfigMap and the allocation is the match rules,
so reallocation is editing the route — and it comes with a constraint the
`device` path does not need (D49). **An Arm may be grown from unallocated
buckets, or withdrawn to mainline, and a bucket may never move from one Arm to
another.** Moving one switches everybody in it mid-experiment, which is the one
thing sticky assignment exists to prevent; it would also mean a participant
whose durable writes were made under Tier 2 rules for Arm a suddenly being
served Arm b, which is the interleaving §13 §3 admits Tier 2 on the strength of
avoiding.

Withdrawal to mainline is safe for the reason O11 already gives: a visitor whose
Arm stops serving falls through to mainline, which is correct, and is what the
withdrawal acknowledgement is taken about.

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
4. **Assignment**, cheapest path first, since each is independently useful and
   the order is also increasing risk. Weighted `backendRefs` for `request`
   (§3.7) need nothing but route generation. The bucket path (§3.6) needs the
   middleware codegen emits, the salt injection, and the bucket-to-Arm match
   rules — all testable as pure functions: given a key, a salt and an
   allocation, which Arm. **The assigner** (§3, §3.5) comes last of the three:
   it is a service to build, deploy and keep alive, and it serves only the
   `device` path.
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
| D23 | Assign once by 302 + cookie, then match on the cookie | Envoy Lua/WASM filter | Header matching is core Gateway API; a Lua filter undoes the portability it claims. **Narrowed by D45 to `assignment_unit: device`** |
| D24 | Weights in a ConfigMap the assigner reads | Assigner queries Mendel per request | Production traffic must not depend on Mendel being up. **Applies where there is an assigner; §3.6 keeps the property with no ConfigMap** |
| D25 | Weights not in `backendRefs` | Weighted backendRefs | They pick per request, splitting one visitor across Arms. **Holds for every sticky unit; D46 takes the `request` carve-out** |
| D26 | Kill switch removes the match rules | Set allocation to 100% mainline | Allocation only affects new visitors; the already-assigned keep their cookie |
| D27 | A Variation may change exactly one deployable unit, which must be a single isolated pod behind the Gateway | Admit several that can each extract the key | Extraction makes an Arm knowable, not reachable: the edge cannot see an internal call, so Arm b's A still reaches mainline B. Edge-entered units avoid that but need cookie scoping and non-302 assignment, neither of which exists (§3.1) |
| D28 | Propagate the Assignment Unit key and recompute the Arm at each hop | Propagate the Arm as trace baggage | Identity is already in flight and needs no correlation; a lost key means mainline, a lost context means a wrong Arm |
| D29 | The edge overwrites the identity header from the validated session | Trust what the client sent | Otherwise participants can select their own Arm |
| D30 | Require the Assignment Unit key to be edge-extractable | Analyse whether extraction precedes divergent code | Reachability in an arbitrary codebase is not decidable from a diff; the structural rule needs no analysis |
| D31 | A decline names the client change that would lift it | Refuse and stop | Mendel writes the client, so the remedy is often a Hop it can propose — and for native clients it is the only mechanism |

### 9.1 Added by the amendment

Numbered from D45 because D32–D44 are taken by
[17_functional_area_matrix.md](17_functional_area_matrix.md), which was written
between this plan and its amendment. Decision numbers are global across these
documents and are never reused, so an amendment takes the next free ones rather
than reopening the range this plan started with.

| # | Decision | Rejected alternative | Why |
|---|---|---|---|
| D45 | Compute the Arm where the identity already is; the edge only matches. Three mechanisms behind one seam, chosen by the declared Assignment Unit | One mechanism for every unit — the edge-minted cookie | It makes the effective unit the browser for *every* experiment, which §13 §14 accepts only for Tier 1 and calls out as a cost |
| D46 | `assignment_unit: request` routes by weighted `backendRefs` | An assigner for it too | Splitting per request is not a hazard of this unit, it is its definition; and §13 §5.1 already bars the durable writes stickiness was protecting |
| D47 | For `user`, `session` and `tenant`, the application mints a bucket from the key and a per-experiment salt Mendel injects; the edge matches it | Prefix-match an existing identity field | No salt to turn, so every experiment draws the same cohort, and it assumes a uniformity a sequential id does not have. A conformance argument was also offered here and is withdrawn — both carry a cookie and both need the regex match O23 is about |
| D48 | One bucket per running experiment, in its own cookie; the salt is fixed for that experiment's life | One project-wide bucket reused across experiments | A single scalar cannot answer for two salts, and rotating mid-flight re-buckets everyone at once |
| D49 | On the bucket path an Arm grows from unallocated buckets or is withdrawn to mainline; a bucket never moves between Arms | Reassign buckets freely when the allocation changes | Moving one switches everybody in it mid-experiment, and hands a participant with Tier 2 writes under Arm a to Arm b |

## 10. Open questions

**O10 — Does the assigner see enough to assign well? — largely resolved by
§3.6.** The assigner sees a cookie-less request and nothing else, so excluding a
cohort — logged-out users, a region, a plan tier — needs an attribute it does
not have.

On the bucket path the question does not arise: the application computes the
bucket, so it can decline to compute one, and a visitor with no bucket is served
mainline. Targeting becomes an ordinary predicate in code that already holds
every attribute anyone would target on, rather than an attribute someone has to
get onto the request for the edge's benefit.

What remains open is the `device` path, where targeting is still limited to
"everyone" — which is the right first cut for presentation experiments on
anonymous traffic, since there is by construction nothing known to target on.

**O11 — What happens to a visitor whose Arm is withdrawn mid-session? —
resolved.** Their cookie names an Arm that no longer routes, so they fall
through to mainline, which is correct. If the Arm wrote per-Assignment-Unit
state they then see some of it and not the rest.

This needs no new mechanism, but it does change the wording of one that exists.
§13 §5 item 5 has the Mendel user type a short phrase
character-for-character over a description written **about rollback**. D26
reaches the same state sooner: the kill switch works by removing the match
rules, so everyone already assigned falls through immediately — no rollback, no
migration reversed, and it happens on the emergency path where nobody is going
to re-read a form.

So the description is written about **the Arm ceasing to serve**, not about
rollback, and the acknowledgement is taken once at admission. Rollback, kill switch and an
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

For live traffic the unit must additionally be a single pod deployable in
isolation that sits behind the Gateway, since a pod no `HTTPRoute` can reach
cannot be routed per-Arm at all.

The count stays at one, and §3.1 records why extraction is not the lifting
condition it looks like: it makes an Arm *knowable*, not *reachable*. The edge
cannot see an internal call, so Arm b's A still lands on mainline B however well
B reads the key. The genuinely safe case — units entered independently from the
edge — is deferred on cookie scoping and on assignment-by-302 being wrong for
XHR, not on anything about counting.

What this definition does settle is that the boundary is not inferred from
directory layout. It is a property of the deployment, which Mendel performs.

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

**O23 — Does GKE honour a RegularExpression header match at runtime?**
Renumbered from O14, which
[17_functional_area_matrix.md](17_functional_area_matrix.md) had already taken;
the two documents were written in parallel and the numbering is global.

The Arm
cookie is matched with a regex on the `Cookie` header, because that header
carries every cookie a visitor has and Gateway API's Exact match cannot find one
value inside a list. `RegularExpression` is an *Extended* feature in the spec,
which implementations may support or not.

A server-side dry run against a real GKE cluster (Gateway API v1.5.0 CRDs)
accepts the HTTPRoute, and the GatewayClass advertises no `supportedFeatures`
list, so acceptance is all that is established. That is exactly the shape of the
two failures this area has already had: `ingressClassName: gce` named a class
nothing provided, and `networking.gke.io/certmap` on an Ingress was ignored
outright -- both accepted, both silent.

It is load-bearing. If regex header matching is not honoured, cookie-based
matching does not work, and the alternatives all give up something the design
chose deliberately: a hostname per Arm changes the URL the visitor sees and the
scope of their cookies, and an Envoy filter undoes the portability §6.2 claims.

Cheap to settle, and worth settling before more is built on it: deploy an
experiment on a throwaway name under the demo wildcard, which already resolves
and carries no real traffic, and observe whether a request with a given Arm
cookie reaches that Arm.

It applies to **every** cookie-carried path, not only the `device` one. §3.6's
bucket travels in a cookie for the same reason the Arm does — a browser sends
cookies unasked and sends nothing else — so it is found inside the same
`Cookie` header by the same kind of match. One experiment settles both. Only
§3.7 is unaffected, since weighted `backendRefs` match nothing at all.

**O12 — One HTTPRoute per experiment, or one per project?** Two experiments on
different paths of one application both want to attach to the same parent
Gateway and hostname. Gateway API merges routes across resources, but the
precedence rules are worth pinning down before relying on them.

The amendment raises the stakes: a bucket-path experiment carries one match rule
per allocated bucket rather than one per Arm, so a route holds tens of rules
instead of a handful. That is still small in absolute terms, and it is generated
rather than written, but it makes "one route per experiment" the obvious answer
and makes any per-implementation limit on rules per route worth checking rather
than assuming.

**O22 — How many concurrent experiments can one application carry?** D48 gives
each running experiment its own bucket cookie, so the application sets one per
experiment and the deployment carries one salt per experiment. Two or three is
plainly fine and twenty is plainly not; where the line falls, and whether Mendel
should cap it or merely report it, is not settled. It interacts with §13 §7's
concurrency work rather than being independent of it.
