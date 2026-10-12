package httpapi

// Connection-routing scenarios cover session headers, HTTP request construction, WS connection reuse, and pool shutdown.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	openaiws "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws"
)

type openAIWSCountingDialer struct {
	mu             sync.Mutex
	dialCount      int
	lastTLSProfile *tlsfingerprint.Profile
}

func TestSetOpenAICodexRoutingHintCanonicalizesOfficialServiceTiers(t *testing.T) {
	oauthProvider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	tests := []struct {
		name        string
		model       string
		serviceTier string
		want        string
	}{
		{name: "fast alias", model: "gpt-5.6", serviceTier: " fast ", want: "model=gpt-5.6;tier=priority"},
		{name: "priority", model: "gpt-5.6", serviceTier: "priority", want: "model=gpt-5.6;tier=priority"},
		{name: "flex", model: "gpt-5.6", serviceTier: "flex", want: "model=gpt-5.6;tier=flex"},
		{name: "ultrafast", model: "gpt-5.6", serviceTier: "ultrafast", want: "model=gpt-5.6;tier=ultrafast"},
		{name: "explicit default sentinel", model: "gpt-5.6", serviceTier: "default", want: "model=gpt-5.6"},
		{name: "omitted tier", model: "gpt-5.6", want: "model=gpt-5.6"},
		{name: "auto is not expanded without catalog support", model: "gpt-5.6", serviceTier: "auto", want: "model=gpt-5.6"},
		{name: "scale is not expanded without catalog support", model: "gpt-5.6", serviceTier: "scale", want: "model=gpt-5.6"},
		{name: "unknown tier does not expand protocol", model: "gpt-5.6", serviceTier: "turbo", want: "model=gpt-5.6"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			gatewayhttp.SetOpenAICodexRoutingHint(headers, oauthProvider, tt.model, tt.serviceTier)
			require.Equal(t, tt.want, headers.Get("x-codex-routing-hint"))
		})
	}

	t.Run("invalid header value is omitted", func(t *testing.T) {
		headers := make(http.Header)
		gatewayhttp.SetOpenAICodexRoutingHint(headers, oauthProvider, "gpt-5.6\ninvalid", "priority")
		require.Empty(t, headers.Get("x-codex-routing-hint"))
	})

	for _, model := range []string{"gpt-5.6;evil", "gpt=5.6"} {
		t.Run("delimiter in model is omitted: "+model, func(t *testing.T) {
			headers := make(http.Header)
			gatewayhttp.SetOpenAICodexRoutingHint(headers, oauthProvider, model, "priority")
			require.Empty(t, headers.Get("x-codex-routing-hint"))
		})
	}

	t.Run("api key strips gateway-owned hint in every key casing", func(t *testing.T) {
		headers := make(http.Header)
		headers["x-codex-routing-hint"] = []string{"lowercase-spoof"}
		headers["X-Codex-Routing-Hint"] = []string{"canonical-spoof"}
		gatewayhttp.SetOpenAICodexRoutingHint(headers, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}, "gpt-5.6", "priority")
		for key := range headers {
			require.False(t, strings.EqualFold(key, "x-codex-routing-hint"))
		}
	})

	t.Run("oauth replaces spoofed lowercase hint", func(t *testing.T) {
		headers := make(http.Header)
		headers["x-codex-routing-hint"] = []string{"model=spoof;tier=flex"}
		gatewayhttp.SetOpenAICodexRoutingHint(headers, oauthProvider, "gpt-5.6", "priority")
		require.Equal(t, "model=gpt-5.6;tier=priority", headers.Get("x-codex-routing-hint"))
		require.Len(t, headers, 1)
	})
}

func TestOpenAIOAuthHTTPBuildersSendRoutingHintFromFinalBody(t *testing.T) {
	oauthProvider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"chatgpt_account_id": "test-provider",
			},
		},
	}
	svc := newWSFixture(wsFixtureInputs{})

	tests := []struct {
		name string
		body []byte
		want string
	}{
		{name: "fast", body: []byte(`{"model":"gpt-5.6-codex","service_tier":"fast"}`), want: "model=gpt-5.6-codex;tier=priority"},
		{name: "flex", body: []byte(`{"model":"gpt-5.6-codex","service_tier":"flex"}`), want: "model=gpt-5.6-codex;tier=flex"},
		{name: "ultrafast", body: []byte(`{"model":"gpt-5.6-codex","service_tier":"ultrafast"}`), want: "model=gpt-5.6-codex;tier=ultrafast"},
		{name: "default", body: []byte(`{"model":"gpt-5.6-codex","service_tier":"default"}`), want: "model=gpt-5.6-codex"},
		{name: "omitted", body: []byte(`{"model":"gpt-5.6-codex"}`), want: "model=gpt-5.6-codex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, passthrough := range []bool{false, true} {
				mode := "ordinary"
				if passthrough {
					mode = "passthrough"
				}
				t.Run(mode, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(tt.body))

					var req *http.Request
					var err error
					if passthrough {
						req, err = svc.Requests.BuildPassthrough(context.Background(), c, oauthProvider, tt.body, "test-token")
					} else {
						req, err = svc.Requests.Build(context.Background(), c, oauthProvider, tt.body, "test-token", false, "", true)
					}
					require.NoError(t, err)
					require.Equal(t, tt.want, req.Header.Get("x-codex-routing-hint"))
				})
			}
		})
	}
}

func TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{options: &wsFixtureOptions{Request: gatewayhttp.OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{Enabled: false}}}})
	body := []byte(`{"model":"gpt-5.6-codex","service_tier":"priority"}`)

	build := func(t *testing.T, provider *gatewayprovider.ExecutionProvider, betaValues []string, rawLowercaseKey bool) http.Header {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		if rawLowercaseKey {
			c.Request.Header["openai-beta"] = append([]string(nil), betaValues...)
		} else {
			for _, value := range betaValues {
				c.Request.Header.Add("OpenAI-Beta", value)
			}
		}

		req, err := svc.Requests.BuildPassthrough(context.Background(), c, provider, body, "test-token")
		require.NoError(t, err)
		return req.Header
	}

	oauth := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"chatgpt_account_id": "test-provider",
			},
		},
	}
	apiKey := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "test-api-key",
			},
		},
	}

	t.Run("oauth legacy only is removed including raw lowercase key", func(t *testing.T) {
		headers := build(t, oauth, []string{"responses=experimental"}, true)
		require.Empty(t, headers.Values("OpenAI-Beta"))
	})

	t.Run("oauth mixed beta preserves independent tokens", func(t *testing.T) {
		headers := build(t, oauth, []string{
			"responses=experimental, future_feature=v1",
			"another_feature=v2, RESPONSES=EXPERIMENTAL",
		}, false)
		require.Equal(t, []string{"future_feature=v1", "another_feature=v2"}, headers.Values("OpenAI-Beta"))
	})

	t.Run("api key explicit beta remains caller controlled", func(t *testing.T) {
		headers := build(t, apiKey, []string{"responses=experimental, future_feature=v1"}, false)
		require.Equal(t, []string{"responses=experimental, future_feature=v1"}, headers.Values("OpenAI-Beta"))
	})
}

func TestOpenAIWSConnPoolPreferredContinuationIgnoresRoutingHintChanges(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Pool.MaxConnsPerProvider = 2
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 2

	pool := newOpenAIWSConnPool(options)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 913, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}

	acquire := func(t *testing.T, hint, preferred string, forcePreferred bool) *openaiws.WSConnLease {
		t.Helper()
		headers := make(http.Header)
		if hint != "" {
			headers.Set("x-codex-routing-hint", hint)
		}
		lease, err := pool.Acquire(context.Background(), openaiws.WSAcquireRequest{
			Provider:           openAIWSPoolProviderView(provider),
			WSURL:              "wss://example.com/v1/responses",
			Headers:            headers,
			PreferredConnID:    preferred,
			ForcePreferredConn: forcePreferred,
		})
		require.NoError(t, err)
		require.NotNil(t, lease)
		return lease
	}

	standard := acquire(t, "model=gpt-5.6-codex", "", false)
	connID := standard.ConnID()
	standard.Release()

	priority := acquire(t, "model=gpt-5.6-codex;tier=priority", connID, true)
	require.True(t, priority.Reused())
	require.Equal(t, connID, priority.ConnID())
	priority.Release()

	standardAgain := acquire(t, "model=gpt-5.6-codex", connID, true)
	require.True(t, standardAgain.Reused())
	require.Equal(t, connID, standardAgain.ConnID())
	standardAgain.Release()

	require.Equal(t, 1, dialer.DialCount(), "routing hint is dial-time advisory, not continuation compatibility")
}

func TestOpenAIWSConnPoolUsesRoutingHintAsSoftDialAffinity(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Pool.MaxConnsPerProvider = 4
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 4

	pool := newOpenAIWSConnPool(options)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 913, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}

	acquire := func(t *testing.T, hint string) *openaiws.WSConnLease {
		headers := make(http.Header)
		headers.Set("x-codex-routing-hint", hint)
		lease, err := pool.Acquire(context.Background(), openaiws.WSAcquireRequest{
			Provider: openAIWSPoolProviderView(provider),
			WSURL:    "wss://example.com/v1/responses",
			Headers:  headers,
		})
		require.NoError(t, err)
		require.NotNil(t, lease)
		return lease
	}

	priority := acquire(t, "model=gpt-5.6-codex;tier=priority")
	priorityConnID := priority.ConnID()
	priority.Release()

	priorityAgain := acquire(t, "model=gpt-5.6-codex;tier=priority")
	require.True(t, priorityAgain.Reused())
	require.Equal(t, priorityConnID, priorityAgain.ConnID())
	priorityAgain.Release()

	flex := acquire(t, "model=gpt-5.6-codex;tier=flex")
	require.False(t, flex.Reused())
	require.NotEqual(t, priorityConnID, flex.ConnID())
	flex.Release()

	otherModel := acquire(t, "model=gpt-5.5-codex;tier=priority")
	require.False(t, otherModel.Reused())
	require.NotEqual(t, priorityConnID, otherModel.ConnID())
	otherModel.Release()

	defaultTier := acquire(t, "model=gpt-5.6-codex")
	require.False(t, defaultTier.Reused())
	require.NotEqual(t, priorityConnID, defaultTier.ConnID())
	defaultConnID := defaultTier.ConnID()
	defaultTier.Release()

	defaultAgain := acquire(t, "model=gpt-5.6-codex")
	require.True(t, defaultAgain.Reused())
	require.Equal(t, defaultConnID, defaultAgain.ConnID())
	defaultAgain.Release()

	require.Equal(t, 4, dialer.DialCount())
}

func (d *openAIWSCountingDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
) (openai.WSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	d.mu.Lock()
	d.dialCount++
	d.lastTLSProfile = profile
	d.mu.Unlock()
	return &openAIWSFakeConn{}, 0, nil, nil
}

func (d *openAIWSCountingDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

func (d *openAIWSCountingDialer) LastTLSProfile() *tlsfingerprint.Profile {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastTLSProfile
}

// TestOpenAIWSConnPoolShutdownSealsLazyCreation verifies that shutdown prevents lazy pool creation and acquisition.
func TestOpenAIWSConnPoolShutdownSealsLazyCreation(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	svc.Connections.Close()
	require.Nil(t, svc.Connections.Pool())
	pool := newOpenAIWSConnPool(nil)
	pool.Close()
	_, err := pool.Acquire(context.Background(), openaiws.WSAcquireRequest{Provider: openAIWSPoolProviderView(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}), WSURL: "wss://example.test"})
	require.ErrorIs(t, err, openai.ErrWSConnClosed)
	pool.Close()
}
