package domain

import "encoding/json"

// StrategyInput represents the JSON input format for loading a strategy.
type StrategyInput struct {
	Project    string          `json:"project"`
	Strategy   StrategyDef     `json:"strategy"`
	Repository RepositoryDef   `json:"repository"`
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
	ID          string  `json:"id"`           // Stable user-provided ID for upsert
	Description string  `json:"description"`
	TargetUnits string  `json:"target_units"`
	TargetDate  *string `json:"target_date,omitempty"` // ISO8601 format
}

// FundingDef defines a funding source.
type FundingDef struct {
	ResourceType string  `json:"resource_type"` // "dollars" or "claude_tokens"
	Amount       float64 `json:"amount"`
}

// RepositoryDef defines the repository configuration.
type RepositoryDef struct {
	URL        string          `json:"url"`
	MainBranch string          `json:"main_branch"`
	Config     json.RawMessage `json:"config,omitempty"`
}

// RepoConfig holds repository-specific configuration.
type RepoConfig struct {
	TestCommand string `json:"test_command,omitempty"`
}
