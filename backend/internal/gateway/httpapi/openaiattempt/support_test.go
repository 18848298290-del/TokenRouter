package openaiattempt

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestResolveOpenAIUpstreamEndpointPrefersForwardResult(t *testing.T) {
	tests := []struct {
		name            string
		provider        *gatewayprovider.ExecutionProvider
		result          *forwardcore.OpenAIResult
		inboundEndpoint string
		runtimeEndpoint string
		want            string
	}{
		{
			name:            "grok raw chat result overrides stale context",
			provider:        &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}},
			result:          &forwardcore.OpenAIResult{UpstreamEndpoint: gatewayhttp.EndpointChatCompletions},
			runtimeEndpoint: gatewayhttp.EndpointResponses,
			want:            gatewayhttp.EndpointChatCompletions,
		},
		{
			name:     "grok chat bridged to responses",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}},
			result:   &forwardcore.OpenAIResult{UpstreamEndpoint: gatewayhttp.EndpointResponses},
			want:     gatewayhttp.EndpointResponses,
		},
		{
			name:     "grok empty result keeps responses default",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}},
			result:   &forwardcore.OpenAIResult{},
			want:     gatewayhttp.EndpointResponses,
		},
		{
			name:            "grok raw error uses runtime endpoint",
			provider:        &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}},
			runtimeEndpoint: gatewayhttp.EndpointChatCompletions,
			want:            gatewayhttp.EndpointChatCompletions,
		},
		{
			name:     "openai behavior remains responses",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}},
			result:   &forwardcore.OpenAIResult{},
			want:     gatewayhttp.EndpointResponses,
		},
		{
			name:            "openai api key chat attempt records runtime endpoint",
			provider:        &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}},
			result:          &forwardcore.OpenAIResult{},
			runtimeEndpoint: gatewayhttp.EndpointChatCompletions,
			want:            gatewayhttp.EndpointChatCompletions,
		},
		{
			name: "openai api key responses attempt records runtime endpoint",
			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
					Type:  capability.ProviderTypeAPIKey,
					Extra: map[string]any{"openai_text_route_mode": "force_responses"},
				},
			},
			result:          &forwardcore.OpenAIResult{},
			runtimeEndpoint: gatewayhttp.EndpointResponses,
			want:            gatewayhttp.EndpointResponses,
		},
		{
			name:            "responses fallback records runtime chat endpoint",
			provider:        &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}},
			result:          &forwardcore.OpenAIResult{},
			inboundEndpoint: gatewayhttp.EndpointResponses,
			runtimeEndpoint: gatewayhttp.EndpointChatCompletions,
			want:            gatewayhttp.EndpointChatCompletions,
		},
		{
			name:            "messages native path records runtime responses endpoint",
			provider:        &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}},
			result:          &forwardcore.OpenAIResult{},
			inboundEndpoint: gatewayhttp.EndpointMessages,
			runtimeEndpoint: gatewayhttp.EndpointResponses,
			want:            gatewayhttp.EndpointResponses,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			inboundEndpoint := tt.inboundEndpoint
			if inboundEndpoint == "" {
				inboundEndpoint = gatewayhttp.EndpointChatCompletions
			}
			c.Request = httptest.NewRequest(http.MethodPost, inboundEndpoint, nil)
			c.Set("_gateway_inbound_endpoint", inboundEndpoint)
			gatewayhttp.SetActualOpenAIUpstreamEndpoint(c, tt.runtimeEndpoint)
			require.Equal(t, tt.want, ResolveOpenAIUpstreamEndpoint(c, tt.provider, tt.result))
		})
	}
}

func TestOpenAIForwardMayFailoverOnlyAfterNonSemanticWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	before := gatewayhttp.OpenAICompactKeepaliveAdjustedWrittenSize(c)

	_, err := fmt.Fprint(c.Writer, ":\n\n")
	require.NoError(t, err)
	c.Writer.Flush()

	require.True(t, OpenAIForwardMayFailover(c, before, &forwardcore.UpstreamFailoverError{
		SafeToFailoverAfterWrite: true,
	}))
	require.False(t, OpenAIForwardMayFailover(c, before, &forwardcore.UpstreamFailoverError{}))
}

func TestOpenAIRequestAllowsFailoverReplayStopsCanceledClient(t *testing.T) {
	require.False(t, OpenAIRequestAllowsFailoverReplay(nil))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	requestCtx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)

	require.True(t, OpenAIRequestAllowsFailoverReplay(c))
	cancel()
	require.False(t, OpenAIRequestAllowsFailoverReplay(c))
}

// TestAppendOpenAIProviderProxyLogFields checks complete proxy diagnostics without credential exposure.
func TestAppendOpenAIProviderProxyLogFields(t *testing.T) {
	proxyID := int64(17)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ProxyID: &proxyID,
			Proxy: &egress.Proxy{
				ID:       proxyID,
				Name:     "openai-egress",
				Host:     "proxy.example.com",
				Port:     8443,
				Username: "proxy-user-secret",
				Password: "proxy-password-secret",
			},
		},
	}
	core, logs := observer.New(zap.WarnLevel)
	log := zap.New(core)

	log.Warn("openai.websocket_proxy_failed", AppendOpenAIProviderProxyLogFields(nil, provider)...)

	entries := logs.All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.EqualValues(t, proxyID, fields["proxy_id"])
	require.Equal(t, "openai-egress", fields["proxy_name"])
	require.Equal(t, "proxy.example.com", fields["proxy_host"])
	require.EqualValues(t, 8443, fields["proxy_port"])
	require.NotContains(t, fields, "proxy_username")
	require.NotContains(t, fields, "proxy_password")
	require.NotContains(t, fields, "proxy_url")
}

// TestAppendOpenAIProviderProxyLogFields_FallsBackToProxyID checks lookup by ID when the proxy is not
// preloaded.
func TestAppendOpenAIProviderProxyLogFields_FallsBackToProxyID(t *testing.T) {
	proxyID := int64(23)
	core, logs := observer.New(zap.WarnLevel)
	log := zap.New(core)

	log.Warn("openai.websocket_proxy_failed", AppendOpenAIProviderProxyLogFields(nil, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ProxyID: &proxyID}})...)

	entries := logs.All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.EqualValues(t, proxyID, fields["proxy_id"])
	require.Len(t, fields, 1)
}
