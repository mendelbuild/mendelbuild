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
    Name string `json:"name" desc:"Short kebab-case identifier (e.g., 'user-onboarding')"`
    Kind string `json:"kind" desc:"Must be one of: 'feature', 'infrastructure', 'performance'"`
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
    // Good: specific constraints and examples
    Kind string `json:"kind" desc:"Hop category. Must be one of: 'feature', 'infrastructure', 'performance', 'code_quality', 'user_engagement', 'cost_reduction'"`

    // Good: explains purpose and format
    ObjectiveIDs []string `json:"objective_ids" desc:"UUIDs of objectives this hop advances. Copy exact IDs from strategy input."`

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

## Environment Variables

- `MENDEL_DB_URL`: PostgreSQL connection string
- `ANTHROPIC_API_KEY`: Anthropic API key for agent calls
