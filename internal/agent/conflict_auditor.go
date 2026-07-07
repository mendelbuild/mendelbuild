package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/bhs/mendelbuild/internal/domain"
)

// ConflictAuditInput contains all variation migrations for a hop to audit.
type ConflictAuditInput struct {
	HopName    string                    `json:"hop_name" desc:"Name of the hop being audited"`
	Variations []VariationMigrationInput `json:"variations" desc:"All variations with their migration instructions"`
}

// VariationMigrationInput represents one variation's migration for audit.
type VariationMigrationInput struct {
	VariationID      string `json:"variation_id" desc:"UUID of the variation"`
	VariationName    string `json:"variation_name" desc:"Human-readable name of the variation"`
	UpInstructions   string `json:"up_instructions" desc:"Instructions to apply the migration"`
	DownInstructions string `json:"down_instructions" desc:"Instructions to revert the migration"`
	Notes            string `json:"notes,omitempty" desc:"Path to migration files in code repo"`
}

// ConflictAuditResponse is the structured output from conflict analysis.
type ConflictAuditResponse struct {
	HasConflicts bool              `json:"has_conflicts" desc:"True if any conflicts were detected between variations"`
	Conflicts    []MigrationConflict `json:"conflicts" desc:"List of detected conflicts. Empty if has_conflicts is false."`
	Summary      string            `json:"summary" desc:"Brief summary of the audit result for display to user"`
}

// MigrationConflict describes a specific conflict between variations.
type MigrationConflict struct {
	VariationIDs   []string `json:"variation_ids" desc:"UUIDs of the conflicting variations (2 or more)"`
	VariationNames []string `json:"variation_names" desc:"Names of the conflicting variations"`
	ConflictType   string   `json:"conflict_type" desc:"Type of conflict: 'table_collision', 'column_collision', 'incompatible_types', 'down_migration_unsafe', 'other'"`
	Description    string   `json:"description" desc:"Clear explanation of why these variations conflict"`
	AffectedSchema string   `json:"affected_schema" desc:"The table/column/schema element that is in conflict"`
}

// ConflictAuditResponseSchema returns the JSON schema for structured output.
func ConflictAuditResponseSchema() json.RawMessage {
	return SchemaFromType(reflect.TypeOf(ConflictAuditResponse{}))
}

const conflictAuditSystemPrompt = `You are an expert database migration auditor. Your job is to analyze migration instructions from multiple code variations and detect conflicts that would prevent safe parallel demoing or sequential application.

CONFLICT TYPES TO DETECT:

1. TABLE COLLISION: Multiple variations create the same table with different structures
2. COLUMN COLLISION: Multiple variations add the same column to a table with different types/constraints
3. INCOMPATIBLE TYPES: Variations assume different data types for the same schema element
4. DOWN MIGRATION UNSAFE: A variation's DOWN migration would destroy data created by another variation
5. OTHER: Any other conflict that would prevent safe operation

WHAT IS NOT A CONFLICT:
- Variations touching completely different tables
- Variations that make identical schema changes (these are compatible)
- Variations that touch the same table but different columns (usually fine)

ANALYSIS APPROACH:
1. Parse each variation's UP and DOWN instructions
2. Identify tables, columns, and types being created/modified/dropped
3. Compare across variations for collisions
4. Pay special attention to DOWN migrations - they must not damage data from other variations
5. Be conservative: if you're unsure, flag it as a potential conflict

OUTPUT:
- Set has_conflicts to true only if you find actual conflicts
- Provide clear, actionable descriptions of each conflict
- Include specific table/column names in affected_schema
- The summary should be 1-2 sentences suitable for display to the user`

// AuditHopVariationConflicts analyzes all variation migrations for a hop and detects conflicts.
func (c *Client) AuditHopVariationConflicts(ctx context.Context, input ConflictAuditInput) (*ConflictAuditResponse, error) {
	// Skip audit if no variations have migrations
	hasMigrations := false
	for _, v := range input.Variations {
		if v.UpInstructions != "" || v.DownInstructions != "" {
			hasMigrations = true
			break
		}
	}
	if !hasMigrations {
		return &ConflictAuditResponse{
			HasConflicts: false,
			Conflicts:    []MigrationConflict{},
			Summary:      "No migrations to audit - all variations are migration-free.",
		}, nil
	}

	// Build the user message with all migration details
	inputJSON, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal input: %w", err)
	}

	userMessage := fmt.Sprintf(`Analyze these variation migrations for conflicts:

%s

Identify any conflicts that would prevent safe demoing of these variations.`, string(inputJSON))

	messages := []Message{
		{Role: "user", Content: userMessage},
	}

	// Use Haiku for cost-effectiveness - this is a focused analysis task
	originalModel := c.model
	c.model = "claude-haiku-4-5"
	defer func() { c.model = originalModel }()

	resp, err := c.SendMessageWithSchema(ctx, conflictAuditSystemPrompt, messages, 2000, ConflictAuditResponseSchema())
	if err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()
	var result ConflictAuditResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("parse response: %w (content: %s)", err, content)
	}

	return &result, nil
}

// BuildConflictAuditInput constructs the input for conflict auditing from domain types.
func BuildConflictAuditInput(hop *domain.Hop, variations []domain.Variation, migrations map[string]*domain.VariationMigration) ConflictAuditInput {
	input := ConflictAuditInput{
		HopName:    hop.Name,
		Variations: make([]VariationMigrationInput, 0, len(variations)),
	}

	for _, v := range variations {
		vi := VariationMigrationInput{
			VariationID:   v.ID.String(),
			VariationName: v.Name,
		}

		if m, ok := migrations[v.ID.String()]; ok && m != nil {
			vi.UpInstructions = m.UpInstructions
			vi.DownInstructions = m.DownInstructions
			if m.Notes != nil {
				vi.Notes = *m.Notes
			}
		}

		input.Variations = append(input.Variations, vi)
	}

	return input
}
