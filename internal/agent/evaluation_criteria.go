package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

const evaluationCriteriaSystemPrompt = `You are an evaluation criteria generator for MendelBuild, an evolutionary software development system.

Your task is to generate evaluation criteria that will be used to compare multiple implementation "Variations" of a single "Hop" (development task). The criteria should enable apples-to-apples comparison between different approaches.

Guidelines:
1. Generate 3-6 criteria that are relevant to the specific hop
2. Include a mix of measurable (objective) and qualitative (subjective) criteria
3. Weight more important criteria higher (1-5 scale)
4. Consider the hop's objectives and what success looks like
5. Think about common software tradeoffs: simplicity vs flexibility, performance vs maintainability, etc.
6. Criteria should help distinguish between genuinely different approaches, not just code style

Common criteria to consider (adapt based on hop context):
- Code clarity and maintainability
- Test coverage and quality
- Performance characteristics
- Error handling robustness
- Alignment with existing codebase patterns
- Extensibility for future requirements
- Implementation completeness`

// EvaluationCriteriaGenerator generates evaluation criteria for comparing Variations.
type EvaluationCriteriaGenerator struct {
	client *Client
}

// NewEvaluationCriteriaGenerator creates a new generator.
func NewEvaluationCriteriaGenerator(client *Client) *EvaluationCriteriaGenerator {
	return &EvaluationCriteriaGenerator{client: client}
}

// GenerateCriteria generates evaluation criteria for a hop.
func (g *EvaluationCriteriaGenerator) GenerateCriteria(ctx context.Context, input EvaluationCriteriaInput) (*EvaluationCriteria, int, error) {
	inputJSON, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return nil, 0, fmt.Errorf("marshal input: %w", err)
	}

	userMessage := fmt.Sprintf(`Generate evaluation criteria for comparing Variations of this hop:

%s

Create criteria that will help a human decide which implementation approach is best.`, string(inputJSON))

	resp, err := g.client.SendMessageWithSchema(ctx, evaluationCriteriaSystemPrompt, []Message{
		{Role: "user", Content: userMessage},
	}, 2048, EvaluationCriteriaResponseSchema())
	if err != nil {
		return nil, 0, fmt.Errorf("send message: %w", err)
	}

	content := resp.GetTextContent()
	var result EvaluationCriteriaResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, 0, fmt.Errorf("parse response: %w (content: %s)", err, content)
	}

	return &result.Criteria, resp.Usage.TotalTokens(), nil
}

// FormatCriteriaAsText converts evaluation criteria to a human-readable text format.
func FormatCriteriaAsText(criteria *EvaluationCriteria) string {
	if criteria == nil || len(criteria.Criteria) == 0 {
		return ""
	}

	result := "## Evaluation Criteria\n\n"
	for i, c := range criteria.Criteria {
		measurable := ""
		if c.Measurable {
			measurable = " (measurable)"
		}
		result += fmt.Sprintf("%d. **%s** (weight: %d/5)%s\n   %s\n\n",
			i+1, c.Name, c.Weight, measurable, c.Description)
	}

	result += fmt.Sprintf("### Rationale\n%s\n\n", criteria.Rationale)
	result += fmt.Sprintf("### Expected Tradeoffs\n%s\n", criteria.Tradeoffs)

	return result
}
