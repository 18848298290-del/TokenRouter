package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// TestTextModelPricingSources distinguishes catalogue prices, manual zero prices, and multiplier-only entries
// without base prices.
func TestTextModelPricingSources(t *testing.T) {
	zero, input, multiplier := 0.0, 2e-7, 2.0
	for _, tc := range []struct {
		name, model string
		card        *routing.ModelPricingEntry
		missing     bool
	}{
		{name: "catalog", model: "gpt-5.6-luna"},
		{name: "missing", model: "codex-auto-review", missing: true},
		{name: "manual", model: "codex-auto-review", card: &routing.ModelPricingEntry{InputPrice: &input}},
		{name: "free", model: "codex-auto-review", card: &routing.ModelPricingEntry{InputPrice: &zero, OutputPrice: &zero}},
		{name: "request_free", model: "codex-auto-review", card: &routing.ModelPricingEntry{BillingMode: pricing.BillingModePerRequest, PerRequestPrice: &zero}},
		{name: "interval", model: "codex-auto-review", card: &routing.ModelPricingEntry{Intervals: []pricing.PricingInterval{{InputPrice: &input}}}},
		{name: "multiplier_missing", model: "codex-auto-review", card: &routing.ModelPricingEntry{FastMultiplier: &multiplier}, missing: true},
		{name: "interval_multiplier_missing", model: "codex-auto-review", card: &routing.ModelPricingEntry{Intervals: []pricing.PricingInterval{{InputMultiplier: &multiplier}}}, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cards []routing.ModelPricingEntry
			if tc.card != nil {
				tc.card.Models = []string{tc.model}
				cards = append(cards, *tc.card)
			}
			err := textPricingFixture(t, cards...).Check(context.Background(), testkit.GroupID(), tc.model)
			if tc.missing {
				require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestTextPricingUsesBillingModel covers key, group, and provider mappings across all billing model sources.
func TestTextPricingUsesBillingModel(t *testing.T) {
	prices := textPricingFixture(t)
	key := &apikey.APIKey{GroupID: testkit.GroupID()}
	for _, tc := range []struct {
		name, requested, mapped, upstream, source string
		missing, passthrough                      bool
		protocol                                  protocol.ProtocolID
	}{
		{name: "key_redirect", requested: "gpt-5.6-luna", mapped: "gpt-5.6-luna", source: routing.BillingModelSourceRequested},
		{name: "group_mapping", requested: "codex-auto-review", mapped: "gpt-5.6-luna", source: routing.BillingModelSourceGroupMapped},
		{name: "provider_mapping", requested: "codex-auto-review", mapped: "codex-auto-review", upstream: "gpt-5.6-luna", source: routing.BillingModelSourceUpstream},
		{name: "default_provider_mapping", requested: "codex-auto-review", mapped: "codex-auto-review", upstream: "gpt-5.6-luna"},
		{name: "requested_missing", requested: "codex-auto-review", mapped: "gpt-5.6-luna", source: routing.BillingModelSourceRequested, missing: true},
		{name: "upstream_missing", requested: "gpt-5.6-luna", mapped: "gpt-5.6-luna", upstream: "private-model", source: routing.BillingModelSourceUpstream, missing: true},
		{name: "passthrough_requested_missing", requested: "codex-auto-review", mapped: "codex-auto-review", upstream: "gpt-5.6-luna", passthrough: true, missing: true},
		{name: "passthrough_upstream", requested: "codex-auto-review", mapped: "codex-auto-review", upstream: "gpt-5.6-luna", source: routing.BillingModelSourceUpstream, passthrough: true},
		{name: "ws_upstream", requested: "codex-auto-review", mapped: "codex-auto-review", upstream: "gpt-5.6-luna", passthrough: true, protocol: protocol.ProtocolResponsesWebSocket},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := &provider.Record{ID: 1, Platform: "openai", Type: "apikey"}
			record.Extra = map[string]any{"openai_passthrough": tc.passthrough}
			if tc.upstream != "" {
				record.Credentials = map[string]any{"model_mapping": map[string]any{tc.mapped: tc.upstream}}
			}
			mapping := routing.GroupMappingResult{MappedModel: tc.mapped, Mapped: tc.mapped != tc.requested, BillingModelSource: tc.source}
			plan := routing.Plan(routing.PlanInput{GroupID: key.GroupID, RequestedModel: tc.requested, GroupMapping: mapping})
			ctx := requeststate.WithRoutePlan(context.Background(), plan)
			source := tc.protocol
			if source == "" {
				source = protocol.ProtocolOpenAIResponses
			}
			err := CheckTextModelPricing(ctx, prices, key, gatewayprovider.NewExecutionProvider(record), tc.requested, tc.mapped, nil, source)
			if tc.missing {
				require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestTextPricingRejectsBeforeForward checks that missing prices prevent streaming and non-streaming upstream calls.
func TestTextPricingRejectsBeforeForward(t *testing.T) {
	executor := &UnifiedTextExecutor{Pricing: textPricingFixture(t)}
	target := gatewayprovider.NewExecutionProvider(&provider.Record{ID: 1, Platform: "openai", Type: "apikey"})
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", path, stream), func(t *testing.T) {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				body := []byte(fmt.Sprintf(`{"model":"codex-auto-review","stream":%t,"input":"review","tools":[{"type":"image_generation"}]}`, stream))
				c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
				c.Set("gateway_effective_key", &apikey.APIKey{GroupID: testkit.GroupID()})
				var result *forward.OpenAIResult
				var err error
				switch path {
				case "/v1/messages":
					result, err = executor.Messages(c.Request.Context(), c, target, body, "", "codex-auto-review")
				case "/v1/chat/completions":
					result, err = executor.Chat(c.Request.Context(), c, target, body, "", "codex-auto-review")
				default:
					result, err = executor.Responses(c.Request.Context(), c, target, body)
				}
				require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
				require.Nil(t, result)
				require.Equal(t, http.StatusBadRequest, recorder.Code)
				require.Contains(t, recorder.Body.String(), admission.ModelPricingUnavailableMessage)
				if path != "/v1/messages" {
					require.Contains(t, recorder.Body.String(), "model_pricing_unavailable")
				}
			})
		}
	}
}

// TestAutoReviewConfiguredPriceForwardsAndBills checks admission and billing with configured review-model rates and usage.
func TestAutoReviewConfiguredPriceForwardsAndBills(t *testing.T) {
	input, output, cache := 2e-7, 1.2e-6, 2e-8
	prices := textPricingFixture(t, routing.ModelPricingEntry{Models: []string{"codex-auto-review"}, InputPrice: &input, OutputPrice: &output, CacheReadPrice: &cache})
	transport := &auxiliaryHTTPRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp-review","object":"response","status":"completed","model":"codex-auto-review","output":[],"usage":{"input_tokens":95262,"output_tokens":50,"input_tokens_details":{"cached_tokens":92928}}}`))}}
	executor := &UnifiedTextExecutor{Pricing: prices, OpenAI: newResponsesFixture(responsesFixtureInputs{transport: transport})}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("gateway_effective_key", &apikey.APIKey{GroupID: testkit.GroupID()})
	target := gatewayprovider.NewExecutionProvider(&provider.Record{ID: 1, Platform: "openai", Type: "apikey", Credentials: map[string]any{"api_key": "test-key"}})
	result, err := executor.Responses(c.Request.Context(), c, target, []byte(`{"model":"codex-auto-review","input":"review"}`))
	require.NoError(t, err)
	require.NotNil(t, transport.lastReq)
	require.NotNil(t, result)
	calculator := billing.NewCalculator(nil, billing.CalculatorOptions{})
	cost, err := calculator.CalculateCostUnified(billing.CostInput{Ctx: context.Background(), GroupID: testkit.GroupID(), Model: "codex-auto-review", Resolver: prices.Resolver, RateMultiplier: 1, Tokens: billing.UsageTokens{InputTokens: result.Usage.InputTokens - result.Usage.CacheReadInputTokens, OutputTokens: result.Usage.OutputTokens, CacheReadTokens: result.Usage.CacheReadInputTokens}})
	require.NoError(t, err)
	require.InDelta(t, 0.00238536, cost.TotalCost, 1e-12)
}
