# Phase 6: OKR Editor

## Overview

Added inline editing capabilities for Objectives and Key Results with hierarchical objective structure, many-to-many KR sharing, OKR tuning feedback, and soft-delete for auditability.

## Key Features Implemented

1. **Hierarchical Objectives**: Objectives can have children via `parent_id`, supporting organizational alignment where sub-objectives roll up to parent objectives
2. **Many-to-Many KRs**: Key Results can be shared across multiple Objectives via the `objective_key_result_pairs` junction table
3. **Inline Editing**: Click pencil icons to toggle between display and edit mode, no separate edit pages
4. **OKR Tuning**: Uses Claude Haiku for cost-effective quality feedback with scores (0.0-1.0) and hover tooltips
5. **Soft Delete**: All deletions set `deleted_at` timestamp rather than removing rows for audit trail
6. **Breadcrumb Navigation**: Navigate nested objectives with breadcrumb trail

## Files Created

### Database Migration
- `schema/migrations/007_okr_editor.up.sql` - Adds parent_id, tune_score, tune_feedback, deleted_at columns; creates junction table
- `schema/migrations/007_okr_editor.down.sql` - Reverts migration changes

### Agent
- `internal/agent/okr_tuner.go` - OKR Tuner agent using Claude Haiku for quality feedback

### Web
- `internal/web/handlers_okr.go` - CRUD handlers for objectives and key results
- `internal/web/templates/okr_editor.html` - Inline editing UI with breadcrumbs and tuning

## Files Modified

### Schema
- `schema/full.sql` - Updated to reflect migration 007 changes

### Domain
- `internal/domain/types.go` - Added ParentID, TuneScore, TuneFeedback, DeletedAt to Objective; changed KeyResult to use StrategyID instead of ObjectiveID with new tuning and soft-delete fields

### Database
- `internal/db/queries.go` - Updated existing queries for new schema; added ~25 new query functions for hierarchical objectives, junction table operations, and tuning

### Agent
- `internal/agent/types.go` - Added OKR tuning types (OKRTuneInput, OKRTuneResponse, ItemTuning, etc.)
- `internal/agent/schema.go` - Added OKRTuneResponseSchema()

### Web
- `internal/web/server.go` - Added OKR routes (10 new routes) and API endpoint for tuning
- `internal/web/handlers.go` - Added template functions (tuneScoreClass, mul100, derefString)
- `internal/web/templates/strategy.html` - Added "Edit OKRs" link
- `internal/web/templates/layout.html` - Added "OKRs" link to navigation bar

## Database Schema Changes

### objectives table additions:
- `parent_id UUID` - References parent objective for hierarchy
- `tune_score REAL` - Quality score 0.0-1.0
- `tune_feedback TEXT` - Brief feedback from tuner
- `deleted_at TIMESTAMP` - Soft delete timestamp

### key_results table changes:
- Removed `objective_id` column
- Added `strategy_id UUID NOT NULL` - Links directly to strategy
- Added `tune_score REAL` - Quality score 0.0-1.0
- Added `tune_feedback TEXT` - Brief feedback from tuner
- Added `deleted_at TIMESTAMP` - Soft delete timestamp

### New table:
```sql
CREATE TABLE objective_key_result_pairs (
    objective_id UUID NOT NULL REFERENCES objectives(id),
    key_result_id UUID NOT NULL REFERENCES key_results(id),
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (objective_id, key_result_id)
);
```

### New indexes:
- `idx_objectives_parent ON objectives(parent_id) WHERE deleted_at IS NULL`
- `idx_objectives_deleted ON objectives(deleted_at)`
- `idx_key_results_strategy ON key_results(strategy_id) WHERE deleted_at IS NULL`
- `idx_key_results_deleted ON key_results(deleted_at)`
- `idx_okr_junction_kr ON objective_key_result_pairs(key_result_id)`

## Routes Added

### Project-scoped routes (`/p/{projectID}/...`)
- `GET /okr` - OKR editor root view
- `GET /okr/objectives/{objectiveID}` - Objective detail view (with breadcrumbs)
- `POST /okr/objectives` - Create objective
- `POST /okr/objectives/{objectiveID}` - Update objective
- `POST /okr/objectives/{objectiveID}/delete` - Soft delete objective
- `POST /okr/key-results` - Create key result
- `POST /okr/key-results/{keyResultID}` - Update key result
- `POST /okr/key-results/{keyResultID}/delete` - Soft delete key result
- `POST /okr/objectives/{objectiveID}/link-kr` - Link KR to objective
- `POST /okr/objectives/{objectiveID}/unlink-kr/{keyResultID}` - Unlink KR (soft-deletes if orphaned)

### API routes
- `POST /api/projects/{projectID}/okr/tune` - Batch tune all untuned objectives and key results

## OKR Tuning Implementation

- Uses Claude 3.5 Haiku (`claude-3-5-haiku-20241022`) for cost-effectiveness
- Evaluates quality based on:
  - **Objectives**: Clarity, inspiration, achievability, specificity
  - **Key Results**: Measurability, clear units, ambitious but realistic targets
- Scoring: 0.0-1.0 scale
  - 0.8-1.0: Excellent (green)
  - 0.6-0.8: Good (blue)
  - 0.4-0.6: Needs work (yellow)
  - 0.0-0.4: Poor (red)
- Tuning is cleared on edit to ensure stale scores aren't shown

## UI Behavior

### Inline Editing
- Click pencil icon to show edit form
- Save submits form via POST, page reloads
- Cancel hides form, shows display

### Delete Confirmation
- JavaScript confirm() dialog before deletion
- Soft delete (sets deleted_at, doesn't remove data)

### Tuning Display
- Score badge with color coding
- Hover tooltip shows feedback text
- "Tune All Unscored Items" button triggers batch tuning
- Spinner shown while tuning in progress

### Many-to-Many KR Linking
- KRs are created linked to an objective
- Can unlink from objective (KR soft-deleted if orphaned)
- KRs belong to strategy, can be linked to multiple objectives within that strategy

## Verification Steps

1. Navigate to Strategy page, click "Edit OKRs" link or "OKRs" in nav
2. See list of root objectives with their key results
3. Click "Add Objective" - creates new objective
4. Click pencil on objective - inline edit, save changes
5. Click into nested objective - see breadcrumb navigation
6. Click "Tune All Unscored Items" - see spinner, then score badges
7. Hover over score - see feedback tooltip
8. Click delete on objective - confirmation dialog, soft delete
9. Check database: deleted items have deleted_at set, not removed
