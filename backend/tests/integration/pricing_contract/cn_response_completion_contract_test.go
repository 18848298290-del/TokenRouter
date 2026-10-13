package pricingcontract

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// A Claude-like client name on a CN provider needs an effective configured
// price before it can become a billing candidate. The recorder fixtures
// distinguish filtered aliases, administrator-priced aliases, and an empty
// candidate list that reports unavailable pricing.

// TestFilterCNProviderBillingModelCandidates passes Claude-like and CN model
// names through Kimi candidate filtering. It checks retained order, an empty
// all-Claude result, and the unchanged Claude candidate on an OpenAI provider.
func TestFilterCNProviderBillingModelCandidates(t *testing.T) {
	svc := completion.NewRecorder(completion.Dependencies{}, completion.RecorderOptions{DefaultMultiplier: 1})

	apiKey := &apikey.APIKey{Group: &routing.Group{ID: 1}}
	cnProvider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformKimi}}

	filtered := svc.FilterCNProviderBillingModelCandidates(
		context.Background(), gatewayprovider.ProjectCompletionProvider(gatewayprovider.ExecutionCompletionRecord(cnProvider)), gatewayprovider.ProjectCompletionKey(apiKey), []string{"kimi-k2-0905-preview", "claude-sonnet-4-5", "sonnet-custom", "moonshot-v1-8k"},
	)
	require.Equal(t, []string{"kimi-k2-0905-preview", "moonshot-v1-8k"}, filtered)

	require.Empty(t, svc.FilterCNProviderBillingModelCandidates(
		context.Background(), gatewayprovider.ProjectCompletionProvider(gatewayprovider.ExecutionCompletionRecord(cnProvider)), gatewayprovider.ProjectCompletionKey(apiKey), []string{"claude-sonnet-4-5"},
	))

	openAIProvider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI}}
	require.Equal(t, []string{"claude-sonnet-4-5"}, svc.FilterCNProviderBillingModelCandidates(
		context.Background(), gatewayprovider.ProjectCompletionProvider(gatewayprovider.ExecutionCompletionRecord(openAIProvider)), gatewayprovider.ProjectCompletionKey(apiKey), []string{"claude-sonnet-4-5"},
	))
}

// TestFilterCNProviderBillingModelCandidatesKeepsExplicitConfigPricing gives a
// Kimi route a manual Claude-name token card. It checks that an effective
// input and output price keeps that candidate available to
// the completion recorder.
func TestFilterCNProviderBillingModelCandidatesKeepsExplicitConfigPricing(t *testing.T) {
	inputPrice := 0.000001
	outputPrice := 0.000002
	billing := billingtestkit.Calculator(nil, nil)
	resolver := billingtestkit.SharedPriceResolver(billing, 1, pricing.DefaultBillingSettings(), []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4-5"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
		OutputPrice: &outputPrice,
	}})
	svc := completion.NewRecorder(completion.Dependencies{Calculator: billing, Prices: resolver}, completion.RecorderOptions{DefaultMultiplier: 1})
	group := &routing.Group{ID: 1}
	apiKey := &apikey.APIKey{Group: group}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformKimi}}

	require.Equal(t, []string{"claude-sonnet-4-5"}, svc.FilterCNProviderBillingModelCandidates(
		context.Background(), gatewayprovider.ProjectCompletionProvider(gatewayprovider.ExecutionCompletionRecord(provider)), gatewayprovider.ProjectCompletionKey(apiKey), []string{"claude-sonnet-4-5"},
	))
}

// TestCalculateOpenAIRecordUsageCostEmptyCandidatesIsPricingUnavailable offers
// token usage with no billing model candidates. It checks that calculation
// returns an error recognized by the
// recorder's unavailable-pricing classifier.
func TestCalculateOpenAIRecordUsageCostEmptyCandidatesIsPricingUnavailable(t *testing.T) {
	svc := completion.NewRecorder(completion.Dependencies{}, completion.RecorderOptions{DefaultMultiplier: 1})

	apiKey := &apikey.APIKey{Group: &routing.Group{ID: 1}}

	_, err := svc.CalculateOpenAIRecordUsageCostAt(
		context.Background(), gatewayprovider.ProjectOpenAICompletionResult(nil, nil), gatewayprovider.ProjectCompletionKey(apiKey), nil,
		1, 1, 1, 1, pricing.UsageTokens{InputTokens: 100}, "", time.Time{},
	)
	require.Error(t, err)
	require.True(t, completion.IsUsagePricingUnavailableError(err), err)
}
