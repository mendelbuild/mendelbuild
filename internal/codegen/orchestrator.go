package codegen

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/google/uuid"
)

const (
	// DefaultConcurrency is the default number of parallel generators.
	DefaultConcurrency = 2
)

// Orchestrator spawns and manages multiple generators.
type Orchestrator struct {
	db          *db.DB
	concurrency int
}

// NewOrchestrator creates a new Orchestrator.
func NewOrchestrator(database *db.DB, concurrency int) *Orchestrator {
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	return &Orchestrator{
		db:          database,
		concurrency: concurrency,
	}
}

// OrchestrateResult contains the results of orchestrating multiple variations.
type OrchestrateResult struct {
	Results       []GenerateResult
	TotalTokens   int
	SuccessCount  int
	FailureCount  int
}

// Orchestrate runs code generation for all variations of a hop.
func (o *Orchestrator) Orchestrate(ctx context.Context, hopID uuid.UUID, proposal *VariationProposalData, config GeneratorConfig) (*OrchestrateResult, error) {
	// 1. Get the hop
	hop, err := o.db.GetHop(ctx, hopID)
	if err != nil {
		return nil, fmt.Errorf("get hop: %w", err)
	}

	// 2. Get repository info for the project
	strategy, err := o.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		return nil, fmt.Errorf("get strategy: %w", err)
	}

	repo, err := o.db.GetRepositoryByProject(ctx, strategy.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("get repository: %w", err)
	}

	// Parse repository config
	var repoConfig struct {
		MainBranch  string `json:"main_branch"`
		AuthToken   string `json:"auth_token"`
		TestCommand string `json:"test_command"`
	}
	if repo.Config != nil {
		json.Unmarshal(repo.Config, &repoConfig)
	}

	// Get project for API key
	project, err := o.db.GetProject(ctx, strategy.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}

	var projectConfig domain.ProjectConfig
	if project.Config != nil {
		json.Unmarshal(project.Config, &projectConfig)
	}

	// Use config from repository and project
	if config.RepositoryURL == "" && repo.URL != nil {
		config.RepositoryURL = *repo.URL
	}
	if config.MainBranch == "" {
		config.MainBranch = repoConfig.MainBranch
		if config.MainBranch == "" {
			config.MainBranch = "main"
		}
	}
	if config.AuthToken == "" {
		config.AuthToken = repoConfig.AuthToken
	}
	if config.TestCommand == "" {
		config.TestCommand = repoConfig.TestCommand
	}
	if config.APIKey == "" {
		config.APIKey = projectConfig.AnthropicAPIKey
	}

	// 3. Create Variation records from proposal
	variations, err := o.createVariations(ctx, hop, proposal)
	if err != nil {
		return nil, fmt.Errorf("create variations: %w", err)
	}

	// 4. Update hop status to active
	if err := o.db.UpdateHopStatus(ctx, hopID, domain.HopStatusActive); err != nil {
		return nil, fmt.Errorf("update hop status: %w", err)
	}

	// 5. Run generators concurrently
	result := o.runGenerators(ctx, variations, hop.Name, config)

	return result, nil
}

// createVariations creates Variation records from the proposal.
func (o *Orchestrator) createVariations(ctx context.Context, hop *domain.Hop, proposal *VariationProposalData) ([]*domain.Variation, error) {
	// Get repository for the hop's project
	strategy, err := o.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		return nil, fmt.Errorf("get strategy: %w", err)
	}

	repo, err := o.db.GetRepositoryByProject(ctx, strategy.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("get repository: %w", err)
	}

	var variations []*domain.Variation
	for _, pv := range proposal.Variations {
		v := &domain.Variation{
			ID:           uuid.New(),
			HopID:        hop.ID,
			Name:         pv.Name,
			Approach:     pv.Approach,
			RepositoryID: &repo.ID,
			Status:       domain.VariationStatusCreating,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}

		if err := o.db.CreateVariation(ctx, v); err != nil {
			return nil, fmt.Errorf("create variation %s: %w", pv.Name, err)
		}

		// Record initial state
		o.db.CreateVariationStateTransition(ctx, v.ID, "", string(domain.VariationStatusCreating), "variation created from proposal")

		variations = append(variations, v)
	}

	return variations, nil
}

// runGenerators runs generators concurrently with limited concurrency.
func (o *Orchestrator) runGenerators(ctx context.Context, variations []*domain.Variation, hopName string, config GeneratorConfig) *OrchestrateResult {
	result := &OrchestrateResult{
		Results: make([]GenerateResult, 0, len(variations)),
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, o.concurrency)

	for _, v := range variations {
		wg.Add(1)
		go func(variation *domain.Variation) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			generator := NewGenerator(o.db, config)
			genResult, _ := generator.Generate(ctx, variation, hopName)

			mu.Lock()
			result.Results = append(result.Results, *genResult)
			result.TotalTokens += genResult.TokensUsed
			if genResult.Success {
				result.SuccessCount++
			} else {
				result.FailureCount++
			}
			mu.Unlock()
		}(v)
	}

	wg.Wait()
	return result
}

// VariationProposalData is the data needed to orchestrate variations.
// This can come from a Decision's details or be constructed manually.
type VariationProposalData struct {
	HopID      uuid.UUID              `json:"hop_id"`
	Variations []ProposedVariationData `json:"variations"`
}

// ProposedVariationData is a single variation in a proposal.
type ProposedVariationData struct {
	Name            string `json:"name"`
	Approach        string `json:"approach"`
	Differentiation string `json:"differentiation"`
	EstimatedTokens int    `json:"estimated_tokens"`
}

// ParseVariationProposal parses a variation proposal from a Decision's details.
func ParseVariationProposal(details string) (*VariationProposalData, error) {
	var proposal VariationProposalData
	if err := json.Unmarshal([]byte(details), &proposal); err != nil {
		return nil, err
	}
	return &proposal, nil
}
