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

	// The fixture prices claude-sonnet-4 input at $3 and output at $15 per million tokens.
	// 1,000 input and 500 output tokens cost $0.0105, plus $1 for 100 searches.
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
	require.InDelta(t, 1.0105, cost.ActualCost, 1e-9)
	require.InDelta(t, 1.0105, cost.TotalCost, 1e-9)
}

func TestCalculateOpenAIRecordUsageCost_SearchOnlyWhenNoTokenPricing(t *testing.T) {
	t.Parallel()

	price := 10.0
	svc := completion.NewRecorder(completion.Dependencies{
		Calculator: newCalculator(nil),
	}, completion.RecorderOptions{DefaultMultiplier: 1})

	apiKey := &completion.KeySnapshot{
		Group: &completion.GroupSnapshot{SearchPricePer1k: &price},
	}
	// With no token usage or model candidates, bill the search surcharge alone.
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
	require.InDelta(t, 1.0, cost.ActualCost, 1e-9)
}

func TestCalculateOpenAIRecordUsageCost_TokenPricingErrorNotSwallowedBySearch(t *testing.T) {
	t.Parallel()

	price := 10.0
	svc := completion.NewRecorder(completion.Dependencies{
		Calculator: newCalculator(nil),
	}, completion.RecorderOptions{DefaultMultiplier: 1})

	apiKey := &completion.KeySnapshot{
		Group: &completion.GroupSnapshot{SearchPricePer1k: &price},
	}
	// An unknown model with token usage must return its pricing error even when search costs are available.
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
