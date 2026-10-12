package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

func TestBuildGrokResponsesRequestPinsOAuthBaseURLAndUsesBearerToken(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"base_url": "https://xai.test/v1/",
			},
		},
	}

	req, err := (&GrokExecutor{Routes: gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}}).BuildResponsesRequest(context.Background(), nil, provider, []byte(`{"model":"grok-4.3"}`), "access-token", "isolated-cache-id", false)
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, req.Method)
	require.Equal(t, "https://xai.test/v1/responses", req.URL.String())
	require.Equal(t, "Bearer access-token", req.Header.Get("Authorization"))
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.Contains(t, req.Header.Get("Accept"), "text/event-stream")
	require.Equal(t, xai.CLIClientVersion, req.Header.Get("X-Grok-Client-Version"))
	require.Equal(t, "isolated-cache-id", req.Header.Get(GrokConversationIDHeader))

	data, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, `{"model":"grok-4.3"}`, strings.TrimSpace(string(data)))
}

func TestBuildGrokResponsesRequestAllowsPublicAPIKeyBaseURLByDefault(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"base_url": "https://grok.example.test/v1/",
			},
		},
	}

	req, err := (&GrokExecutor{Routes: gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}}).BuildResponsesRequest(context.Background(), nil, provider, []byte(`{"model":"grok-4.3"}`), "api-key", "", false)
	require.NoError(t, err)
	require.Equal(t, "https://grok.example.test/v1/responses", req.URL.String())
	require.Equal(t, "Bearer api-key", req.Header.Get("Authorization"))
	require.Empty(t, req.Header.Get("X-Grok-Client-Version"))
	require.NotEqual(t, xai.DefaultGrokUpstreamUserAgent(), req.Header.Get("User-Agent"))
}

func TestBuildGrokResponsesRequestHonorsOAuthOfficialEndpointSwitch(t *testing.T) {
	t.Parallel()

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"base_url": xai.DefaultBaseURL,
			},
		},
	}

	req, err := (&GrokExecutor{Routes: gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}}).BuildResponsesRequest(context.Background(), nil, provider, []byte(`{"model":"grok-4.3"}`), "access-token", "", false)
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/responses", req.URL.String())
}

func TestBuildGrokResponsesRequestAppliesHeaderOverridesLast(t *testing.T) {
	t.Parallel()

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"base_url":                "https://relay.example.test/v1",
				"header_override_enabled": true,
				"header_overrides": map[string]any{
					"User-Agent":            "relay-client/2.0",
					"X-Grok-Client-Version": "9.9.9",
					"X-Relay-Token":         "relay-secret",
				},
			},
		},
	}

	req, err := (&GrokExecutor{Routes: gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}}).BuildResponsesRequest(context.Background(), nil, provider, []byte(`{"model":"grok-4.3"}`), "access-token", "conv-1", false)
	require.NoError(t, err)
	require.Equal(t, "https://relay.example.test/v1/responses", req.URL.String())
	// Overrides replace default CLI identity headers. Headers outside the wire-casing map use lowercase keys.
	require.Equal(t, "relay-client/2.0", req.Header.Get("User-Agent"))
	require.Equal(t, []string{"9.9.9"}, map[string][]string(req.Header)["x-grok-client-version"])
	require.Empty(t, req.Header.Get("X-Grok-Client-Version"))
	require.Equal(t, []string{"relay-secret"}, map[string][]string(req.Header)["x-relay-token"])
	// Session-routing and authorization headers are protected from overrides.
	require.Equal(t, "conv-1", req.Header.Get(GrokConversationIDHeader))
	require.Equal(t, "Bearer access-token", req.Header.Get("Authorization"))
}

func TestBuildGrokResponsesRequestIgnoresBlockedHeaderOverrides(t *testing.T) {
	t.Parallel()

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"header_override_enabled": true,
				"header_overrides": map[string]any{
					"Authorization":  "Bearer stolen",
					"x-grok-conv-id": "pinned-conversation",
				},
			},
		},
	}

	req, err := (&GrokExecutor{Routes: gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}}).BuildResponsesRequest(context.Background(), nil, provider, []byte(`{"model":"grok-4.3"}`), "api-key", "conv-2", false)
	require.NoError(t, err)
	require.Equal(t, "Bearer api-key", req.Header.Get("Authorization"))
	require.Equal(t, "conv-2", req.Header.Get(GrokConversationIDHeader))
}

func TestApplyDefaultGrokUpstreamHeadersUsesCLIUserAgent(t *testing.T) {
	t.Setenv(xai.CLIVersionEnv, "")

	req, err := http.NewRequest(http.MethodGet, "https://api.x.ai/v1/responses", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "claude-code/1.2.3")
	req.Header.Set("x-grok-client-version", "none")

	xai.ApplyDefaultGrokUpstreamHeaders(req)

	require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), req.Header.Get("User-Agent"))
	require.Equal(t, xai.CLIClientVersion, req.Header.Get("x-grok-client-version"))
	require.Equal(t, xai.CLIClientIdentifier, req.Header.Get("x-grok-client-identifier"))
}

func TestApplyDefaultGrokUpstreamHeadersHonorsCLIVersionOverride(t *testing.T) {
	t.Setenv(xai.CLIVersionEnv, "0.2.95")

	req, err := http.NewRequest(http.MethodGet, "https://api.x.ai/v1/responses", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "codex_cli_rs/0.144.0")

	xai.ApplyDefaultGrokUpstreamHeaders(req)

	require.Equal(t, "0.2.95", req.Header.Get("x-grok-client-version"))
	require.Equal(t, xai.CLIUserAgent("0.2.95"), req.Header.Get("User-Agent"))
	require.Equal(t, "grok-shell", req.Header.Get("x-grok-client-identifier"))
}

func TestResolveGrokUpstreamUserAgentNeverPassthrough(t *testing.T) {
	t.Setenv(xai.CLIVersionEnv, "")

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.0.0 (Mac OS; arm64)")

	executor := &GrokExecutor{Routes: gatewayprovider.GrokRoutes{Validate: xai.ValidateBaseURL}}
	target := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	request, err := executor.BuildResponsesRequest(context.Background(), c, target, []byte(`{}`), "token", "", false)
	require.NoError(t, err)
	require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), request.Header.Get("User-Agent"))
	request, err = executor.BuildResponsesRequest(context.Background(), nil, target, []byte(`{}`), "token", "", false)
	require.NoError(t, err)
	require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), request.Header.Get("User-Agent"))
}

func TestApplyGrokRuntimeHeadersKeepsCLIUserAgent(t *testing.T) {
	t.Setenv(xai.CLIVersionEnv, "")

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "claude-code/9.9.9")

	xai.ApplyGrokRuntimeHeaders(req, "codex_cli_rs")

	require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), req.Header.Get("User-Agent"))
	require.Equal(t, "codex_cli_rs", req.Header.Get("Originator"))
	require.Equal(t, xai.CLIClientVersion, req.Header.Get("x-grok-client-version"))
}

func TestApplyGrokTLSProfileHeadersAlwaysUsesCLIUserAgent(t *testing.T) {
	t.Setenv(xai.CLIVersionEnv, "")

	req, err := http.NewRequest(http.MethodPost, "https://api.x.ai/v1/responses", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "grok-native/1.0")

	// The profile supplies TLS settings; HTTP headers supply Originator and User-Agent.
	xai.ApplyDefaultGrokUpstreamHeaders(req)

	require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), req.Header.Get("User-Agent"))
	require.Equal(t, xai.CLIClientVersion, req.Header.Get("x-grok-client-version"))
}
