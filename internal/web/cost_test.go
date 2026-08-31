package web

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
)

func f64(v float64) *float64 { return &v }
func strp(v string) *string  { return &v }

// A budget figure on its own says nothing about whether a project is on track.
// Pace is the comparison that makes it mean something, so it has to hold up at
// both ends and refuse to answer when it cannot.
func TestStrategyCostViewPace(t *testing.T) {
	cases := []struct {
		name           string
		budget, spent  float64
		elapsed        float64
		hasPeriod      bool
		wantSubstring  string
	}{
		{"burning ahead of schedule", 1000, 800, 0.5, true, "faster"},
		{"comfortably behind", 1000, 100, 0.5, true, "slower"},
		{"tracking the schedule", 1000, 500, 0.5, true, "on pace"},
		// With no period declared there is no schedule to be on.
		{"no period means no verdict", 1000, 800, 0, false, ""},
		// Right at the start, any spend divides by almost nothing and would
		// read as a runaway.
		{"too early to judge", 1000, 10, 0.01, true, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &StrategyCostView{
				BudgetUSD: tc.budget, SpentUSD: tc.spent,
				ElapsedFraction: tc.elapsed, HasPeriod: tc.hasPeriod,
			}
			got := v.Pace()
			if tc.wantSubstring == "" {
				if got != "" {
					t.Errorf("Pace() = %q, want no verdict", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSubstring) {
				t.Errorf("Pace() = %q, want it to mention %q", got, tc.wantSubstring)
			}
		})
	}
}

func TestStrategyCostViewRemainingNeverGoesNegative(t *testing.T) {
	v := &StrategyCostView{BudgetUSD: 100, SpentUSD: 250}
	if got := v.RemainingUSD(); got != 0 {
		t.Errorf("RemainingUSD = %v, want 0 when overspent", got)
	}
	if !v.OverBudget() {
		t.Error("OverBudget should be true when spend exceeds budget")
	}
}

func TestHopCostViewVariance(t *testing.T) {
	cases := []struct {
		name     string
		estimate *float64
		actual   float64
		want     string
	}{
		{"blew past the estimate", f64(10), 35, "3.5x over estimate"},
		{"came in cheap", f64(10), 4, "40% of estimate"},
		{"near enough", f64(10), 10.2, "on estimate"},
		// Nothing to compare against must say nothing, not "0x".
		{"never estimated", nil, 25, ""},
		{"estimated at zero", f64(0), 25, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &HopCostView{EstimateUSD: tc.estimate, ActualUSD: tc.actual}
			if got := v.Variance(); got != tc.want {
				t.Errorf("Variance() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A Hop with no ceiling has no budget configured, which is not the same as
// having spent its budget; callers must not read the zero as "nothing left".
func TestHopCostViewRemainingWithoutCeiling(t *testing.T) {
	if got := (&HopCostView{ActualUSD: 5}).RemainingUSD(); got != 0 {
		t.Errorf("RemainingUSD = %v, want 0", got)
	}
	v := &HopCostView{LimitUSD: f64(20), ActualUSD: 5}
	if got := v.RemainingUSD(); got != 15 {
		t.Errorf("RemainingUSD = %v, want 15", got)
	}
	if v.OverBudget() {
		t.Error("should not be over budget at 5 of 20")
	}
}

func TestFundingSourceElapsed(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC)
	src := &domain.FundingSource{PeriodStart: &start, PeriodEnd: &end}

	got, ok := src.Elapsed(time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC))
	if !ok || got != 0.5 {
		t.Errorf("Elapsed halfway = %v (ok=%v), want 0.5", got, ok)
	}

	// Past the end, the period is fully elapsed rather than over-elapsed.
	if got, _ := src.Elapsed(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)); got != 1 {
		t.Errorf("Elapsed after end = %v, want 1", got)
	}
	if _, ok := (&domain.FundingSource{}).Elapsed(time.Now()); ok {
		t.Error("a budget with no period should report no elapsed fraction")
	}
}

// The budget card is the whole point of the rev, and a bad field path in a
// template only fails when that branch executes. These render it with data in
// each shape it has to survive.
func TestStrategyBudgetCardRenders(t *testing.T) {
	projectID := uuid.New()
	strategyView := &StrategyView{
		Project:  &domain.Project{ID: projectID, Name: "pong"},
		Strategy: &domain.Strategy{ID: uuid.New(), Name: "Q3"},
	}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	target := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	full := &StrategyCostView{
		BudgetUSD: 2000, SpentUSD: 1500,
		Tokens:          domain.TokenCounts{InputTokens: 120000, OutputTokens: 45000, CacheReadTokens: 8_400_000},
		ElapsedFraction: 0.5, HasPeriod: true,
		Sources: []FundingSourceView{{
			Source: domain.FundingSource{
				Name: "Q3 build", AmountUSD: 2000,
				PeriodStart: &start, PeriodEnd: &end,
			},
			KeyResults: []db.FundedKeyResult{{
				Description: "Weekly active users",
				TargetUnits: "1000 users",
				TargetDate:  &target,
			}},
			// Funds one Key Result of three, so which ones is real information.
			TotalKeyResults: 3,
		}},
		Components: []db.ComponentCost{
			{Component: "codegen", Kind: domain.CostKindModel, AmountUSD: 1400},
			{Component: "deploy", Kind: domain.CostKindHosting, AmountUSD: 100},
		},
	}

	t.Run("budget with a period and funded key results", func(t *testing.T) {
		body := renderPageForTest(t, "costs.html", map[string]interface{}{
			"ProjectID": projectID.String(), "Strategy": strategyView, "Cost": full,
		})
		for _, want := range []string{
			"$1500", "of $2000 budgeted", "$500", "Remaining",
			"75% of budget spent", "50% of the period elapsed", "faster than the schedule",
			"Q3 build", "Weekly active users", "1 of", "8.4M", "codegen", "Where the money went",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("budget card missing %q", want)
			}
		}
		// A partial budget names which Key Results it funds, but not their
		// targets and dates: those live on the OKR page, and repeating them
		// under a budget is what made this card read like a second roadmap.
		for _, unwanted := range []string{"1000 users", "15 Sep 2026"} {
			if strings.Contains(body, unwanted) {
				t.Errorf("budget card should not restate %q; that belongs on the OKR page", unwanted)
			}
		}
	})

	// The ordinary case for a project created through the guided flow: one
	// budget covering every Key Result. Listing them all restates the OKR page
	// and reads like a plan, so it collapses to a single line.
	t.Run("budget funding every key result does not list them", func(t *testing.T) {
		everything := *full
		everything.Sources = []FundingSourceView{{
			Source: domain.FundingSource{Name: "MVP build", AmountUSD: 250},
			KeyResults: []db.FundedKeyResult{
				{Description: "Weekly active users", TargetUnits: "1000 users", TargetDate: &target},
				{Description: "Polls published", TargetUnits: ">= 1 poll", TargetDate: &target},
			},
			TotalKeyResults: 2,
		}}

		body := renderPageForTest(t, "costs.html", map[string]interface{}{
			"ProjectID": projectID.String(), "Strategy": strategyView, "Cost": &everything,
		})

		if !strings.Contains(body, "all 2 key results") {
			t.Error("a budget funding everything should say so in one line")
		}
		for _, unwanted := range []string{"Weekly active users", "Polls published", "1000 users"} {
			if strings.Contains(body, unwanted) {
				t.Errorf("budget card restated %q; a budget covering every key result "+
					"should link to the OKR page rather than reproduce it", unwanted)
			}
		}
	})

	// Spend recorded with no budget set must say so plainly rather than
	// rendering a progress bar against zero.
	t.Run("spend recorded but no budget set", func(t *testing.T) {
		body := renderPageForTest(t, "costs.html", map[string]interface{}{
			"ProjectID": projectID.String(), "Strategy": strategyView,
			"Cost": &StrategyCostView{SpentUSD: 42.5},
		})
		if !strings.Contains(body, "No budget is defined") {
			t.Error("expected the card to say no budget is defined")
		}
		for _, want := range []string{"$42.50", "Spent to date"} {
			if !strings.Contains(body, want) {
				t.Errorf("expected spend to date to still be shown: missing %q", want)
			}
		}
	})

	t.Run("no cost data at all", func(t *testing.T) {
		body := renderPageForTest(t, "costs.html", map[string]interface{}{
			"ProjectID": projectID.String(), "Strategy": strategyView,
		})
		if strings.Contains(body, "No budget is defined") {
			t.Error("the budget card should be absent entirely, not empty")
		}
	})
}

func TestHopCostCardRenders(t *testing.T) {
	body := renderHopPageWithCost(t, &HopCostView{
		EstimateUSD: f64(12), LimitUSD: f64(15), ActualUSD: 41.5,
		Confidence: f64(0.35), Basis: strp("Comparable to auth-refactor, assuming 3 variations."),
		Estimator: "proposer",
		Tokens:    domain.TokenCounts{InputTokens: 90000, OutputTokens: 30000, CacheReadTokens: 2_100_000},
	})

	for _, want := range []string{
		"$41.50", "$12.00", "$15.00",
		"3.5x over estimate", "over its ceiling",
		"2.1M", "35% confidence", "auth-refactor",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("hop cost card missing %q", want)
		}
	}
}

// renderHopPageWithCost renders hop_detail.html carrying a cost view.
func renderHopPageWithCost(t *testing.T, costView *HopCostView) string {
	t.Helper()
	projectID := uuid.New()
	now := time.Now()
	hop := &domain.Hop{
		ID: uuid.New(), StrategyID: uuid.New(),
		Name: "rate-limiting", Commentary: "Protect the API from bursts.",
		Status: domain.HopStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	return renderForTest(t, "hop_detail.html", projectID, &HopDetailView{
		Hop:      hop,
		Strategy: &domain.Strategy{ID: hop.StrategyID, Name: "Q3"},
		Project:  &domain.Project{ID: projectID, Name: "Demo"},
		Cost:     costView,
		Ribbon:   ribbonView(domain.HopLifecycle(hop, nil)),
	})
}

// The roadmap review is where a human decides whether to spend the money, so
// the estimates and the auditor's challenge to them have to render together.
// A bad field path in a template only fails when that branch executes.
func TestRoadmapReviewRendersCostAudit(t *testing.T) {
	projectID := uuid.New()
	strategyID := uuid.New()

	view := &InputRequestDetailView{
		InputRequest: &domain.InputRequest{
			ID: uuid.New(), ProjectID: projectID,
			Kind: domain.InputRequestKindRoadmapReview, Title: "Roadmap Review: Q3",
			Status: domain.InputRequestStatusNeedsAssignment,
		},
		Strategy: &domain.Strategy{ID: strategyID, Name: "Q3"},
		Roadmap: &agent.ProposedRoadmap{
			Hops: []agent.ProposedHop{
				{
					Name: "auth-refactor", Commentary: "Split the auth module.",
					EstimatedCostUSD: 30, CostConfidence: 0.6,
					CostBasis: "Comparable to the last two refactors.",
				},
				{
					Name: "rate-limiting", Commentary: "Protect the API.",
					EstimatedCostUSD: 12, CostConfidence: 0.2,
					CostBasis: "No comparable hop; scope is unclear.",
				},
			},
			FeasibilityNotes: "Tight but achievable.",
		},
		CostAudit: &agent.CostAuditResponse{
			Hops: []agent.HopCostVerdict{
				{HopName: "auth-refactor", Verdict: "sound", RevisedEstimateUSD: 30, Confidence: 0.6,
					Reasoning: "Matches two completed refactors."},
				{HopName: "rate-limiting", Verdict: "understated", RevisedEstimateUSD: 45, Confidence: 0.4,
					Reasoning: "Touches middleware across 20 files; three variations is optimistic."},
			},
			TotalRevisedUSD: 75,
			BudgetVerdict:   "exceeds",
			Summary:         "This roadmap does not fit the remaining budget.",
			Risks:           []string{"rate-limiting may need more than three variations"},
		},
		StrategyCost: &StrategyCostView{BudgetUSD: 100, SpentUSD: 40},
	}

	body := renderForTest(t, "input_request_roadmap.html", projectID, view)

	for _, want := range []string{
		"Cost Review",
		"$42",          // proposed total, 30 + 12
		"$75",          // audited total
		"$60",          // budget remaining, 100 - 40
		"exceeds",
		"does not fit the remaining budget",
		"understated", "$45.00",
		"Touches middleware across 20 files",
		"may need more than three variations",
		"60% confidence", // the proposer's own confidence on auth-refactor
	} {
		if !strings.Contains(body, want) {
			t.Errorf("roadmap review missing %q", want)
		}
	}

	// A hop the auditor endorsed must not be flagged as disputed in the table.
	if strings.Contains(body, "auditor: sound") {
		t.Error("endorsed hops should not carry an auditor disagreement note")
	}
}

// With no audit stored, the review still has to render: the auditor is a
// best-effort second opinion, not a precondition for reviewing a roadmap.
func TestRoadmapReviewRendersWithoutCostAudit(t *testing.T) {
	projectID := uuid.New()
	view := &InputRequestDetailView{
		InputRequest: &domain.InputRequest{
			ID: uuid.New(), ProjectID: projectID,
			Kind: domain.InputRequestKindRoadmapReview, Title: "Roadmap Review: Q3",
			Status: domain.InputRequestStatusNeedsAssignment,
		},
		Roadmap: &agent.ProposedRoadmap{
			Hops: []agent.ProposedHop{{Name: "auth-refactor", Commentary: "Split auth.", EstimatedCostUSD: 30}},
		},
	}

	body := renderForTest(t, "input_request_roadmap.html", projectID, view)
	if strings.Contains(body, "Cost Review") {
		t.Error("the cost review card should be absent when no audit was made")
	}
	if !strings.Contains(body, "$30.00") {
		t.Error("the proposer's estimate should still show without an audit")
	}
}

// Per-success cost is the figure that actually compares models: a cheaper model
// that retries more can be the expensive one. These pin that arithmetic and the
// guards around a model with nothing finished yet.
func TestModelUsageOutcomeMaths(t *testing.T) {
	cheapButFlaky := db.ModelUsage{Model: "cheap", AmountUSD: 60, Succeeded: 2, Failed: 6}
	pricierButSolid := db.ModelUsage{Model: "pricier", AmountUSD: 80, Succeeded: 8, Failed: 0}

	if got := cheapButFlaky.SuccessRate(); got != 0.25 {
		t.Errorf("cheap success rate = %v, want 0.25", got)
	}
	if got := cheapButFlaky.USDPerSuccess(); got != 30 {
		t.Errorf("cheap $/success = %v, want 30", got)
	}
	if got := pricierButSolid.USDPerSuccess(); got != 10 {
		t.Errorf("pricier $/success = %v, want 10", got)
	}
	// The whole point: lower total spend, higher cost per result.
	if !(cheapButFlaky.AmountUSD < pricierButSolid.AmountUSD &&
		cheapButFlaky.USDPerSuccess() > pricierButSolid.USDPerSuccess()) {
		t.Error("expected the cheaper-looking model to cost more per success")
	}

	fresh := db.ModelUsage{Model: "new", AmountUSD: 5}
	if fresh.HasFinished() || fresh.HasSuccess() {
		t.Error("a model with nothing finished must report no rate and no per-success figure")
	}
	if fresh.SuccessRate() != 0 || fresh.USDPerSuccess() != 0 {
		t.Error("guarded accessors should return zero rather than divide by zero")
	}
}

func TestStrategyPageRendersPerModelCosts(t *testing.T) {
	projectID := uuid.New()
	strategyView := &StrategyView{
		Project:  &domain.Project{ID: projectID, Name: "pong"},
		Strategy: &domain.Strategy{ID: uuid.New(), Name: "Q3"},
	}

	body := renderPageForTest(t, "costs.html", map[string]interface{}{
		"ProjectID": projectID.String(), "Strategy": strategyView,
		"Cost": &StrategyCostView{
			BudgetUSD: 500, SpentUSD: 140,
			Models: []db.ModelUsage{
				{
					Model: "claude-sonnet-4-6", AmountUSD: 120,
					Tokens:    domain.TokenCounts{InputTokens: 2_000_000, OutputTokens: 40_000},
					Variations: 8, Succeeded: 2, Failed: 6,
				},
				{
					Model: "claude-sonnet-5", AmountUSD: 20,
					Tokens:    domain.TokenCounts{InputTokens: 30_000, OutputTokens: 20_000, CacheReadTokens: 3_400_000},
					Variations: 8, Succeeded: 7, Failed: 1,
				},
			},
		},
	})

	for _, want := range []string{
		"Cost by model",
		"claude-sonnet-4-6", "$120", "25%", "$60.00", // flaky: 2 of 8, $60/success
		"claude-sonnet-5", "$20", "88%", "$2.86",     // solid: 7 of 8, $2.86/success
		"3.4M",                                       // cache reads, visible only since the ledger records them
	} {
		if !strings.Contains(body, want) {
			t.Errorf("cost page missing %q", want)
		}
	}
}

// An unpriced model records its tokens but prices to zero, so the totals lie.
// That has to be visible, since nothing else signals it.
func TestStrategyPageWarnsAboutUnpricedModels(t *testing.T) {
	projectID := uuid.New()
	strategyView := &StrategyView{
		Project:  &domain.Project{ID: projectID, Name: "pong"},
		Strategy: &domain.Strategy{ID: uuid.New(), Name: "Q3"},
	}

	body := renderPageForTest(t, "costs.html", map[string]interface{}{
		"ProjectID": projectID.String(), "Strategy": strategyView,
		"Cost": &StrategyCostView{
			BudgetUSD: 500, SpentUSD: 140,
			UnpricedModels: []string{"claude-experimental-9"},
		},
	})
	for _, want := range []string{"unpriced models", "claude-experimental-9", "mendel rates refresh"} {
		if !strings.Contains(body, want) {
			t.Errorf("unpriced-model warning missing %q", want)
		}
	}

	clean := renderPageForTest(t, "costs.html", map[string]interface{}{
		"ProjectID": projectID.String(), "Strategy": strategyView,
		"Cost": &StrategyCostView{BudgetUSD: 500, SpentUSD: 140},
	})
	if strings.Contains(clean, "unpriced models") {
		t.Error("warning should be absent when every model is priced")
	}
}

// A run paused on cost is not a failure, and the page has to say so: the work
// is intact and the only question is whether finishing it is worth more money.
func TestVariationPageOffersToContinueAPausedRun(t *testing.T) {
	projectID := uuid.New()
	now := time.Now()
	hop := &domain.Hop{
		ID: uuid.New(), StrategyID: uuid.New(),
		Name: "google-oauth", Commentary: "Add Google sign-in.",
		Status: domain.HopStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	paused := &domain.Variation{
		ID: uuid.New(), HopID: hop.ID, Name: "oauth-a", Approach: "x",
		Status:           domain.VariationStatusBlocked,
		BudgetPausedUSD:  f64(5.02),
		BudgetCeilingUSD: f64(5.00),
		CreatedAt:        now, UpdatedAt: now,
	}

	body := renderForTest(t, "variation_detail.html", projectID, &VariationDetailView{
		Variation: paused,
		Hop:       hop,
		Ribbon:    variationRibbon(projectID, paused, nil, hop, false),
	})

	for _, want := range []string{
		// The ribbon states the situation and offers the move...
		"Paused at its spend ceiling",
		"Nothing went wrong",
		"Continue where it left off",
		// ...and the page carries the two numbers the decision turns on.
		"$5.02", "$5.00",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("paused-run panel missing %q", want)
		}
	}
	// Rebuilding would throw the work away, which is the opposite of what this
	// state calls for.
	if strings.Contains(body, "Rebuild from scratch") {
		t.Error("a budget-paused run must not offer a from-scratch retry")
	}
}

// A variation blocked for another reason (credentials) keeps the ordinary
// retry affordance and shows no spend panel.
func TestVariationBlockedForOtherReasonsIsUnchanged(t *testing.T) {
	projectID := uuid.New()
	now := time.Now()
	hop := &domain.Hop{
		ID: uuid.New(), StrategyID: uuid.New(), Name: "h", Commentary: "c",
		Status: domain.HopStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	blocked := &domain.Variation{
		ID: uuid.New(), HopID: hop.ID, Name: "v", Approach: "x",
		Status: domain.VariationStatusBlocked, CreatedAt: now, UpdatedAt: now,
	}

	body := renderForTest(t, "variation_detail.html", projectID, &VariationDetailView{
		Variation: blocked,
		Hop:       hop,
		Ribbon:    variationRibbon(projectID, blocked, nil, hop, false),
	})

	if strings.Contains(body, "Paused at its spend ceiling") {
		t.Error("a variation blocked on credentials must not claim a spend pause")
	}
	if !strings.Contains(body, "Rebuild from scratch") {
		t.Error("ordinary blocked variations should keep the from-scratch retry")
	}
}

func TestVariationBudgetAccessorsAreSafeWhenNotPaused(t *testing.T) {
	v := &domain.Variation{Status: domain.VariationStatusCreating}
	if v.PausedForBudget() {
		t.Error("a running variation is not paused for budget")
	}
	if v.BudgetSpentUSD() != 0 || v.BudgetLimitUSD() != 0 {
		t.Error("accessors must return zero rather than dereferencing nil")
	}
}
