package provider

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Grok quota display can combine billing state, active probe headers and stored
// credential tier hints. These cases exercise grok_quota_view.go's configured
// core view, checking JWT precedence, model-qualified Heavy evidence, observation
// time ordering, unknown/no-header state and authentication/access diagnostics.

// TestProviderUsageServiceGrokRefreshUsesBillingOnly refreshes the usage view
// through the billing probe callback. It checks two billing GETs, local 24-hour
// token statistics and the absence of a known billing utilization percentage.
func TestProviderUsageServiceGrokRefreshUsesBillingOnly(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(54)
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	upstream := &grokHybridUpstream{}
	usageRepo := &grokQuotaUsageLogRepo{stats: &providercore.WindowStats{Tokens: 750_000}}
	quotaService := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil, usageRepo)
	cache := providercore.NewOAuthUsageCache()
	statistics := providercore.NewLocalUsageStatistics(usageRepo, cache, providercore.LocalUsageStatisticsOptions{Now: time.Now})
	usageService := providercore.NewOAuthUsageService(nil, cache, statistics, providercore.OAuthUsageOptions{Grok: providercore.GrokUsageOptions{
		Available: func() bool { return true }, StatsAvailable: func() bool { return true },
		Build: NewGrokQuotaView().BuildUsageInfo, Enrich: EnrichUsageWithProviderError,
		Probe: func(ctx context.Context, id int64) (*providercore.GrokUsageProbe, error) {
			value, err := quotaService.ProbeBilling(ctx, id)
			if value == nil {
				return nil, err
			}
			return &providercore.GrokUsageProbe{Billing: value.Billing, LocalUsage24h: value.LocalUsage24h, LocalUsage7d: value.LocalUsage7d, LocalUsageMonthly: value.LocalUsageMonthly}, err
		},
	}})

	usage, err := usageService.GetGrokUsage(context.Background(), provider, false)
	require.NoError(t, err)
	require.NotNil(t, usage.GrokBilling)
	require.Nil(t, usage.GrokBilling.UsagePercent)
	require.NotNil(t, usage.GrokLocalUsage24h)
	require.EqualValues(t, 750_000, usage.GrokLocalUsage24h.Tokens)
	require.Equal(t, 1, usageRepo.calls)
	require.Len(t, usageRepo.startTimes, 1)
	// Allow one second for the query and assertion clocks to observe the window.
	require.WithinDuration(t, time.Now().UTC().Add(-24*time.Hour), usageRepo.startTimes[0], time.Second)

	requests, _ := upstream.snapshot()
	require.Len(t, requests, 2)
	for _, req := range requests {
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "/v1/billing", req.URL.Path)
	}
}

// TestGrokQuotaFetcherBuildUsageInfoUnknownUntilFirstSnapshot supplies an OAuth
// record without observations. It checks passive source, free-token allowance
// and an explicit unknown-quota diagnostic before any billing or header data.
func TestGrokQuotaFetcherBuildUsageInfoUnknownUntilFirstSnapshot(t *testing.T) {
	t.Parallel()

	usage := NewGrokQuotaView().BuildUsageInfo(&providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth})
	require.Equal(t, "passive", usage.Source)
	require.Equal(t, xai.GrokFreeRolling24hTokenLimit, usage.GrokFreeTokenLimit)
	require.Equal(t, "quota_unknown", usage.ErrorCode)
	require.Contains(t, usage.Error, "unknown until billing is probed")
}

// TestGrokQuotaFetcherDoesNotTreatGrok45ResponsesWindowAsHeavy supplies ambiguous
// Pro credentials and Heavy-sized windows without a model. It checks display
// remains SuperGrok because the required model-qualified evidence is absent.
func TestGrokQuotaFetcherDoesNotTreatGrok45ResponsesWindowAsHeavy(t *testing.T) {
	t.Parallel()

	// These window limits match the Heavy evidence values used by the parser,
	// but this fixture omits its model qualification so the ambiguous Pro label
	// must remain on the ordinary SuperGrok display path.
	reqLimit, tokLimit := int64(8300), int64(53_000_000)
	fresh := time.Now().UTC().Format(time.RFC3339)
	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"subscription_tier": "SuperGrokPro",
		},
		Extra: map[string]any{
			providercore.GrokQuotaSnapshotExtraKey: &xai.QuotaSnapshot{
				Requests:          &xai.QuotaWindow{Limit: &reqLimit},
				Tokens:            &xai.QuotaWindow{Limit: &tokLimit},
				LastHeadersSeenAt: fresh,
				HeadersObserved:   true,
				UpdatedAt:         fresh,
			},
		},
	}

	usage := NewGrokQuotaView().BuildUsageInfo(provider)
	require.Equal(t, "supergrok", usage.SubscriptionTier)
}

// TestGrokQuotaFetcherUsesGrok45ResponsesWindowAsHeavy supplies fresh Heavy-sized
// request/token windows for grok-4.5 with ambiguous Pro credentials.
// It checks display resolves that qualified observation to the Heavy tier.
func TestGrokQuotaFetcherUsesGrok45ResponsesWindowAsHeavy(t *testing.T) {
	t.Parallel()

	reqLimit, tokLimit := int64(8300), int64(53_000_000)
	fresh := time.Now().UTC().Format(time.RFC3339)
	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"subscription_tier": "SuperGrokPro",
		},
		Extra: map[string]any{
			providercore.GrokQuotaSnapshotExtraKey: &xai.QuotaSnapshot{
				Model:             "grok-4.5",
				Requests:          &xai.QuotaWindow{Limit: &reqLimit},
				Tokens:            &xai.QuotaWindow{Limit: &tokLimit},
				LastHeadersSeenAt: fresh,
				HeadersObserved:   true,
				UpdatedAt:         fresh,
			},
		},
	}

	usage := NewGrokQuotaView().BuildUsageInfo(provider)
	require.Equal(t, "supergrok_heavy", usage.SubscriptionTier)
}

// TestGrokQuotaFetcherJWTBeatsAmbiguousSuperGrokProQuota supplies a JWT tier beside
// an ambiguous Pro label and request limit. It checks the live JWT result wins
// over the incomplete quota-plan evidence.
func TestGrokQuotaFetcherJWTBeatsAmbiguousSuperGrokProQuota(t *testing.T) {
	t.Parallel()

	heavyReq := int64(8300)
	fresh := time.Now().UTC().Format(time.RFC3339)
	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":      makeGrokOAuthJWT(map[string]any{"tier": 1}),
			"subscription_tier": "SuperGrokPro",
		},
		Extra: map[string]any{
			providercore.GrokQuotaSnapshotExtraKey: &xai.QuotaSnapshot{
				Requests:          &xai.QuotaWindow{Limit: &heavyReq},
				LastHeadersSeenAt: fresh,
				HeadersObserved:   true,
				UpdatedAt:         fresh,
			},
		},
	}

	usage := NewGrokQuotaView().BuildUsageInfo(provider)
	require.Equal(t, "supergrok", usage.SubscriptionTier)
}

// TestGrokQuotaFetcherPrefersLiveJWTTierOverStaleBillingPlan supplies a Free JWT
// alongside stored Heavy hints and billing. It checks both resolved and raw tier
// labels come from the JWT claim.
func TestGrokQuotaFetcherPrefersLiveJWTTierOverStaleBillingPlan(t *testing.T) {
	t.Parallel()

	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":      makeGrokOAuthJWT(map[string]any{"tier": 0}),
			"subscription_tier": "supergrok_heavy",
		},
		Extra: map[string]any{
			providercore.GrokUsageBillingExtraKey: &xai.BillingSummary{
				Plan:       "SuperGrok Heavy",
				StatusCode: http.StatusOK,
				UpdatedAt:  "2030-01-01T00:00:00Z",
			},
		},
	}

	usage := NewGrokQuotaView().BuildUsageInfo(provider)
	require.Equal(t, "free", usage.SubscriptionTier)
	require.Equal(t, "free", usage.SubscriptionTierRaw)
}

// TestGrokQuotaFetcherUsesCredentialTierWhenBillingHasNoPlan supplies trimmed
// credential tier/entitlement hints and planless billing. It checks normalized
// tier, retained raw label and trimmed entitlement while billing remains attached.
func TestGrokQuotaFetcherUsesCredentialTierWhenBillingHasNoPlan(t *testing.T) {
	t.Parallel()

	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"subscription_tier":  " FREE ",
			"entitlement_status": " active ",
		},
		Extra: map[string]any{
			providercore.GrokUsageBillingExtraKey: &xai.BillingSummary{
				PeriodType: "weekly",
				StatusCode: http.StatusOK,
				UpdatedAt:  "2030-01-01T00:00:00Z",
			},
		},
	}

	usage := NewGrokQuotaView().BuildUsageInfo(provider)

	require.NotNil(t, usage.GrokBilling)
	require.Equal(t, "free", usage.SubscriptionTier)
	require.Equal(t, "FREE", usage.SubscriptionTierRaw)
	require.Equal(t, "active", usage.GrokEntitlementStatus)
}

// TestGrokQuotaFetcherBuildUsageInfoFromSnapshot supplies request/token windows
// and a 429 observation with timestamps. It checks request-window values, retry timing,
// tier/entitlement display, recorded status and the parsed update instant.
func TestGrokQuotaFetcherBuildUsageInfoFromSnapshot(t *testing.T) {
	t.Parallel()

	updatedAt := "2030-01-01T00:00:00Z"
	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Extra: map[string]any{
			providercore.GrokQuotaSnapshotExtraKey: &xai.QuotaSnapshot{
				Requests: &xai.QuotaWindow{
					Limit:     grokInt64PtrForTest(100),
					Remaining: grokInt64PtrForTest(12),
					ResetAt:   updatedAt,
				},
				Tokens: &xai.QuotaWindow{
					Limit:     grokInt64PtrForTest(1000),
					Remaining: grokInt64PtrForTest(900),
				},
				RetryAfterSeconds: grokIntPtrForTest(30),
				SubscriptionTier:  "supergrok",
				EntitlementStatus: "active",
				StatusCode:        http.StatusTooManyRequests,
				LastProbeAt:       updatedAt,
				LastHeadersSeenAt: updatedAt,
				UpdatedAt:         updatedAt,
			},
		},
	}

	usage := NewGrokQuotaView().BuildUsageInfo(provider)
	require.Equal(t, "passive", usage.Source)
	require.Equal(t, "rate_limited", usage.ErrorCode)
	require.Equal(t, "observed", usage.GrokQuotaSnapshotState)
	require.Equal(t, "supergrok", usage.SubscriptionTier)
	require.Equal(t, "active", usage.GrokEntitlementStatus)
	require.Equal(t, int64(100), *usage.GrokRequestQuota.Limit)
	require.Equal(t, int64(12), *usage.GrokRequestQuota.Remaining)
	require.Equal(t, 30, *usage.GrokRetryAfterSeconds)
	require.NotNil(t, usage.UpdatedAt)
	require.Equal(t, updatedAt, usage.GrokLastQuotaProbeAt)
	require.Equal(t, updatedAt, usage.GrokLastHeadersSeenAt)
	require.Equal(t, http.StatusTooManyRequests, usage.GrokLastStatusCode)
	require.True(t, usage.UpdatedAt.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
}

// TestGrokQuotaFetcherSnapshotErrorOverridesSuccessfulBillingStatus combines
// successful billing with a rate-limited quota snapshot. It checks the display
// reports the snapshot failure category and HTTP status for that observation.
func TestGrokQuotaFetcherSnapshotErrorOverridesSuccessfulBillingStatus(t *testing.T) {
	t.Parallel()

	updatedAt := "2030-01-01T00:00:00Z"
	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Extra: map[string]any{
			providercore.GrokUsageBillingExtraKey: &xai.BillingSummary{
				PeriodType: "weekly",
				StatusCode: http.StatusOK,
				UpdatedAt:  updatedAt,
			},
			providercore.GrokQuotaSnapshotExtraKey: &xai.QuotaSnapshot{
				StatusCode: http.StatusTooManyRequests,
				UpdatedAt:  updatedAt,
			},
		},
	}

	usage := NewGrokQuotaView().BuildUsageInfo(provider)

	require.Equal(t, "rate_limited", usage.ErrorCode)
	require.Equal(t, http.StatusTooManyRequests, usage.GrokLastStatusCode)
}

// TestGrokQuotaFetcherNewerSuccessfulActiveProbeClearsBillingForbidden combines
// a later successful active probe with forbidden billing. It checks cleared
// access/error hints and the newer probe status and timestamp in display.
func TestGrokQuotaFetcherNewerSuccessfulActiveProbeClearsBillingForbidden(t *testing.T) {
	t.Parallel()

	billingAt := "2030-01-01T00:00:00Z"
	probeAt := "2030-01-01T00:05:00Z"
	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"entitlement_status": "forbidden",
		},
		Extra: map[string]any{
			providercore.GrokUsageBillingExtraKey: &xai.BillingSummary{
				StatusCode: http.StatusForbidden,
				UpdatedAt:  billingAt,
			},
			providercore.GrokQuotaSnapshotExtraKey: &xai.QuotaSnapshot{
				StatusCode:        http.StatusOK,
				ObservationSource: "active_probe",
				LastProbeAt:       probeAt,
				UpdatedAt:         probeAt,
			},
		},
	}

	usage := NewGrokQuotaView().BuildUsageInfo(provider)

	require.False(t, usage.IsForbidden)
	require.Empty(t, usage.ForbiddenType)
	require.Empty(t, usage.ErrorCode)
	require.Empty(t, usage.GrokEntitlementStatus)
	require.Equal(t, http.StatusOK, usage.GrokLastStatusCode)
	require.Equal(t, probeAt, usage.GrokLastQuotaProbeAt)
	require.NotNil(t, usage.UpdatedAt)
	require.True(t, usage.UpdatedAt.Equal(time.Date(2030, 1, 1, 0, 5, 0, 0, time.UTC)))
}

// TestGrokQuotaFetcherSameSecondSuccessfulActiveProbeClearsBillingForbidden uses
// equal billing and successful active-probe timestamps. It checks that second
// precision permits recovery and the display retains the probe's successful status.
func TestGrokQuotaFetcherSameSecondSuccessfulActiveProbeClearsBillingForbidden(t *testing.T) {
	t.Parallel()

	observedAt := "2030-01-01T00:05:00Z"
	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Extra: map[string]any{
			providercore.GrokUsageBillingExtraKey: &xai.BillingSummary{
				StatusCode: http.StatusForbidden,
				UpdatedAt:  observedAt,
			},
			providercore.GrokQuotaSnapshotExtraKey: &xai.QuotaSnapshot{
				StatusCode:        http.StatusOK,
				ObservationSource: "active_probe",
				LastProbeAt:       observedAt,
				UpdatedAt:         observedAt,
			},
		},
	}

	usage := NewGrokQuotaView().BuildUsageInfo(provider)

	require.False(t, usage.IsForbidden)
	require.Empty(t, usage.ForbiddenType)
	require.Empty(t, usage.ErrorCode)
	require.Equal(t, http.StatusOK, usage.GrokLastStatusCode)
}

// TestGrokQuotaFetcherDoesNotClearBillingForbiddenWithoutNewerSuccessfulActiveProbe
// supplies older active, newer passive and newer failed probes.
// It checks all three leave billing access rejection and its forbidden display
// category in place.
func TestGrokQuotaFetcherDoesNotClearBillingForbiddenWithoutNewerSuccessfulActiveProbe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string            // subtest display label
		snapshot xai.QuotaSnapshot // candidate quota observation data
	}{
		{
			name: "older active probe",
			snapshot: xai.QuotaSnapshot{
				StatusCode:        http.StatusOK,
				ObservationSource: "active_probe",
				LastProbeAt:       "2030-01-01T00:04:59Z",
				UpdatedAt:         "2030-01-01T00:04:59Z",
			},
		},
		{
			name: "newer passive response",
			snapshot: xai.QuotaSnapshot{
				StatusCode:        http.StatusOK,
				ObservationSource: "upstream_response",
				UpdatedAt:         "2030-01-01T00:05:01Z",
			},
		},
		{
			name: "newer failed active probe",
			snapshot: xai.QuotaSnapshot{
				StatusCode:        http.StatusTooManyRequests,
				ObservationSource: "active_probe",
				LastProbeAt:       "2030-01-01T00:05:01Z",
				UpdatedAt:         "2030-01-01T00:05:01Z",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := &providercore.Record{
				Platform: capability.PlatformGrok,
				Type:     capability.ProviderTypeOAuth,
				Extra: map[string]any{
					providercore.GrokUsageBillingExtraKey: &xai.BillingSummary{
						StatusCode: http.StatusForbidden,
						UpdatedAt:  "2030-01-01T00:05:00Z",
					},
					providercore.GrokQuotaSnapshotExtraKey: tt.snapshot,
				},
			}

			usage := NewGrokQuotaView().BuildUsageInfo(provider)

			require.True(t, usage.IsForbidden)
			require.Equal(t, "forbidden", usage.ForbiddenType)
			require.Equal(t, "forbidden", usage.ErrorCode)
		})
	}
}

// TestGrokQuotaFetcherBuildUsageInfoFromNoHeadersProbe supplies a successful active
// observation with no quota headers. It checks the no_headers state, retained
// probe timestamp/status and absent request/token windows rather than inferred
// quota.
func TestGrokQuotaFetcherBuildUsageInfoFromNoHeadersProbe(t *testing.T) {
	t.Parallel()

	probedAt := "2030-01-01T00:00:00Z"
	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Extra: map[string]any{
			providercore.GrokQuotaSnapshotExtraKey: xai.QuotaSnapshot{
				StatusCode:        http.StatusOK,
				HeadersObserved:   false,
				ObservationSource: "active_probe",
				LastProbeAt:       probedAt,
				UpdatedAt:         probedAt,
			},
		},
	}

	usage := NewGrokQuotaView().BuildUsageInfo(provider)
	require.Equal(t, "quota_unknown", usage.ErrorCode)
	require.Equal(t, "no_headers", usage.GrokQuotaSnapshotState)
	require.Contains(t, usage.Error, "No xAI quota headers observed")
	require.Equal(t, probedAt, usage.GrokLastQuotaProbeAt)
	require.Empty(t, usage.GrokLastHeadersSeenAt)
	require.Equal(t, http.StatusOK, usage.GrokLastStatusCode)
	require.Nil(t, usage.GrokRequestQuota)
	require.Nil(t, usage.GrokTokenQuota)
}

// TestGrokQuotaFetcherClassifiesForbiddenAndReauth supplies observed 401 and 403
// snapshots. It checks the distinct reauthorization/access flags, stable error
// categories and forbidden entitlement hint returned to the provider display.
func TestGrokQuotaFetcherClassifiesForbiddenAndReauth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string // subtest display label
		statusCode  int    // observed upstream HTTP status
		wantReauth  bool   // expected reauthorization display flag
		wantForbid  bool   // expected access-rejection display flag
		wantCode    string // expected usage error category
		wantEntitle string // expected entitlement display hint
	}{
		{name: "reauth", statusCode: http.StatusUnauthorized, wantReauth: true, wantCode: "unauthenticated"},
		{name: "forbidden", statusCode: http.StatusForbidden, wantForbid: true, wantCode: "forbidden", wantEntitle: "forbidden"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := &providercore.Record{
				Platform: capability.PlatformGrok,
				Type:     capability.ProviderTypeOAuth,
				Extra: map[string]any{
					providercore.GrokQuotaSnapshotExtraKey: xai.QuotaSnapshot{
						StatusCode:      tt.statusCode,
						HeadersObserved: true,
						UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
					},
				},
			}
			usage := NewGrokQuotaView().BuildUsageInfo(provider)
			require.Equal(t, tt.wantReauth, usage.NeedsReauth)
			require.Equal(t, tt.wantForbid, usage.IsForbidden)
			require.Equal(t, tt.wantCode, usage.ErrorCode)
			require.Equal(t, tt.wantEntitle, usage.GrokEntitlementStatus)
		})
	}
}

// grokInt64PtrForTest returns a pointer holding the supplied 64-bit integer
// fixture value.
func grokInt64PtrForTest(v int64) *int64 { return &v }

// grokIntPtrForTest returns a pointer holding the supplied integer quota
// fixture value.
func grokIntPtrForTest(v int) *int { return &v }
