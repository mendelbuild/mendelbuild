# Making a Live Experiment Legible

Companion to [18_live_traffic_experiments_built.md](18_live_traffic_experiments_built.md),
which ends with a list of what was not built. This closes the first three items
on that list.

The machinery worked and the product said almost nothing about it. An entire
debugging session was carried out with `kubectl` and `gcloud` because Mendel
could not answer questions about its own experiments: which arms exist, what
they were built from, which one a visitor is on, whether traffic is being split
at all. Every answer existed somewhere in a cluster. None of it was in the
product.

---

## 1. What was built

| | |
|---|---|
| **Arms get an environment** | `ArmDeployment.EnvFrom`, per Arm, from the union of production's requirements and the Variation's own |
| **A condition, not a fourth check** | `experiment.arms-get-the-same-environment` in the matrix |
| **Build provenance** | Migration 049: `source_commit`, `image`, `built_at` |
| **Freshness** | `domain.DescribeArmBuild`, four states, against `git ls-remote` |
| **Rebase, rebuild, roll out** | Per Arm and for every Arm, never unprompted |
| **Arm identity on the response** | `X-Mendel-Arm` and `X-Mendel-Experiment`, set at the gateway |
| **Sampling the split** | A button, and the command it replaces, shown |
| **The experiment as data** | `/p/{id}/experiments/{id}/live.json` |
| **Where it is operated from** | The Hop's page, not the settings tab |

---

## 2. Arms had no environment, and that is not a comparison

`ExperimentDeployment.EnvFrom` was a field nothing ever assigned. Both arm pods
logged `Google OAuth: NOT configured` while mainline beside them had the secrets
the ordinary deploy injected.

Three things follow from that, and only the first is obvious.

**The arms differed from the control in a way nobody chose.** Sign-in did not
work on them. Any result would have measured the missing configuration rather
than the change under test — and it would have looked like a result, not like a
fault, which is the worse failure.

**It blocked per-user assignment (D47) entirely**, since that needs somebody to
be able to log in.

**And it revealed that experiments were an ungated deploy path.** The demo and
production paths both refuse to run when a required value is not stored;
`StartExperiment` injected the same values and refused nothing. Per
[17](17_functional_area_matrix.md), anything that gates is a Functional Area
Condition rather than a check placed wherever the omission was noticed, so it is
now a column — `experiment.arms-get-the-same-environment` — and the generated
matrix in that document picked the row up on the next test run.

Two details worth keeping:

- **`EnvFrom` is per Arm, not per experiment.** A Variation declares its own
  requirements, so one Arm's environment is genuinely not another's. Each Arm
  gets the union of what production needs and what its own Variation declared:
  it runs mainline's code plus a change, so it needs everything mainline needs.
- **The values never enter the rendered manifest.** `Manifest()` names the
  Secret; `applyEnvSecret` applies it separately. That keeps the pure function
  printable, diffable and golden-testable, which is the whole reason it is pure.

Teardown deletes the Secrets too. It did not, at first, and a later start would
have reused a stale one.

---

## 3. Nothing recorded what an arm was built from

Code changed on both experiment branches while the first experiment ran. Whether
the running arms included it was unknowable. They did — because the experiment
had been restarted — but that is luck rather than knowledge, and a comparison
resting on somebody's memory of having pressed a button is not a comparison.

Migration 049 records the commit, the image and the time, **after the build
succeeds and never before**. A row written up front and left in place when the
build failed would have the Arm claiming to serve code that was never produced,
which is the same shape as reporting an action as the state of the world.

Staleness is then a comparison against the branch head, read with **one
`ls-remote` for the whole repository** rather than a clone per Arm, and **cached
off the render path**.

The first version got the second half wrong. It put the `ls-remote` inside
`handleHopDetail`, so every load of the page made a network call to the user's
git host — on a page that is otherwise a database read — and a slow remote made
the page slow. The comment above it said "the question is asked on every render
of the page", which was the argument for one call instead of several and should
have been the argument for none.

It is now a TTL cache with a background refresh, the same arrangement the domain
and experiment observations use, with one deliberate difference: a cold entry is
looked up in the *foreground* under a five-second timeout. Those pages poll a
status endpoint until their cache fills; the Hop page has none, so a cold entry
would report every Arm unchecked until somebody reloaded — the state a reader is
least able to act on, offered at exactly the moment they came to look. An aged
entry answers from what it has while the refresh happens behind it: a staleness
verdict a minute behind is not a category of error, and a page that waits on a
network call is. A look that fails is stored too, so a remote that is down costs
one reader the timeout rather than every reader.

**Four states, not two** (`domain.ArmFreshness`):

| | |
|---|---|
| `never-built` | No image. A draft experiment, or a start that failed early. |
| `current` | Built from the commit the branch head names. |
| `stale` | Built from something else. Visitors are not seeing the later change. |
| `unknown` | Mendel could not read the branch. |

The last is the one that matters, and it is the same distinction `Fact` exists
for. Folding "could not look" into "stale" tells somebody to rebuild an Arm that
is already current — which wastes a build and, mid-experiment, changes what its
participants see for no reason at all. Folding it into "current" is worse: it
reports an Arm up to date on the strength of never having checked.

**Mainline is never judged.** It keeps the Deployment the ordinary production
deploy made; rebuilding the control would make the comparison run against
something nobody had been running.

### 3.1 Refresh is rebase, build and roll out — and it is a button

The design question was whether to auto-rebuild. It is not auto: rebuilding an
Arm that is serving traffic changes what its participants see part-way through,
which is a decision somebody makes rather than housekeeping Mendel does. That is
the dissonance §13 §5 is about, and each refresh is recorded against the Arm, so
a result is never read without knowing the Arm changed underneath it.

The scope is both: **one Arm** when one Variation changed, and **every Arm** when
mainline moved and each is now being compared against a control it no longer
contains.

The rebase is pushed. Without that it would live only in a temporary directory,
and every subsequent staleness check would report the Arm behind the un-rebased
head it had just moved past.

Rolling out uses `set image` and then waits on `rollout status`. Not a re-apply
of the manifest: the routing, the weights and the gateway are all serving
correctly and none of them is what changed. And not `set image` alone, because
that returning success means the API server accepted the object, not that a pod
running the new code ever came up — which is §2 of doc 18 in one line.

A stopped experiment is rebased but not built. There is no Deployment to roll an
image into, and starting builds anyway, so building here would be paying for a
Cloud Build nothing could run.

---

## 4. Nothing said which arm a visitor was on

The assignment cookie was in every response for the whole first experiment and
the product never mentioned it. The first person to run one could not tell which
version he was being served and **gave each arm a different background colour**
to distinguish them, which is the clearest possible statement of the gap.

### 4.1 The header, which is the direct answer

Every response now carries `X-Mendel-Arm` and `X-Mendel-Experiment`, set by the
gateway. So:

```
curl -sI https://app.pong.mendel.build/ | grep -i x-mendel
```

At the gateway rather than in the application, because a user's repository should
need no awareness of Mendel: the routing was already rewriting these responses to
place the cookie, and two more headers cost the application nothing and work for
every Arm of every project without a line of anybody's code.

On the **fallback rule as well as the cookie-matched ones**. The fallback serves a
visitor's first request, which is exactly the one somebody sampling the split is
looking at; a header present on every request but the first is a header nobody
can rely on.

**The values are quoted, and this is not cosmetic.** Mainline's slug is `0`, and
an unquoted `0` in YAML is the integer zero. Gateway API's `value` field is a
string, so the CRD rejects the object — taking the whole route with it, and with
it every Arm match. One Arm whose name happens to look like a number would have
broken the routing for all of them. The test parses the manifest rather than
matching strings, so it sees a number as a number; removing the quoting makes it
fail.

### 4.2 Sampling the split

What was actually run to verify the first experiment:

```
for i in $(seq 12); do curl -s -i https://app.pong.mendel.build/ \
  | grep -io 'mendel_arm=[a-z0-9-]*'; done | sort | uniq -c
```

A reasonable thing to want and an unreasonable thing to require. It is also the
only check that exercises the edge gateway, the proxy, the weighted fallback and
the cookie **together** — no amount of reading Mendel's own records substitutes
for it, because every one of the six corrections in doc 18 was a case of Mendel's
records being right and the world being otherwise.

So it is a button, and the command is still shown, because a check you cannot
reproduce outside the product is a check you have to take on trust.

The result says what was **seen**, never what Mendel did. It keeps three things
apart that a single count would merge: responses naming an Arm, responses naming
none (the experiment is not in the path), and requests that did not complete. A
sample that is entirely unnamed is the clearest possible statement that the split
is not happening, and it would be invisible if unnamed responses were simply
dropped.

Arms that drew nobody are still listed, since an Arm missing from the table reads
as an Arm that does not exist. A slug belonging to no Arm of this experiment is
also shown — almost certainly a previous experiment's route that teardown left
behind, which is worth seeing rather than discarding.

It is **not persisted**. A sample is meaningful for about as long as somebody is
looking at it, and a stale one read as the current state of the split is exactly
the mistake this area exists to correct.

### 4.3 `/version`, and why this is not on it

The suggestion was to put experiment information on the `/version` JSON. That
endpoint is public, project-independent, and exists so a deploy can be confirmed;
experiments are per-project. Putting per-project state there would publish which
projects exist and what they are running to whoever asks the load balancer.

So the data lives at `/p/{projectID}/experiments/{experimentID}/live.json`,
project-scoped and behind the authentication everything else here already has.
Arm slugs are not secret — the cookie already told anyone who looked, and a
participant is generally entitled to know they are in an experiment — but the
list of a person's projects is a different thing.

---

## 5. The experiment moved to its Hop's page

Not asked for in the brief, raised in review, and it is the right shape.

The settings tab answers **"can this project run experiments at all"** — a
cluster, a domain, a datastore, none of them about any particular experiment.
Everything else is about one Hop: its Variations are the Arms, its branches are
what they were built from, and the split being sampled is the split between them.
Operating an experiment from settings meant choosing its Hop out of a dropdown
before doing anything to it, which put the controls a page away from the thing
they control.

So the settings tab keeps readiness and becomes an index. The Hop page gains the
arms, their allocation and freshness, the reach command for each, start, stop,
refresh at both scopes, the sample and the JSON link.

**At the width of the page, directly under the decision ribbon.** It was first
dropped into the sidebar column beside Cost, which is a third of the page: the
arms table was crushed to a few characters a cell and the build verdict — the
column that says whether visitors are seeing your latest change — wrapped to one
word a line. A live experiment is the widest thing on this page and the most
consequential, not an aside. The test asserts the ordering against the two-column
section rather than the rendering, since that is the property that was wrong.

One piece of copy went with it. The readiness note read "the start button appears
once it knows" on a *running* experiment, beside a stop button and a badge saying
"running" — three claims of which two were true. Readiness gates starting and
nothing else, so it is shown only where that is what is being decided.

The blockers are repeated on the Hop page **from the same conditions and as the
same strings**, per D39: if the two can differ they will, and the checklist stops
being the answer to "why can't I".

---

## 6. What this corrected on the way

**A form disappeared and the page rendered perfectly.** The per-arm rebuild
button referred to `$.Experiment` inside a nested `range`. `$` is the page's data
map, which holds `View` and `ProjectID` and no `Experiment` — so the whole form
rendered as nothing, silently, on a page that otherwise looked finished.

It was caught because the tests assert **which actions are reachable** rather than
what the page says, which is the lesson the orphaned-route and capability checks
were written for and which is recorded in memory as *reachability over UI tests*.
A test pinning the wording would have passed. This is the fourth instance of the
family those checks describe and the first one caught by a test rather than by
somebody noticing.

**A column had never been written.** `experiment_arms.deployment_name` was added
in 047 to record "what was deployed for an Arm", with a setter no caller ever
called, so every row held `''` for the table's whole life. Adding `image` beside
it in 049 made it visible: two columns answering one question, one of them always
empty. It is dropped in 050, and what an Arm's objects are called stays derived —
`experimentArmResource` computes it, and both the teardown and the rollout call
that rather than reading a column.

**Three tests moved rather than being deleted.** Start being refused until the
cluster can route, no button while an action is in flight, and a failure rendered
with its cause behind a disclosure are all still true and still worth asserting —
they are simply properties of a different page now. The first was strengthened on
the way: it drives a real `ExperimentObservation` through the real conditions
rather than setting a boolean, so it tests the wiring between them.

---

## 7. Verification

- `go test ./...` clean, including `./schema/...` against a real PostgreSQL.
- Migration 049 applies and matches `full.sql`.
- The rendered manifest parses as YAML and every header value is a string,
  including mainline's `0`. Removing the quoting fails the test.
- The Hop page renders with a running experiment and offers every route the
  experiment registers; mainline is offered no rebuild.
- Unquoting, and folding unknown freshness into stale, both fail their tests.
- The branch-head cache serves warm entries without looking, answers from an aged
  entry while refreshing behind it, and sends exactly one of thirty-two
  concurrent readers to the remote.
- The experiment panel renders at the same width as the two-column section below
  it, and above it, with no horizontal overflow.

Not verified against the live cluster. The experiment on pong is serving real
traffic and stopping it is not a convenience for testing — so the arm headers,
the environment injection and the rollout have been proved against the rendered
objects and the code paths, and not against GKE. Doc 18 §3 is six consecutive
demonstrations that this is a real distinction: **the next thing to do with this
is to run it.** A demo hostname under the existing wildcard is how §7 did that
without touching production.

---

## 8. What is still not built

From doc 18 §8, unchanged:

- **Per-user assignment (D47).** No longer blocked — arms can log a user in now —
  but not started.
- **Measurement.** OTLP ingest, contingency tables, guardrails, auto-rollback.
  An experiment runs, sticks, says which arm you are on and can be brought up to
  date; nothing yet reads the result.

And one this added:

- **Staleness is not watched.** It is computed when somebody looks at the page,
  and now cached for a minute after that.
  An experiment left running for two weeks does not notice its own arms going
  behind, and nothing tells anybody. That is deliberate for now — Mendel acting
  on staleness unprompted is the thing §3.1 declines to do — but *noticing* and
  *acting* are separable, and only the second was decided against.
