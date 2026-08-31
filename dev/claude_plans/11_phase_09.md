# Phase 11 — Variation Requirements: What the Code Needs to Run

## Overview

A demo deployed a variation that had wired up Google sign-in, and it started,
and it did not work. There was no client secret, and nobody had registered the
demo's redirect URI with Google. Mendel had no way to know either was needed
and no place to put them if it had.

This phase gives a variation a way to say what its code needs in order to run
anywhere, and gives Mendel a way to collect it, hold it, and inject it — before
a deploy that cannot possibly succeed consumes a build.

## The framing that matters

The obvious framing is "demo requirements", and it is wrong. A variation that
needs an OAuth client secret needs it wherever it runs. Demos are merely where
it first bites, because a demo is the first time the code is pushed through a
deployment channel. The same requirements gate a production deploy of that
variation once it is merged.

So the requirement belongs to the **variation**, not to the demo — which is
also why the tables are named `variation_requirements` and not anything
demo-flavoured, and why gating lives in both `handleStartDemo` and
`runChannelProdDeployment`.

## Key design decisions

### Two kinds, distinguished by what Mendel can do about them

- **`secret`** — a value Mendel needs from the user (`GOOGLE_CLIENT_SECRET`).
  Stored encrypted, injected at deploy time. Mendel can hold this.
- **`acknowledgement`** — an action taken somewhere else, where Mendel already
  knows the string involved (the deployment's OAuth redirect URI) and only
  needs confirmation it was done. Mendel stores the confirmation, never a
  secret.

The split is not cosmetic: it is the difference between something Mendel can
supply and something only a human can do.

### Acknowledgements are keyed by the string confirmed, not by the requirement

`instructions` may contain `{{deploy_url}}`, which resolves to whichever
deployment is being gated. One requirement therefore yields the demo's redirect
URI and production's as **separate** acknowledgements, both of which must be
registered.

`requirement_acknowledgements` is keyed by `(requirement_id, resolved_value)`.
A changed URL leaves no matching row, so the requirement is unmet again rather
than silently vouching for a string nobody registered.

### Deferral, because two of three platforms cannot know their URL in advance

Fly.io's hostname is deterministically `<app>.fly.dev`, so a redirect URI can be
registered before the first deploy. Cloud Run assigns a hash at deploy time
(`<service>-<hash>-<region>.a.run.app`) and GKE a LoadBalancer IP after
provisioning.

On those platforms an acknowledgement naming `{{deploy_url}}` is **deferred**:
listed, explained, but neither met nor blocking, because blocking would make the
demo unstartable. Once a deployment exists, its real URL is used and the
acknowledgement becomes ordinary and actionable. `predictedDeployURL` is the one
place that knows which platforms can answer in advance.

### Values are project-scoped, requirements are variation-scoped

An OAuth client ID is the same for every variation and for production, so it is
entered once per project (`project_env_vars`). Requirements are declared per
variation because what is needed depends on the code that was written.

`project_env_vars` is kept apart from `project_credentials`, which holds the
platform credentials Mendel needs in order to deploy *at all*, rather than
values belonging to the user's own application.

### Declared in the repo, in `.mendel/requirements.json`

This amends the `.mendel/` allowed-files rule in CLAUDE.md, deliberately. The
declaration follows the `migration.json` precedent for the same reason: what
the code requires is a property of the code, code generation is the only thing
that knows it, and it belongs next to what it describes.

The alternative — reporting requirements through the codegen response — keeps
user repos untouched, which is the stronger reading of "minimize repository
dependencies." It was rejected because the declaration would then live nowhere
the code's author can see it.

The file is committed to the variation's branch, so on a revision it is still
there unless the code no longer needs it. **Absent therefore means nothing is
required**, and clears what a previous run declared — otherwise a revision that
drops the OAuth flow leaves a demo blocked on a secret nothing reads.

A re-declaration preserves the IDs of requirements that survive it, and with
them their acknowledgements: re-running codegen must not make the user
re-register a redirect URI that is still required and still correct.

### Production requires what was merged

`runChannelProdDeployment` deploys main, not a variation, so it gates on the
union of requirements from variations in `merged`/`selected` status,
deduplicated by `(kind, name)`. Two variations that both wired up Google sign-in
describe the same requirement, and production should ask for it once.

## New and modified files

**New**
- `internal/domain/requirements.go` — types, `{{deploy_url}}` resolution,
  `EvaluateRequirements` and the met/deferred/blocking judgement
- `internal/domain/requirements_test.go`
- `internal/db/requirement_queries.go` — requirements, env vars, acknowledgements
- `internal/db/requirement_queries_test.go` — real-SQL tests over an isolated
  schema loaded from `full.sql`
- `internal/web/requirements.go` — URL prediction, status assembly, secret
  decryption, the three form handlers
- `internal/web/requirements_test.go`
- `internal/codegen/requirements_test.go`
- `schema/migrations/031_variation_requirements.{up,down}.sql`

**Modified**
- `internal/codegen/generator.go` — `saveRequirements`, declaration validation
- `internal/codegen/cli.go` — the prompt section teaching the declaration format
- `internal/web/handlers_demo.go` — gating in `handleStartDemo` and
  `runChannelDemoDeployment`; secret injection in all three deploy functions
- `internal/web/handlers_prod_deploy.go` — gating and injection for production
- `internal/web/handlers_hop.go` — requirements on the variation detail view
- `internal/web/templates/variation_detail.html` — the "What This Needs to Run"
  section
- `internal/web/server.go` — three routes
- `CLAUDE.md` — `requirements.json` added to the `.mendel/` spec
- `internal/web/flyurl_test.go` (new) — see below

## Schema changes (migration 031)

- `variation_requirements` — `(variation_id, kind, name)` unique; a CHECK
  constraint refuses an acknowledgement with no instructions, since there would
  be nothing to act on
- `project_env_vars` — `(project_id, name)` unique, `encrypted_value BYTEA`
- `requirement_acknowledgements` — `(requirement_id, resolved_value)` unique

## Secret injection, per platform

- **Fly.io** — `flyctl secrets set --stage` before `deploy`, so the first
  machine to start already has them and no extra deploy is triggered
- **Cloud Run** — `--set-env-vars` with the `^|^` delimiter override, because
  the default comma separator would misread any value containing a comma
- **GKE** — a `Secret` applied from a manifest written outside `workDir` and
  deleted once applied, referenced by `envFrom`, so values never sit in the
  checked-out repository

## Also in this phase: the Fly.io URL bug

Demo URLs were captured by taking the first `https://` in flyctl's output.
flyctl prints a dashboard link before the app URL, and the build log carries
whatever the project's toolchain emits — so staging recorded a monitoring page
and, on another deploy, npm's release notes as the demo URL.

`flyDeployedURL` now looks for the "Visit your newly deployed app at" marker,
falls back to a `*.fly.dev` host preferring one that names the app, and finally
to the deterministic hostname. It can never return a `fly.io` dashboard link.
Both real staging failures are covered by tests.

## Verification

```bash
go build ./... && go vet ./...
MENDEL_TEST_DB_URL="postgres://bhs:@localhost:5432/mendel_test?sslmode=disable" go test ./... -count=1
```

The `internal/db` tests exercise real SQL against an isolated schema. They
caught a genuine bug during development: the first cut of
`ReplaceVariationRequirements` joined kind and name with a NUL byte, which
Postgres text cannot hold, so every re-declaration would have failed at runtime.

## Known gaps

- A deferred acknowledgement is surfaced on the variation page after the first
  deploy, but nothing *tells* the user it has become actionable — they have to
  look. A notification or an InputRequest would close this.
- Production gating uses merged variations' requirements. A requirement that
  reached main by a route other than a Mendel merge is invisible to it.
- Nothing yet re-checks acknowledgements when a deployment's URL changes; the
  next deploy simply finds the requirement unmet, which is correct but late.
