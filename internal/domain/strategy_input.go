package domain

import "encoding/json"

// StrategyInput represents the JSON input format for loading a strategy.
type StrategyInput struct {
	Project     string         `json:"project"`
	Strategy    StrategyDef    `json:"strategy"`
	Repository  RepositoryDef  `json:"repository"`
	Credentials CredentialsDef `json:"credentials,omitempty"`
}

// CredentialsDef holds project-wide credentials.
type CredentialsDef struct {
	AnthropicAPIKey string `json:"anthropic_api_key,omitempty"`
}

// StrategyDef defines a strategy with objectives and funding.
type StrategyDef struct {
	Name       string          `json:"name"`
	Objectives []ObjectiveDef  `json:"objectives"`
	Funding    []FundingDef    `json:"funding"`
}

// ObjectiveDef defines an objective with key results.
type ObjectiveDef struct {
	ID          string         `json:"id"`          // Stable user-provided ID for upsert
	Description string         `json:"description"`
	KeyResults  []KeyResultDef `json:"key_results"`
}

// KeyResultDef defines a key result.
type KeyResultDef struct {
	ID          string `json:"id"` // Stable user-provided ID for upsert
	Description string `json:"description"`

	// The target, structured [037]: the comparison to make, the number to make
	// it against, and what that number counts. The unit is for display only, so
	// it carries any qualifier ("ms p99", "signups per week").
	TargetComparator string  `json:"target_comparator"`
	TargetValue      float64 `json:"target_value"`
	TargetUnit       string  `json:"target_unit"`

	TargetDate *string `json:"target_date,omitempty"` // ISO8601 format
}

// FundingDef defines a USD budget for a strategy.
//
// Budgets are denominated in dollars, not tokens: token prices differ ~10x
// across models and cache reads and writes are priced differently again, so a
// token budget does not describe a fixed amount of value.
type FundingDef struct {
	Name      string  `json:"name"`
	AmountUSD float64 `json:"amount_usd"`

	// The period this budget is meant to cover (ISO8601 dates). Optional, but
	// without it Mendel can report spend and not whether it is on pace.
	PeriodStart *string `json:"period_start,omitempty"`
	PeriodEnd   *string `json:"period_end,omitempty"`

	// KeyResultIDs names the key results this budget is meant to move, using
	// the stable IDs declared under key_results in this same file. This is what
	// ties a budget to OKR milestones: those key results carry target dates.
	KeyResultIDs []string `json:"key_result_ids,omitempty"`
}

// RepositoryDef defines the repository configuration.
type RepositoryDef struct {
	URL        string          `json:"url"`
	MainBranch string          `json:"main_branch"`
	Config     json.RawMessage `json:"config,omitempty"`
}

// RepoConfig holds repository-specific configuration.
type RepoConfig struct {
	AuthToken string `json:"auth_token,omitempty"` // Git auth token (works for GitHub, GitLab, etc.)
}
