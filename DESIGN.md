# MendelBuild Design Document

> **Status**: Draft - v0.1
> **Last Updated**: 2025-06-08

---

## 1. Overview & Goals

MendelBuild is an experimental way of delivering software that's designed to take full advantage of modern AI/agentic coding while aligning to product and technology goals and staying within long-term budget envelopes. The thesis is that software innovation can happen much more quickly if we embrace a more evolutionary model of innovation (of product, design, and engineering) where many software variations coexist until natural selection decides which is fittest. Rather than carefully planning a single implementation path, MendelBuild enables teams to:

1. **Generate** multiple potential variations automatically
2. **Evaluate** their fitness through measurable criteria
3. **Select** survivors that become ancestors for future iterations

The system shifts bottlenecks from coding speed to decision-making quality, concentrating human involvement on defining objectives and fitness criteria rather than pushing specific implementations forward (as in a more traditional model).

### 1.1 Design Principles

- **Always know the strategy**: Strategy is a first-class citizen and provides the context that agents need to take informed action
- **Always know the budget**: Tokens, infrastructure costs, error budgets, time, and even fractional user traffic are all accounted for and allocated according to ROI
- **Empirical over predictive**: Prefer experimental evidence to forecasting
- **Decisions as first-class citizens**: Every choice is recorded, scored, and auditable; and humans can decide where the line is for human review vs human decision.

### 1.2 Non-Goals (for v1)

- Full autonomy without human oversight
- Support for non-code artifacts (marketing, legal, etc.) — code-first
- Multi-tenant SaaS deployment — single-project focus initially

---

## 2. Key Concepts

This section defines the core primitives and explains how they compose into a coherent system.

### 2.1 The Evolutionary Hierarchy

```
Project
  └── Strategy (OKRs, funding sources)
        └── Roadmap
              └── Hop
                    └── Variation (1..n)
```

**Project**: The top-level container. A Project has a Strategy, one or more Repositories, and connections to Ecosystems where code runs.

**Strategy**: Captures objectives (OKRs), funding sources, and the Roadmap. Strategies can nest (sub-strategies) for organizational alignment.

**Roadmap**: A DAG of Hops. Unlike traditional roadmaps, less about wall-clock time than sequencing and dependencies.

### 2.2 Hops and Variations

**Hop**: The fundamental unit of evolutionary experimentation. A Hop defines:
- **Commentary**: Context (in English) about the Hop; particularly if it might help with qualitative pruning or scoring (see below)
- **Pruner**: A function that rejects unfit Variations (binary pass/fail)
- **Scorer**: A function that ranks surviving Variations (continuous fitness score)
- **Budget**: Resource limits (tokens, dollars, error budget, etc.)

A Hop does *not* specify *how* to achieve the goal — that's what Variations are for.

**Variation**: A concrete implementation attempt within a Hop. Each Variation has:
- A location in a Repository (specific SHA / commit)
- An optional "deployment" in an Ecosystem (canonical example would be "a pod in prod k8s")
- A lifecycle progression (see longer lifecycle section below)
- Evaluation results against Hop Scorer/Pruner

Each Hop can have many Variations; Variations are filtered using Hop Pruners, assessed via Hop Scorers, and at most one is "selected" and merged back to `main`. Note that Variations (and Hops) can be long-lived.

### 2.3 Decisions and the Decision Queue

**Decision**: A choice point in the system. Every Decision has:
- **Kind**: Pass/Fail, Choose One, Choose Many
- **Details**: Based on the type and 'Kind' of decision, a human- and agent-readable summary of the option or options for the Decision (perhaps with hyperlinks out of the MendelBuild system for more detail)
- **Objectivity score**: How objectively measurable is this decision? (0.0–1.0)
- **Importance score**: How much does this affect strategic goals? (0.0–1.0)
- **Audit log**: Who/what decided, when, with what rationale


**Decision Queue**: An abstraction representing all pending (and historical) decisions. This is *not* exclusively a queue for human decisions — it's a unified interface where decisions are resolved by the appropriate actor. The more important and subjective (i.e., non-objective) decisions are more likely to require human involvement, but the thresholds can be configured.

Eventually, humans with expertise in a particular product or tech area should be the ones to handle decisiosn in those areas, much like code review assignment works/worked in the past, before agentic coding went mainstream.

### 2.4 Repositories and Ecosystems

**Repository**: A versioned store of artifacts (code, design files, etc.). Each Repository has:
- A type (code, design, documentation, etc.)
- An interface for branch creation, commits, and abandonment
- A concept of `main` — the current blessed state

**Ecosystem**: A runtime environment where Variations can be deployed and evaluated. Examples:
- A Kubernetes cluster
- A Vercel/Netlify deployment
- A Squarespace site
- An AdWords campaign

Ecosystems have HealthFuncs that provide baseline availability and quality signals.

### 2.5 Budgets and Funding

**FundingSource**: A pool of resources (dollars, tokens, cloud credits) with a strategic allocation.

**BudgetAllocation**: A slice of funding assigned to a specific Hop. Tracks:
- Limit (hard ceiling)
- Spending (actual, broken down by line item)
- Forecast (predicted spend)

Budget enforcement is soft by default: every time control returns to MendelBuild, budgets are checked. Exceeding a budget triggers a Decision (pause, kill, or continue with human approval).

### 2.6 How It All Fits Together

```
┌────────────────────────────────────────────────────────────────────┐
│                           PROJECT                                  │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ STRATEGY (OKRs + Funding)                                    │  │
│  │   ┌────────────────────────────────────────────────────────┐ │  │
│  │   │ ROADMAP (DAG of Hops)                                  │ │  │
│  │   │   ┌───────────┐   ┌──────────┐   ┌──────────┐          │ │  │
│  │   │   │    Hop    |──▶│    Hop   │──▶│    Hop   │ ──▶ ...  │ │  │
│  │   │   │  ┌─────┐  │   │ ┌─────┐  │   │ ┌─────┐  │          │ │  │
│  │   │   │  │Var A│  │   │ │Var X│  │   │ │Var L│  │          │ │  │
│  │   │   │  │Var B│  │   │ │Var Y│  │   │ │Var M│  │          │ │  │
│  │   │   │  │Var C│  │   │ └─────┘  │   │ |Var N|  │          │ │  │
│  │   │   │  └─────┘  │   └────────-─┘   | |Var O|  |          │ │  │
│  │   │   └-────────-─┘                  | └-----┘  |          │ │  │
│  │   |                                  └----------┘          | │  │
│  │   └────────────────────────────────────────────────────────┘ │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                    │
│    ┌──────────────┐  ┌──────────────┐  ┌──────────────────────┐    │
│    │ Repositories │  │  Ecosystems  │  │   Decision Queue     │    │
│    │ (code, main) │  │  (k8s, web)  │  │ (pending decisions)  │    │
│    └──────────────┘  └──────────────┘  └──────────────────────┘    │
└──────────────────────────────────────────────────────────────---───┘
```

**Flow**:
1. Strategy defines what success looks like (OKRs) and available resources (Funding)
2. Roadmap sequences Hops based on dependencies
4. Each Hop spawns multiple Variations (via agents)
5. Pruner kills unfit Variations; Scorer ranks survivors
6. Decisions enter the queue; resolved by agents or humans based on scores
7. Selected Variation merges to main; others are cleaned up

---

## 3. Architecture Layers

### 3.1 MendelBuild Core (Go + SQL)

The central control plane. Responsibilities:
- Persist all primitives (Projects, Hops, Variations, Decisions, etc.)
- Orchestrate Hop execution and Variation lifecycle
- Enforce budgets and track spending
- Expose APIs for agents, UIs, and the routing SDK

**Database**: A relational SQL database (Postgres initially, but schema should be portable). Stores:
- All entity state
- Decision audit logs
- Migration tracking (see 3.4)

### 3.2 Variation Runtime

Infrastructure for running multiple Variations simultaneously:
- **Traffic Router**: Directs requests to appropriate Variation based on routing key
- **Migration Manager**: Tracks schema changes per-Variation, handles cleanup
- **Metrics Collector**: Gathers evaluation data from Ecosystems

### 3.3 Agent Adapters

Thin interfaces for AI agents (Claude Code, etc.) to:
- Receive Hop specifications and context
- Return Variation artifacts (code, commits)
- Report progress and cost consumption

Agents are stateless from MendelBuild's perspective — all durable state lives in Core.

---

## 4. Traffic Routing SDK

A lightweight, LaunchDarkly-inspired SDK for consistent traffic routing across Variations.

### 4.1 Design Goals

- Minimal API surface (one primary function)
- Consistent bucketing (same key always routes to same Variation)
- Local evaluation (no per-request network calls)
- Language-agnostic protocol (Go reference, then JS/Python/etc.)

### 4.2 Public API Surface

```go
// MendelClient is the entry point for routing decisions
type MendelClient interface {
    // GetVariation returns the Variation ID for a given Hop and routing key
    GetVariation(hopID string, routingKey string) (string, error)

    // Close cleanly shuts down the client
    Close() error
}

// Config for initializing the client
type MendelConfig struct {
    ProjectID   string
    Endpoint    string        // MendelBuild Core API
    SyncInterval time.Duration // How often to refresh rules
}

// NewClient creates a MendelClient
func NewClient(cfg MendelConfig) (MendelClient, error)
```

### 4.3 Routing Key & Consistent Bucketing

The **routing key** determines which Variation a request sees. Choose keys that ensure:
- **Consistency**: Same user/session/entity always sees same Variation
- **Independence**: Different Hops can use different keys without correlation

**Bucketing Algorithm** (LaunchDarkly-inspired):
```
bucket = SHA1(hopID + "." + salt + "." + routingKey)[0:15]
bucket_int = parse_hex(bucket)
bucket_pct = bucket_int / 0xFFFFFFFFFFFFFFF  // 0.0 to 1.0
```

Traffic allocation rules (e.g., "Variation A: 50%, Variation B: 50%") are fetched from Core and cached locally.

### 4.4 Rule Sync Protocol

1. On init, SDK fetches current rules from MendelBuild Core
2. Rules cached in-memory
3. Background goroutine polls for updates (default: 30s)
4. Graceful fallback if Core unreachable (use cached rules)

TODO: Consider streaming (SSE/WebSocket) for real-time updates in v2.

---

## 5. Variation Lifecycle

TODO: Formalize state machine. Reference k8s pod lifecycle and LaunchDarkly flag states.

### 5.1 Proposed States

```
┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
│ CREATING │───▶│ PENDING  │───▶│  ACTIVE  │───▶│ SELECTED │
└──────────┘    └────┬─────┘    └────┬─────┘    └──────────┘
                     │               │
                     ▼               ▼
                ┌──────────┐    ┌──────────┐
                │  FAILED  │    │ DRAINING │
                └──────────┘    └────┬─────┘
                                     ▼
                                ┌──────────┐
                                │ TERMINATED│
                                └──────────┘
```

- **CREATING**: Agent is generating the Variation
- **PENDING**: Code exists, awaiting promotion decision
- **ACTIVE**: Receiving live traffic in an Ecosystem
- **DRAINING**: Traffic being shifted away, cleanup pending
- **TERMINATED**: Fully cleaned up (migrations reverted, resources freed)
- **FAILED**: Pruned or errored out
- **SELECTED**: Chosen as the winner, merged to main

TODO: Define triggers for each transition. Can transitions go backwards (e.g., ACTIVE → PENDING for pause)?

---

## 6. Data Model (SQL Schema Sketches)

TODO: Full schema. Initial sketch:

```sql
-- Core entities
CREATE TABLE projects (...);
CREATE TABLE strategies (...);
CREATE TABLE roadmaps (...);
CREATE TABLE deliverables (...);
CREATE TABLE hops (...);
CREATE TABLE hop_goals (...);
CREATE TABLE variations (...);

-- Decisions
CREATE TABLE decisions (
    id UUID PRIMARY KEY,
    kind TEXT NOT NULL,
    objectivity_score REAL NOT NULL,
    importance_score REAL NOT NULL,
    status TEXT NOT NULL,  -- pending, resolved
    resolved_by TEXT,      -- agent ID or user ID
    resolved_at TIMESTAMP,
    rationale TEXT
);

-- Migrations (per-variation schema changes)
CREATE TABLE variation_migrations (
    id UUID PRIMARY KEY,
    variation_id UUID REFERENCES variations(id),
    direction TEXT NOT NULL,  -- up, down
    sql_up TEXT NOT NULL,
    sql_down TEXT NOT NULL,
    applied_at TIMESTAMP,
    reverted_at TIMESTAMP
);

-- Budget tracking
CREATE TABLE budget_allocations (...);
CREATE TABLE budget_spend_log (...);
```

---

## 7. Open Questions / TBD

| Area | Question | Notes |
|------|----------|-------|
| **Variation Lifecycle** | Exact state transitions and triggers? | Reference k8s, LaunchDarkly |
| **Scorer/Pruner** | How to specify? DSL, code reference, plugin? | Must be serializable/distributable |
| **Migration Cleanup** | Finalizer semantics for killed Variations? | Backup before drop? |
| **Repository Interface** | Abstraction for branch/commit/abandon? | Git-first, but pluggable |
| **Budget Enforcement** | Hard stop vs soft limit vs graceful degradation? | Currently: soft with Decision |
| **Incident Detection** | OTel integration specifics? | HealthFuncs on Ecosystem? |
| **Rollback** | Automatic vs Decision-gated? | Importance/objectivity scoring |
| **Multi-Hop Transactions** | Atomicity across dependent Hops? | Deferred to v2? |
| **Agent Protocol** | Exact interface for agent adapters? | Start with Claude Code |

---

## 8. Glossary

| Term | Definition |
|------|------------|
| **Hop** | A unit of evolutionary experimentation with goals, budget, and multiple competing Variations |
| **Variation** | A concrete implementation attempt within a Hop |
| **Decision** | A choice point requiring resolution (by agent or human) |
| **Ecosystem** | A runtime environment where Variations are deployed |
| **Pruner** | Function that rejects unfit Variations (binary) |
| **Scorer** | Function that ranks surviving Variations (continuous) |
| **Routing Key** | Identifier used to consistently assign requests to Variations |

---

## Appendix A: References

- [LaunchDarkly SDK Architecture](https://launchdarkly.com/docs/home/getting-started/architecture)
- [LaunchDarkly Flag Evaluation Rules](https://launchdarkly.com/docs/sdk/concepts/flag-evaluation-rules)
- Blog: "Natural Selection, But in Production" — foggyfuture.substack.com
