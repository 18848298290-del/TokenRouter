package completion

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// imageTokenCatalog supplies token prices for the specified image model.
type imageTokenCatalog struct{}

func (imageTokenCatalog) GetModelPricing(model string) *pricing.CatalogModelPricing {
	if model != "gpt-image-2" {
		return nil
	}
	return &pricing.CatalogModelPricing{InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, OutputCostPerImageToken: 3e-6}
}

func (imageTokenCatalog) ForceUpdate() error { return nil }

// TestImageTokenUsageUsesExactTokenPricing checks image generation billing against reported token usage.
func TestImageTokenUsageUsesExactTokenPricing(t *testing.T) {
	calculator := billing.NewCalculator(imageTokenCatalog{}, billing.CalculatorOptions{})
	recorder := NewRecorder(Dependencies{Calculator: calculator}, RecorderOptions{})
	result := &Result{Model: "gpt-image-2", ImageCount: 1, Usage: TokenUsage{InputTokens: 100, OutputTokens: 20, ImageOutputTokens: 20}}
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 20, ImageOutputTokens: 20}
	want := 100*1e-6 + 20*3e-6
	openAI, err := recorder.CalculateOpenAIRecordUsageCostAt(context.Background(), result, &KeySnapshot{}, []string{result.Model}, 1, 1, 1, 1, tokens, "", time.Now())
	require.NoError(t, err)
	require.Equal(t, string(BillingModeToken), openAI.BillingMode)
	require.InDelta(t, want, openAI.ActualCost, 1e-12)
	messages := recorder.CalculateRecordUsageCost(context.Background(), result, &KeySnapshot{}, &ProviderSnapshot{}, result.Model, result.Model, "", "", 1, 1, nil)
	require.Equal(t, string(BillingModeToken), messages.BillingMode)
	require.InDelta(t, want, messages.ActualCost, 1e-12)
	result.Usage = TokenUsage{}
	_, err = recorder.CalculateOpenAIRecordUsageCostAt(context.Background(), result, &KeySnapshot{}, []string{result.Model}, 1, 1, 1, 1, UsageTokens{}, "", time.Now())
	require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
}
