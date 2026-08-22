# MendelBuild Development Guidelines

## Prototype Stage

Mendel is currently in **prototype stage** — it's not yet running continuously in a cloud environment with a stable domain. Until that changes:

- **Prioritize clean code over backwards compatibility.** Don't add migration shims, fallback paths, or compatibility layers for old data formats.
- **It's OK to break existing Mendel app state.** If a schema change or refactor would require complex migration code, just make the clean change and reset/regenerate affected data.
- **Avoid accumulating technical debt** to preserve prototype artifacts.

This guidance will change once Mendel is deployed to production with real users.

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

When Mendel-specific configuration is unavoidable (like `.mendel/demo-config.yml`), keep it:
- Self-contained (no references to external Mendel docs)
- Using standard tooling under the hood (Docker, npm, etc.)
- Optional when possible (sensible defaults)

The goal: if a user stops using Mendel, their repository should work exactly the same without cleanup.

### `.mendel/` Directory Structure

User repositories have a `.mendel/` directory for Mendel configuration. **Only these files are allowed:**

```
.mendel/
  docker-compose.demo.yml   # Demo infrastructure
  docker-compose.test.yml   # Test infrastructure (optional)
  demo-config.yml           # Demo settings
  test-config.yml           # Test settings (optional)
  migration.json            # Migration instructions (optional)
```

**DO NOT create any other files in `.mendel/`** - no documentation. Docs belong in repo root or `docs/`.

### `demo-config.yml` Spec

Defined in `internal/demo/config.go`:

```yaml
version: 1
service: app              # Which docker-compose service to expose (required)
container_port: 3000      # Port inside the container (required)
health_path: /health      # Endpoint to check for readiness
health_timeout: 60        # Seconds to wait for health check
health_interval: 2        # Seconds between health check attempts
after_up:                 # Commands to run after containers start
  - "docker-compose exec app npm run migrate"
before_down:              # Commands to run before teardown
  - "..."
```

### `test-config.yml` Spec

Defined in `internal/test/config.go`. Tests run **inside Docker**, not on the host:

```yaml
version: 1
service: app              # Which container to run tests in (required)
test_command: npm test    # Command to run inside the container (required)
startup_timeout: 60       # Seconds to wait for test services
```

Flow: `docker-compose.test.yml up` → `exec <service> <test_command>` → check exit code → `down`

### `demo-hosting.yml` Spec

Defined in `internal/demo/config.go`. This is a **platform-agnostic** config for deploying demos to cloud hosting. Mendel doesn't understand specific platforms - it just runs scripts with secrets injected as environment variables.

```yaml
version: 1
required_secrets:           # Secrets that must exist in Mendel project settings
  - GCP_PROJECT_ID          # Injected as env vars when running scripts
  - GCP_SERVICE_ACCOUNT_KEY
deploy_script: deploy-demo.sh    # Script to deploy (relative to .mendel/)
teardown_script: teardown-demo.sh # Script to tear down
url_from: stdout            # How to get URL: "stdout" or "file:<path>"
```

**Flow:**
1. AI asks user to select hosting platform via InputRequest
2. AI asks for platform-specific credentials via InputRequests
3. AI generates platform-specific deploy/teardown scripts
4. AI generates `demo-hosting.yml` listing required secrets
5. Mendel validates secrets exist, injects as env vars, runs scripts

**Script requirements:**
- `deploy_script`: Receives `MENDEL_VARIATION_ID` env var, prints demo URL to stdout
- `teardown_script`: Receives `MENDEL_VARIATION_ID` env var, cleans up resources

This keeps platform-specific logic in AI-generated scripts, not hardcoded in Mendel.

Codegen prompts instruct Claude Code to follow these rules.

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
    "model": "claude-sonnet-4-6",
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

Current models (as of June 2026):
- `claude-opus-4-5` - Most capable, highest quality
- `claude-sonnet-4-6` - Balanced speed and intelligence (default)
- `claude-haiku-4-5` - Fastest, most cost-effective

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
MENDEL_TEST_DB_URL="postgres://bhs:@localhost:5432/mendel_test?sslmode=disable" go test ./schema/...
```
This validates that migrations apply correctly and match full.sql. The test requires a real PostgreSQL connection and will fail (not skip) if it cannot connect.

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
