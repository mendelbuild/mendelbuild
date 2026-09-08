# Datastore Adapters — Design

Status: **draft for review.** No code written for this document. It supersedes
part of [13_live_traffic_experiments.md](13_live_traffic_experiments.md) §15 and
the provisioning work already landed under D53.

Numbered 20 rather than 18: two documents landed from other sessions while this
was being written, and the sequence is worth more than the filename.

Short, because the substance is three principles and their consequences rather
than a mechanism with many parts.

---

## 1. The problem, stated as what is wrong today

`experiment.Datastore` has one implementation. So its semantics have been
"whatever Postgres does", a second adapter would be written by reading the first,
and a project on anything else gets `ErrUnsupportedDatastore` — which is an
honest decline, and also a list of supported engines that someone has to
maintain by hand forever.

Two things follow from that arrangement that are worth naming before fixing it:

- **Mendel dials the user's database directly.** The verification datastore is a
  connection string Mendel opens a pool against. Most production databases are
  on a private address, so this asks a user either to expose one to the internet
  or to give up.
- **A datastore-specific tool ended up in Mendel's own image** so that Mendel
  could run it against a user's database. That is the assumption arriving as a
  dependency, and it was removed rather than justified.

---

## 2. Three principles, and the design falls out of them

### 2.1 Mendel's stack says nothing about the user's

The oldest principle here, and the one being applied more strictly rather than
newly. A user's project is on whatever it converged on. Postgres is *Mendel's*
database.

The strong form of this is not "put it behind an interface" — that was already
done, and it still produced a Postgres-shaped world, because the only
implementation defined what the interface meant. The strong form is that **Mendel
should not need to contain the user's datastore world at all**: no client
library, no dialect knowledge, no tool for one engine sitting in its image
waiting for a project that happens to use it.

### 2.2 Trust nothing that can be checked instead

This runs through every decision in §13 and §16. Do not trust the classifier —
enforce with grants and a NetworkPolicy. Do not trust that a migration is
additive — run it and diff the catalogue. Do not trust that a manifest is
supported — read the capability table.

Applied here: **do not trust an adapter, verify it.** `internal/experiment/conformance`
states what `Datastore` means apart from any implementation, and an adapter is
whatever passes it. That is what makes a *generated* adapter a reasonable thing
to attempt.

The distinction that makes this sound, and it is worth being precise because it
looks like a contradiction of §13 D6: an LLM asserting *this migration is safe*
is a claim about one change with nothing downstream to check it, and D6 rightly
refuses it. An adapter is not an assertion. It is code with observable
behaviour, and a suite either passes or does not. We do not trust the generated
adapter; we trust the suite, and the suite is falsifiable — mutation testing
already found a hole in it where a broken adapter passed.

### 2.3 The process boundary is the security boundary

Generated *application* code runs in the user's cluster, deployed deliberately,
reaching only what Mendel gave it. Generated *adapter* code running inside Mendel
would sit in the process holding every project's credentials, with network
access to everything Mendel can reach. A bad adapter written for one project
would execute beside another project's secrets.

No amount of conformance testing addresses that: the suite checks what an adapter
does to a database, not what code does to the process it is in.

---

## 3. The design: the adapter runs where the datastore is

**An adapter is deployed through the project's own deployment channel, and
Mendel drives it over a narrow protocol** (D54).

That is the same mechanism `runChannelDemoDeployment` already uses, into the
namespace Mendel already owns, gated by the channel validation that is already a
Functional Area Condition.

It satisfies all three principles at once, which is the reason to believe it:

| Principle | How |
|---|---|
| Mendel's stack says nothing about the user's | No client library or dialect tooling in Mendel; the adapter carries its own |
| Trust nothing checkable | The adapter is whatever passes conformance, run wherever it runs |
| The process boundary is the security boundary | Generated code never enters Mendel's process |

And it fixes something none of them predicted: **the adapter can reach the
database.** It runs where the application runs, and the application already
connects. A database on a private address stops being a case Mendel cannot serve.

### 3.1 What Mendel keeps

Everything that is a judgment: admission, the namespace rule, the archive
policy, the Mendel-side lock, the recorded shapes, the decision to decline.
`Applier` is unchanged in what it decides. What changes is that the `Datastore`
it holds is a remote one.

### 3.2 What becomes harder

`Datastore` becomes a network interface, and three things follow:

- **A failed call is ambiguous.** A transient network error is not an answer, and
  reporting it as one would report "Mendel could not tell" as "no" — the mistake
  `Fact` and `unchecked` exist to prevent. The protocol must distinguish them.
- **`VerifySpeculatively` is stateful across a call.** It holds a transaction
  open in the remote process for the duration. That is fine, and it means the
  adapter cannot be a stateless function invoked per request.
- **It costs money and has a lifecycle.** A deployment to start, drive and tear
  down, per admission. That belongs in the cost model like every experiment-day.

### 3.3 What this supersedes

D53 provisions a verification database from Mendel, dialing the user's server.
The decision itself survives — Mendel makes the sandbox rather than asking a
person for one, per experiment, on a server the project already has — but
**where it runs moves into the channel**, and `pgstore.Provisioner` is one
adapter's implementation rather than something Mendel performs.

---

## 4. What is still not verified, independent of all this

Worth recording here because this document is about not trusting things, and
these are two places Mendel currently does.

- **`Apply` writes to production unchecked.** It splits the migration, calls
  `Exec` per statement, and never re-reads the catalogue to compare what
  happened against the `Delta` admission recorded. Whatever the adapter actually
  did, Mendel would not know. The mechanism to fix it already exists — the same
  read-apply-read-compare admission uses.
- **`Load` restores rows with nothing checking where they landed.** The
  catalogue diff is about structure and says nothing about a restore that
  reinstated an archive into the wrong place.

Both predate this design and are true of the hand-written adapter.

---

## 5. Decisions

| # | Decision | Rejected alternative | Why |
|---|---|---|---|
| D54 | An adapter is deployed through the project's own channel and driven remotely | Run adapters in Mendel's process | Generated code would sit beside every project's credentials, which no conformance suite addresses; and Mendel usually cannot reach a production database anyway |
| D55 | An adapter is whatever passes the conformance suite | A maintained list of supported engines | The list is the maintenance burden, and passing a falsifiable suite is a better claim than appearing in it |
| D56 | Generation may be dynamic; no human merge step | Generate a change for review | The review step existed only to keep untrusted code out of Mendel's process, and D54 removes that reason |
| D57 | A datastore's fitness is a Functional Area Condition, `probed`, and its failure names the requirement | `ErrUnsupportedDatastore` naming the engine | "Mendel wrote an adapter and it failed the requirement that finding out what a change does leaves no trace" tells a user something; "unsupported datastore" does not |
| D58 | No datastore-specific dependency in Mendel's image | Install the tools for the engines we expect | It is an assumption wearing the shape of a dependency, and the adapter carries its own tooling wherever it runs |
| D59 | An adapter is a job that reads a JSON instruction and reports a JSON result outbound | An HTTP service Mendel calls | A service needs an inbound path to something holding database credentials, which undoes the reason the adapter is in the channel; a job needs only outbound, which the cluster already has |
| D60 | One adapter per admission | A long-lived adapter shared across experiments | Teardown becomes the absence of anything rather than an operation that can fail; revisit when there is a measured performance problem |
| D61 | Re-gate on the suite version and on the datastore the adapter reports connecting to | A maintained list of repository files whose change forces re-gating | A list is inference from the repository where observation is available every run, must be maintained, and breaks silently on a different layout |
| D62 | Rename the channel's demo path to the non-production path; the demo itself keeps its name | Rename both, or neither | The destination widened and the user-facing feature did not; they were only ever one word by accident |
| D63 | Generation failure retries with the conformance failure as the fix, bounded by the cost model | Let a user supply an adapter; retry without a bound | The failure messages name requirements, which is what a fix can act on; and an unbounded retry is an unbounded agent spend |

---

## 6. Resolved

**O26 — What is the protocol? — resolved: JSON, carried by a job rather than
served by a service** (D59).

The review asked for HTTP and JSON, and floated an alternative: rather than a
service answering method calls, an adapter invoked with instructions that runs,
leaves its output somewhere, and exits. The second is right, and for a reason
neither half of the question stated.

**A service in the user's cluster would need an inbound path.** Mendel would
have to reach it, which means a LoadBalancer or an Ingress — exposing a process
holding database credentials to the internet. That is the reachability problem
of §1 in reverse, and it undoes the reason for putting the adapter in the
channel at all: if a production database is not reachable from Mendel, neither
should the thing holding its credentials be.

A job needs only **outbound** connectivity, which the cluster already has and
which every other thing Mendel deploys already uses. It is also the shape of
everything else here — a deploy is a job, a test run is `up`, `exec`, `down` —
rather than a new operational kind.

The two halves of the question are not opposed, because HTTP was the transport
and JSON the format. Keep the format: an adapter reads a JSON instruction and
writes a JSON result, which stays inspectable, diffable and pasteable into a bug
report. Drop the transport.

Where the result goes has a precedent to copy rather than invent: §13 §10.2 has
deployed code POST to Mendel with a per-deployment bearer token, identity
resolved server-side from the token and never from the payload (D10). An
adapter reports its results the same way.

One consequence to design for. A job cannot be a chatty sequence of method
calls, and admission *is* a sequence — what to shape depends on what the delta
said was added. So the unit of invocation is a phase rather than a method:
"verify this migration and return the delta **and the shapes of everything it
touched**". The adapter knows what it added, so gathering the shapes needs no
judgment; Mendel still does all the judging, on data it was handed.

**O27 — How long does an adapter live? — resolved: per admission**, until there
is a performance problem worth measuring (D60). It composes with D59, since a
job is per-invocation by nature, and it makes teardown the absence of anything
rather than an operation that can fail.

**O28 — What re-gates a cached adapter? — resolved, and it is two things.**

The review suggested a list of files in the user's repository whose modification
forces re-gating: the datastore's files matter, a stylesheet does not. The
instinct is right — most changes are irrelevant and re-gating on all of them
would be useless — but the list is inference from the repository where direct
observation is available and cheaper.

Two different things invalidate an adapter, and they are worth separating (D61):

- **The suite changed.** An adapter records which version of the conformance
  suite it passed. Hardening the suite re-gates every cached adapter rather than
  grandfathering it, which is the same discipline versioned rate cards are under
  and the reason those are never rewritten.
- **The datastore is no longer the one the adapter was written for.** Not
  observed from files: the adapter is invoked per admission anyway, so it can
  report what it actually connected to — engine and version — and a change
  regenerates. That is the same "ask, do not infer" rule as
  `SelfSubjectAccessReview` and as probing `CREATE DATABASE` by attempting it.

A file list would have to be maintained, would silently stop working when a
repository is laid out differently, and answers a question the adapter can
answer directly every time it runs.

Note what does *not* re-gate: the schema changing. An adapter is about the
engine, not the structure, and schema drift is already caught by `checkDrift`
and the shape comparison at admission.

**O29 — Which path does the adapter deploy through? — resolved: the existing
non-production path, renamed** (D62).

Reuse rather than a third path: it is validated, it deploys into the namespace
Mendel owns, and its validation is already a Functional Area Condition an
experiment can depend on.

The rename is worth doing with it, and it is narrower than it first looks. What
widens is the *deployment target* — the path is no longer only for demos, so
`IsDemoValidated` and `channel.demo-path-validated` become non-production ones.
What does not widen is the **demo** itself: a demo of a Variation is a thing a
user looks at and asks for by that name, and renaming it would churn user-facing
copy to no purpose. The noun for the destination and the noun for the feature
are different nouns, and only the first was overloaded.

**O30 — What happens when generation fails? — resolved: retry with a fix, and
users do not write adapters** (D63).

The same shape as a failed demo, which already has
`UpdateDemoInstanceWithSuggestedFix` and a retry that feeds the failure back in.
The input to the fix is the conformance failure, which is why those messages
name the requirement rather than the observation — "finding out what a change
does must leave no trace" is something to act on, where "expected 2 got 3" is
not.

Retries are bounded like any other agent run: each is an agent call and a
deployment, so the budget machinery in `internal/codegen/budget.go` applies
rather than an open-ended loop. After the bound, Mendel declines and names the
requirement it could not meet, which is D57.

---

## 6a. Still open

**O31 — What does a phase boundary look like, exactly?** D59 makes the unit of
invocation a phase rather than a method, and names one: verify-and-shape. Whether
admission is one phase or two, and whether apply and rollback are each their own,
is not settled and wants writing down before the protocol is fixed.

## 7. Build order

1. **The boundary, with an adapter that already passes.** The instruction and
   result formats, the job deploy, the outbound report, and O31's phases.
   Exercised with whatever adapter the test project's datastore needs — which
   adapter that is, is a fact about the test project, not a step here. If
   conformance passes across the boundary unchanged, the boundary is real.

   The conformance suite is what makes this checkable: it takes a
   `experiment.Datastore`, and a remote adapter is one, so the same twenty-two
   checks run against the job-backed implementation with no new assertions to
   write.

2. **Verification of the apply** (§4), which is right regardless and closes a
   gap that exists now.

3. **Generation**, targeting a boundary that already works, so a failure is
   unambiguously the generated adapter's rather than the mechanism's.

4. **The Functional Area Condition** (D57), once there is something for it to
   report.

Step 1 is the one that can fail, and failing there is cheap — the same reason
§13 §16 put migration non-interference first, and §17 put the domain ladder
first.
