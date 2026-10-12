package openaiattempt

// These scenarios cover group fallback and completion capture across Responses, Chat, and Messages attempts.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

type completionAttempt interface {
	Complete()
	TryGroupFallback(error) (bool, bool)
}

type completionCaptureEffects struct {
	input *completion.SettlementInput
}

func TestCompletionCaptureBeforeSubmission(t *testing.T) {
	for _, entry := range []struct {
		name     string
		endpoint string
		protocol protocol.ProtocolID
	}{
		{"responses", "/v1/responses", protocol.ProtocolOpenAIResponses},
		{"chat", "/v1/chat/completions", protocol.ProtocolOpenAIChatCompletions},
		{"messages", "/v1/messages", protocol.ProtocolAnthropicMessages},
	} {
		t.Run(entry.name, func(t *testing.T) {
			for _, mode := range []string{"ordinary", "partial", "typed nil quota"} {
				t.Run(mode, func(t *testing.T) {
					ctx := context.WithValue(context.Background(), telemetry.RequestID, "local-before")
					ctx = requeststate.WithClientProtocol(ctx, entry.protocol)
					ctx = requeststate.WithRequestedReasoningEffort(ctx, "medium")
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, entry.endpoint, nil).WithContext(ctx)
					c.Request.Header.Set("User-Agent", "capture-agent")
					c.Request.Header.Set("X-Session-Id", "session-before")
					c.Request.RemoteAddr = "198.51.100.9:4567"
					targetGroup := int64(2)
					original := &apikey.APIKey{ID: 10, GroupID: new(int64(1)), Group: &routing.Group{ID: 1, FallbackGroupIDOnInvalidRequest: &targetGroup}}
					key := &apikey.APIKey{
						ID: 20, UserID: 30, User: &identity.User{ID: 30, Balance: 15}, GroupID: &targetGroup,
						Group: &routing.Group{ID: 2, Status: routing.StatusActive, RateMultiplier: 1.25, AllowedProtocols: []protocol.ProtocolID{entry.protocol}},
					}
					sub := &billing.UserSubscription{ID: 50, Plan: &billing.SubscriptionPlan{GroupIDs: []int64{2}, GroupRateMultipliers: map[int64]float64{2: 1.5}}}
					providerRate := 1.75
					target := &gatewayprovider.ExecutionProvider{Record: provider.Record{ID: 40, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, RateMultiplier: &providerRate, LoadLocation: time.LoadLocation}}
					result := &forward.OpenAIResult{
						RequestID: "upstream-before", Model: "gpt-4o", UpstreamModel: "gpt-4o", UpstreamEndpoint: "/v1/responses",
						Usage: openai.ForwardUsage{InputTokens: 10, OutputTokens: 6}, Duration: time.Second,
						ImageCount: 1, ImageOutputSizes: []string{"1K"}, ImageSizeBreakdown: map[string]int{"1K": 1},
					}
					if mode == "partial" {
						result.ClientDisconnect = true
						result.NativeUsage = &protocol.TokenUsage{InputTokens: 10, OutputTokens: 6}
					}
					logs, funds := &testkit.UsageLogStore{}, &testkit.SettlementStore{}
					effects := &completionCaptureEffects{}
					recording := testkit.NewRecording(logs, funds, nil, true)
					recording.Dependencies.Effects = effects
					nowCalls := 0
					recording.Options.Now = func() time.Time {
						nowCalls++
						return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
					}
					mapping := routing.GroupMappingResult{Mapped: true, MappedModel: "gpt-4o", PricingConfigID: 60, BillingModelSource: "group_mapped"}
					fixed := &openAIExecutionDependencies{
						recorder:           completion.NewRecorder(recording.Dependencies, recording.Options),
						replaceModelInBody: openai.ReplaceModelInBody,
						fallback: GroupFallbackPorts{
							Resolve: func(context.Context, *apikey.APIKey, int64, protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
								return key, sub, nil
							},
							Plan: func(_ context.Context, key *apikey.APIKey, requested string) routing.RoutePlan {
								return routing.Plan(routing.PlanInput{Group: key.Group, RequestedModel: requested, ClientProtocol: entry.protocol, GroupMapping: mapping})
							},
						},
					}
					if mode == "typed nil quota" {
						var quota *apikey.APIKeyService
						fixed.apiKeyService = quota
					}
					base := &responsesAttemptBridge{
						fixed: fixed, c: c, apiKey: original, provider: target, result: result,
						subject: authctx.AuthSubject{UserID: 30}, reqModel: "client-model", nativeCompactionV2: true,
						body: []byte(`{"model":"client-model","reasoning":{"effort":"high"}}`),
					}
					attempt, active := newCompletionAttempt(entry.name, base)
					handled, retry := attempt.TryGroupFallback(&antigravity.PromptTooLongError{})
					require.True(t, handled)
					require.True(t, retry)
					require.Same(t, key, active.apiKey)
					if entry.name == "messages" {
						active.groupMapping = routing.GroupMappingResult{Mapped: true, MappedModel: "other-entry", PricingConfigID: 999}
					}
					wantHash := billing.HashUsageRequestPayload(active.body)
					if entry.name == "chat" {
						wantHash = "client:local-before"
					}
					var task completion.UsageRecordTask
					submissions := 0
					fixed.submitOpenAIUsageRecordTask = func(gotContext *gin.Context, gotResult *forward.OpenAIResult, submitted completion.UsageRecordTask) {
						submissions++
						require.Same(t, c, gotContext)
						require.Same(t, result, gotResult)
						require.Equal(t, 1, gotResult.ImageCount)
						require.Equal(t, "medium", *gotResult.RequestedReasoningEffort)
						require.Zero(t, nowCalls)
						key.ID, key.User.ID, key.User.Balance = 200, 300, 150
						*key.GroupID, key.Group.RateMultiplier = 22, 9
						target.Record.ID, providerRate = 400, 9
						sub.ID, sub.Plan.GroupIDs[0], sub.Plan.GroupRateMultipliers[2] = 500, 22, 9
						result.Model, result.UpstreamModel = "changed", "changed"
						result.Usage.InputTokens, result.ImageCount = 999, 99
						result.ImageOutputSizes[0], result.ImageSizeBreakdown["1K"] = "4K", 99
						*result.RequestedReasoningEffort = "low"
						if result.NativeUsage != nil {
							result.NativeUsage.InputTokens = 999
						}
						active.body[0] = '!'
						c.Request.Header.Set("User-Agent", "changed")
						c.Request.Header.Set("X-Session-Id", "changed")
						task = submitted
					}
					attempt.Complete()
					require.Equal(t, 1, submissions)
					require.Zero(t, logs.Calls)
					require.NotNil(t, task)
					task(context.Background())
					require.Equal(t, 1, logs.Calls)
					require.Equal(t, 1, nowCalls)
					require.NotNil(t, effects.input)
					require.Equal(t, int64(20), effects.input.APIKey.ID)
					require.Equal(t, int64(2), *effects.input.APIKey.GroupID)
					require.Equal(t, 1.25, effects.input.APIKey.Group.RateMultiplier)
					require.Equal(t, int64(30), effects.input.User.ID)
					require.Equal(t, 15.0, effects.input.User.Balance)
					require.Equal(t, int64(40), effects.input.Provider.ID)
					require.Equal(t, 1.75, effects.input.Provider.RateMultiplier)
					require.Equal(t, int64(50), effects.input.Subscription.ID)
					require.Equal(t, []int64{2}, effects.input.Subscription.Plan.GroupIDs)
					require.Equal(t, map[int64]float64{2: 1.5}, effects.input.Subscription.Plan.GroupRateMultipliers)
					require.Equal(t, mode == "typed nil quota", effects.input.QuotaUpdates)
					require.Equal(t, wantHash, funds.LastCmd.RequestPayloadHash)
					log := logs.LastLog
					require.Equal(t, "local-before", log.RequestID)
					require.Equal(t, "gpt-4o", log.Model)
					require.Equal(t, "client-model", log.RequestedModel)
					require.Equal(t, "client-model→gpt-4o", *log.ModelMappingChain)
					require.Equal(t, int64(60), *log.PricingConfigID)
					require.Equal(t, "medium", *log.RequestedReasoningEffort)
					require.Equal(t, 10, log.InputTokens)
					require.Equal(t, 6, log.OutputTokens)
					require.Equal(t, 1, log.ImageCount)
					require.Equal(t, map[string]int{"1K": 1}, log.ImageSizeBreakdown)
					require.Equal(t, entry.name == "responses" && mode != "partial", log.NativeCompactionV2)
					require.Equal(t, entry.endpoint, *log.InboundEndpoint)
					require.Equal(t, "/v1/responses", *log.UpstreamEndpoint)
					require.Equal(t, "capture-agent", *log.UserAgent)
					require.Equal(t, "198.51.100.9", *log.IPAddress)
					require.Equal(t, "session-before", *log.SessionID)
				})
			}
		})
	}
}

func TestCompletionCaptureNilResultDoesNotSubmit(t *testing.T) {
	for _, entry := range []string{"responses", "chat", "messages"} {
		t.Run(entry, func(t *testing.T) {
			attempt, _ := newCompletionAttempt(entry, &responsesAttemptBridge{})
			require.NotPanics(t, attempt.Complete)
		})
	}
}

func newCompletionAttempt(entry string, base *responsesAttemptBridge) (completionAttempt, *responsesAttemptBridge) {
	switch entry {
	case "chat":
		attempt := &openAIChatAttemptBridge{responsesAttemptBridge: *base}
		return attempt, &attempt.responsesAttemptBridge
	case "messages":
		attempt := &openAIMessageAttemptBridge{responsesAttemptBridge: *base}
		return attempt, &attempt.responsesAttemptBridge
	default:
		return base, base
	}
}

func (e *completionCaptureEffects) ProviderUsed(int64) {}

func (e *completionCaptureEffects) InvalidateAuth(context.Context, string) {}

func (e *completionCaptureEffects) Settled(input completion.SettlementInput, _ *billing.UsageBillingApplyResult) {
	e.input = &input
}
