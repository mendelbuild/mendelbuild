# MendelBuild Development Guidelines

## Prototype Stage

Mendel is currently in **prototype stage** — it's not yet running continuously in a cloud environment with a stable domain. Until that changes:

- **Prioritize clean code over backwards compatibility.** Don't add migration shims, fallback paths, or compatibility layers for old data formats.
- **It's OK to break existing Mendel app state.** If a schema change or refactor would require complex migration code, just make the clean change and reset/regenerate affected data.
- **Avoid accumulating technical debt** to preserve prototype artifacts.

This guidance will change once Mendel is deployed to production with real users.

## Working Alongside Other Sessions

Several Claude sessions often work on this repo at once, in separate git
worktrees, and `main` moves underneath you without warning.

**Sync before starting anything substantial, not at the end.** Discovering
mid-merge that `main` has moved means rebasing and re-running the whole suite —
work you already did, done again. One command up front avoids it:

```bash
git fetch origin && git status -sb && git log --oneline HEAD..origin/main
```

If that last command prints anything, rebase onto it *before* writing code. Do
it again before a long verification run if the session has been going a while.

Three things that have actually bitten:

- **The primary checkout is sometimes on `main` itself.** Commits then land on
  `main` directly, and `git branch -f main` fails with "used by worktree".
  Check `git rev-parse --abbrev-ref HEAD` rather than assuming a feature branch.
  Moving the ref only works when `main` is not checked out anywhere; otherwise
  run `git merge --ff-only` from inside the worktree that holds it.

- **Uncommitted files in the shared checkout may belong to another session.**
  Stage explicit paths. Never `git add -A` without first reading what it caught,
  or you will commit someone else's half-finished work — and a committed
  migration is immutable, which forces them into a follow-up migration to fix
  something they had not finished writing.

- **`schema/full.sql` is a collision point**, because every migration touches
  it. If it is already modified when you arrive, that is someone else's
  in-flight migration; stage only your own hunks.

## Template Data Conventions

When passing JSON data to HTML templates for use in JavaScript:

- **Use `template.JS` type**, not `string` — Go's html/template HTML-escapes strings, which breaks JSON parsing in JavaScript
- Example: `view.DataJSON = template.JS(jsonBytes)` not `view.DataJSON = string(jsonBytes)`
- See `handlers.go` `HopsJSON`/`EdgesJSON` for the correct pattern

## Core Design Principles

### Minimize User Repository Dependencies on Mendel

User repositories should have **minimal to no awareness** of Mendel. This applies to:

- **Code**: No Mendel-specific imports, SDKs, or dependencies
- **Configuration**: Prefer standard formats (docker-compose.yml, package.json scripts) over Mendel-specific files when possible
- **Documentation**: User repos should not need to document Mendel integration
- **Docker images**: Use standard images (postgres, redis, node) not mendel/* images

When Mendel-specific configuration is unavoidable, keep it:
- Self-contained (no references to external Mendel docs)
- Using standard tooling under the hood (Docker, npm, etc.)
- Optional when possible (sensible defaults)

The goal: if a user stops using Mendel, their repository should work exactly the same without cleanup.

### `.mendel/` Directory Structure

User repositories have a `.mendel/` directory for Mendel configuration. **Only these files are allowed:**

```
.mendel/
  docker-compose.test.yml   # Test infrastructure (optional)
  test-config.yml           # Test settings (optional)
  migration.json            # Migration instructions (optional)
  requirements.json         # What the code needs in order to run (optional)
```

**DO NOT create any other files in `.mendel/`** - no documentation. Docs belong in repo root or `docs/`.

### `test-config.yml` Spec

Defined in `internal/test/config.go`. Tests run **inside Docker**, not on the host:

```yaml
version: 1
service: app              # Which container to run tests in (required)
test_command: npm test    # Command to run inside the container (required)
startup_timeout: 60       # Seconds to wait for test services
```

Flow: `docker-compose.test.yml up` → `exec <service> <test_command>` → check exit code → `down`

### `requirements.json` Spec

Parsed in `internal/codegen/generator.go` (`saveRequirements`), stored in
`variation_requirements`. Written by code generation, because what the code
needs is a property of the code it just wrote:

```json
{
  "requirements": [
    {
      "kind": "secret",
      "name": "GOOGLE_CLIENT_SECRET",
      "description": "OAuth client secret for Google sign-in",
      "console_url": "https://console.cloud.google.com/apis/credentials"
    },
    {
      "kind": "acknowledgement",
      "name": "google-redirect-uri",
      "instructions": "Add {{deploy_url}}/auth/callback to Authorized redirect URIs.",
      "console_url": "https://console.cloud.google.com/apis/credentials"
    }
  ]
}
```

A `secret` is a value Mendel holds encrypted (project-scoped, in
`project_env_vars`) and injects as an environment variable at deploy time. An
`acknowledgement` is an action taken elsewhere that Mendel cannot perform and
only records; `{{deploy_url}}` resolves per deployment, so the demo and
production yield separate acknowledgements.

Requirements gate both `handleStartDemo` and `runChannelProdDeployment`. The
file is optional and most variations will not have one.

### Deployment Channels

Demos and production deployments use **deployment channels** - validated (artifact_kind, hosting_platform) pairs. See `internal/hosting/platforms.go` for the supported combinations:
- `container` → `fly-io`, `cloud-run`
- `kubernetes` → `gke`

Deployment is deterministic (no AI-generated scripts). Platform-specific deployment code lives in `handlers_demo.go` (`deployToFlyIO`, `deployToCloudRun`, `deployToGKE`).

Before demos can run, the deployment channel must be **validated** via a hello-world deploy → health check → teardown test. This ensures credentials are correct.

### No Hardcoded Platform Options

**NEVER hardcode lists of hosting platforms, cloud providers, or deployment options in Go code.** These change frequently and vary by Mendel installation.

Instead:
- **Store platform data in the database** (`hosting_platforms` table)
- **Seed on startup** if the table is empty
- **Refresh via CLI** (`mendel platforms refresh`) to update with current options
- **Let AI suggest platforms** dynamically based on what's popular/available

This applies to:
- Hosting platforms (Fly.io, Render, Vercel, Cloud Run, etc.)
- Any enumerated options that users choose from
- Platform-specific configuration (deployer images, instructions)

The goal: Mendel's platform knowledge stays current without code changes.

## Cost Model

All spend is denominated in **USD**. Tokens are telemetry, not currency — prices
differ ~10x across models, a cache read is 0.1x an input token and a cache write
1.25x, so a token-denominated budget floats in worth. See
`dev/claude_plans/phase_08.md` for the full design.

### Recording spend

Every charge goes through `cost.Recorder`. Never write `cost_entries` directly.

```go
s.recordHopSpend(ctx, hopID, "variation_proposer", spend)   // web layer helpers
s.recordStrategySpend(ctx, strategyID, "okr_tuner", spend)
```

Agent methods return `agent.Spend` (model + all four token counts), not a bare
token total. **When you add an agent call, record its spend** — an unrecorded
call makes the project's cost silently understate itself.

`InputTokens` from the API is the *uncached remainder only*. Full prompt size is
`input + cache_read + cache_write`. Never price or report input tokens alone.

### Never hardcode prices

Rate cards live in `model_rate_cards` / `hosting_rate_cards`, are seeded on
startup, and are refreshed with `mendel rates refresh`. This is the same rule as
hosting platforms. Cards are versioned by `effective_from` and never rewritten,
so figures already in the ledger stay verifiable.

To update prices, edit `DefaultModelRates()` in `internal/cost/rates.go` and run
`mendel rates refresh` — **verify current pricing from up-to-date online sources
first**, the same as for model names.

### Estimates must be falsifiable

`hop_cost_estimates` is append-only and records who estimated, their confidence,
and their stated basis. Estimating agents are given `CostCalibration` — this
project's observed estimate-vs-actual history — and are instructed to anchor to
it, and to say plainly when there is no history rather than inventing a
confident figure. Preserve that instruction when editing those prompts: a
fabricated number a human then plans against is worse than an admitted unknown.

Hosting costs are **estimates** from list prices and wall-clock, never provider
invoices. Label them as such in any UI.

## Structured LLM API Conventions

All LLM API calls in MendelBuild use Anthropic's **structured outputs** feature for guaranteed JSON compliance. Schemas are generated from Go struct tags.

### How It Works

1. Define Go types with `desc` tags on each field
2. Generate JSON Schema at runtime using `SchemaFromType()`
3. Pass schema to API via `output_config`

```go
// 1. Define types with desc tags (types.go)
type ProposedHop struct {
    Name       string `json:"name" desc:"Short kebab-case identifier (e.g., 'user-onboarding')"`
    Commentary string `json:"commentary" desc:"Explains what this hop achieves and its expected impact. 2-4 sentences."`
}

// 2. Generate schema from type (schema.go)
schema := SchemaFromType(reflect.TypeOf(MyResponse{}))

// 3. Call API with schema
resp, err := client.SendMessageWithSchema(ctx, systemPrompt, messages, maxTokens, schema)
```

The API request includes:
```json
{
    "model": "claude-sonnet-5",
    "messages": [...],
    "output_config": {
        "format": {
            "type": "json_schema",
            "schema": { /* generated from Go types */ }
        }
    }
}
```

### Field Description Tags

The `desc` tag is the source of truth for LLM guidance. Be specific:

```go
type ProposedHop struct {
    // Good: explains purpose and format
    ObjectiveIDs []string `json:"objective_ids" desc:"UUIDs of objectives this hop advances. Copy exact IDs from strategy input."`

    // Good: specific format and expectations
    Commentary string `json:"commentary" desc:"Explains what this hop achieves, why it matters, and its expected impact. 2-4 sentences."`

    // Bad: too vague
    Name string `json:"name" desc:"The name"`
}
```

### Schema Generator

`internal/agent/schema.go` provides:

- `SchemaFromType(t reflect.Type)` - generates JSON Schema from any Go type
- Reads `json` tags for field names, `desc` tags for descriptions
- Handles nested structs, arrays, pointers
- Sets `additionalProperties: false` and `required` automatically

### Adding New Agents

1. Define request/response types in `internal/agent/types.go` with `desc` tags on every field
2. Create a schema function: `func MyAgentSchema() json.RawMessage { return SchemaFromType(reflect.TypeOf(MyResponse{})) }`
3. Use `SendMessageWithSchema()` with the generated schema
4. System prompt provides context; schema enforces structure

### Current Agents

- **Roadmap Proposer** (`internal/agent/proposer.go`)
  - Types: `ProposerResponse`, `ProposedRoadmap`, `ProposedHop`
  - Schema: `ProposerResponseSchema()` - generated from `desc` tags
- **OKR Tuner** (`internal/agent/okr_tuner.go`)
  - Uses Haiku for cost-effective quality feedback on objectives and key results
  - Types: `OKRTuneInput`, `OKRTuneResponse`, `ItemTuning`

### Model Names

**IMPORTANT:** Always verify model names from up-to-date online sources before using them. Model names in training data become outdated quickly.

Current models and list prices, USD per million tokens (verified 2026-08-27
against the same source seeded into `model_rate_cards`):

| Model | Input | Output | Use |
|---|---|---|---|
| `claude-opus-5` | $5 | $25 | Hardest reasoning |
| `claude-sonnet-5` | $2 | $10 | **Default** for codegen and agents |
| `claude-haiku-4-5` | $1 | $5 | Cheap bulk work (OKR tuning, conflict audit) |

`claude-sonnet-4-6` ($3/$15) is superseded: Sonnet 5 is newer *and* a third
cheaper, so nothing should be left on 4.6. Cache reads cost 0.1x the input rate
and cache writes 1.25x; batch requests are half price.

To verify current model names, check:
- https://docs.anthropic.com/en/docs/about-claude/models
- https://console.anthropic.com (model selector in playground)

## Project Structure

```
cmd/mendel/          # CLI entry point
internal/
  agent/             # AI agents (Anthropic API integration)
    client.go        # API client with structured output support
    schema.go        # JSON Schema generator from Go types
    proposer.go      # Roadmap proposer
    types.go         # Go types with desc tags (source of truth)
  db/                # Database queries and migrations
  domain/            # Core domain types
  web/               # HTTP server and templates
schema/migrations/   # SQL migration files
```

## Database Migrations

**Never edit existing migrations.** Once a migration is committed, treat it as immutable. To change the schema:

1. Create a new migration file (e.g., `003_add_column.up.sql`)
2. Write the ALTER statements needed to transform the current schema
3. Create the corresponding `.down.sql` to revert
4. Update `schema/full.sql` to reflect the final schema state
5. **Run `go test ./schema/...` to verify the migration is valid**

Migration files live in `schema/migrations/` and are read at runtime. The `full.sql` file represents the complete current schema for reference.

**IMPORTANT:** After ANY change to `schema/migrations/` or `schema/full.sql`, you MUST run:
```bash
go test ./schema/...
```
This validates that migrations apply correctly and match full.sql. The test
needs a real PostgreSQL connection and fails (not skips) if it cannot connect —
a schema change that silently goes unverified is worse than a noisy failure.

It defaults to `postgres://localhost:5432/mendel_test?sslmode=disable` and
creates that database if it is missing, so no setup or environment variable is
needed. Set `MENDEL_TEST_DB_URL` only to point somewhere else. The database is
shared across worktrees on a machine, which is safe: each run works inside its
own throwaway schemas, so concurrent runs from separate checkouts cannot
collide.

Example: To add a NOT NULL constraint to an existing column:
```sql
-- 003_make_commentary_required.up.sql
ALTER TABLE hops ALTER COLUMN commentary SET NOT NULL;

-- 003_make_commentary_required.down.sql
ALTER TABLE hops ALTER COLUMN commentary DROP NOT NULL;
```

## Deployment

**Always ask for confirmation before deploying to GKE.** Deploys can interrupt running test/generation jobs. Wait for user approval before running `./deploy/gke-deploy.sh` or equivalent kubectl commands that update the deployment.

## Environment Variables

- `MENDEL_DB_URL`: PostgreSQL connection string
- `ANTHROPIC_API_KEY`: Anthropic API key for agent calls
- `MENDEL_WORK_DIR`: Working directory for git clones (default: `~/.mendel/work`)

## Development Plans

Final implementation plans for each phase are stored in `dev/claude_plans/` for future reference. These documents capture architectural decisions and implementation details.

**At the end of each development phase**, write the plan to `dev/claude_plans/phase_XX.md` before moving on. The plan should include:
- Overview of what was built
- Key design decisions and rationale
- New/modified files
- Database schema changes
- Workflow states and transitions
- Verification steps
