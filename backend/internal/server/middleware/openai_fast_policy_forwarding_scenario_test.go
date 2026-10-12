package middleware

// These tests cover user Fast policy forwarding in gateway/httpapi/authorization.go,
// gateway/httpapi/openai_responses_execution.go, and gateway/provider/execution_fast_policy.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/apikey/testkit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type openAIFastPolicyForwardingAPIKeyRepo struct {
	apikey.APIKeyRepository
	apiKeys map[string]*apikey.APIKey
}

type openAIFastPolicyForwardingSettingRepo struct {
	settingscore.Repository
	value string
}

type openAIFastPolicyForwardingHTTPUpstream struct {
	client *http.Client
}

func TestAPIKeyAuthForwardsUserScopedOpenAIFastPolicyToUpstream(t *testing.T) {
	upstreamBodies := make(chan []byte, 2)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request body", http.StatusInternalServerError)
			return
		}
		upstreamBodies <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response","model":"gpt-5","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer upstreamServer.Close()

	settings := &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{
			{
				ServiceTier: tierpolicy.OpenAIFastTierPriority,
				Action:      anthropic.BetaPolicyActionFilter,
				Scope:       anthropic.BetaPolicyScopeAll,
			},
			{
				ServiceTier: tierpolicy.OpenAIFastTierPriority,
				Action:      anthropic.BetaPolicyActionPass,
				Scope:       anthropic.BetaPolicyScopeAll,
				UserIDs:     []int64{42},
			},
		},
	}
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true

	settingService := gatewaytestkit.RuntimeReaders(&openAIFastPolicyForwardingSettingRepo{
		value: string(settingsJSON),
	})

	// Selection and forwarding share response, block, and transport state; this scenario uses HTTP upstream.
	responses := session.NewOpenAIWSStateStore(nil, gatewayprovider.LogOpenAIWSModeInfo)
	transient := providercore.NewModelTransientState(0)
	circuit := egress.NewProxyStreamCircuit(egress.DefaultProxyStreamCircuitSettings())
	blocks := providercore.NewRuntimeBlockState(time.Now)
	choices := selection.NewCompatible(selection.CompatibleDependencies{Responses: responses, ModelTransient: transient, ProxyCircuit: circuit, RuntimeBlocks: blocks}, selection.Options{})
	output := &gatewayhttp.OpenAIResponseOutput{Options: gatewayhttp.OpenAIResponseOptions{Configured: true, ReadLimit: config.DefaultUpstreamResponseReadMaxBytes}, Health: &provideradapter.OpenAIResponseHealth{Runtime: blocks, ModelTransient: transient}, Corrector: openai.NewCodexToolCorrector(), ProxyCircuit: circuit, Responses: responses, ResponseTTL: choices.OpenAIHTTPResponseStickyTTL, Headers: responseHeaderFilterForTest(cfg)}
	transport := &openAIFastPolicyForwardingHTTPUpstream{client: upstreamServer.Client()}
	requests := &gatewayhttp.OpenAIRequests{Options: gatewayhttp.OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{AllowInsecureHTTP: true}}, Transport: transport, Readers: settingService, Credentials: &providercore.OpenAIExecutionCredentials{}, Identity: gatewayprovider.NewExecutionAgentIdentity(&providercore.OpenAITaskCoordinator{}, nil, nil, nil), ClientPolicy: &provideradapter.OpenAIProbePolicy{Available: true, DefaultBrowserUserAgent: gateway.DefaultOpenAICodexUserAgent}}
	text := &gatewayhttp.OpenAITextExecutor{Requests: requests, Output: output, FastPolicy: &gatewayprovider.ExecutionFastPolicy{Readers: settingService}, CodexUsage: &provideradapter.CodexUsageObserver{}, ResponseTTL: choices.OpenAIHTTPResponseStickyTTL, Compact: &gatewayhttp.CompactExecutor{}}
	executor := &gatewayhttp.OpenAIResponsesExecutor{Requests: requests, Output: output, Text: text, Lineage: &gatewayhttp.OpenAIEncryptedLineage{Store: responses, TTL: choices.SessionStickyTTL}, ImageBridge: &gatewayprovider.ResponseImagePolicy{}}

	groupID := int64(101)
	group := &routing.Group{
		ID:     groupID,
		Name:   "openai",
		Status: billing.StatusActive,

		Hydrated: true,
	}
	apiKeys := map[string]*apikey.APIKey{
		"key-user-42": newOpenAIFastPolicyForwardingAPIKey(1, "key-user-42", 42, groupID, group),
		"key-user-43": newOpenAIFastPolicyForwardingAPIKey(2, "key-user-43", 43, groupID, group),
	}
	apiKeyService := testkit.NewService(&openAIFastPolicyForwardingAPIKeyRepo{apiKeys: apiKeys}, nil, nil, nil, nil, nil, cfg)
	apiKeyService.Start()
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 900,
			Name:        "openai-upstream",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": upstreamServer.URL,
			},
			Extra: map[string]any{"use_responses_api": true},
		},
	}

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, cfg)))
	router.POST("/v1/responses", func(c *gin.Context) {
		body, readErr := io.ReadAll(c.Request.Body)
		if readErr != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		gatewayhttp.SetOpenAIClientTransport(c, gatewayhttp.OpenAIClientTransportHTTP)
		if _, forwardErr := executor.Forward(c.Request.Context(), c, provider, body); forwardErr != nil {
			c.Status(http.StatusBadGateway)
			return
		}
		c.Status(http.StatusOK)
	})

	send := func(apiKey string) {
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/responses",
			bytes.NewBufferString(`{"model":"gpt-5","stream":false,"service_tier":"priority","input":"hi"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("x-api-key", apiKey)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
	}

	send("key-user-42")
	send("key-user-43")

	allowedUserBody := <-upstreamBodies
	otherUserBody := <-upstreamBodies
	require.Equal(t, tierpolicy.OpenAIFastTierPriority, gjson.GetBytes(allowedUserBody, "service_tier").String())
	require.False(t, gjson.GetBytes(otherUserBody, "service_tier").Exists())
}

func newOpenAIFastPolicyForwardingAPIKey(id int64, key string, userID, groupID int64, group *routing.Group) *apikey.APIKey {
	return &apikey.APIKey{
		ID:      id,
		UserID:  userID,
		Key:     key,
		Status:  billing.StatusActive,
		GroupID: &groupID,
		User: &identity.User{
			ID:          userID,
			Role:        identity.RoleUser,
			Status:      billing.StatusActive,
			Balance:     10,
			Concurrency: 1,
		},
		Group: group,
	}
}

func (r *openAIFastPolicyForwardingAPIKeyRepo) GetByKeyForAuth(_ context.Context, key string) (*apikey.APIKey, error) {
	apiKey, ok := r.apiKeys[key]
	if !ok {
		return nil, apikey.ErrAPIKeyNotFound
	}
	clone := *apiKey
	return &clone, nil
}

func (r *openAIFastPolicyForwardingAPIKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error {
	return nil
}

func (r *openAIFastPolicyForwardingSettingRepo) GetValue(context.Context, string) (string, error) {
	return r.value, nil
}

func (u *openAIFastPolicyForwardingHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	// #nosec G704 -- The target is a local httptest.Server.
	return u.client.Do(req)
}

func (u *openAIFastPolicyForwardingHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, providerID, providerConcurrency)
}

func responseHeaderFilterForTest(cfg *config.Config) *egress.CompiledHeaderFilter {
	if cfg == nil {
		return nil
	}
	return egress.CompileHeaderFilter(egress.ResponseHeaderOptions{Enabled: cfg.Security.ResponseHeaders.Enabled, AdditionalAllowed: cfg.Security.ResponseHeaders.AdditionalAllowed, ForceRemove: cfg.Security.ResponseHeaders.ForceRemove})
}
