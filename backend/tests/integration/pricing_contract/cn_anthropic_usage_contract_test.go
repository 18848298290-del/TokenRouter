package pricingcontract

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
	billingpricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

func TestCNProviderAnthropicUsageBillsUncachedInput(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		body      string
		wantInput int
	}{
		{
			name:      "Kimi",
			model:     "k3",
			body:      `{"usage":{"input_tokens":173306,"output_tokens":166,"prompt_tokens":173306,"cached_tokens":173056}}`,
			wantInput: 250,
		},
		{
			name:      "GLM",
			model:     "glm-5.2",
			body:      `{"usage":{"input_tokens":1200,"output_tokens":30,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}}`,
			wantInput: 400,
		},
		{
			name:      "DeepSeek",
			model:     "deepseek-v4-flash",
			body:      `{"usage":{"input_tokens":1200,"output_tokens":30,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}}`,
			wantInput: 400,
		},
	}

	billing := billingtestkit.Calculator(nil, nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claudeUsage := protocolanthropic.ParseClaudeUsageFromResponseBody([]byte(tt.body))
			openAIUsage := openaiforward.AnthropicUsageToOpenAI(claudeUsage)
			uncachedInput := max(openAIUsage.InputTokens-openAIUsage.CacheReadInputTokens-openAIUsage.CacheCreationInputTokens, 0)
			require.Equal(t, tt.wantInput, uncachedInput)

			// Use a fixed off-peak time to isolate the charge for uncached input.
			cost, err := billing.CalculateCostUnified(billingcore.CostInput{
				Ctx: context.Background(), Model: tt.model, RateMultiplier: 1,
				Resolver:  billingtestkit.PriceResolver(nil, billing),
				PricingAt: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC),
				Tokens: billingpricing.UsageTokens{
					InputTokens: uncachedInput, OutputTokens: openAIUsage.OutputTokens,
					CacheCreationTokens: openAIUsage.CacheCreationInputTokens,
					CacheReadTokens:     openAIUsage.CacheReadInputTokens,
				},
			})
			require.NoError(t, err)
			require.Positive(t, cost.InputCost, "uncached input must contribute to the final charge")

			pricing, err := billing.GetModelPricing(tt.model)
			require.NoError(t, err)
			require.InDelta(t, float64(tt.wantInput)*pricing.InputPricePerToken, cost.InputCost, 1e-12)
		})
	}
}
