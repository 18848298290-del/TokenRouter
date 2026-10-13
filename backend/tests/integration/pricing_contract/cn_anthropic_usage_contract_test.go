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

// CN-compatible Messages usage can include cached tokens inside the reported
// input total. The fixtures pass Kimi, GLM, and DeepSeek observations through
// Anthropic parsing and OpenAI adaptation before checking that only the
// uncached remainder receives the input price.

// TestCNProviderAnthropicUsageBillsUncachedInput supplies three CN-compatible
// usage payloads with cached input included in their totals. It checks the
// uncached remainder and its positive dollar charge against the fixture
// model's per-token price.
func TestCNProviderAnthropicUsageBillsUncachedInput(t *testing.T) {
	tests := []struct {
		name      string // subtest provider label
		model     string // priced fixture model
		body      string // serialized usage response
		wantInput int    // expected uncached input tokens
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
			// The OpenAI adapter includes cache reads and writes in
			// InputTokens. Subtract those buckets before charging uncached
			// input, and clamp inconsistent totals at zero.
			uncachedInput := max(openAIUsage.InputTokens-openAIUsage.CacheReadInputTokens-openAIUsage.CacheCreationInputTokens, 0)
			require.Equal(t, tt.wantInput, uncachedInput)

			// Fix the pricing instant so the input-cost comparison uses the
			// same request conditions for each
			// normalized provider-usage fixture.
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
			// Compare the uncached-input USD charge with an
			// absolute rounding tolerance.
			require.InDelta(t, float64(tt.wantInput)*pricing.InputPricePerToken, cost.InputCost, 1e-12)
		})
	}
}
