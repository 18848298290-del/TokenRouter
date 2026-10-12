package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	pricingprovider "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/testutil"
)

// gatewayExecutionProviderRows supplies execution providers by group for forwarding tests.
type gatewayExecutionProviderRows struct {
	gatewayprovider.ExecutionProviderStore

	byGroup map[int64][]gatewayprovider.ExecutionProvider
}

// gatewaySessionLimitCacheStub records cache operations; scheduling and completion decide when to release
// sessions.
type gatewaySessionLimitCacheStub struct {
	testutil.StubSessionLimitCache
	registered   map[int64][]string
	unregistered map[int64][]string
}

type gatewaySessionUpstreamStub struct {
	respond func(int64) (*http.Response, error)
	called  []int64
}

func (s *gatewayExecutionProviderRows) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]gatewayprovider.ExecutionProvider, error) {
	providers, ok := s.byGroup[groupID]
	if !ok {
		return nil, nil
	}
	out := make([]gatewayprovider.ExecutionProvider, len(providers))
	copy(out, providers)
	return out, nil
}

func TestPrepareGatewayAttemptRequestUsesCurrentAPIKeyGroupMapping(t *testing.T) {
	sourceGroupID := int64(6101)
	fallbackGroupID := int64(6102)
	pricingConfigService := routingtestkit.NewPricingConfigService(&gatewayExecutionPricingConfigRows{
		modelConfigs: []routingtestkit.Configuration{
			{
				ID:           701,
				Status:       billing.StatusActive,
				GroupIDs:     []int64{sourceGroupID},
				ModelMapping: map[string]string{"client-alias": "source-group-model"},
			},
			{
				ID:           702,
				Status:       billing.StatusActive,
				GroupIDs:     []int64{fallbackGroupID},
				ModelMapping: map[string]string{"client-alias": "fallback-group-model"},
			},
		},
		groupPlatforms: map[int64]string{
			sourceGroupID:   capability.PlatformAnthropic,
			fallbackGroupID: capability.PlatformAnthropic,
		},
	}, nil, routing.PricingConfigOptions{Warn: slog.Warn, Now: time.Now, LoadLocation: pricingprovider.LoadPricingLocation},
	)
	handler := newGatewayExecutionHandlerWithPricingConfigForTest(&gatewayExecutionProviderRows{}, pricingConfigService)
	body := []byte(`{"model":"client-alias","messages":[{"role":"user","content":"hello"}]}`)
	parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), capability.PlatformAnthropic)
	require.NoError(t, err)

	sourceAPIKey := &apikey.APIKey{GroupID: &sourceGroupID}
	sourceAttempt, sourceMapping, err := handler.prepareGatewayAttemptRequest(
		context.Background(), parsed, body, sourceAPIKey, "client-alias",
	)
	require.NoError(t, err)
	require.Equal(t, sourceGroupID, *sourceAttempt.GroupID)
	require.Equal(t, "source-group-model", sourceAttempt.Model)
	require.Equal(t, "source-group-model", gjson.GetBytes(sourceAttempt.Body.Bytes(), "model").String())
	require.Equal(t, int64(701), sourceMapping.PricingConfigID)

	fallbackAPIKey := &apikey.APIKey{GroupID: &fallbackGroupID}
	fallbackAttempt, fallbackMapping, err := handler.prepareGatewayAttemptRequest(
		context.Background(), parsed, body, fallbackAPIKey, "client-alias",
	)
	require.NoError(t, err)
	require.Equal(t, fallbackGroupID, *fallbackAttempt.GroupID)
	require.Equal(t, "fallback-group-model", fallbackAttempt.Model)
	require.Equal(t, "fallback-group-model", gjson.GetBytes(fallbackAttempt.Body.Bytes(), "model").String())
	require.Equal(t, int64(702), fallbackMapping.PricingConfigID)

	usageFields := fallbackMapping.ToUsageFields("client-alias", "upstream-model")
	require.Equal(t, int64(702), usageFields.PricingConfigID)
	require.Equal(t, "fallback-group-model", usageFields.GroupMappedModel)
	require.Equal(t, "client-alias", parsed.Model)
	require.Equal(t, "client-alias", gjson.GetBytes(parsed.Body.Bytes(), "model").String())
}

// TestPrepareGatewayAttemptRequestUsesGeminiGroupMapping checks that Messages bodies use the Gemini group's
// mapped model.
func TestPrepareGatewayAttemptRequestUsesGeminiGroupMapping(t *testing.T) {
	groupID := int64(6103)
	pricingConfigService := routingtestkit.NewPricingConfigService(&gatewayExecutionPricingConfigRows{
		modelConfigs: []routingtestkit.Configuration{{
			ID:           703,
			Status:       billing.StatusActive,
			GroupIDs:     []int64{groupID},
			ModelMapping: map[string]string{"client-alias": "gemini-group-model"},
		}},
		groupPlatforms: map[int64]string{groupID: capability.PlatformGemini},
	}, nil, routing.PricingConfigOptions{Warn: slog.Warn, Now: time.
		Now, LoadLocation: pricingprovider.LoadPricingLocation},
	)
	handler := newGatewayExecutionHandlerWithPricingConfigForTest(&gatewayExecutionProviderRows{}, pricingConfigService)
	body := []byte(`{"model":"client-alias","messages":[{"role":"user","content":"hello"}]}`)
	parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), capability.PlatformAnthropic)
	require.NoError(t, err)

	attempt, mapping, err := handler.prepareGatewayAttemptRequest(
		context.Background(), parsed, body, &apikey.APIKey{GroupID: &groupID}, "client-alias",
	)
	require.NoError(t, err)
	require.Equal(t, "gemini-group-model", attempt.Model)
	require.Equal(t, "gemini-group-model", gjson.GetBytes(attempt.Body.Bytes(), "model").String())
	require.Equal(t, int64(703), mapping.PricingConfigID)
}

func (s *gatewaySessionLimitCacheStub) RegisterSession(_ context.Context, providerID int64, sessionID string, _ int, _ time.Duration) (bool, error) {
	s.registered[providerID] = append(s.registered[providerID], sessionID)
	return true, nil
}

func (s *gatewaySessionLimitCacheStub) UnregisterSession(_ context.Context, providerID int64, sessionID string) error {
	s.unregistered[providerID] = append(s.unregistered[providerID], sessionID)
	return nil
}

func (s *gatewaySessionUpstreamStub) Do(_ *http.Request, _ string, providerID int64, _ int) (*http.Response, error) {
	s.called = append(s.called, providerID)
	return s.respond(providerID)
}

func (s *gatewaySessionUpstreamStub) DoWithTLS(req *http.Request, proxy string, providerID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxy, providerID, concurrency)
}

func gatewaySessionResponse(status int, stream bool) *http.Response {
	contentType := "application/json"
	body := `{"id":"msg_session","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1},"stop_reason":"end_turn"}`
	if status >= 400 {
		body = `{"type":"error","error":{"type":"invalid_request_error","message":"request rejected"}}`
	} else if stream {
		contentType = "text/event-stream"
		body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_session\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"usage\":{\"input_tokens\":1}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

// newGatewaySessionLimitFixture exercises scheduling, forwarding, and handler completion with upstream, Redis,
// and billing stubs.
func newGatewaySessionLimitFixture(t *testing.T, providerType string, failover bool, upstream *gatewaySessionUpstreamStub) (*messageEndpointsFixture, *apikey.APIKey, *gatewaySessionLimitCacheStub) {
	t.Helper()
	groupID := int64(11)
	group := &routing.Group{ID: groupID, Hydrated: true, Status: billing.StatusActive}
	providers := []*gatewayprovider.ExecutionProvider{{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 12, Name: "session-test", Platform: capability.PlatformAnthropic, Type: providerType,
			Credentials: map[string]any{"access_token": "test-token", "model_mapping": map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5-20250929"}}, Extra: map[string]any{"max_sessions": 1},
			Concurrency: 2, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: []providercore.GroupMembership{{ProviderID: 12, GroupID: groupID}},
		},
	}}
	if failover {
		second := *providers[0]
		second.Record.ID, second.Record.Priority = 13, 1
		second.Record.ProviderGroups = []providercore.GroupMembership{{ProviderID: second.Record.ID, GroupID: groupID}}
		providers = append(providers, &second)
	}
	sessions := &gatewaySessionLimitCacheStub{registered: make(map[int64][]string), unregistered: make(map[int64][]string)}
	cfg := &config.Config{}
	snapshots := scheduler.NewSnapshotService(&fakeSchedulerCache{providers: providers}, nil, nil, nil, nil, scheduler.SnapshotBindings{})
	billingCache := newBillingEligibilityFixture(cfg)
	billingCache.Start()
	t.Cleanup(billingCache.Stop)
	completionInput1 := billingtestkit.Calculator(nil, nil)
	gateway, gatewayChoices, messages := newGenericExecutionAndSelectionFixture(
		nil, &fakeGroupRepo{group: group}, nil, nil, cfg, snapshots, nil, nil, nil, upstream, nil, nil, sessions, sessions,
		nil, nil, nil, nil, nil, nil, responseHeaderFilterForTest(cfg),
	)
	gateway.Recorder = newHTTPCompletionFixture(cfg, nil, completionInput1, billingCache, nil,
		nil, nil, false)

	h := newMessageEndpointsFixture(gateway, messages, newFundingAdmissionFixture(billingCache, cfg), gatewayhttp.NewConcurrencyHelper(scheduler.NewConcurrencyService(&fakeConcurrencyCache{}, scheduler.Diagnostics{
		Logf:  logging.LegacyPrintf,
		Event: logging.Event,
	},
	), gatewayhttp.SSEPingFormatClaude, 0), gatewayhttp.MessagesHTTPOptions{MaxBodyBytes: openAITextOptions(cfg).MaxBodyBytes, MaxSwitches: 1, MaxGeminiSwitches: 0}, newExecutionAvailabilityForTest(nil,
		nil, cfg), gatewayChoices,
	)
	key := &apikey.APIKey{
		ID: 21, UserID: 22, GroupID: &groupID, Status: billing.StatusActive, Group: group,
		User: &identity.User{ID: 22, Concurrency: 10, Balance: 100},
	}
	return h, key, sessions
}

func serveGatewaySessionMessage(h *messageEndpointsFixture, key *apikey.APIKey, stream bool) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := `{"model":"claude-sonnet-4-5","max_tokens":256,"messages":[{"role":"user","content":"check session lifecycle"}]`
	if stream {
		body += `,"stream":true`
	}
	body += `}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request = c.Request.WithContext(requeststate.WithGroup(c.Request.Context(), key.Group))
	c.Set(string(keyhttp.ContextKeyAPIKey), key)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: key.UserID, Concurrency: 10})
	h.Messages(c)
	return recorder
}

func TestGatewayHandlerMessages_SessionSlotLifecycle(t *testing.T) {
	for _, providerType := range []string{capability.ProviderTypeOAuth, capability.ProviderTypeSetupToken} {
		for _, tc := range []struct {
			name         string
			stream       bool
			upstreamCode int
			transportErr bool
			failover     bool
		}{
			{name: "non_stream_success", upstreamCode: http.StatusOK},
			{name: "stream_success", stream: true, upstreamCode: http.StatusOK},
			{name: "request_rejected", upstreamCode: http.StatusBadRequest},
			{name: "transport_failure", transportErr: true},
			{name: "failover_success", upstreamCode: http.StatusInternalServerError, failover: true},
		} {
			t.Run(providerType+"/"+tc.name, func(t *testing.T) {
				upstream := &gatewaySessionUpstreamStub{respond: func(providerID int64) (*http.Response, error) {
					if tc.transportErr {
						return nil, errors.New("test upstream connection failed")
					}
					status := tc.upstreamCode
					if tc.failover && providerID == 13 {
						status = http.StatusOK
					}
					return gatewaySessionResponse(status, tc.stream), nil
				}}
				h, key, sessions := newGatewaySessionLimitFixture(t, providerType, tc.failover, upstream)
				response := serveGatewaySessionMessage(h, key, tc.stream)
				require.NotEmpty(t, sessions.registered[12])
				if tc.failover {
					require.Equal(t, http.StatusOK, response.Code, response.Body.String())
					require.Equal(t, []int64{12, 13}, upstream.called)
					require.Equal(t, sessions.registered[12], sessions.unregistered[12], "失败提供商的槽必须释放")
					require.NotEmpty(t, sessions.registered[13])
					require.Empty(t, sessions.unregistered[13], "成功提供商的槽必须保留")
					return
				}
				require.Equal(t, []int64{12}, upstream.called)
				if tc.upstreamCode == http.StatusOK {
					require.Equal(t, http.StatusOK, response.Code, response.Body.String())
					require.Contains(t, response.Body.String(), "ok")
					require.Empty(t, sessions.unregistered, "成功会话必须保留到空闲超时")
				} else {
					require.GreaterOrEqual(t, response.Code, http.StatusBadRequest)
					require.Equal(t, sessions.registered[12], sessions.unregistered[12], "未成功服务的会话必须立即释放")
				}
			})
		}
	}
}

func TestOpenAIGatewayMessagesProtocolPolicyAllowsGrokGroups(t *testing.T) {
	t.Run("openai_group_without_messages_protocol_is_rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`))
		groupID := int64(4101)
		c.Set(string(keyhttp.ContextKeyAPIKey), &apikey.APIKey{
			ID:      5101,
			GroupID: &groupID,
			User:    &identity.User{ID: 6101},
			Group: &routing.Group{
				ID: groupID,
				AllowedProtocols: []protocol.ProtocolID{
					protocol.ProtocolOpenAIResponses,
					protocol.ProtocolOpenAIChatCompletions,
				},
			},
		})
		c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 6101, Concurrency: 1})

		h := newGatewayHTTPEndpoints(gatewayHTTPFixtureInput{})
		h.Messages(c)

		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, "permission_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
		require.Contains(t, rec.Body.String(), "This group does not allow Anthropic Messages requests")
	})

	t.Run("grok_group_with_messages_protocol_reaches_gateway_dependencies", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"grok-4.3","messages":[{"role":"user","content":"hi"}]}`))
		groupID := int64(4102)
		c.Set(string(keyhttp.ContextKeyAPIKey), &apikey.APIKey{
			ID:      5102,
			GroupID: &groupID,
			User:    &identity.User{ID: 6102},
			Group: &routing.Group{
				ID: groupID,
				AllowedProtocols: []protocol.ProtocolID{
					protocol.ProtocolAnthropicMessages,
					protocol.ProtocolOpenAIResponses,
					protocol.ProtocolOpenAIChatCompletions,
				},
			},
		})
		c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 6102, Concurrency: 1})

		h := newGatewayHTTPEndpoints(gatewayHTTPFixtureInput{})
		h.Messages(c)

		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		require.Equal(t, "api_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
		require.NotContains(t, rec.Body.String(), "This group does not allow Anthropic Messages requests")
	})
}
