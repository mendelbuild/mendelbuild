# MendelBuild Development Guidelines

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

Migration files live in `schema/migrations/` and are read at runtime. The `full.sql` file represents the complete current schema for reference.

Example: To add a NOT NULL constraint to an existing column:
```sql
-- 003_make_commentary_required.up.sql
ALTER TABLE hops ALTER COLUMN commentary SET NOT NULL;

-- 003_make_commentary_required.down.sql
ALTER TABLE hops ALTER COLUMN commentary DROP NOT NULL;
```

## Environment Variables

- `MENDEL_DB_URL`: PostgreSQL connection string
- `ANTHROPIC_API_KEY`: Anthropic API key for agent calls
- `MENDEL_WORK_DIR`: Working directory for git clones (default: `/tmp/mendel`)

## Development Plans

Final implementation plans for each phase are stored in `dev/claude_plans/` for future reference. These documents capture architectural decisions and implementation details.

**At the end of each development phase**, write the plan to `dev/claude_plans/phase_XX.md` before moving on. The plan should include:
- Overview of what was built
- Key design decisions and rationale
- New/modified files
- Database schema changes
- Workflow states and transitions
- Verification steps
