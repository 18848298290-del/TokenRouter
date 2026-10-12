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

// GetByID returns stored records; UpdateExtra captures writes for assertions.
type sparkShadowUsageTestRepo struct {
	providercore.OAuthUsageReader
	providers     map[int64]*providercore.Record
	updateExtraCh chan map[string]any
}

type quotaReadFixture interface {
	GetProvider(context.Context, int64) (*providercore.Record, error)
}

// quotaFixture binds production quota querying, recovery, and caching to the platform client factory.
type quotaFixture struct {
	*providercore.OpenAIQuotaService
	factory *OpenAIQuotaFactory
}

type agentIdentityWSInvalidationRecorder struct{ providerIDs []int64 }

// stubQuotaProviderRepo stores multiple providers and records Extra updates for quota tests.
type stubQuotaProviderRepo struct {
	providers        map[int64]*providercore.Record
	extraUpdates     map[int64]map[string]any
	extraUpdateCalls int
	extraUpdateErr   error
}

type quotaProviderGetter interface {
	GetByID(context.Context, int64) (*providercore.Record, error)
}

type stubQuotaAdminService struct {
	repo quotaProviderGetter
}

// stubQuotaTokenCache returns configured tokens through the AccessTokenCache interface.
type stubQuotaTokenCache struct {
	tokens map[string]string
}

type stubQuotaHTTPUpstream struct {
	capturedProviderID string
	responseBody       string
	responses          map[string]stubQuotaHTTPResponse
	redirectTarget     *url.URL
}

type stubQuotaHTTPResponse struct {
	status int
	body   string
}

// TestGetOpenAIUsage_SparkShadow_WritesExtraAndReturnsNonEmptyWindows checks that parent credentials
// authenticate the request while usage is saved on the shadow record and returned as five-hour
// and seven-day windows in the same call.
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

func TestQueryUsageResetCreditCountPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		usageBody   string
		detailBody  string
		wantCount   int
		wantCredits int
		wantNil     bool
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

// TestResetCreditShadowRejected checks the structured conflict before upstream preparation.
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

// TestResetCreditAgentIdentityReusesConcurrentlyRecoveredTask reuses a task recovered by another request.
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

// TestPrepareProviderShadowResolve checks that a shadow with no account credentials uses its parent's
// identity.
//
// The cached parent token isolates credential resolution from token refresh.
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

// TestResetCreditGetByIDError_FailsClosed returns lookup errors before upstream preparation.
// Nil transport and token dependencies would expose an incorrect transition into preparation.
func TestResetCreditGetByIDError_FailsClosed(t *testing.T) {
	repo := &stubQuotaProviderRepo{providers: map[int64]*providercore.Record{}}
	svc := newQuotaForTest(stubQuotaAdminService{repo: repo}, nil, nil, nil, nil)

	_, err := svc.ResetCredit(context.Background(), 200)
	require.Error(t, err, "GetByID error must propagate; got nil")
	require.NotContains(t, err.Error(), "not configured",
		"error reached prepareProvider config-check — guard did not fail-closed; got: %v", err)
}

// TestQueryUsageShadowResolve_EndToEnd queries shadow usage with the parent account header.
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

func (r *sparkShadowUsageTestRepo) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	if acc, ok := r.providers[id]; ok {
		return acc, nil
	}
	return nil, fmt.Errorf("provider %d not found", id)
}

func (r *sparkShadowUsageTestRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if r.updateExtraCh != nil {
		copied := make(map[string]any, len(updates))
		maps.Copy(copied, updates)
		r.updateExtraCh <- copied
	}
	return nil
}

// UpdateUsageExtraIfUnchanged checks the shadow row identity before capturing usage writes.
func (r *sparkShadowUsageTestRepo) UpdateUsageExtraIfUnchanged(ctx context.Context, version providercore.UsageObservationVersion, updates map[string]any) (bool, error) {
	current := r.providers[version.ID]
	if current == nil || !reflect.DeepEqual(providercore.ObserveUsageVersion(current), version) {
		return false, nil
	}
	return true, r.UpdateExtra(ctx, version.ID, updates)
}

func newQuotaForTest(reader quotaReadFixture, transport QoderTransport, token *providercore.OpenAITokenSource, profiles *egressprovider.TLSProfiles, routers OpenAITokenRouterReader) *quotaFixture {
	factory := &OpenAIQuotaFactory{Transport: transport, Profiles: profiles, Routers: routers, Tasks: &providercore.OpenAITaskCoordinator{}}
	if proxy, ok := reader.(interface {
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

func (r *stubQuotaProviderRepo) Update(ctx context.Context, value *providercore.Record) error {
	return r.UpdateCredentials(ctx, value.ID, value.Credentials)
}

func newOpenAITokenSourceForTest(repo providercore.RefreshRepository, cache providercore.AccessTokenCache, _ *providercore.OpenAIAuthorization) *providercore.OpenAITokenSource {
	return &providercore.OpenAITokenSource{Repository: repo, Cache: cache, Metrics: &providercore.OpenAITokenMetricsStore{}, Policy: providercore.OpenAIProviderRefreshPolicy(), Debug: slog.Debug, Warn: slog.Warn}
}

func decodeAgentAssertionTask(t *testing.T, header string) string {
	t.Helper()
	encoded := strings.TrimPrefix(header, "AgentAssertion ")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var envelope struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal(decoded, &envelope))
	return envelope.TaskID
}

func (r *agentIdentityWSInvalidationRecorder) InvalidateAgentIdentityWSConnections(id int64) {
	r.providerIDs = append(r.providerIDs, id)
}

func (r *stubQuotaProviderRepo) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	acc, ok := r.providers[id]
	if !ok {
		return nil, fmt.Errorf("provider %d not found", id)
	}
	return acc, nil
}

func (s stubQuotaAdminService) GetProvider(ctx context.Context, id int64) (*providercore.Record, error) {
	return s.repo.GetByID(ctx, id)
}

func (r *stubQuotaProviderRepo) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	acc, ok := r.providers[id]
	if !ok {
		return fmt.Errorf("provider %d not found", id)
	}
	acc.Credentials = credentials
	return nil
}

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

func (c *stubQuotaTokenCache) GetAccessToken(_ context.Context, key string) (string, error) {
	if t, ok := c.tokens[key]; ok {
		return t, nil
	}
	return "", errors.New("token not found")
}

func (c *stubQuotaTokenCache) SetAccessToken(_ context.Context, _ string, _ string, _ time.Duration) error {
	return nil
}

func (c *stubQuotaTokenCache) DeleteAccessToken(_ context.Context, _ string) error { return nil }

func (c *stubQuotaTokenCache) AcquireRefreshLock(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}

func (c *stubQuotaTokenCache) ReleaseRefreshLock(_ context.Context, _ string) error { return nil }

func (s *stubQuotaHTTPUpstream) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

func (s *stubQuotaHTTPUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	s.capturedProviderID = req.Header.Get("chatgpt-account-id")
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

// newQuotaRedirectingUpstream sends requests to a local server while retaining their paths and headers.
func newQuotaRedirectingUpstream(t *testing.T, srv *httptest.Server) *stubQuotaHTTPUpstream {
	t.Helper()
	target, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return &stubQuotaHTTPUpstream{redirectTarget: target}
}
