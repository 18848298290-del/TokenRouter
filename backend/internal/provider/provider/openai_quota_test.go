package provider

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	openaiupstream "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// sparkShadowUsageTestRepo retains parent and shadow fixtures and captures
// conditional usage patches.
type sparkShadowUsageTestRepo struct {
	providercore.OAuthUsageReader // unimplemented usage repository methods
	// providers retains the initializer's parent and shadow pointers. GetByID
	// returns those same records for synchronous reads and delayed version
	// comparisons, so tests keep them stable during observation writes.
	providers map[int64]*providercore.Record // shared parent and shadow records
	// updateExtraCh optionally captures shallow quota patches. Nil skips capture,
	// and tests buffer it for writes that can complete asynchronously.
	updateExtraCh chan map[string]any
}

// quotaReadFixture supplies provider records to the production quota test
// service.
type quotaReadFixture interface {
	// GetProvider loads the requested provider for quota preparation or returns a
	// lookup error. The service resolves shadow ownership and authentication after
	// this configured fixture read succeeds.
	GetProvider(context.Context, int64) (*providercore.Record, error)
}

// quotaFixture retains production quota querying and its configurable platform
// client factory.
type quotaFixture struct {
	*providercore.OpenAIQuotaService // embedded production quota service
	// factory remains captured by the production Client callback. Install task
	// callbacks before quota operations and keep that configuration stable while
	// the retained factory constructs or executes clients.
	factory *OpenAIQuotaFactory // retained configurable quota client factory
}

// agentIdentityWSInvalidationRecorder records provider IDs whose task recovery
// invalidates WebSocket connections.
type agentIdentityWSInvalidationRecorder struct {
	providerIDs []int64 // captured connection invalidation provider identifiers
}

// stubQuotaProviderRepo retains provider pointers and captures Extra patches
// for sequential quota tests.
type stubQuotaProviderRepo struct {
	// providers retains caller record pointers by ID. Sequential tests replace
	// their credential maps while quota preparation and task recovery read them.
	providers map[int64]*providercore.Record
	// extraUpdates retains accepted quota patches by provider ID. Their nested
	// values remain shared with the caller for later assertions.
	extraUpdates     map[int64]map[string]any
	extraUpdateCalls int // accepted Extra patch write count
	// extraUpdateErr returns before any patch capture or accepted-write count. Nil
	// lets UpdateExtra retain the requested observation.
	extraUpdateErr error
}

// quotaProviderGetter loads provider records for administrative quota
// preparation fixtures.
type quotaProviderGetter interface {
	// GetByID returns the selected provider record or lookup error to the
	// administrative wrapper. Fixture implementations document whether that
	// returned record shares storage with test setup.
	GetByID(context.Context, int64) (*providercore.Record, error)
}

// stubQuotaAdminService forwards provider reads to the configured quota
// repository fixture.
type stubQuotaAdminService struct {
	repo quotaProviderGetter // configured quota record lookup fixture
}

// stubQuotaTokenCache supplies preconfigured access tokens and accepts other
// cache operations as no-ops.
type stubQuotaTokenCache struct {
	// tokens is shared with test setup and read directly by GetAccessToken.
	// Configure it before quota operations; accepted cache writes do not update it.
	tokens map[string]string // preconfigured access tokens by key
}

// stubQuotaHTTPUpstream captures account identity and serves quota fixtures or
// local HTTP responses.
type stubQuotaHTTPUpstream struct {
	capturedProviderID string // latest captured ChatGPT account header
	// responseBody is the fallback response text after path scripts miss. Empty
	// text supplies an empty JSON object with HTTP 200.
	responseBody string
	// responses selects fixtures by exact request path when no redirect target is
	// set. Missing paths retain the fallback response-body choice.
	responses map[string]stubQuotaHTTPResponse
	// redirectTarget sends cloned requests to a local scheme and host when nonnil.
	// It wins over scripted responses and stays stable during calls.
	redirectTarget *url.URL
}

// stubQuotaHTTPResponse supplies one quota endpoint's status and JSON response
// text.
type stubQuotaHTTPResponse struct {
	// status supplies the scripted HTTP code. Zero becomes HTTP 200 when the
	// transport creates this response body.
	status int
	body   string // scripted quota response JSON text
}

// TestGetOpenAIUsage_SparkShadow_WritesExtraAndReturnsNonEmptyWindows queries
// Spark-specific quota using its parent's credentials. The returned short and
// weekly windows must be nonempty, and the shadow's conditional Extra writer
// must receive Spark utilization.
func TestGetOpenAIUsage_SparkShadow_WritesExtraAndReturnsNonEmptyWindows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	pid := int64(100)
	shadow := &providercore.Record{
		ID:               200,
		ParentProviderID: &pid,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		Status:           providercore.StatusActive,
		QuotaDimension:   providercore.QuotaDimensionSpark,
	}
	parent := &providercore.Record{
		ID:       100,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   providercore.StatusActive,
		Credentials: map[string]any{
			"chatgpt_account_id": "org-spark-parent",
		},
	}

	// Share the repository between parent resolution and shadow usage persistence.
	updateExtraCh := make(chan map[string]any, 1)
	repo := &sparkShadowUsageTestRepo{
		// Stored snapshots are independent of request-local mutations.
		providers: map[int64]*providercore.Record{
			200: providercore.CloneRecord(shadow),
			100: providercore.CloneRecord(parent),
		},
		updateExtraCh: updateExtraCh,
	}

	tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
		providercore.OpenAITokenCacheKey(parent): "fake-access-token",
	}}
	tokenProvider := newOpenAITokenSourceForTest(repo, tokenCache, nil)

	// The stub captures the account header and returns Spark-specific quota windows.
	resp := openai.OpenAIQuotaUsage{
		AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
			{
				MeteredFeature: "codex_bengalfox",
				RateLimit: &openai.OpenAIRateLimit{
					// Five-hour primary window (18,000 seconds).
					PrimaryWindow: &openai.OpenAIRateLimitWindow{
						UsedPercent:        42.5,
						ResetAfterSeconds:  3600,
						LimitWindowSeconds: 18000,
					},
					// Seven-day secondary window (604,800 seconds).
					SecondaryWindow: &openai.OpenAIRateLimitWindow{
						UsedPercent:        10.0,
						ResetAfterSeconds:  86400,
						LimitWindowSeconds: 604800,
					},
				},
			},
		},
	}
	payload, err := json.Marshal(resp)
	require.NoError(t, err)

	upstream := &stubQuotaHTTPUpstream{responseBody: string(payload)}
	quotaFactory := &OpenAIQuotaFactory{Transport: upstream}
	quotaService := providercore.NewOpenAIQuotaService(providercore.OpenAIQuotaOptions{
		Configured: func() bool { return true },
		Read: func(ctx context.Context, id int64) (*providercore.Record, error) {
			value, err := repo.GetByID(ctx, id)
			return providercore.CloneRecord(value), err
		},
		Client: quotaFactory.Client, Token: tokenProvider.GetAccessToken, Warn: slog.Warn, Info: slog.Info,
	})
	svc := providercore.NewOAuthUsageService(repo, nil, nil, providercore.OAuthUsageOptions{OpenAI: providercore.OpenAIUsageOptions{
		Shadow: func(ctx context.Context, id int64, now time.Time) (map[string]any, error) {
			value, err := quotaService.QueryUsage(ctx, id)
			if err != nil {
				return nil, err
			}
			return providercore.BuildCodexSparkWindowExtraUpdates(value, now), nil
		},
	}})

	usage, err := svc.GetOpenAIUsage(ctx, shadow, true /* force */)
	require.NoError(t, err)

	require.Equal(t, "org-spark-parent", upstream.capturedProviderID,
		"QueryUsage must use parent's chatgpt-account-id for spark shadow providers")

	select {
	case updates := <-updateExtraCh:
		require.Contains(t, updates, "codex_5h_used_percent",
			"persisted extra must contain codex_5h_used_percent")
		require.InDelta(t, 42.5, updates["codex_5h_used_percent"], 0.01,
			"codex_5h_used_percent must match the upstream value")
	case <-time.After(2 * time.Second):
		t.Fatal("UpdateExtra was not called within timeout — spark shadow persist did not happen")
	}

	require.NotNil(t, usage.FiveHour,
		"returned UsageInfo.FiveHour must be non-nil (rebuild from merged Extra must happen)")
	require.NotNil(t, usage.SevenDay,
		"returned UsageInfo.SevenDay must be non-nil (rebuild from merged Extra must happen)")
}

// TestQueryUsageResetCreditCountPrecedence combines usage counts with explicit,
// absent, invalid, and filtered detail responses. Authoritative detail counts
// or lists must take their defined precedence while unusable detail shapes
// retain available usage data.
func TestQueryUsageResetCreditCountPrecedence(t *testing.T) {
	tests := []struct {
		name        string // credit detail precedence scenario label
		usageBody   string // supplied usage response JSON
		detailBody  string // supplied credit detail response JSON
		wantCount   int    // expected available reset-credit count
		wantCredits int    // expected displayed expiration detail count
		// wantNil expects the merged credit result to be absent. True skips count and
		// expiration-length checks after the nil assertion.
		wantNil bool
	}{
		{
			name:       "detail count creates missing usage credits",
			usageBody:  `{}`,
			detailBody: `{"available_count":3,"credits":[{"expires_at":"2026-07-03T04:05:06Z"}]}`,
			wantCount:  3, wantCredits: 1,
		},
		{
			name:       "explicit detail zero overrides usage and records",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":4}}`,
			detailBody: `{"available_count":0,"credits":[{"expires_at":"2026-07-03T04:05:06Z"}]}`,
			wantCount:  0, wantCredits: 1,
		},
		{
			name:       "available records override usage when detail count is absent",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `{"credits":[{"expires_at":"2026-07-03T04:05:06Z"},{"expiresAt":"2026-07-04T04:05:06Z"}]}`,
			wantCount:  2, wantCredits: 2,
		},
		{
			name:       "empty detail list overrides usage with zero",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `{"credits":[]}`,
			wantCount:  0,
		},
		{
			name:       "fully filtered list overrides usage with zero",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `{"credits":[{"reset_type":"codex_rate_limits","status":"redeemed","expires_at":"2026-07-03T04:05:06Z"},{"reset_type":"other","status":"available","expires_at":"2026-07-04T04:05:06Z"}]}`,
			wantCount:  0,
		},
		{
			name:       "available records without expiry still count",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `{"credits":[{"status":"available"},{"status":"available","expires_at":"2026-07-04T04:05:06Z"}]}`,
			wantCount:  2, wantCredits: 1,
		},
		{
			name:        "shape without count or list preserves usage details",
			usageBody:   `{"rate_limit_reset_credits":{"available_count":5,"credits":[{"expires_at":"usage-expiry"}]}}`,
			detailBody:  `{}`,
			wantCount:   5,
			wantCredits: 1,
		},
		{
			name:        "valid detail count survives malformed authoritative list",
			usageBody:   `{"rate_limit_reset_credits":{"available_count":7,"credits":[{"expires_at":"usage-expiry"}]}}`,
			detailBody:  `{"available_count":2,"credits":"malformed"}`,
			wantCount:   2,
			wantCredits: 1,
		},
		{
			name:       "valid detail count creates quota despite malformed authoritative list",
			usageBody:  `{}`,
			detailBody: `{"available_count":2,"credits":"malformed"}`,
			wantCount:  2,
		},
		{
			name:       "negative detail count without list preserves usage",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":4}}`,
			detailBody: `{"available_count":-1}`,
			wantCount:  4,
		},
		{
			name:       "negative detail count falls back to available records",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":4}}`,
			detailBody: `{"available_count":-1,"credits":[{"status":"available","expires_at":"2026-07-04T04:05:06Z"}]}`,
			wantCount:  1, wantCredits: 1,
		},
		{
			name:       "empty object preserves missing usage credits",
			usageBody:  `{}`,
			detailBody: `{}`,
			wantNil:    true,
		},
		{
			name:       "null body preserves missing usage credits",
			usageBody:  `{}`,
			detailBody: `null`,
			wantNil:    true,
		},
		{
			name:       "empty body preserves missing usage credits",
			usageBody:  `{}`,
			detailBody: ``,
			wantNil:    true,
		},
		{
			name:       "null object record is not counted",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `{"credits":[null]}`,
			wantCount:  0,
		},
		{
			name:       "null top level record is not counted",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `[null]`,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &providercore.Record{
				ID:       100,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Status:   billing.StatusActive,
				Credentials: map[string]any{
					"chatgpt_account_id": "org-parent123",
				},
			}
			repo := &stubQuotaProviderRepo{providers: map[int64]*providercore.Record{100: provider}}
			tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
				providercore.OpenAITokenCacheKey(provider): "fake-token",
			}}
			tokenProvider := newOpenAITokenSourceForTest(repo, tokenCache, nil)

			upstream := &stubQuotaHTTPUpstream{responses: map[string]stubQuotaHTTPResponse{
				"/backend-api" + "/wham/usage":                    {body: tt.usageBody},
				"/backend-api" + "/wham/rate-limit-reset-credits": {body: tt.detailBody},
			}}
			svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, upstream, tokenProvider, nil, nil)
			usage, err := svc.QueryUsage(context.Background(), 100)
			require.NoError(t, err)
			require.NotNil(t, usage)
			if tt.wantNil {
				require.Nil(t, usage.RateLimitResetCredits)
				return
			}
			require.NotNil(t, usage.RateLimitResetCredits)
			require.Equal(t, tt.wantCount, usage.RateLimitResetCredits.AvailableCount)
			require.Len(t, usage.RateLimitResetCredits.Credits, tt.wantCredits)
		})
	}
}

// TestOpenAIQuotaServiceQueryUsageUsesCodexHeaders queries usage and expiration
// details through the desktop endpoint family. Both requests must carry paired
// Codex, account, integrity, and OpenAI transport headers while parsed credit
// details remain available.
func TestOpenAIQuotaServiceQueryUsageUsesCodexHeaders(t *testing.T) {
	provider := &providercore.Record{
		ID:          42,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 3,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}
	upstream := &codexInviteResetHTTPUpstreamStub{responses: []*http.Response{
		codexInviteResetJSONResponse(`{"user_id":"user-1","rate_limit_reset_credits":{"available_count":2}}`),
		codexInviteResetJSONResponse(`{"credits":[{"expires_at":"2026-07-03T04:05:06Z"},{"expiresAt":"2026-07-04T04:05:06Z"}]}`),
	}}
	svc := newQuotaForTest(codexInviteResetAdminServiceStub{provider: provider}, upstream, nil, nil, nil)

	usage, err := svc.QueryUsage(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, "user-1", usage.UserID)
	require.NotNil(t, usage.RateLimitResetCredits)
	require.Equal(t, 2, usage.RateLimitResetCredits.AvailableCount)
	require.Equal(t, []openai.OpenAIRateLimitResetCreditDetail{
		{ExpiresAt: "2026-07-03T04:05:06Z"},
		{ExpiresAt: "2026-07-04T04:05:06Z"},
	}, usage.RateLimitResetCredits.Credits)
	require.Greater(t, usage.FetchedAt, int64(0))

	require.Len(t, upstream.requests, 2)
	require.Equal(t, "/backend-api/wham/usage", upstream.requests[0].URL.Path)
	require.Equal(t, "true", upstream.requests[0].URL.Query().Get("supports_rewardless_invites"))
	require.Equal(t, "/backend-api/wham/rate-limit-reset-credits", upstream.requests[1].URL.Path)
	for _, req := range upstream.requests {
		require.Equal(t, "Bearer oauth-token", req.Header.Get("Authorization"))
		require.Equal(t, "codex-1", req.Header.Get("OpenAI-Beta"))
		require.Equal(t, "Codex Desktop", req.Header.Get("originator"))
		require.Equal(t, openaiupstream.CodexInviteDefaultUserAgent, req.Header.Get("User-Agent"))
		require.Equal(t, "chatgpt-acc", req.Header.Get("chatgpt-account-id"))
		require.Equal(t, "1", req.Header.Get("X-OpenAI-Attach-Auth"))
		require.Equal(t, "1", req.Header.Get("X-OpenAI-Attach-Integrity-State"))
		require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(req.Context()))
	}
}

// TestOpenAIQuotaServiceResetCreditUsesAutomaticSelectionAndRedeemRequestID
// consumes a reset without choosing a credit ID. The request must omit that ID,
// include a generated redemption identifier, and return the upstream reset
// result.
func TestOpenAIQuotaServiceResetCreditUsesAutomaticSelectionAndRedeemRequestID(t *testing.T) {
	provider := &providercore.Record{
		ID:       9,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}
	upstream := &codexInviteResetHTTPUpstreamStub{responses: []*http.Response{
		codexInviteResetJSONResponse(`{"code":"reset","windows_reset":1}`),
	}}
	svc := newQuotaForTest(codexInviteResetAdminServiceStub{provider: provider}, upstream, nil, nil, nil)

	result, err := svc.ResetCredit(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, "reset", result.Code)
	require.Equal(t, 1, result.WindowsReset)

	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/backend-api/wham/rate-limit-reset-credits/consume", upstream.requests[0].URL.Path)
	require.Equal(t, "application/json", upstream.requests[0].Header.Get("Content-Type"))
	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(upstream.bodies[0]), &payload))
	require.NotContains(t, payload, "credit_id")
	require.NotEmpty(t, payload["redeem_request_id"])
	require.Contains(t, payload["redeem_request_id"], "-")
}

// TestOpenAIQuotaServiceResetCreditReturnsUpstreamNoCreditResult serves a
// successful no-credit response. The service must return its code and zero
// reset count as a result rather than a transport error.
func TestOpenAIQuotaServiceResetCreditReturnsUpstreamNoCreditResult(t *testing.T) {
	provider := &providercore.Record{
		ID:       10,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}
	upstream := &codexInviteResetHTTPUpstreamStub{responses: []*http.Response{
		codexInviteResetJSONResponse(`{"code":"no_credit","windows_reset":0}`),
	}}
	svc := newQuotaForTest(codexInviteResetAdminServiceStub{provider: provider}, upstream, nil, nil, nil)

	result, err := svc.ResetCredit(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, "no_credit", result.Code)
	require.Equal(t, 0, result.WindowsReset)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/backend-api/wham/rate-limit-reset-credits/consume", upstream.requests[0].URL.Path)
}

// TestOpenAIQuotaServiceCacheResetCreditsSnapshot compares complete expiration
// details, positive counts without details, empty zero counts, and storage
// failure. Snapshot acceptance and returned errors must preserve the defined
// validity and persistence rules.
func TestOpenAIQuotaServiceCacheResetCreditsSnapshot(t *testing.T) {
	t.Run("保存带到期明细的快照", func(t *testing.T) {
		repo := &stubQuotaProviderRepo{}
		svc := &providercore.OpenAIQuotaService{Options: providercore.OpenAIQuotaOptions{SaveExtra: repo.UpdateExtra}}
		credits := &openai.OpenAIRateLimitResetCredits{
			AvailableCount: 1,
			Credits: []openai.OpenAIRateLimitResetCreditDetail{
				{ExpiresAt: "2099-07-03T04:05:06Z"},
			},
		}

		require.NoError(t, svc.CacheResetCreditsSnapshot(context.Background(), 42, credits))
		require.Equal(t, credits, repo.extraUpdates[42]["codex_reset_credit_snapshot"])
	})

	t.Run("正数次数缺少到期明细时保留旧缓存", func(t *testing.T) {
		repo := &stubQuotaProviderRepo{}
		svc := &providercore.OpenAIQuotaService{Options: providercore.OpenAIQuotaOptions{SaveExtra: repo.UpdateExtra}}

		err := svc.CacheResetCreditsSnapshot(context.Background(), 42, &openai.OpenAIRateLimitResetCredits{AvailableCount: 1})

		require.Error(t, err)
		require.Empty(t, repo.extraUpdates)
	})

	t.Run("零次数允许空明细", func(t *testing.T) {
		repo := &stubQuotaProviderRepo{}
		svc := &providercore.OpenAIQuotaService{Options: providercore.OpenAIQuotaOptions{SaveExtra: repo.UpdateExtra}}
		credits := &openai.OpenAIRateLimitResetCredits{AvailableCount: 0}

		require.NoError(t, svc.CacheResetCreditsSnapshot(context.Background(), 42, credits))
		require.Equal(t, credits, repo.extraUpdates[42]["codex_reset_credit_snapshot"])
	})

	t.Run("仓储错误向调用方返回", func(t *testing.T) {
		repo := &stubQuotaProviderRepo{extraUpdateErr: errors.New("database unavailable")}
		svc := &providercore.OpenAIQuotaService{Options: providercore.OpenAIQuotaOptions{SaveExtra: repo.UpdateExtra}}
		credits := &openai.OpenAIRateLimitResetCredits{
			AvailableCount: 1,
			Credits:        []openai.OpenAIRateLimitResetCreditDetail{{ExpiresAt: "2099-07-03T04:05:06Z"}},
		}

		err := svc.CacheResetCreditsSnapshot(context.Background(), 42, credits)

		require.ErrorContains(t, err, "database unavailable")
	})
}

// TestOpenAIQuotaServiceUsesTLSRouterInviteResetSettings configures direct and
// invitation-specific client identity settings. Quota requests must use the
// trimmed invitation agent and resolved invitation profile before provider
// defaults.
func TestOpenAIQuotaServiceUsesTLSRouterInviteResetSettings(t *testing.T) {
	quotaProfileID := int64(20)
	provider := &providercore.Record{
		ID:       44,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": int64(10),
			"tls_fingerprint_router_id":  int64(9),
		},
	}
	upstream := &codexInviteResetHTTPUpstreamStub{responses: []*http.Response{
		codexInviteResetJSONResponse(`{"rate_limit_reset_credits":{"available_count":0}}`),
	}}
	routerReader := &openAIOAuthTokenRouterReaderStub{routers: map[int64]*egress.TLSFingerprintRouter{
		9: {
			ID:                                      9,
			Enabled:                                 true,
			CodexInviteResetUserAgent:               " Codex Desktop/0.135.0-alpha.1 (Windows 10.0.26200; x86_64) ",
			CodexInviteResetTLSFingerprintProfileID: &quotaProfileID,
		},
	}}
	profileService := newTLSProfileServiceWithCacheForTest(map[int64]*egress.TLSFingerprintProfile{
		10: {ID: 10, Name: "provider-fixed"},
		20: {ID: 20, Name: "router-token"},
	})
	svc := newQuotaForTest(codexInviteResetAdminServiceStub{provider: provider}, upstream, nil, profileService, routerReader)

	_, err := svc.QueryUsage(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Len(t, upstream.requests, 2)
	require.Len(t, upstream.profiles, 2)
	for i := range upstream.requests {
		require.Equal(t, "Codex Desktop/0.135.0-alpha.1 (Windows 10.0.26200; x86_64)", upstream.requests[i].Header.Get("User-Agent"))
		require.NotNil(t, upstream.profiles[i])
		require.Equal(t, "router-token", upstream.profiles[i].Name)
	}
}

// TestOpenAIQuotaServiceRejectsUnsupportedProvider supplies an API-key record
// to the OAuth quota service. Query must return a structured
// unsupported-provider error while omitting the credential value from its
// diagnostic.
func TestOpenAIQuotaServiceRejectsUnsupportedProvider(t *testing.T) {
	provider := &providercore.Record{
		ID:       7,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
	}
	svc := newQuotaForTest(codexInviteResetAdminServiceStub{provider: provider}, &codexInviteResetHTTPUpstreamStub{}, nil, nil, nil)

	_, err := svc.QueryUsage(context.Background(), provider.ID)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
	require.Equal(t, "OPENAI_QUOTA_UNSUPPORTED_PROVIDER", apperror.Reason(err))
	require.False(t, strings.Contains(err.Error(), "sk-test"))
}

// TestResetCreditShadowRejected supplies a Spark shadow with no usable
// transport dependencies. The shadow guard must return the structured conflict
// before upstream client preparation can run.
func TestResetCreditShadowRejected(t *testing.T) {
	pid := int64(100)
	shadow := &providercore.Record{
		ID:               200,
		ParentProviderID: &pid,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		QuotaDimension:   providercore.QuotaDimensionSpark,
	}
	repo := &stubQuotaProviderRepo{
		providers: map[int64]*providercore.Record{200: shadow},
	}
	// A nil transport would produce a configuration error if the shadow guard were bypassed.
	svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, nil, nil, nil, nil)

	_, err := svc.ResetCredit(context.Background(), 200)
	require.ErrorIs(t, err, providercore.ErrSparkShadowResetNotSupported,
		"shadow ResetCredit should return ErrSparkShadowResetNotSupported, got: %v", err)
	require.Equal(t, http.StatusConflict, httpx.ErrorCode(err),
		"shadow ResetCredit 应映射为 409 Conflict 而非 500")
}

// TestResetCreditAgentIdentityUsesAssertionAndRecoversInvalidTaskOnce rejects
// the first assertion-authenticated reset with invalid_task_id. One
// registration and one replay must use a new assertion, persist the new task,
// and invalidate its provider's WebSocket connections.
func TestResetCreditAgentIdentityUsesAssertionAndRecoversInvalidTaskOnce(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	provider := &providercore.Record{
		ID:       201,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":          providercore.OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":   "runtime-reset-recovery",
			"agent_private_key":  base64.StdEncoding.EncodeToString(der),
			"task_id":            "task-reset-old",
			"chatgpt_account_id": "provider-reset-recovery",
		},
	}
	repo := &stubQuotaProviderRepo{providers: map[int64]*providercore.Record{provider.ID: provider}}
	resetCalls := 0
	registerCalls := 0
	var assertions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if strings.Contains(r.URL.Path, "/task/register") {
			registerCalls++
			_, _ = w.Write([]byte(`{"task_id":"task-reset-new"}`))
			return
		}
		resetCalls++
		assertions = append(assertions, r.Header.Get("authorization"))
		require.Equal(t, "provider-reset-recovery", r.Header.Get("chatgpt-account-id"))
		if resetCalls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_task_id"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":"ok","windows_reset":2}`))
	}))
	defer srv.Close()

	invalidator := &agentIdentityWSInvalidationRecorder{}
	svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, newQuotaRedirectingUpstream(t, srv), nil, nil, nil)
	bindQuotaRepositoryForTest(svc, repo, srv.URL)
	svc.factory.TaskOptions.Invalidate = invalidator.InvalidateAgentIdentityWSConnections

	result, err := svc.ResetCredit(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, "ok", result.Code)
	require.Equal(t, 2, result.WindowsReset)
	require.Equal(t, 2, resetCalls)
	require.Equal(t, 1, registerCalls)
	require.Len(t, assertions, 2)
	require.True(t, strings.HasPrefix(assertions[0], "AgentAssertion "))
	require.True(t, strings.HasPrefix(assertions[1], "AgentAssertion "))
	require.NotEqual(t, assertions[0], assertions[1])
	require.Equal(t, "task-reset-new", provider.GetCredential("task_id"))
	require.Equal(t, []int64{provider.ID}, invalidator.providerIDs)
}

// TestResetCreditAgentIdentityReusesConcurrentlyRecoveredTask replaces the
// stored task before returning its first invalid-task response. Replay must use
// that concurrently recovered task and perform no additional registration.
func TestResetCreditAgentIdentityReusesConcurrentlyRecoveredTask(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	provider := &providercore.Record{
		ID:       202,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":          providercore.OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":   "runtime-reset-concurrent",
			"agent_private_key":  base64.StdEncoding.EncodeToString(der),
			"task_id":            "task-reset-old",
			"chatgpt_account_id": "provider-reset-concurrent",
		},
	}
	repo := &stubQuotaProviderRepo{providers: map[int64]*providercore.Record{provider.ID: provider}}
	resetCalls := 0
	registerCalls := 0
	var assertions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if strings.Contains(r.URL.Path, "/task/register") {
			registerCalls++
			_, _ = w.Write([]byte(`{"task_id":"task-reset-unexpected"}`))
			return
		}
		resetCalls++
		assertions = append(assertions, r.Header.Get("authorization"))
		if resetCalls == 1 {
			// Replace the task through a fresh credential map before returning
			// invalid_task_id. The recovering caller then must reuse that newer task
			// instead of registering another.
			credentials := querycache.ShallowMap(provider.Credentials)
			credentials["task_id"] = "task-reset-concurrent"
			provider.Credentials = credentials
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_task_id"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":"ok","windows_reset":1}`))
	}))
	defer srv.Close()

	svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, newQuotaRedirectingUpstream(t, srv), nil, nil, nil)
	bindQuotaRepositoryForTest(svc, repo, srv.URL)
	result, err := svc.ResetCredit(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, "ok", result.Code)
	require.Equal(t, 2, resetCalls)
	require.Zero(t, registerCalls)
	require.Equal(t, "task-reset-old", decodeAgentAssertionTask(t, assertions[0]))
	require.Equal(t, "task-reset-concurrent", decodeAgentAssertionTask(t, assertions[1]))
}

// TestPrepareProviderShadowResolve supplies a credentialless shadow and a
// cached parent token. Quota preparation must return the parent's account
// identity after shadow resolution without requiring a token exchange.
func TestPrepareProviderShadowResolve(t *testing.T) {
	ctx := context.Background()
	pid := int64(100)

	// The shadow deliberately has no chatgpt_account_id.
	shadow := &providercore.Record{
		ID:               200,
		ParentProviderID: &pid,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		Status:           billing.StatusActive,
		QuotaDimension:   providercore.QuotaDimensionSpark,
	}
	parent := &providercore.Record{
		ID:       100,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"chatgpt_account_id": "org-parent123",
		},
	}
	repo := &stubQuotaProviderRepo{providers: map[int64]*providercore.Record{200: shadow, 100: parent}}

	tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
		providercore.OpenAITokenCacheKey(parent): "fake-access-token",
	}}
	tokenProvider := newOpenAITokenSourceForTest(repo, tokenCache, nil)

	upstream := &stubQuotaHTTPUpstream{}
	svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, upstream, tokenProvider, nil, nil)

	providerCtx, err := svc.PrepareProvider(ctx, 200)
	require.NoError(t, err, "shadow resolve should succeed; got error: %v", err)
	require.Equal(t, "org-parent123", providerCtx.Provider.GetChatGPTAccountID(),
		"prepareProvider should use parent's chatgpt_account_id after shadow resolve")
}

// TestQueryUsageAgentIdentityUsesAssertionWithoutOAuthToken supplies runtime
// signing credentials and a FedRAMP account marker. Usage must authenticate
// with an assertion and transmit account and FedRAMP headers while returning
// parsed quota.
func TestQueryUsageAgentIdentityUsesAssertionWithoutOAuthToken(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	provider := &providercore.Record{
		ID:       300,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":                  providercore.OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":           "runtime-quota",
			"agent_private_key":          base64.StdEncoding.EncodeToString(der),
			"task_id":                    "task-quota",
			"chatgpt_account_id":         "provider-quota",
			"chatgpt_account_is_fedramp": true,
		},
	}
	repo := &stubQuotaProviderRepo{providers: map[int64]*providercore.Record{provider.ID: provider}}
	var authorization string
	var providerHeader string
	var fedrampHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("authorization")
		providerHeader = r.Header.Get("chatgpt-account-id")
		fedrampHeader = r.Header.Get("x-openai-fedramp")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"plan_type":"pro","rate_limit":{"allowed":true}}`))
	}))
	defer srv.Close()
	svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, newQuotaRedirectingUpstream(t, srv), nil, nil, nil)
	bindQuotaRepositoryForTest(svc, repo, srv.URL)
	usage, err := svc.QueryUsage(context.Background(), provider.ID)
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.True(t, strings.HasPrefix(authorization, "AgentAssertion "))
	require.Equal(t, "provider-quota", providerHeader)
	require.Equal(t, "true", fedrampHeader)
}

// TestQueryUsageAgentIdentityRecoversInvalidTaskOnce rejects the first usage
// request for its stored task. One registration and one replay must persist the
// replacement task and invalidate the provider's WebSocket connections.
func TestQueryUsageAgentIdentityRecoversInvalidTaskOnce(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	provider := &providercore.Record{
		ID:       301,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":          providercore.OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":   "runtime-quota-recovery",
			"agent_private_key":  base64.StdEncoding.EncodeToString(der),
			"task_id":            "task-quota-old",
			"chatgpt_account_id": "provider-quota-recovery",
		},
	}
	repo := &stubQuotaProviderRepo{providers: map[int64]*providercore.Record{provider.ID: provider}}
	usageCalls := 0
	registerCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if strings.Contains(r.URL.Path, "/task/register") {
			registerCalls++
			_, _ = w.Write([]byte(`{"task_id":"task-quota-new"}`))
			return
		}
		if strings.Contains(r.URL.Path, "rate-limit-reset-credits") {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		usageCalls++
		if usageCalls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_task_id"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"plan_type":"pro","rate_limit":{"allowed":true}}`))
	}))
	defer srv.Close()

	invalidator := &agentIdentityWSInvalidationRecorder{}
	svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, newQuotaRedirectingUpstream(t, srv), nil, nil, nil)
	bindQuotaRepositoryForTest(svc, repo, srv.URL)
	svc.factory.TaskOptions.Invalidate = invalidator.InvalidateAgentIdentityWSConnections
	usage, err := svc.QueryUsage(context.Background(), provider.ID)
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 2, usageCalls)
	require.Equal(t, 1, registerCalls)
	require.Equal(t, "task-quota-new", provider.GetCredential("task_id"))
	require.Equal(t, []int64{provider.ID}, invalidator.providerIDs)
}

// TestQueryUsageIncludesResetCreditExpirations_EndToEnd combines usage count
// with mixed expiration spellings in credit details. The result must expose
// expiration dates while serialized quota data omits the upstream credit
// identifier.
func TestQueryUsageIncludesResetCreditExpirations_EndToEnd(t *testing.T) {
	ctx := context.Background()
	provider := &providercore.Record{
		ID:       100,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"chatgpt_account_id": "org-parent123",
		},
	}
	repo := &stubQuotaProviderRepo{providers: map[int64]*providercore.Record{100: provider}}
	tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
		providercore.OpenAITokenCacheKey(provider): "fake-token",
	}}
	tokenProvider := newOpenAITokenSourceForTest(repo, tokenCache, nil)
	upstream := &stubQuotaHTTPUpstream{
		responses: map[string]stubQuotaHTTPResponse{
			"/backend-api/wham/usage": {
				body: `{"rate_limit_reset_credits":{"available_count":2}}`,
			},
			"/backend-api/wham/rate-limit-reset-credits": {
				body: `{"credits":[{"id":"secret-credit-id","expires_at":"2026-07-03T04:05:06Z"},{"expiresAt":"2026-07-04T04:05:06Z"}]}`,
			},
		},
	}

	svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, upstream, tokenProvider, nil, nil)
	usage, err := svc.QueryUsage(ctx, 100)
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.NotNil(t, usage.RateLimitResetCredits)
	require.Equal(t, 2, usage.RateLimitResetCredits.AvailableCount)
	require.Equal(t, []openai.OpenAIRateLimitResetCreditDetail{
		{ExpiresAt: "2026-07-03T04:05:06Z"},
		{ExpiresAt: "2026-07-04T04:05:06Z"},
	}, usage.RateLimitResetCredits.Credits)

	encoded, err := json.Marshal(usage)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-credit-id")
}

// TestQueryUsageResetCreditDetails401NonFatal returns a valid usage count and
// an unauthorized detail endpoint. The overall query must succeed with that
// count and empty credit details.
func TestQueryUsageResetCreditDetails401NonFatal(t *testing.T) {
	ctx := context.Background()
	provider := &providercore.Record{
		ID:       100,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"chatgpt_account_id": "org-parent123",
		},
	}
	repo := &stubQuotaProviderRepo{providers: map[int64]*providercore.Record{100: provider}}
	tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
		providercore.OpenAITokenCacheKey(provider): "fake-token",
	}}
	tokenProvider := newOpenAITokenSourceForTest(repo, tokenCache, nil)
	upstream := &stubQuotaHTTPUpstream{
		responses: map[string]stubQuotaHTTPResponse{
			"/backend-api/wham/usage": {
				body: `{"rate_limit_reset_credits":{"available_count":1}}`,
			},
			"/backend-api/wham/rate-limit-reset-credits": {
				status: http.StatusUnauthorized,
				body:   `{"error":"unauthorized","id":"secret-error-id"}`,
			},
		},
	}

	svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, upstream, tokenProvider, nil, nil)
	usage, err := svc.QueryUsage(ctx, 100)
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.NotNil(t, usage.RateLimitResetCredits)
	require.Equal(t, 1, usage.RateLimitResetCredits.AvailableCount)
	require.Empty(t, usage.RateLimitResetCredits.Credits)
}

// TestCachePostResetSnapshot supplies fresh zero-use short and weekly windows
// with zero credits. One Extra write must contain both quota measurements and
// the reset-credit snapshot.
func TestCachePostResetSnapshot(t *testing.T) {
	repo := &stubQuotaProviderRepo{}
	svc := &providercore.OpenAIQuotaService{Options: providercore.OpenAIQuotaOptions{SaveExtra: repo.UpdateExtra}}
	credits := &openai.OpenAIRateLimitResetCredits{AvailableCount: 0}
	usage := &openai.OpenAIQuotaUsage{
		RateLimitResetCredits: credits,
		RateLimit: &openai.OpenAIRateLimit{
			PrimaryWindow: &openai.OpenAIRateLimitWindow{
				UsedPercent: 0, LimitWindowSeconds: 5 * 60 * 60, ResetAfterSeconds: 5 * 60 * 60,
			},
			SecondaryWindow: &openai.OpenAIRateLimitWindow{
				UsedPercent: 0, LimitWindowSeconds: 7 * 24 * 60 * 60, ResetAfterSeconds: 7 * 24 * 60 * 60,
			},
		},
	}

	require.NoError(t, svc.CachePostResetSnapshot(context.Background(), 100, usage))
	require.Equal(t, 1, repo.extraUpdateCalls)
	require.Equal(t, credits, repo.extraUpdates[100]["codex_reset_credit_snapshot"])
	require.Equal(t, 0.0, repo.extraUpdates[100]["codex_5h_used_percent"])
	require.Equal(t, 0.0, repo.extraUpdates[100]["codex_7d_used_percent"])
}

// TestResetCreditGetByIDError_FailsClosed looks up an absent provider while
// transport and token dependencies are missing. The lookup error must return
// before client preparation produces a configuration failure.
func TestResetCreditGetByIDError_FailsClosed(t *testing.T) {
	repo := &stubQuotaProviderRepo{providers: map[int64]*providercore.Record{}}
	svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, nil, nil, nil, nil)

	_, err := svc.ResetCredit(context.Background(), 200)
	require.Error(t, err, "GetByID error must propagate; got nil")
	require.NotContains(t, err.Error(), "not configured",
		"error reached prepareProvider config-check — guard did not fail-closed; got: %v", err)
}

// TestQueryUsageShadowResolve_EndToEnd sends a shadow query with a cached
// parent token. The transport must receive the parent's account header while
// returning a nonnil quota result.
func TestQueryUsageShadowResolve_EndToEnd(t *testing.T) {
	ctx := context.Background()
	pid := int64(100)

	shadow := &providercore.Record{
		ID: 200, ParentProviderID: &pid,
		Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billing.StatusActive, QuotaDimension: providercore.QuotaDimensionSpark,
	}
	parent := &providercore.Record{
		ID: 100, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": "org-e2e-parent"},
	}
	repo := &stubQuotaProviderRepo{providers: map[int64]*providercore.Record{200: shadow, 100: parent}}

	tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
		providercore.OpenAITokenCacheKey(parent): "fake-token-e2e",
	}}
	tokenProvider := newOpenAITokenSourceForTest(repo, tokenCache, nil)

	payload, err := json.Marshal(openai.OpenAIQuotaUsage{})
	require.NoError(t, err)
	upstream := &stubQuotaHTTPUpstream{responseBody: string(payload)}

	svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, upstream, tokenProvider, nil, nil)
	usage, err := svc.QueryUsage(ctx, 200)
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, "org-e2e-parent", upstream.capturedProviderID,
		"upstream should receive parent's chatgpt-account-id; got: %s", upstream.capturedProviderID)
}

// GetByID returns the stored provider pointer under an exact fixture ID.
// Missing IDs return a fixture error; callers share that record with the
// sequential test setup.
func (r *sparkShadowUsageTestRepo) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	if acc, ok := r.providers[id]; ok {
		return acc, nil
	}
	return nil, fmt.Errorf("provider %d not found", id)
}

// UpdateExtra sends a shallow quota-patch copy to the optional capture channel.
// Nil channels skip capture, and tests buffer configured channels so snapshot
// persistence can finish before assertions read them.
func (r *sparkShadowUsageTestRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if r.updateExtraCh != nil {
		copied := make(map[string]any, len(updates))
		maps.Copy(copied, updates)
		// Publish a complete copied patch to the buffered assertion channel, allowing
		// the asynchronous usage write to finish before the test consumes its
		// observation.
		r.updateExtraCh <- copied
	}
	return nil
}

// UpdateUsageExtraIfUnchanged compares the current shadow's complete usage
// version before capturing a patch. Missing or mismatched rows return false,
// while a match delegates to UpdateExtra under the observed ID.
func (r *sparkShadowUsageTestRepo) UpdateUsageExtraIfUnchanged(ctx context.Context, version providercore.UsageObservationVersion, updates map[string]any) (bool, error) {
	current := r.providers[version.ID]
	if current == nil || !reflect.DeepEqual(providercore.ObserveUsageVersion(current), version) {
		return false, nil
	}
	return true, r.UpdateExtra(ctx, version.ID, updates)
}

// newQuotaForTest builds production quota querying with a retained client
// factory and nonnil reader. Proxy-capable readers supply its proxy callback,
// optional tokens supply bearer lookup, and missing transport makes the
// configured readiness predicate false.
func newQuotaForTest(reader quotaReadFixture, transport QoderTransport, token *providercore.OpenAITokenSource, profiles *egressprovider.TLSProfiles, routers OpenAITokenRouterReader) *quotaFixture {
	factory := &OpenAIQuotaFactory{Transport: transport, Profiles: profiles, Routers: routers, Tasks: &providercore.OpenAITaskCoordinator{}}
	if proxy, ok := reader.(interface {
		// GetProxy loads the configured proxy relation used by the client factory.
		// The quota fixture installs this optional method when the reader exposes it
		// and propagates its lookup failures.
		GetProxy(context.Context, int64) (*egress.Proxy, error)
	}); ok {
		factory.Proxy = proxy.GetProxy
	}
	options := providercore.OpenAIQuotaOptions{Configured: func() bool { return reader != nil && transport != nil }, Read: reader.GetProvider, Client: factory.Client, RedeemID: openaiupstream.GenerateOpenAIQuotaRedeemRequestID, Warn: slog.Warn, Info: slog.Info}
	if token != nil {
		options.Token = token.GetAccessToken
	}
	return &quotaFixture{OpenAIQuotaService: providercore.NewOpenAIQuotaService(options), factory: factory}
}

// bindQuotaRepositoryForTest installs Extra persistence and task read,
// registration, and credential-persistence callbacks on the fixture. Configure
// it before operations; registration uses the supplied local root and
// persistence delegates to production credential writing.
func bindQuotaRepositoryForTest(value *quotaFixture, repo *stubQuotaProviderRepo, baseURL string) {
	value.Options.SaveExtra = repo.UpdateExtra
	value.factory.TaskOptions.Read = repo.GetByID
	value.factory.TaskOptions.Register = func(ctx context.Context, record *providercore.Record) (string, error) {
		return RegisterAgentIdentityTask(ctx, record, baseURL)
	}
	value.factory.TaskOptions.Persist = func(ctx context.Context, record *providercore.Record, credentials map[string]any) error {
		_, err := providercore.PersistCredentials(ctx, repo, record, credentials, slog.Warn)
		return err
	}
}

// Update delegates the supplied record's credentials to the fixture's
// credential writer.
func (r *stubQuotaProviderRepo) Update(ctx context.Context, value *providercore.Record) error {
	return r.UpdateCredentials(ctx, value.ID, value.Credentials)
}

// newOpenAITokenSourceForTest binds repository, token cache, fresh metrics, and
// production refresh policy to a token source. The authorization parameter is
// unused, and the supplied dependencies remain shared with the fixture.
func newOpenAITokenSourceForTest(repo providercore.RefreshRepository, cache providercore.AccessTokenCache, _ *providercore.OpenAIAuthorization) *providercore.OpenAITokenSource {
	return &providercore.OpenAITokenSource{Repository: repo, Cache: cache, Metrics: &providercore.OpenAITokenMetricsStore{}, Policy: providercore.OpenAIProviderRefreshPolicy(), Debug: slog.Debug, Warn: slog.Warn}
}

// decodeAgentAssertionTask decodes the assertion envelope and returns its task
// ID for request comparisons. It fails the test on base64 or JSON errors and
// performs no signature verification.
func decodeAgentAssertionTask(t *testing.T, header string) string {
	t.Helper()
	encoded := strings.TrimPrefix(header, "AgentAssertion ")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var envelope struct {
		TaskID string `json:"task_id"` // signed assertion task identifier
	}
	require.NoError(t, json.Unmarshal(decoded, &envelope))
	return envelope.TaskID
}

// InvalidateAgentIdentityWSConnections appends the provider ID targeted by
// task-recovery invalidation.
func (r *agentIdentityWSInvalidationRecorder) InvalidateAgentIdentityWSConnections(id int64) {
	r.providerIDs = append(r.providerIDs, id)
}

// GetByID returns the shared record pointer for an exact fixture ID or a
// missing-provider error. The fixture retains caller-owned records, so
// sequential tests observe their credential replacements directly.
func (r *stubQuotaProviderRepo) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	acc, ok := r.providers[id]
	if !ok {
		return nil, fmt.Errorf("provider %d not found", id)
	}
	return acc, nil
}

// GetProvider delegates exact provider lookup to the fixture's configured
// repository.
func (s stubQuotaAdminService) GetProvider(ctx context.Context, id int64) (*providercore.Record, error) {
	return s.repo.GetByID(ctx, id)
}

// UpdateCredentials replaces a matching record's credentials with the supplied
// map and returns a missing-provider error otherwise. The accepted map remains
// shared with the caller, and fixture operations are sequential.
func (r *stubQuotaProviderRepo) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	acc, ok := r.providers[id]
	if !ok {
		return fmt.Errorf("provider %d not found", id)
	}
	acc.Credentials = credentials
	return nil
}

// UpdateExtra returns the configured storage error before capture when present.
// Otherwise it counts the accepted write and retains the supplied patch under
// the provider ID, sharing nested values with its caller.
func (r *stubQuotaProviderRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	if r.extraUpdateErr != nil {
		return r.extraUpdateErr
	}
	r.extraUpdateCalls++
	if r.extraUpdates == nil {
		r.extraUpdates = make(map[int64]map[string]any)
	}
	r.extraUpdates[id] = updates
	return nil
}

// GetAccessToken returns the preconfigured token for an exact cache key or a
// missing-token error. The fixture does not derive provider identity or perform
// upstream refresh during this lookup.
func (c *stubQuotaTokenCache) GetAccessToken(_ context.Context, key string) (string, error) {
	if t, ok := c.tokens[key]; ok {
		return t, nil
	}
	return "", errors.New("token not found")
}

// SetAccessToken accepts token writes without changing the preconfigured
// fixture map.
func (c *stubQuotaTokenCache) SetAccessToken(_ context.Context, _ string, _ string, _ time.Duration) error {
	return nil
}

// DeleteAccessToken accepts invalidation without changing the preconfigured
// token fixture map.
func (c *stubQuotaTokenCache) DeleteAccessToken(_ context.Context, _ string) error { return nil }

// AcquireRefreshLock reports successful fixture admission for every requested
// cache key.
func (c *stubQuotaTokenCache) AcquireRefreshLock(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}

// ReleaseRefreshLock accepts fixture release without retaining any lock state.
func (c *stubQuotaTokenCache) ReleaseRefreshLock(_ context.Context, _ string) error { return nil }

// Do delegates quota response selection to DoWithTLS with a nil profile.
func (s *stubQuotaHTTPUpstream) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

// DoWithTLS captures the account header and selects a local-server,
// path-specific, or fallback response. Returned bodies belong to their callers,
// and this fixture is used sequentially.
//
// A redirectTarget changes the cloned URL's scheme and host, then clears its
// explicit Host so local HTTP uses the destination host. Path, query, and
// headers remain, and the body reader stays shared. Otherwise a matching scripted
// path wins, with zero scripted status selecting HTTP 200; missing scripts use
// responseBody or empty JSON. This keeps task registration and quota paths
// distinct while allowing assertion recovery to run through actual HTTP
// framing.
func (s *stubQuotaHTTPUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	s.capturedProviderID = req.Header.Get("chatgpt-account-id")
	// Clone the request and copy its URL before changing the destination, so the
	// supplied URL and Host remain unchanged while the local sender uses the same
	// body reader.
	if s.redirectTarget != nil {
		cloned := req.Clone(req.Context())
		target := *req.URL
		target.Scheme = s.redirectTarget.Scheme
		target.Host = s.redirectTarget.Host
		cloned.URL = &target
		cloned.Host = ""
		return http.DefaultClient.Do(cloned)
	}
	if s.responses != nil {
		if response, ok := s.responses[req.URL.Path]; ok {
			status := response.status
			if status == 0 {
				status = http.StatusOK
			}
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"content-type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(response.body)),
			}, nil
		}
	}
	body := s.responseBody
	if body == "" {
		body = `{}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"content-type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

// newQuotaRedirectingUpstream configures local-server sending after parsing the
// server URL. It fails the test on parse errors and retains the target for
// later request cloning that keeps original paths and headers.
func newQuotaRedirectingUpstream(t *testing.T, srv *httptest.Server) *stubQuotaHTTPUpstream {
	t.Helper()
	target, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return &stubQuotaHTTPUpstream{redirectTarget: target}
}
