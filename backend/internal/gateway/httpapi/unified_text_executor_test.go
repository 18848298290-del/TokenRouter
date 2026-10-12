package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/messageforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

type unifiedRecordWriter struct{ rows []*usage.UsageLog }

type unifiedRecordEffects struct{}

type unifiedRecordModels struct{}

type unifiedSearchSource struct{}

// TestTextPricingCompactModel checks Compact global models, provider overrides, and the final billing model.
func TestTextPricingCompactModel(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, tc := range []struct {
			name, requested, global, providerCompact, upstream, billingSource string
			ordinary, rejected                                                bool
		}{
			{name: "unpriced_global", requested: "gpt-5.6-luna", global: "private-compact", rejected: true},
			{name: "priced_global", requested: "private-model", global: "gpt-5.6-luna", upstream: "gpt-5.6-luna"},
			{name: "provider_overrides_global", requested: "gpt-5.6-luna", global: "private-compact", providerCompact: "gpt-5.6-luna", upstream: "gpt-5.6-luna"},
			{name: "unpriced_provider_override", requested: "gpt-5.6-luna", global: "gpt-5.6-luna", providerCompact: "private-compact", rejected: true},
			{name: "requested_billing", requested: "gpt-5.6-luna", global: "private-compact", upstream: "private-compact", billingSource: routing.BillingModelSourceRequested},
			{name: "ordinary_responses", requested: "gpt-5.6-luna", global: "private-compact", upstream: "gpt-5.6-luna", ordinary: true},
		} {
			t.Run(fmt.Sprintf("%s/passthrough=%t", tc.name, passthrough), func(t *testing.T) {
				prices := textPricingFixture(t)
				groupID := int64(100)
				key := &apikey.APIKey{GroupID: &groupID}
				source := protocol.ProtocolResponsesCompact
				path := "/v1/responses/compact"
				if tc.ordinary {
					source = protocol.ProtocolOpenAIResponses
					path = "/v1/responses"
				}
				billingSource := tc.billingSource
				if billingSource == "" {
					billingSource = routing.BillingModelSourceUpstream
				}
				mapping := routing.GroupMappingResult{MappedModel: tc.requested, BillingModelSource: billingSource}
				ctx := requeststate.WithClientProtocol(context.Background(), source)
				ctx = requeststate.WithRoutePlan(ctx, routing.Plan(routing.PlanInput{GroupID: &groupID, ClientProtocol: source, RequestedModel: tc.requested, GroupMapping: mapping}))
				response := fmt.Sprintf(`{"id":"resp-compact","object":"response","status":"completed","model":%q,"output":[],"usage":{"input_tokens":100,"output_tokens":10}}`, tc.upstream)
				transport := &auxiliaryHTTPRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}}
				executor := &UnifiedTextExecutor{Pricing: prices, OpenAI: newResponsesFixture(responsesFixtureInputs{transport: transport, compactModel: tc.global})}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, path, nil).WithContext(ctx)
				c.Set("gateway_effective_key", key)
				target := gatewayadapter.NewExecutionProvider(&provider.Record{
					ID: 1, Platform: provider.PlatformOpenAI, Type: "apikey",
					Extra:       map[string]any{"openai_passthrough": passthrough},
					Credentials: map[string]any{"api_key": "test-key", provider.UpstreamProtocolsKey: []string{"openai_responses", "openai_responses_compact"}},
				})
				if tc.providerCompact != "" {
					target.Record.Credentials["compact_model_mapping"] = map[string]any{tc.requested: tc.providerCompact}
				}
				body := []byte(fmt.Sprintf(`{"model":%q,"input":"compact this"}`, tc.requested))
				result, err := executor.Responses(ctx, c, target, body)
				if tc.rejected {
					require.ErrorIs(t, err, admission.ErrModelPricingRejected)
					require.Nil(t, result)
					require.Nil(t, transport.lastReq)
					require.Equal(t, http.StatusBadRequest, recorder.Code)
					require.Contains(t, recorder.Body.String(), "model_pricing_unavailable")
					return
				}
				require.NoError(t, err)
				require.NotNil(t, transport.lastReq)
				require.Equal(t, tc.upstream, gjson.GetBytes(transport.lastBody, "model").String())
				// Passthrough omits UpstreamModel for an unchanged model, so Model supplies the effective name.
				require.Equal(t, tc.upstream, completion.ForwardResultBillingModel(result.UpstreamModel, result.Model))
				billingModel := completion.OpenAIUsageBillingModel(&completion.Result{Model: result.Model, BillingModel: result.BillingModel, UpstreamModel: result.UpstreamModel}, mapping.ToUsageFields(tc.requested, result.UpstreamModel))
				require.NoError(t, prices.Check(ctx, &groupID, billingModel))
			})
		}
	}
}

// TestTextPricingResolvesResponsesChatRoute checks preflight against forwarding when Responses resolves to Chat.
func TestTextPricingResolvesResponsesChatRoute(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough=%t", passthrough), func(t *testing.T) {
			prices := textPricingFixture(t)
			group := &routing.Group{ID: 100, ProtocolFallbacks: map[protocol.ProtocolID][]protocol.ProtocolID{protocol.ProtocolOpenAIResponses: {protocol.ProtocolOpenAIChatCompletions}}}
			key := &apikey.APIKey{GroupID: &group.ID, Group: group}
			mapping := routing.GroupMappingResult{MappedModel: "codex-auto-review"}
			ctx := requeststate.WithClientProtocol(context.Background(), protocol.ProtocolOpenAIResponses)
			ctx = requeststate.WithRoutePlan(ctx, routing.Plan(routing.PlanInput{Group: group, ClientProtocol: protocol.ProtocolOpenAIResponses, RequestedModel: "codex-auto-review", GroupMapping: mapping}))
			target := gatewayadapter.NewExecutionProvider(&provider.Record{
				ID: 1, Platform: provider.PlatformOpenAI, Type: "apikey",
				Extra:       map[string]any{"openai_passthrough": passthrough},
				Credentials: map[string]any{"api_key": "test-key", provider.UpstreamProtocolsKey: []string{"openai_chat_completions"}, "model_mapping": map[string]any{"codex-auto-review": "gpt-5.6-luna"}},
			})
			transport := &auxiliaryHTTPRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"chat-review","object":"chat.completion","model":"gpt-5.6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`))}}
			executor := &UnifiedTextExecutor{Pricing: prices, OpenAI: newResponsesFixture(responsesFixtureInputs{transport: transport})}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			c.Set("gateway_effective_key", key)
			result, err := executor.Responses(ctx, c, target, []byte(`{"model":"codex-auto-review","input":"review"}`))
			require.NoError(t, err)
			require.NotNil(t, transport.lastReq)
			require.Equal(t, "/v1/chat/completions", transport.lastReq.URL.Path)
			require.Equal(t, "gpt-5.6-luna", result.BillingModel)
			require.Equal(t, "gpt-5.6-luna", result.UpstreamModel)
			require.Empty(t, target.Route.Protocol())
		})
	}
}

// TestTextPricingRouteErrorPreservesCause checks that protocol rejection reaches route-error handling.
func TestTextPricingRouteErrorPreservesCause(t *testing.T) {
	group := &routing.Group{ID: 100, ProtocolFallbacks: map[protocol.ProtocolID][]protocol.ProtocolID{protocol.ProtocolOpenAIResponses: {}}}
	ctx := requeststate.WithClientProtocol(context.Background(), protocol.ProtocolOpenAIResponses)
	ctx = requeststate.WithRoutePlan(ctx, routing.Plan(routing.PlanInput{Group: group, ClientProtocol: protocol.ProtocolOpenAIResponses, RequestedModel: "codex-auto-review"}))
	target := gatewayadapter.NewExecutionProvider(&provider.Record{ID: 1, Platform: provider.PlatformOpenAI, Type: "apikey", Credentials: map[string]any{provider.UpstreamProtocolsKey: []string{"openai_chat_completions"}}})
	executor := &UnifiedTextExecutor{Pricing: textPricingFixture(t)}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	_, err := executor.Responses(ctx, c, target, []byte(`{"model":"codex-auto-review","input":"review"}`))
	require.Error(t, err)
	require.NotErrorIs(t, err, admission.ErrModelPricingRejected)
	require.Contains(t, err.Error(), "no enabled route")
	require.False(t, c.Writer.Written())
}

// TestUnifiedTextDispatchUsesSelectedProvider installs the selected platform's executor so incorrect dispatch
// fails immediately.
func TestUnifiedTextDispatchUsesSelectedProvider(t *testing.T) {
	tests := []struct{ platform, model, path, response string }{
		{"anthropic", "claude-sonnet-4-5", "/v1/messages", `{"id":"msg-native","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":3,"cache_creation_input_tokens":7,"cache_read_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":5},"speed":"fast"}}`},
		{"gemini", "gemini-2.5-flash", ":generateContent", `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"totalTokenCount":13}}`},
		{"openai", "gpt-5.4", "/v1/responses", `{"id":"resp-native","object":"response","status":"completed","model":"gpt-5.4","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}`},
	}
	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			transport := &auxiliaryHTTPRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(tt.response))}}
			target := gatewayadapter.NewExecutionProvider(&provider.Record{ID: 42, Name: "selected", Platform: tt.platform, Type: "apikey", Concurrency: 1, Credentials: map[string]any{"api_key": "test-key"}})
			executor := &UnifiedTextExecutor{}
			switch tt.platform {
			case "anthropic":
				executor.Anthropic = NewMessagesExecutor(messageforward.NewRuntime(messageforward.Dependencies{Search: searchtools.NewEmulator(unifiedSearchSource{}, nil, nil, nil, nil, nil), Credentials: &provider.MessageCredentialSource{}, Transport: transport, Deferred: &provider.DeferredService{}}, messageforward.Options{Configured: true, ResponseReadLimit: 1 << 20}), nil)
			case "gemini":
				executor.Gemini = &GeminiExecutor{Runtime: &googleforward.Gemini{Transport: transport, Options: googleforward.Options{Configured: true, ResponseReadLimit: 1 << 20}}}
			case "openai":
				executor.OpenAI = newResponsesFixture(responsesFixtureInputs{transport: transport})
			}
			var result *forward.OpenAIResult
			var err error
			if tt.platform == "openai" {
				result, err = executor.Responses(context.Background(), c, target, []byte(`{"model":"`+tt.model+`","input":"hello"}`))
			} else {
				result, err = executor.Messages(context.Background(), c, target, []byte(`{"model":"`+tt.model+`","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`), "", tt.model)
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Contains(t, transport.lastReq.URL.Path, tt.path)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), "ok")
			if tt.platform == "openai" {
				require.Nil(t, result.NativeUsage)
			} else {
				require.NotNil(t, result.NativeUsage)
				require.Equal(t, 10, result.NativeUsage.InputTokens)
			}
		})
	}
}

func (w *unifiedRecordWriter) Create(_ context.Context, row *usage.UsageLog) (bool, error) {
	w.rows = append(w.rows, row)
	return true, nil
}

func (unifiedRecordEffects) ProviderUsed(int64) {}

func (unifiedRecordEffects) InvalidateAuth(context.Context, string) {}

func (unifiedRecordEffects) Settled(completion.SettlementInput, *billing.UsageBillingApplyResult) {}

func (unifiedRecordModels) Candidates(model string, _ ...string) []string { return []string{model} }

// TestUnifiedNativeUsageSurvivesCaptureAndRecord checks native input-token categories through conversion, capture,
// and billing.
func TestUnifiedNativeUsageSurvivesCaptureAndRecord(t *testing.T) {
	tier := "priority"
	native := &forward.MessagesResult{RequestID: "native-usage", Model: "claude-sonnet-4-5", UpstreamModel: "claude-sonnet-4-5", Usage: protocol.TokenUsage{InputTokens: 100, OutputTokens: 10, CacheReadInputTokens: 30, CacheCreationInputTokens: 12, CacheCreation5mTokens: 5, CacheCreation1hTokens: 7, Speed: "fast"}, Stream: true, ClientDisconnect: true, ServiceTier: &tier, Duration: time.Second}
	result := nativeTextResult(native, "/v1/messages")
	require.NotNil(t, result.NativeUsage)
	require.Equal(t, native.Usage, *result.NativeUsage)
	require.True(t, result.ClientDisconnect)
	native.Usage.CacheCreation1hTokens = 99
	require.Equal(t, 7, result.NativeUsage.CacheCreation1hTokens)
	user := &identity.User{ID: 1}
	input := gatewayadapter.CaptureOpenAI(context.Background(), &gatewayadapter.OpenAICapture{Result: result, APIKey: &apikey.APIKey{ID: 2, User: user}, User: user, Provider: &provider.Record{ID: 3, Platform: "anthropic", Type: "apikey"}})
	require.True(t, input.Result.NativeUsage)
	require.Equal(t, 5, input.Result.Usage.CacheCreation5mTokens)
	require.Equal(t, 7, input.Result.Usage.CacheCreation1hTokens)
	require.Equal(t, "fast", input.Result.Usage.Speed)
	result.NativeUsage.InputTokens = 999
	require.Equal(t, 100, input.Result.Usage.InputTokens)
	logs := &unifiedRecordWriter{}
	recorder := completion.NewRecorder(completion.Dependencies{Calculator: billing.NewCalculator(nil, billing.CalculatorOptions{}), Funds: unifiedRecordFunds{}, Logs: logs, Effects: unifiedRecordEffects{}, Models: unifiedRecordModels{}}, completion.RecorderOptions{DefaultMultiplier: 1})
	require.NoError(t, recorder.Record(context.Background(), input, true))
	require.Len(t, logs.rows, 1)
	row := logs.rows[0]
	require.Equal(t, 100, row.InputTokens, "统一OpenAI外壳不能扣减原生缓存桶")
	require.Equal(t, 30, row.CacheReadTokens)
	require.Equal(t, 12, row.CacheCreationTokens)
	require.Equal(t, 5, row.CacheCreation5mTokens)
	require.Equal(t, 7, row.CacheCreation1hTokens)
	require.Equal(t, "anthropic", row.Platform)
	require.Equal(t, "priority", *row.ServiceTier)

	// When interpreted as OpenAI total input, the same value includes cache tokens that billing subtracts.
	input.Result.NativeUsage = false
	require.NoError(t, recorder.Record(context.Background(), input, true))
	require.Len(t, logs.rows, 2)
	require.Equal(t, 58, logs.rows[1].InputTokens)
}

func (unifiedSearchSource) Current() searchtools.Searcher { return nil }
