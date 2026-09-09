# Retiring a Project — Design

## Overview

A project could be created and never removed. This adds a soft delete: the row
is marked, not deleted, and everything hanging off it is kept.

Marking rather than removing is the whole design. A project's rows are mostly
evidence about money that has already been spent — the cost ledger, the
roadmap, the record of what was deployed and what it cost — and that evidence
stays true whether or not anyone still wants the project. `ON DELETE CASCADE`
runs from `projects` through nearly every table in the schema, so a real DELETE
would take all of it. An administrator can reverse a mark; nobody can reverse a
cascade.

There is no un-delete surface yet, deliberately: it is an administrator's
action, wanted rarely, and inventing a page for it now would be inventing a
page nobody has asked to use.

## Key decisions

**Deleting declines while something is running.** Retiring marks a row. It does
not reach out to Fly, to Cloud Run or to a cluster and stop anything, so a demo
still serving would go on serving — and the hosting meter would go on charging
for it — against a project that no page in the app still lists. Rather than
hide a bill nobody can find, `SoftDeleteProject` refuses and names what is live:
running demos and running live experiments, each with a sentence saying what
would make deletion possible and a link to where to do it. Both are things
Mendel believes are running, can stop, and would cost money if left.

**A production deployment is a warning, not a gate.** Nothing in Mendel ever
moves a production deployment off `running` — there is no route that takes one
down and no code that marks one terminated — so gating on that status would
refuse forever. A gate nobody can satisfy is worse than no gate. What is true of
production is said instead, on the page, before the button: this deployment
stays up and stays billing, take it down on the platform if you want it
stopped. This is the distinction CLAUDE.md draws under "Every Gate Is a
Functional Area Condition": a thing worth saying that gates nothing is a
warning, and warnings live beside the gates rather than in them.

Deletion is not modelled as a Functional Area. A Functional Area is something
Mendel can do *for* a project — run a demo, deploy to production — and the
matrix renders on the project's own "Available" page. Retirement is an act *on*
the project record, and putting it in the matrix would list a destructive
administrative action among the project's capabilities. The two rules from that
design that do carry over are honoured directly: every blocker names what would
make it true, and the declining path and the page render the same string.

**The check and the write are one statement.** A demo started between reading
the settings page and pressing the button would otherwise slip through.
`SoftDeleteProject` re-runs the blocker predicates inside the `UPDATE`'s `WHERE`
clause, and re-reads the blockers when no row was affected rather than guessing
whether that meant "already retired" or "something started just now".

**Deleting stops the project spending.** The four worker sweeps find hops across
every project at once and had no notion of which project a hop belonged to. Each
now joins through `strategies` to `projects` and skips retired ones. Without
that, deleting a project mid-roadmap would keep making agent calls nobody could
see a page for, let alone stop.

**The hosting meter is left alone.** It goes on recording what a running
deployment costs, even for a retired project. Deleting a project does not make
its Fly app free, and the meter's job is to record money that was actually
spent; an un-delete should not find a hole in the ledger.

**One choke point closes the pages.** `requireLiveProject` is mounted on
`/p/{projectID}` and on the project-scoped JSON endpoints. It runs whether or
not auth is enabled, unlike `requireProjectAccess`, because retirement is a fact
about the project rather than about who is asking. Without it, "deleted" would
mean no more than "absent from the dashboard": every bookmark, redirect and form
action a browser had kept would go on working. A retired project answers 404
with a sentence saying it was deleted and can be restored, rather than
redirecting to the dashboard and leaving the reader guessing.

## Files

New:

- `schema/migrations/052_project_soft_delete.{up,down}.sql`
- `internal/domain/project_deletion.go` — `ProjectDeletionBlocker`
- `internal/db/project_deletion.go` — blockers, the warning, the delete, `ProjectIsLive`
- `internal/db/project_deletion_test.go`
- `internal/web/project_delete_test.go`

Modified:

- `schema/full.sql` — `deleted_at`, `deleted_by`, `idx_projects_live`. The
  foreign key is declared beneath the `users` table, which this file defines
  after `projects`, and the columns come last so their ordinal positions match
  what the `ALTER TABLE` produces.
- `internal/db/queries.go` — read paths filtered; the four worker sweeps joined
  through to `projects`
- `internal/web/handlers.go` — `listProjects` filtered
- `internal/web/server.go` — `requireLiveProject`, the delete route
- `internal/web/handlers_settings.go` — `handleDeleteProject`, deletion data on
  the settings page
- `internal/web/templates/project_settings.html` — the delete section

## Schema

```sql
ALTER TABLE projects ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE projects ADD COLUMN deleted_by UUID REFERENCES users(id);
CREATE INDEX idx_projects_live ON projects (name) WHERE deleted_at IS NULL;
```

Partial, because every read path filters on it and the live projects are the
ones worth indexing.

## Flow

1. The project's Settings page shows a delete section to an owner, with whatever
   is currently live and whatever production would be left running.
2. The reader types the project's name. Friction against a reflexive click, in
   the same spirit as the dissonance phrase on a live experiment, not a
   comprehension test.
3. `handleDeleteProject` checks ownership and the typed name, then calls
   `SoftDeleteProject`.
4. Declined: back to Settings, which re-reads the blockers as it renders, so the
   reader sees what is live now rather than what was live when they clicked.
5. Retired: back to the dashboard, where the project is gone. Its pages answer
   404, its hops stop being swept, and its rows stay exactly where they were.

## Verification

```bash
go test ./schema/...
TZ=America/Los_Angeles go test ./internal/db/
go test ./...
```

`internal/db/project_deletion_test.go` covers each read path, each worker sweep,
the two declines and their recovery, production warning without blocking,
deleting twice, and `ProjectIsLive` telling retired from absent.
`internal/web/project_delete_test.go` covers the page offering delete at all,
the decline naming what is running and where to stop it, the production warning
not removing the form, and — walking the route table — every project-scoped
route sitting behind `requireLiveProject`. That last one fails loudly if the
middleware is ever dropped; it was checked by removing it.

## Closed in 22

The gap this shipped with — nothing ever moved `hosting_deployments.status` off
`running`, so demos were gated on `demo_instances` and production was a warning
rather than a gate — is closed by
[22_deployment_lifecycle.md](22_deployment_lifecycle.md). Both blockers now read
`hosting_deployments`, and production is a gate with a route that satisfies it.
