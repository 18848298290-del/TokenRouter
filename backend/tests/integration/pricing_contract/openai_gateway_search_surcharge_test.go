package pricingcontract

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
)

// SearchCount adds a per-thousand-call operation charge to successful token
// pricing. A search-only request can produce its own breakdown when no token
// model is offered, while an offered model with unavailable token pricing
// still returns the calculation error.

// TestCalculateOpenAIRecordUsageCost_SearchIsAdditiveToTokens combines known
// Claude fixture tokens with one hundred searches. It checks that both base
// and user totals include the token amount plus
// the per-thousand-call surcharge.
func TestCalculateOpenAIRecordUsageCost_SearchIsAdditiveToTokens(t *testing.T) {
	t.Parallel()

	price := 10.0 // USD per 1,000 searches; 100 searches cost $1.
	svc := completion.NewRecorder(completion.Dependencies{
		Calculator: newCalculator(nil),
	}, completion.RecorderOptions{DefaultMultiplier: 1})

	apiKey := &completion.KeySnapshot{
		Group: &completion.GroupSnapshot{
			SearchPricePer1k: &price,
		},
	}

	// The fixture prices claude-sonnet-4 input at $3 and output at $15 per
	// million tokens. 1,000 input and 500 output tokens cost $0.0105, plus $1
	// for 100 searches.
	cost, err := svc.CalculateOpenAIRecordUsageCostAt(
		context.Background(), gatewaycapture.ProjectOpenAICompletionResult(&forwardcore.OpenAIResult{SearchCount: 100}, nil), apiKey, []string{"claude-sonnet-4"},
		1.0,
		1.0,
		1.0,
		1.0,
		pricing.UsageTokens{InputTokens: 1000, OutputTokens: 500},
		"", time.Time{},
	)
	require.NoError(t, err)
	require.NotNil(t, cost)
	// The token/search USD amount allows a small absolute rounding difference.
	require.InDelta(t, 1.0105, cost.ActualCost, 1e-9)
	require.InDelta(t, 1.0105, cost.TotalCost, 1e-9)
}

// TestCalculateOpenAIRecordUsageCost_SearchOnlyWhenNoTokenPricing offers
// searches with empty token usage and no model candidates. It checks that the
// recorder returns a successful breakdown containing the
// search-only user amount.
func TestCalculateOpenAIRecordUsageCost_SearchOnlyWhenNoTokenPricing(t *testing.T) {
	t.Parallel()

	price := 10.0
	svc := completion.NewRecorder(completion.Dependencies{
		Calculator: newCalculator(nil),
	}, completion.RecorderOptions{DefaultMultiplier: 1})

	apiKey := &completion.KeySnapshot{
		Group: &completion.GroupSnapshot{SearchPricePer1k: &price},
	}
	// Omit model candidates together with token usage so the recorder can
	// return the search-only breakdown instead of a
	// selected-model pricing error.
	cost, err := svc.CalculateOpenAIRecordUsageCostAt(
		context.Background(), gatewaycapture.ProjectOpenAICompletionResult(&forwardcore.OpenAIResult{SearchCount: 100}, nil), apiKey, nil,
		1.0,
		1.0,
		1.0,
		1.0,
		pricing.UsageTokens{},
		"", time.Time{},
	)
	require.NoError(t, err)
	require.NotNil(t, cost)
	// The search-only USD charge allows a small absolute rounding difference.
	require.InDelta(t, 1.0, cost.ActualCost, 1e-9)
}

// TestCalculateOpenAIRecordUsageCost_TokenPricingErrorNotSwallowedBySearch
// combines searches with token usage for an unknown model. It checks that
// unavailable model pricing returns an error and leaves the
// cost breakdown nil.
func TestCalculateOpenAIRecordUsageCost_TokenPricingErrorNotSwallowedBySearch(t *testing.T) {
	t.Parallel()

	price := 10.0
	svc := completion.NewRecorder(completion.Dependencies{
		Calculator: newCalculator(nil),
	}, completion.RecorderOptions{DefaultMultiplier: 1})

	apiKey := &completion.KeySnapshot{
		Group: &completion.GroupSnapshot{SearchPricePer1k: &price},
	}
	// Keep a nonempty unknown candidate in this row. Removing that candidate
	// would exercise the separate search-only branch instead of token failure.
	cost, err := svc.CalculateOpenAIRecordUsageCostAt(
		context.Background(), gatewaycapture.ProjectOpenAICompletionResult(&forwardcore.OpenAIResult{SearchCount: 100}, nil), apiKey, []string{"totally-unknown-model-xyz-no-pricing"},
		1.0,
		1.0,
		1.0,
		1.0,
		pricing.UsageTokens{InputTokens: 1000, OutputTokens: 500},
		"", time.Time{},
	)
	require.Error(t, err)
	require.Nil(t, cost)
}
