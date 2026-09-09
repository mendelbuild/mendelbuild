# 22 — A Deployment's Life Has an End

## Overview

`hosting_deployments` rows were opened and never closed. Nothing in the tree
moved one off `running`: `UpdateHostingDeploymentStatus` had no callers,
stopping a demo wrote to `demo_instances`, and `finished_at` was set at the
wrong moment by the one function that set it at all. Three downstream things
were built around that absence and each of them was wrong in a different way.

This closes the lifecycle. Demos and production deploys are now the same record,
that record ends when the deployment ends, the hosting meter reads the ending,
and project deletion gates on it.

## What was actually broken

The reported symptom was that every deployment accrues hosting cost forever.
That is not what the code did, and the difference is worth writing down because
it is the same root cause pointing the other way.

`CompleteHostingDeployment` set `finished_at = now()` when a deploy *succeeded*,
reading the column as "the deploy finished". `DeploymentMeterRow.BillableThrough`
reads the same column as "the deployment stopped". So:

- A production deployment was metered for its **deploy duration and nothing
  after**. An app serving for a month billed for the four minutes it took to
  push it.
- A demo was metered **not at all**, because the demo path never created a
  `hosting_deployments` row in the first place. Every demo Mendel has ever run
  is missing from the ledger.
- Nothing was ever marked `terminated`, so `SettleHostingSpend`'s inclusion of
  `terminated` rows was dead, and project deletion could not gate on a status
  nothing maintained.

One column meaning two things is the whole bug. The meter's reading is the one
that has to win, because it is the one that decides whether an app left running
goes on costing money.

## Key design decisions

### Demos are hosting deployments

Migration 029 created `hosting_deployments` "shaped so demo deploys can move
onto it (kind = 'demo' with variation_id set) and retire `demo_instances`".
Until now they did not, and the two records drifted the way parallel records do:
stopping a demo closed one and left the other open, because there was no other.

Merging them was the cheapest way to make stopping a demo close a hosting
deployment, and it paid for itself immediately elsewhere — `runCloudTeardown`
now takes any deployment, which is most of what a production teardown needed.

What `demo_instances` carried and what became of it:

| Column | Outcome |
| --- | --- |
| `suggested_fix` | Added to `hosting_deployments`. A failed production deploy can carry a proposed fix just as sensibly as a failed demo. |
| `process_info` | Dropped. Only ever held `{"work_dir": …}`, was written and never read, and is recomputable from the project and variation IDs. |
| `stopped_at` | `finished_at` already means this. |
| `status` | `starting`/`running`/`stopped`/`error` map onto `deploying`/`running`/`terminated`/`failed`. No new statuses. |

The demo's `hosting_deployments` row is opened by `openDemoDeployment` before
anything is spent, for two reasons: the demo's logs are keyed by it, and if it
were only written on success a deploy that died halfway would leave an app
running that nothing in Mendel knew about.

### `finished_at` means stopped, and only that

`CompleteHostingDeployment` no longer sets it. Deployments now end in exactly
three places, and all three set it:

- `TerminateHostingDeployment` — torn down on request, or superseded.
- `FailHostingDeployment` / `FailHostingDeploymentWithFix` — never came up.
- `TerminateSupersededDeployments` — replaced by a newer deploy of the same app.

`TerminateHostingDeployment` is idempotent by predicate (`WHERE finished_at IS
NULL`) rather than by a read-then-write: a second call describes the same ending
as the first, and the earlier instant is the true one.

### A deploy supersedes what shares its app name

A deploy lands on an app name — derived from the project for production, from
the variation for a demo — so a second deploy of the same thing goes to the same
name and the platform replaces what was there. Two open rows on one app name
means the meter bills that app twice for every hour after the redeploy.

`TerminateSupersededDeployments` is keyed on `(project_id, app_name)` rather
than on kind, because the app name is what actually collides. Nothing has to
remember which kinds share a namespace.

### A failed teardown does not end a deployment

`handleStopDemo` used to mark a demo `error` when its teardown command failed.
Under the merged record that would mean `failed`, which stops the meter — for an
app that is demonstrably still running, since the command that would have
stopped it errored.

So a failed teardown records `error_message` and changes nothing else
(`NoteHostingDeploymentError`). The deployment stays `running`, goes on being
metered, goes on blocking project deletion, and the Stop button stays on the
page. The variation page reads a note on a running demo as "this is still up"
rather than as a failure.

The migration revert moved inside the success branch for the same reason:
reverting while the deployment is still serving pulls the schema out from under
a running app.

### A restart does not stop a cloud deployment

`cleanupStaleDemos` marked every running demo stopped on startup, with a comment
about Docker containers no longer running. That was true when a demo was a
container on this host and has been true of nothing since. A demo runs in the
user's cloud; Mendel restarting says nothing about it.

`cleanupInterruptedDeploys` replaces it and touches only deploys that genuinely
cannot survive the process that was running them: status `deploying` **with no
teardown command**. A deployment is given one only once it has landed, so a
`deploying` row without one is a goroutine that died and a row nothing will ever
advance. A `deploying` row *with* one is the deliberate case from
`MarkHostingDeploymentProvisioning` — deployed correctly, waiting on a load
balancer — and it is still coming up whether or not Mendel is watching.

### Production is a gate, so production needs a way down

`ProjectDeletionBlockers` said production could not be a blocker because nothing
moved it off `running` and there was no route that took one down: "a gate nobody
can satisfy is worse than no gate at all."

Both halves are now false, but only because both were fixed. `handleTeardownProd`
is the route, `POST /p/{projectID}/deployment/teardown-prod`, and it exists
because the gate needs it — the same teardown command demos use, in the same
column, run by the same function. Only the button is new.

`ProdDeploymentStillUp` and its warning callout are gone. A gate that names what
to do and links to the page that does it replaces them.

Deployments still `deploying` block too. A deploy in flight is creating
infrastructure right now, and a project retired underneath it leaves an app
nobody can find the page for.

## New / modified files

| File | Change |
| --- | --- |
| `schema/migrations/053_demos_are_hosting_deployments.{up,down}.sql` | New. Adds `suggested_fix`, indexes `(variation_id, status)`, drops `demo_instances`. |
| `schema/full.sql` | `demo_instances` removed; `hosting_deployments` gains `suggested_fix` and the column comment that `finished_at` means stopped. |
| `internal/domain/types.go` | `DemoInstance`/`DemoInstanceStatus` deleted. `HostingDeployment` gains `SuggestedFix`, `Live`, `Failed`, `TornDown`, `Active`, `DisplayURL`, `Teardown`. |
| `internal/domain/status_view.go` | `DemoStatus` deleted; `DeploymentStatus` covers demos. |
| `internal/db/queries.go` | Demo-instance queries deleted. `CompleteHostingDeployment` stops setting `finished_at`; adds `FailHostingDeploymentWithFix`, `NoteHostingDeploymentError`, `TerminateHostingDeployment`, `TerminateSupersededDeployments`, `GetActiveDemoDeployment`, `GetLatestDemoDeployment`, `GetActiveProdDeployment`, `InterruptedDeployments`. `UpdateHostingDeploymentStatus` (never called) removed. |
| `internal/db/project_deletion.go` | Both blockers read `hosting_deployments`; production is a gate; `ProdDeploymentStillUp` removed. |
| `internal/web/handlers_demo.go` | `openDemoDeployment`; demo start/restart/retry/stop/teardown all on `hosting_deployments`. |
| `internal/web/handlers_prod_deploy.go` | Supersedes on a successful deploy; `handleTeardownProd`. |
| `internal/web/server.go` | `cleanupInterruptedDeploys` replaces `cleanupStaleDemos`; teardown route. |
| `internal/web/handlers_settings.go` | `ActiveProdDeployment` for the page; `ProdStillUp` removed. |
| `internal/web/templates/` | `variation_detail.html` reads `.Demo`; `deployment_channel.html` offers teardown; `project_settings.html` drops the production warning. |

## Workflow states and transitions

```
                    CreateHostingDeployment
                              |
                          deploying ------------------- InterruptedDeployments
                          /       \                     (no teardown command)
   MarkHostingDeployment-/         \-Complete-               |
   Provisioning (keeps               HostingDeployment       v
   deploying, has a                        |              failed
   teardown command)                       v
                                        running
                                        /     \
                       NoteHostingDeployment-   \-Terminate / TerminateSuperseded
                       Error (stays running,      |
                       still metered)             v
                                              terminated
```

`deploying` and `running` are the metered pair; `failed` and `terminated` are the
two endings, and both set `finished_at`. `HostingDeployment.Active()` names the
first pair in one place, so the meter, the demo buttons and project deletion all
ask the same question.

## Verification

```bash
go test ./schema/...
TZ=America/Los_Angeles go test ./internal/db/
go test ./...
```

- `internal/cost/hosting_test.go` — the requested assertion, driven through
  `SettleHostingSpend` against a fake: a stopped demo's total stops moving
  across five further settlements, a running one keeps accruing, and
  `BillableThrough` freezes at the ending.
- `internal/db/hosting_deployment_lifecycle_test.go` — the same against real
  SQL, plus: a serving deployment has no `finished_at`, stopping twice keeps the
  first ending, a failed teardown stays billable, a redeploy supersedes its
  predecessor and leaves other apps alone, only in-flight deploys count as
  interrupted, and a failed deploy is closed and unmetered.
- `internal/db/project_deletion_test.go` — production declines and then stops
  declining once taken down, an in-flight deploy declines, finished deployments
  do not.
- `internal/web/project_delete_test.go` — the decline links to the deployment
  page, and the route table actually carries `teardown-prod`.

## Known gaps

- **A provisioning deployment is not metered.** `MarkHostingDeploymentProvisioning`
  leaves the status at `deploying`, which `GetHostingDeploymentsToMeter`
  excludes — but a load balancer coming up is already costing money. Metering
  `deploying` would also start charging for deploys that fail, whose wall-clock
  is deploy time rather than runtime. Both readings are defensible and neither
  is free; it wants deciding rather than guessing.
- **Nothing advances a provisioning deployment to `running`.** It waits for a
  redeploy. Pre-existing, and unrelated to metering only because of the gap
  above.
- **Demos before 053 are not in the ledger.** They never had a
  `hosting_deployments` row, and inventing rows for them would invent the
  figures too. Hosting spend for a project understates its history by however
  many demos it ran.
