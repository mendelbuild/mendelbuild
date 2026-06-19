# MendelBuild Development Guidelines

## Structured LLM API Conventions

All LLM API calls in MendelBuild use structured JSON for both input and output (after system prompt). No free-form text exchanges.

### Why Structured JSON?

1. **Reliability**: JSON parsing is deterministic; free-form text parsing is error-prone
2. **Validation**: JSON schemas can be validated programmatically
3. **Consistency**: All agents follow the same input/output patterns
4. **Debuggability**: JSON logs are easy to inspect and replay

### Pattern for Agent Implementations

```go
// System prompt ends with strict JSON instructions
const systemPrompt = `You are an agent that...

IMPORTANT: Your response must be valid JSON matching this exact schema:
{
  "field": "description"
}

DO NOT include any text outside the JSON structure.`

// User messages contain structured context as JSON
userMessage := fmt.Sprintf(`Process this request:

%s

Return only valid JSON.`, contextJSON)

// Parse response as JSON
var result ResponseType
if err := json.Unmarshal([]byte(response), &result); err != nil {
    return nil, fmt.Errorf("parse response: %w", err)
}
```

### Current Agents

- **Roadmap Proposer** (`internal/agent/proposer.go`): Generates and revises roadmap proposals
  - Input: StrategyContext (objectives, funding, etc.)
  - Output: ProposedRoadmap (hops with dependencies and cost estimates)

### Adding New Agents

1. Define input/output types in `internal/agent/types.go`
2. Create system prompt with explicit JSON schema
3. Format user message with JSON context
4. Parse response as strongly-typed struct
5. Handle parsing errors gracefully (include raw response in error)

## Project Structure

```
cmd/mendel/          # CLI entry point
internal/
  agent/             # AI agents (Anthropic API integration)
  db/                # Database queries and migrations
  domain/            # Core domain types
  web/               # HTTP server and templates
schema/migrations/   # SQL migration files
```

## Environment Variables

- `MENDEL_DB_URL`: PostgreSQL connection string
- `ANTHROPIC_API_KEY`: Anthropic API key for agent calls
