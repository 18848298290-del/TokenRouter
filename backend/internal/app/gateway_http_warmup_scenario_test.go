package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Antigravity warmup interception returns a local Messages response after
// selecting a provider. These scenarios assemble production handlers with
// cached candidates and check both ordinary selection and forced-platform
// contexts, including the selected provider and generated message identity.

// newTestGatewayHandler builds Messages endpoints using a supplied group and
// cached provider candidates. It starts billing cache workers and returns their
// cleanup function, which callers invoke after handler execution while fixture
// dependencies remain usable.
func newTestGatewayHandler(t *testing.T, group *routing.Group, providers []*gatewayprovider.ExecutionProvider) (*messageEndpointsFixture, func()) {
	t.Helper()

	schedulerCache := &fakeSchedulerCache{providers: providers}
	schedulerSnapshot := scheduler.NewSnapshotService(schedulerCache, nil, nil, nil, nil, scheduler.SnapshotBindings{})

	gwSvc, gwSvcChoices, messages := newGenericExecutionAndSelectionFixture(
		nil, // providerRepo: the scheduler snapshot supplies candidates.
		&fakeGroupRepo{group: group},
		nil, // usageLogRepo
		nil, // cache: sticky sessions are disabled.
		nil, // cfg
		schedulerSnapshot,
		nil, // concurrencyService: disable load-aware selection and let slot acquisition succeed.
		nil, // healthObserver
		nil, // identityService
		nil, // httpUpstream
		nil, // deferredService
		nil, // messageCredentials
		nil, // sessionLimitCache
		nil, // windowCostCache
		nil, // rpmCache
		nil, // digestStore
		nil, // settingService
		nil, // tlsFPProfileService
		nil, // pricingConfigService
		nil, // resolver
		responseHeaderFilterForTest(nil),
	)
	// Bind a completion recorder directly for the warmup scenario.
	gwSvc.Recorder = newHTTPCompletionFixture(nil, nil, nil, nil, nil, nil, nil, false)

	cfg := &config.Config{}
	billingCacheSvc := newBillingEligibilityFixture(cfg)
	billingCacheSvc.Start()

	concurrencySvc := scheduler.NewConcurrencyService(&fakeConcurrencyCache{}, scheduler.Diagnostics{
		Logf:  logging.LegacyPrintf,
		Event: logging.Event,
	},
	)
	concurrencyHelper := gatewayhttp.NewConcurrencyHelper(concurrencySvc, gatewayhttp.SSEPingFormatClaude, 0)

	h := newMessageEndpointsFixture(gwSvc, messages, newFundingAdmissionFixture(billingCacheSvc, cfg), concurrencyHelper, gatewayhttp.MessagesHTTPOptions{MaxBodyBytes: openAITextOptions(nil).MaxBodyBytes, MaxSwitches: 1, MaxGeminiSwitches: 1}, newExecutionAvailabilityForTest(nil,

		nil, nil), gwSvcChoices,
	)

	cleanup := func() {
		// The helper starts billing cache workers, so the returned cleanup drains
		// them after each handler scenario before its fixture dependencies go out of use.
		billingCacheSvc.Stop()
	}
	return h, cleanup
}

// TestGatewayHandlerMessages_InterceptWarmup_AntigravityProvider_MixedSchedulingV1
// sends a warmup Messages body through cached Antigravity selection. It
// checks the selected provider, successful local response, generated message
// ID, client model, and New Conversation content.
func TestGatewayHandlerMessages_InterceptWarmup_AntigravityProvider_MixedSchedulingV1(t *testing.T) {
	groupID := int64(2001)
	providerID := int64(1001)

	group := &routing.Group{
		ID:       groupID,
		Hydrated: true,
		Status:   billing.StatusActive,
	}

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: providerID,
			Name:     "ag-1",
			Platform: capability.PlatformAntigravity,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token":              "tok_xxx",
				"intercept_warmup_requests": true,
			},
			Extra: map[string]any{
				"mixed_scheduling": true, // Allow selection by an Anthropic group through mixed scheduling.
			},
			Concurrency:    1,
			Priority:       1,
			Status:         billing.StatusActive,
			Schedulable:    true,
			ProviderGroups: []providercore.GroupMembership{{ProviderID: providerID, GroupID: groupID}},
		},
	}

	h, cleanup := newTestGatewayHandler(t, group, []*gatewayprovider.ExecutionProvider{provider})
	defer cleanup()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	body := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 256,
		"messages": [{"role":"user","content":[{"type":"text","text":"Warmup"}]}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(requeststate.WithGroup(req.Context(), group))
	c.Request = req

	apiKey := &apikey.APIKey{
		ID:      3001,
		UserID:  4001,
		GroupID: &groupID,
		Status:  billing.StatusActive,
		User: &identity.User{
			ID:          4001,
			Concurrency: 10,
			Balance:     100,
		},
		Group: group,
	}

	c.Set(string(keyhttp.ContextKeyAPIKey), apiKey)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

	h.Messages(c)

	require.Equal(t, 200, rec.Code)

	// Selection metadata identifies the provider whose warmup option produced
	// this local response; inspecting the body alone would not establish its source.
	selected, ok := c.Get(gatewayhttp.OpsProviderIDKey)
	require.True(t, ok)
	require.Equal(t, providerID, selected)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Regexp(t, `^msg_01[0-9A-Za-z]{22}$`, resp["id"])
	require.Equal(t, "claude-sonnet-4-5", resp["model"])

	content, ok := resp["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	first, ok := content[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "New Conversation", first["text"])
}

// TestGatewayHandlerMessages_InterceptWarmup_AntigravityProvider_ForcePlatform
// binds Antigravity forcing in request and Gin contexts before a warmup call.
// It checks successful interception, the selected provider ID, message-ID
// shape, and response model.
func TestGatewayHandlerMessages_InterceptWarmup_AntigravityProvider_ForcePlatform(t *testing.T) {
	groupID := int64(2002)
	providerID := int64(1002)

	group := &routing.Group{
		ID:       groupID,
		Hydrated: true,
		Status:   billing.StatusActive,
	}

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: providerID,
			Name:     "ag-2",
			Platform: capability.PlatformAntigravity,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token":              "tok_xxx",
				"intercept_warmup_requests": true,
			},
			Concurrency:    1,
			Priority:       1,
			Status:         billing.StatusActive,
			Schedulable:    true,
			ProviderGroups: []providercore.GroupMembership{{ProviderID: providerID, GroupID: groupID}},
		},
	}

	h, cleanup := newTestGatewayHandler(t, group, []*gatewayprovider.ExecutionProvider{provider})
	defer cleanup()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	body := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 256,
		"messages": [{"role":"user","content":[{"type":"text","text":"Warmup"}]}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/antigravity/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Bind both middleware contexts because execution reads the request context
	// while HTTP preparation reads Gin values before planning the forced-platform attempt.
	ctx := requeststate.WithGroup(req.Context(), group)
	ctx = apikey.WithForcePlatform(ctx, capability.PlatformAntigravity)
	req = req.WithContext(ctx)
	c.Request = req
	c.Set(string(keyhttp.ContextKeyForcePlatform), capability.PlatformAntigravity)

	apiKey := &apikey.APIKey{
		ID:      3002,
		UserID:  4002,
		GroupID: &groupID,
		Status:  billing.StatusActive,
		User: &identity.User{
			ID:          4002,
			Concurrency: 10,
			Balance:     100,
		},
		Group: group,
	}

	c.Set(string(keyhttp.ContextKeyAPIKey), apiKey)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

	h.Messages(c)

	require.Equal(t, 200, rec.Code)

	selected, ok := c.Get(gatewayhttp.OpsProviderIDKey)
	require.True(t, ok)
	require.Equal(t, providerID, selected)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Regexp(t, `^msg_01[0-9A-Za-z]{22}$`, resp["id"])
	require.Equal(t, "claude-sonnet-4-5", resp["model"])
}
