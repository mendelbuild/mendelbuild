# Demo Infrastructure Design Notes

*Captured from conversation on 2026-07-01*

## Problem Statement

Variations need to be demo-able before selection. This requires:
- Running the variation's code somewhere accessible
- Providing a URL to the Mendel user
- Managing lifecycle (startup, teardown, crash recovery)

## Constraints

1. **No Mendel-hosted infrastructure** - Mendel doesn't pay for users' compute
2. **No user deployment burden** - Users shouldn't have to manually deploy demos
3. **Stateless Mendel** - Process can crash and recover without orphaned resources

## Proposed Approach

### MENDEL.md File

A markdown file checked into the user's git repo that describes deployment infrastructure. This file:
- Is created/updated by Mendel when it first encounters a repo
- Contains instructions for headless Claude Code to start services
- Is evolvable via Hops/Variations (dogfooding!)
- Can reference other files in the repo

**Example structure:**
```markdown
# Mendel Configuration

## Demo Deployment

### Local Development
- Command: `npm run dev`
- Port: 3000
- Health check: `http://localhost:3000/health`

### Environment Variables
- Copy from `.env.example`

### Teardown
- Kill process on port 3000: `lsof -ti:3000 | xargs kill -9`
```

### Demo Instances Table

Track running demos with enough info to recover from crashes:

```sql
CREATE TABLE demo_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id),
    url TEXT NOT NULL,
    teardown_instructions TEXT NOT NULL,  -- shell commands to kill it
    started_at TIMESTAMP NOT NULL DEFAULT NOW(),
    stopped_at TIMESTAMP,
    status TEXT NOT NULL DEFAULT 'running',  -- running, stopped, error
    process_info JSONB,  -- pid, port, container_id, etc
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_demo_instances_variation ON demo_instances(variation_id);
CREATE INDEX idx_demo_instances_status ON demo_instances(status) WHERE status = 'running';
```

### Claude Code as Deployment Agent

Mendel delegates to Claude Code for actual deployment:
- Uses user's existing credentials/permissions
- Reads MENDEL.md for instructions
- Starts services, captures URL/PID
- Records teardown instructions in DB

### Lifecycle

1. **Startup**: When variation reaches "pending" status (code complete), Mendel can start demo
2. **Running**: Demo accessible via URL, tracked in DB
3. **Teardown**:
   - Automatic when variation is selected/rejected
   - Manual via UI
   - Recovery: on Mendel startup, check for orphaned demos and run teardown

## Open Questions

1. **Port management**: How to avoid conflicts when multiple variations run locally?
   - Could assign ports based on variation ID hash
   - Could let MENDEL.md specify port allocation strategy

2. **Cloud deployment**: How to handle deploy-to-cloud scenarios?
   - MENDEL.md could have cloud-specific sections
   - Could use existing CI/CD patterns (Vercel, Railway, etc.)

3. **Demo access control**: Should demos be password-protected?
   - Localhost is fine for now
   - Cloud deployments might need auth

4. **Timeout/auto-cleanup**: Should demos auto-terminate after N hours?
   - Prevents resource leaks
   - Could be configurable in MENDEL.md

## Future Considerations

- **Runtime variation routing**: LaunchDarkly-style routing where one deployment serves multiple variations based on user/session ID
- **Traffic splitting**: For A/B testing in production
- **Ecosystem integration**: Connecting to user's existing infrastructure
