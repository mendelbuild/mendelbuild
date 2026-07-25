# Cloud Deployment & Traffic Splitting Plan

> **Status**: Draft
> **Last Updated**: 2026-07-24

## Overview

Enable Mendel to deploy variations to cloud environments and split production traffic between them using Envoy as a reverse proxy.

## Design Rationale

Each variation is a separate git branch deployed as its own service instance. There's no single app running multiple code paths — we need infrastructure-level routing, not app-level branching. Envoy provides:

- **Consistent hashing**: Ring-hash load balancing ensures same user → same variation
- **Weighted routing**: Configurable traffic splits (90% main, 7% var-a, 3% var-b)
- **Dynamic configuration**: xDS API allows Mendel to push config updates without restarts
- **Health checking**: Built-in backend health monitoring
- **Battle-tested**: Production-ready, widely deployed

## Architecture

```
                            ┌─────────────────────────────────────────────┐
                            │         User's Cloud Environment            │
                            │                                             │
    Incoming Traffic        │  ┌-─────────────────────────────────────┐   │
          │                 │  │             Envoy Proxy              │   │
          │                 │  │  ┌─────────────────────────────────┐ │   │
          ▼                 │  │  │ Ring-hash LB on X-User-ID header│ │   │
    ┌───────────┐           │  │  │ Weighted clusters per variation │ │   │
    │ Load      │──────────▶│  │  └─────────────────────────────────┘ │   │
    │ Balancer  │           │  └────────-─────┬───────────────────────┘   │
    └───────────┘           │                 │                           │
                            │    ┌────────────┼────────────┐              │
                            │    ▼            ▼            ▼              │
                            │ ┌──────┐    ┌──────┐    ┌──────┐            │
                            │ │ main │    │var-a │    │var-b │            │
                            │ │ 90%  │    │  7%  │    │  3%  │            │
                            │ └──────┘    └──────┘    └──────┘            │
                            └─────────────────────────────────────────────┘
                                              ▲
                                              │ xDS config push
                                              │ (or static config)
┌-─────────────────────────────────────────────┴───────────────────────────┐
│                           Mendel Infrastructure                          │
│                                                                          │
│  ┌────────────────────────────────────────────────────────────────────┐  │
│  │                        Mendel Core                                 │  │
│  │                                                                    │  │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────────┐ │  │
│  │  │   Web UI    │  │  xDS Server │  │      Deploy Runner          │ │  │
│  │  │             │  │  (optional) │  │                             │ │  │
│  │  │ - Traffic   │  │             │  │  ┌───────────────────────┐  │ │  │
│  │  │   sliders   │  │ - Serves    │  │  │  Docker Container     │  │ │  │
│  │  │ - Deploy    │  │   cluster   │  │  │  (mendel-deploy-tools)│  │ │  │
│  │  │   buttons   │  │   configs   │  │  │  - gcloud, aws, tf    │  │ │  │
│  │  │ - Creds UI  │  │ - Updates   │  │  │  - user deploy script │  │ │  │
│  │  │             │  │   on change │  │  └───────────────────────┘  │ │  │
│  │  └─────────────┘  └─────────────┘  └─────────────────────────────┘ │  │
│  │                                                                    │  │
│  └────────────────────────────────────────────────────────────────────┘  │
│                                    │                                     │
│                                    ▼                                     │
│                          ┌──────────────────┐                            │
│                          │    PostgreSQL    │                            │
│                          │  - credentials   │                            │
│                          │  - allocations   │                            │
│                          │  - instances     │                            │
│                          └──────────────────┘                            │
└──────────────────────────────────────────────────────────────────────────┘
```

## Components

### 1. Envoy Proxy (User-Deployed)

Mendel generates Envoy configuration; users deploy Envoy in their environment.

**Routing Logic:**
- Hash on `X-Mendel-User` header (or cookie, configurable)
- Ring-hash load balancing for consistency
- Weighted clusters map to variation backends

**Configuration Approaches (choose one per deployment):**

A. **Static config + reload**: Mendel generates `envoy.yaml`, user redeploys/reloads
B. **xDS dynamic**: Mendel runs xDS server, Envoy polls for updates (more complex, but no restarts)

For prototype, start with (A) — simpler, no additional server needed.

**Example Envoy Config (generated by Mendel):**

```yaml
static_resources:
  listeners:
  - name: main_listener
    address:
      socket_address:
        address: 0.0.0.0
        port_value: 8080
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: ingress_http
          route_config:
            name: local_route
            virtual_hosts:
            - name: backend
              domains: ["*"]
              routes:
              - match:
                  prefix: "/"
                route:
                  cluster: variation_cluster
          http_filters:
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router

  clusters:
  - name: variation_cluster
    type: STRICT_DNS
    lb_policy: RING_HASH
    ring_hash_lb_config:
      minimum_ring_size: 1024
    load_assignment:
      cluster_name: variation_cluster
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: main.service.internal
                port_value: 8080
          load_balancing_weight: 90
        - endpoint:
            address:
              socket_address:
                address: variation-a.service.internal
                port_value: 8080
          load_balancing_weight: 7
        - endpoint:
            address:
              socket_address:
                address: variation-b.service.internal
                port_value: 8080
          load_balancing_weight: 3
    health_checks:
    - timeout: 5s
      interval: 10s
      unhealthy_threshold: 2
      healthy_threshold: 1
      http_health_check:
        path: "/health"
```

### 2. Deploy Runner

Executes deployment scripts inside Docker containers.

**Docker Image:** `mendel-deploy-tools`
- Base: ubuntu:22.04
- Installed: gcloud CLI, aws CLI, terraform, kubectl, docker CLI, envoy (for config validation)
- Entrypoint: runs user's deploy script

**Execution Flow:**
1. **Validate credentials**: Load `deploy-config.yml`, check all required credentials exist in `project_credentials`. If any missing, fail immediately with clear error listing missing credentials.
2. Clone variation branch to temp dir
3. Mount into container
4. Decrypt and inject credentials as env vars
5. Run deploy script
6. Capture stdout/stderr and exit code
7. Parse output for deployment URL/identifiers
8. Update `deployed_instances` table

### 3. Envoy Config Generator

Generates Envoy configuration from traffic allocation state.

**Input:** Traffic allocations from DB + deployed instance URLs
**Output:** Complete `envoy.yaml` (or xDS response)

**Location:** `internal/envoy/config.go`

### 4. Credential Management & Project Settings

**Storage:** AES-256-GCM encryption, key from `MENDEL_CREDENTIAL_KEY` env var.

**Project Settings Page** (`/p/<project>/settings`):

```
┌─────────────────────────────────────────────────────────────────┐
│ Project Settings                                                │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│ Credentials                                                     │
│ ─────────────────────────────────────────────────────────────── │
│ These are injected as environment variables when running        │
│ deployment scripts. Values are encrypted at rest.               │
│                                                                 │
│ ┌─────────────────────┬────────────────────┬──────────────────┐ │
│ │ Name                │ Value              │ Actions          │ │
│ ├─────────────────────┼────────────────────┼──────────────────┤ │
│ │ GCP_SERVICE_ACCOUNT │ ••••••••••••       │ [Edit] [Delete]  │ │
│ │ DATABASE_URL        │ ••••••••••••       │ [Edit] [Delete]  │ │
│ │ REDIS_URL           │ (not set)          │ [Set]            │ │
│ └─────────────────────┴────────────────────┴──────────────────┘ │
│                                                                 │
│ [+ Add Credential]                                              │
│                                                                 │
│ Required by deploy-config.yml:                                  │
│   ✓ GCP_SERVICE_ACCOUNT                                         │
│   ✓ DATABASE_URL                                                │
│   ✗ REDIS_URL (missing)                                         │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

**Key behaviors:**
- Shows all credentials defined for the project
- Cross-references with `deploy-config.yml` to show which are required vs optional
- Missing required credentials shown with warning
- Values always masked after save (shown as `••••••••••••`)
- Edit reveals a password input field; save re-encrypts
- "Required by" section populated by reading `deploy-config.yml` from repo

### 5. Database Schema

```sql
-- Encrypted credentials for cloud deployments
CREATE TABLE project_credentials (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    encrypted_value BYTEA NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(project_id, name)
);

-- Deployed variation instances in cloud
-- Note: No universal standard exists for cross-cloud service references.
-- Each provider uses different identifiers (Cloud Run URLs, ECS ARNs, Vercel IDs).
-- We use ecosystem + instance_info JSONB for flexibility.
CREATE TABLE deployed_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,
    cloud_ecosystem TEXT NOT NULL,     -- 'gcp-cloudrun', 'aws-ecs', 'vercel', etc.
    url TEXT NOT NULL,                -- internal service URL for Envoy routing
    public_url TEXT,                  -- optional external URL for direct access
    instance_info JSONB,              -- cloud-specific: project, region, service name, etc.
    deployed_at TIMESTAMP NOT NULL DEFAULT NOW(),
    status TEXT NOT NULL DEFAULT 'deploying'
        CHECK (status IN ('deploying', 'running', 'failed', 'terminated')),
    error_message TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_deployed_instances_variation ON deployed_instances(variation_id);
CREATE INDEX idx_deployed_instances_status ON deployed_instances(status);

-- Traffic allocation rules (per hop)
CREATE TABLE traffic_allocations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hop_id UUID NOT NULL REFERENCES hops(id) ON DELETE CASCADE,
    bucket_salt TEXT NOT NULL,  -- combined with hopID and routingKey for bucketing
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(hop_id)
);

-- Traffic slices within an allocation
CREATE TABLE traffic_allocation_slices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    traffic_allocation_id UUID NOT NULL REFERENCES traffic_allocations(id) ON DELETE CASCADE,
    variation_id UUID NOT NULL REFERENCES variations(id) ON DELETE CASCADE,
    fraction REAL NOT NULL CHECK (fraction >= 0.0 AND fraction <= 1.0),
    bucket_order INTEGER NOT NULL,  -- deterministic ordering for consistent bucketing
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(traffic_allocation_id, variation_id),
    UNIQUE(traffic_allocation_id, bucket_order)
);

-- Generated Envoy configs (for audit/rollback)
CREATE TABLE traffic_allocation_envoy_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    config_yaml TEXT NOT NULL,
    generated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    applied_at TIMESTAMP,             -- null until user confirms deployment
    superseded_at TIMESTAMP           -- set when newer config is applied
);
```

### 6. `.mendel/deploy-config.yml` Spec

**Go struct** (source of truth, in `internal/deploy/config.go`):

```go
// DeployConfig defines how Mendel deploys and manages cloud instances.
// Loaded from .mendel/deploy-config.yml in user repositories.
type DeployConfig struct {
    Version int `yaml:"version"` // Schema version, currently 1

    Deploy DeploySettings `yaml:"deploy"` // Deployment script configuration

    Health HealthSettings `yaml:"health"` // Health check configuration

    // Credential names required for deployment. Values are stored
    // encrypted in Mendel's database and injected as env vars.
    Credentials []string `yaml:"credentials"`

    Envoy EnvoySettings `yaml:"envoy"` // Optional Envoy-specific overrides
}

type DeploySettings struct {
    // Path to deployment script, relative to repo root.
    // Script receives credentials as env vars and should output
    // MENDEL_URL=<url> on success.
    Script string `yaml:"script"`

    // Path to teardown script for cleanup on variation termination.
    TeardownScript string `yaml:"teardown_script"`

    // Working directory for scripts. Defaults to repo root.
    WorkingDir string `yaml:"working_dir,omitempty"`

    Output OutputPatterns `yaml:"output"` // Patterns to parse script output
}

type OutputPatterns struct {
    // Regex to extract internal service URL (for Envoy routing).
    // Script should print: MENDEL_URL=https://my-service.run.app
    URLPattern string `yaml:"url_pattern"`

    // Regex to extract public URL (optional, for direct access).
    PublicURLPattern string `yaml:"public_url_pattern,omitempty"`

    // Regex to extract cloud-specific instance identifier.
    // e.g., Cloud Run revision, ECS task ARN
    InstancePattern string `yaml:"instance_pattern,omitempty"`
}

type HealthSettings struct {
    // HTTP path for health checks. Defaults to "/health".
    Endpoint string `yaml:"endpoint"`

    // Seconds to wait for healthy status after deploy. Defaults to 120.
    Timeout int `yaml:"timeout"`

    // Seconds between health check attempts. Defaults to 5.
    Interval int `yaml:"interval"`
}

type EnvoySettings struct {
    // HTTP header used for consistent hashing. Defaults to "X-User-ID".
    HashHeader string `yaml:"hash_header,omitempty"`

    // Health check path for Envoy backend checks. Defaults to "/health".
    HealthPath string `yaml:"health_path,omitempty"`
}
```

## Implementation Phases

### Phase A: Foundation

1. Database migrations for new tables
2. Credential CRUD (`internal/db/credentials.go`)
3. Encryption utilities (`internal/crypto/aes.go`)
4. Traffic allocation CRUD (`internal/db/traffic.go`)
5. Deployed instance CRUD (`internal/db/instances.go`)

### Phase B: Deploy Runner

1. Dockerfile for `mendel-deploy-tools` image
2. Deploy execution logic (`internal/deploy/runner.go`)
3. Credential decryption and injection
4. Output parsing for URLs/identifiers
5. Instance status tracking

### Phase C: Envoy Config Generator

1. Config generator (`internal/envoy/config.go`)
2. Template for basic Envoy setup
3. Generate from traffic allocations + deployed instances
4. Config storage and versioning in DB
5. CLI command: `mendel envoy-config <project>` — outputs config to stdout

### Phase D: Web UI

1. **Project Settings page** (`/p/<project>/settings`):
   - List/add/edit/delete credentials
   - Mask values after save
   - Read `deploy-config.yml` from repo to show required vs missing credentials
   - Link from project nav sidebar
2. Variation detail: "Deploy to Prod" button (disabled with tooltip if credentials missing)
3. Hop detail: Traffic allocation sliders (weights must sum to 1.0)
4. "Generate Envoy Config" button — downloads YAML
5. Dashboard: Show deployed instances and their traffic %

### Phase E: Integration & Lifecycle

1. Wire up variation lifecycle (PENDING → MIGRATING → ACTIVE → DRAINING)
2. Automatic teardown on variation termination
3. Regenerate Envoy config on allocation changes
4. Health check monitoring for deployed instances (via Envoy stats or direct checks)

### Phase F (Future): xDS Server

1. Implement xDS gRPC server for dynamic Envoy updates
2. No manual config regeneration needed
3. Real-time traffic shifting

## User Workflow

1. **Setup (once per project)**
   - Create `.mendel/deploy-config.yml` in repo (lists required credentials)
   - Go to Project Settings in Mendel UI
   - UI shows which credentials are required but missing
   - Add each credential (GCP_SERVICE_ACCOUNT_KEY, DATABASE_URL, etc.)
   - Deploy Envoy in their environment (Cloud Run, k8s, VM, etc.)

2. **Per-variation deployment**
   - Variation reaches PENDING (code generation complete)
   - User clicks "Deploy to Prod" in Mendel UI
   - If credentials missing: error with link to Project Settings
   - If credentials present: Mendel runs deploy script, captures service URL
   - Instance status → `running`

3. **Traffic allocation**
   - User adjusts sliders (main: 90%, var-a: 10%)
   - Mendel generates new Envoy config
   - User downloads and applies config (or xDS pushes automatically)

4. **Selection**
   - User selects winning variation
   - Losing variations: teardown script runs, traffic → 0%
   - Winner merged to main

## Open Questions (to resolve during implementation)

| Area | Question | Tentative Answer |
|------|----------|------------------|
| Envoy deployment | Who deploys Envoy? How? | User deploys; Mendel provides config. Could provide Terraform module. |
| Config application | Manual download or automated push? | Manual for prototype; xDS later |
| Hash consistency | What happens when weights change? | Ring-hash redistributes some users; acceptable for prototype |
| Multi-hop routing | One Envoy per hop or shared? | Start with one Envoy per project, route based on path/header |
| Rollback | Auto-rollback on health check failure? | Defer; manual for now |

## Success Criteria

1. Can store and manage project credentials via UI
2. Deploy runner executes user scripts with credentials injected
3. Deployed instances tracked in DB with status
4. Traffic allocations configurable via UI
5. Envoy config correctly generated from allocations
6. End-to-end: deploy variation → set traffic → see requests routed correctly
