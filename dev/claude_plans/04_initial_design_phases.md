# Phase 4: Variation Selection

## Overview

After code generation completes for all Variations in a Hop, a human selects at most one winner. The winning Variation is merged to main, losers are marked rejected (branches kept), and dependent Hops are activated.

**Key flow:**
1. All Variations reach `pending` status (code generated, tests passed)
2. System creates `variation_selection` Decision
3. Human reviews Variations using AI-generated evaluation criteria
4. Human selects winner → merged to main, hop marked `completed`
5. Dependent Hops automatically activated

---

## Key Design Decisions

1. **Human selection**: For the foreseeable future, humans select winners (not AI). AI provides "apples-to-apples" evaluation criteria to assist.

2. **Evaluation criteria timing**: Generated during variation proposal (not hop creation), stored as JSONB on the Hop.

3. **Selection Decision timing**: Created when ANY Variation reaches `pending`, but selection UI only enabled when ALL Variations are done (none in `creating`).

4. **Variation final statuses**: `merged` (winner) or `rejected` (losers). Error/terminated variations keep their status.

5. **Git merge**: Winner branch merged to main via clone → merge → push workflow.

6. **Dependent Hop activation**: Automatic when a Hop completes - any Hop whose dependencies are all `completed` is set to `active`.

7. **Reject all option**: If no Variation is acceptable, human can reject all and return Hop to `active` for new proposals.

---

## New/Modified Files

### Migration
- `schema/migrations/006_variation_selection.up.sql` / `.down.sql`
  - `hops.evaluation_criteria` JSONB column
  - Updated `hops.status` constraint: added `selecting`, `rejected`
  - Updated `variations.status` constraint: added `merged`, `rejected`
  - Updated `decisions.kind` constraint: added `variation_selection`

### Domain Types (`internal/domain/types.go`)
- `HopStatusSelecting`, `HopStatusRejected`
- `VariationStatusMerged`, `VariationStatusRejected`
- `DecisionKindVariationSelection`
- `Hop.EvaluationCriteria` changed to `json.RawMessage`

### Agent (`internal/agent/`)
- `evaluation_criteria.go` - AI generates criteria for comparing Variations
- `types.go` - `EvaluationCriterion`, `EvaluationCriteria`, `EvaluationCriteriaInput`
- `schema.go` - `EvaluationCriteriaResponseSchema()`

### Database (`internal/db/queries.go`)
- `UpdateHopEvaluationCriteria(ctx, hopID, criteria json.RawMessage)`
- `GetHopsNeedingSelectionDecision(ctx)` - Hops with pending variations needing a Decision
- `GetHopsReadyForSelection(ctx)` - Hops ready to transition to `selecting`
- `ActivateDependentHops(ctx, completedHopID)` - Activate Hops whose deps are done
- `GetDecisionsByProject(ctx, projectID)` - Fixed to include hop/variation decisions

### Git (`internal/git/git.go`)
- `Fetch(ctx, authToken)` - Fetch from remote
- `MergeRemoteBranch(ctx, branchName, authToken)` - Merge remote branch to current

### Web Server (`internal/web/server.go`)
- Background worker processes:
  - `processSelectionDecisions()` - Creates Decisions for hops with pending variations
  - `processHopStatusUpdates()` - Updates hop status to `selecting` when all done
- `createSelectionDecision(ctx, hop)` - Creates the selection Decision with variation info

### Handlers (`internal/web/handlers_decision.go`)
- `handleSelectWinner` - Merges winner, updates statuses, activates dependents
- `handleRejectAllVariations` - Rejects all, returns hop to `active`
- `mergeWinnerToMain(ctx, hop, winner)` - Git clone/merge/push workflow
- Updated `handleDecisionDetail` for `variation_selection` kind

### Templates
- `decision_selection.html` - Selection UI with:
  - Evaluation criteria display
  - Radio buttons to select winner (only when all variations done)
  - "Reject All & Request New Variations" button
  - Variation cards showing status, approach, commit ref

---

## Database Schema Changes

```sql
-- Hops: add evaluation_criteria and new statuses
ALTER TABLE hops ADD COLUMN evaluation_criteria JSONB;
ALTER TABLE hops DROP CONSTRAINT hops_status_check;
ALTER TABLE hops ADD CONSTRAINT hops_status_check
    CHECK (status IN ('pending', 'active', 'selecting', 'completed', 'rejected', 'abandoned'));

-- Variations: add merged/rejected statuses
ALTER TABLE variations ADD CONSTRAINT variations_status_check
    CHECK (status IN ('creating', 'pending', 'migrating', 'active', 'draining',
                      'error', 'terminated', 'pruned', 'selected', 'merged', 'rejected'));

-- Decisions: add variation_selection kind
ALTER TABLE decisions DROP CONSTRAINT decisions_kind_check;
ALTER TABLE decisions ADD CONSTRAINT decisions_kind_check
    CHECK (kind IN ('pass_fail', 'choose_one', 'choose_many', 'roadmap_review',
                    'variation_review', 'variation_selection'));
```

---

## Evaluation Criteria Structure

Stored as JSONB in `hops.evaluation_criteria`:

```json
{
  "criteria": [
    {
      "name": "Code Clarity",
      "description": "How readable and maintainable is the code?",
      "measurable": false,
      "weight": 4
    },
    {
      "name": "Test Coverage",
      "description": "Percentage of code covered by tests",
      "measurable": true,
      "weight": 3
    }
  ],
  "rationale": "These criteria focus on long-term maintainability...",
  "tradeoffs": "Higher test coverage may come at the cost of..."
}
```

Generated during `handleProposeVariations` if not already present.

---

## Workflow States

### Hop Status Flow
```
pending → active → selecting → completed
                 ↘ rejected (if all variations rejected)
```

### Variation Status Flow
```
creating → pending → merged (winner)
                  ↘ rejected (losers)
         → error/terminated (failures, unchanged)
```

### Selection Decision Flow
1. Created when first Variation reaches `pending`
2. `CanSelect = false` until all Variations done (none `creating`)
3. Human selects winner → Decision resolved as `approved`
4. Human rejects all → Decision resolved as `rejected`

---

## Background Worker Logic

Every 5 seconds, the worker runs:

1. **processCreatingVariations()** - Triggers code generation (existing)

2. **processSelectionDecisions()** - For hops in `active`/`selecting` with pending variations but no selection Decision, create one

3. **processHopStatusUpdates()** - For `active` hops where all variations are done (none `creating`) and at least one is `pending`, update to `selecting`

---

## Dependent Hop Activation

When a Hop is marked `completed`:

```sql
UPDATE hops SET status = 'active'
WHERE status = 'pending'
  AND NOT EXISTS (
    SELECT 1 FROM hop_dependencies hd
    JOIN hops dep ON hd.depends_on_hop_id = dep.id
    WHERE hd.hop_id = hops.id
      AND dep.status != 'completed'
  )
```

This activates any Hop whose dependencies are all completed.

---

## Verification

1. Complete code generation for all Variations in a Hop
2. Navigate to Decisions page → see `variation_selection` Decision
3. Click into Decision → see evaluation criteria and variation cards
4. Wait for all Variations to complete (status badges turn green)
5. Select a winner → verify branch merged to main
6. Check hop status → `completed`
7. Check dependent hops → should be `active` if deps satisfied
8. Alternative: click "Reject All" → hop returns to `active`
