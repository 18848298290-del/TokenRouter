package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

type qoderUsageHTTPUpstreamStub struct {
	req         *http.Request
	statusCode  int
	statusCodes []int
	body        string
	bodies      []string
	calls       int32
}

type qoderObservationIdentityUpstream struct {
	qoderUsageHTTPUpstreamStub
	beforeReturn func()
}

func TestProviderUsageService_QoderUsageFetchesQuotaAndPersistsSnapshot(t *testing.T) {
	t.Parallel()

	upstream := &qoderUsageHTTPUpstreamStub{body: `{
		"userType":"teams",
		"usageType":"credits",
		"totalUsagePercentage":0.125,
		"isQuotaExceeded":false,
		"expiresAt":1783875207000,
		"userQuota":{"total":2940,"used":2,"remaining":2938,"percentage":0.01,"unit":"credits"}
	}`}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:          1,
			Platform:    capability.PlatformQoder,
			Type:        capability.ProviderTypeCosy,
			Credentials: qoderUsageCredentials("sec-token"),
		}}},
		updateExtraCh: make(chan map[string]any, 1),
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{
		providerRepo: repo,
		cache:        providercore.NewOAuthUsageCache(),
		httpUpstream: upstream,
	})

	usage, err := svc.GetUsage(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.QoderQuota == nil || usage.QoderQuota.UserQuota == nil {
		t.Fatalf("expected qoder quota in usage: %#v", usage)
	}
	if usage.QoderQuota.UserType != "teams" {
		t.Fatalf("UserType = %q, want teams", usage.QoderQuota.UserType)
	}
	if usage.QoderQuota.UserQuota.Remaining != 2938 {
		t.Fatalf("remaining = %v, want 2938", usage.QoderQuota.UserQuota.Remaining)
	}
	if usage.QoderQuota.TotalUsagePercentage != 12.5 {
		t.Fatalf("total percentage = %v, want 12.5", usage.QoderQuota.TotalUsagePercentage)
	}
	if usage.QoderQuota.UserQuota.Percentage != 1 {
		t.Fatalf("user quota percentage = %v, want 1", usage.QoderQuota.UserQuota.Percentage)
	}
	if got := upstream.req.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer COSY.") {
		t.Fatalf("Authorization = %q, want signed COSY bearer", got)
	}
	select {
	case updates := <-repo.updateExtraCh:
		if updates[providercore.QoderUsageQuotaSnapshotExtraKey] == nil {
			t.Fatalf("expected qoder quota snapshot update: %#v", updates)
		}
	case <-time.After(time.Second):
		t.Fatal("expected UpdateExtra call")
	}
}

func TestProviderUsageService_QoderCNQuotaUsesSignedGatewayQueryAndParsesExtensions(t *testing.T) {
	upstream := &qoderUsageHTTPUpstreamStub{body: `{
		"userId":"user-cn",
		"userType":"enterprise_standard",
		"usageType":"credits",
		"isPlanQuotaProrated":true,
		"expiresAt":"1783875207000",
		"addCreditsUrl":"https://qoder.com.cn/credits",
		"orgResourcePackage":{"organizationId":"org-cn","cap":100,"used":20,"remaining":80,"available":true}
	}`}
	credentials := qoderUsageCredentials("cosy-cn")
	credentials["site"] = "cn"
	credentials["refresh_mode"] = qoder.RefreshModeQoderCN20
	credentials["organization_id"] = "org-cn"
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:          2,
			Platform:    capability.PlatformQoder,
			Type:        capability.ProviderTypeCosy,
			Credentials: credentials,
		}}},
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	usage, err := svc.GetUsage(context.Background(), 2)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if upstream.req == nil {
		t.Fatal("expected signed quota request")
	}
	if upstream.req.URL.Path != "/algo"+qoder.QuotaUsagePath {
		t.Fatalf("quota path = %q", upstream.req.URL.Path)
	}
	if upstream.req.URL.Query().Get("orgId") != "org-cn" || upstream.req.URL.Query().Has("quotaKey") {
		t.Fatalf("quota query = %q", upstream.req.URL.RawQuery)
	}
	if upstream.req.Header.Get("Cosy-Version") != qoder.CNClientVersion {
		t.Fatalf("Cosy-Version = %q", upstream.req.Header.Get("Cosy-Version"))
	}
	if usage.QoderQuota == nil || usage.QoderQuota.OrgResourcePackage == nil {
		t.Fatalf("expected CN quota extensions: %#v", usage.QoderQuota)
	}
	if usage.QoderQuota.UserID != "user-cn" || !usage.QoderQuota.IsPlanQuotaProrated {
		t.Fatalf("unexpected CN quota metadata: %#v", usage.QoderQuota)
	}
	if usage.QoderQuota.AddCreditsURL != "https://qoder.com.cn/credits" || usage.QoderQuota.OrgResourcePackage.OrganizationID != "org-cn" {
		t.Fatalf("unexpected CN quota extension fields: %#v", usage.QoderQuota)
	}
}

func TestProviderUsageService_QoderCNLegacyQuotaUsesBearerToken(t *testing.T) {
	upstream := &qoderUsageHTTPUpstreamStub{body: `{
		"userType":"teams",
		"usageType":"credits",
		"isQuotaExceeded":false,
		"expiresAt":1783875207000,
		"userQuota":{"total":100,"used":20,"remaining":80,"percentage":20,"unit":"credits"}
	}`}
	credentials := qoderUsageCredentials("legacy-cn-token")
	credentials["site"] = "cn"
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:          20,
			Platform:    capability.PlatformQoder,
			Type:        capability.ProviderTypeCosy,
			Credentials: credentials,
		}}},
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	usage, err := svc.GetUsage(context.Background(), 20)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if upstream.req == nil {
		t.Fatal("expected bearer quota request")
	}
	if got := upstream.req.Header.Get("Authorization"); got != "Bearer legacy-cn-token" {
		t.Fatalf("Authorization = %q, want legacy bearer token", got)
	}
	if upstream.req.Header.Get("Cosy-Key") != "" || upstream.req.Header.Get("Cosy-Date") != "" {
		t.Fatalf("legacy bearer request unexpectedly contains COSY signature headers: %#v", upstream.req.Header)
	}
	if upstream.req.URL.Query().Get("orgId") != "org-usage" || upstream.req.URL.Query().Has("quotaKey") {
		t.Fatalf("quota query = %q", upstream.req.URL.RawQuery)
	}
	if usage.QoderQuota == nil || usage.QoderQuota.UserQuota == nil || usage.QoderQuota.UserQuota.Remaining != 80 {
		t.Fatalf("unexpected qoder quota: %#v", usage.QoderQuota)
	}
}

func TestProviderUsageService_QoderUsagePrefersPATBootstrapOverStoredSecurityToken(t *testing.T) {
	t.Parallel()

	upstream := &qoderUsageHTTPUpstreamStub{bodies: []string{
		`{"id":"user-1","name":"Qoder User","userType":"teams","securityOauthToken":"fresh-token","refreshToken":"refresh-1"}`,
		`{
			"userType":"teams",
			"usageType":"credits",
			"totalUsagePercentage":1,
			"isQuotaExceeded":false,
			"expiresAt":1783875207000,
			"userQuota":{"total":100,"used":1,"remaining":99,"percentage":1,"unit":"credits"}
		}`,
	}}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:       5,
			Platform: capability.PlatformQoder,
			Type:     capability.ProviderTypeCosy,
			Credentials: map[string]any{
				"pat":                  "pat-token",
				"security_oauth_token": "stale-token",
				"machine_id":           "machine-1",
				"machine_token":        "machine-token",
				"machine_type":         "5",
				"organization_id":      "org-test",
			},
		}}},
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	usage, err := svc.GetUsage(context.Background(), 5)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.QoderQuota == nil || usage.QoderQuota.UserQuota == nil || usage.QoderQuota.UserQuota.Used != 1 {
		t.Fatalf("unexpected qoder quota: %#v", usage.QoderQuota)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 2 {
		t.Fatalf("upstream calls = %d, want PAT exchange + quota usage", got)
	}
	if got := upstream.req.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer COSY.") {
		t.Fatalf("quota Authorization = %q, want signed COSY bearer", got)
	}
}

func TestProviderUsageService_QoderUsageDoesNotReuseStoredTokenWhenPATBootstrapFails(t *testing.T) {
	t.Parallel()

	upstream := &qoderUsageHTTPUpstreamStub{
		statusCodes: []int{http.StatusInternalServerError, http.StatusOK},
		bodies: []string{
			`{"message":"pat unavailable","securityOauthToken":"leaked"}`,
			`{
				"userType":"teams",
				"usageType":"credits",
				"totalUsagePercentage":3,
				"isQuotaExceeded":false,
				"expiresAt":1783875207000,
				"userQuota":{"total":100,"used":3,"remaining":97,"percentage":3,"unit":"credits"}
			}`,
		},
	}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:       6,
			Platform: capability.PlatformQoder,
			Type:     capability.ProviderTypeCosy,
			Credentials: map[string]any{
				"pat":                  "pat-token",
				"security_oauth_token": "stored-token",
				"machine_id":           "machine-1",
				"machine_token":        "machine-token",
				"machine_type":         "5",
				"organization_id":      "org-test",
			},
		}}},
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	usage, err := svc.GetUsage(context.Background(), 6)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.QoderQuota != nil || usage.Error == "" {
		t.Fatalf("expected degraded usage after PAT exchange failure: %#v", usage)
	}
	if strings.Contains(usage.Error, "leaked") {
		t.Fatalf("degraded usage leaked upstream credential: %q", usage.Error)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 1 {
		t.Fatalf("upstream calls = %d, want only failed PAT exchange", got)
	}
}

func TestProviderUsageService_QoderCNPATRebuildsSessionAfterAuthenticationFailure(t *testing.T) {
	upstream := &qoderUsageHTTPUpstreamStub{
		statusCodes: []int{http.StatusUnauthorized, http.StatusOK},
		bodies: []string{
			`{"code":"401","message":"expired"}`,
			`{
				"userType":"teams",
				"usageType":"credits",
				"totalUsagePercentage":2,
				"isQuotaExceeded":false,
				"expiresAt":1783875207000,
				"userQuota":{"total":100,"used":2,"remaining":98,"percentage":2,"unit":"credits"}
			}`,
		},
	}
	exchangeCalls := 0
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{ExchangeCNPAT: func(_ context.Context, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		exchangeCalls++
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "uid-cn",
			OrganizationID:     "org-cn",
			SecurityOauthToken: fmt.Sprintf("cosy-cn-%d", exchangeCalls),
			RefreshToken:       "refresh-cn",
		}, time.Now().Add(time.Hour), nil
	}})
	provider := providercore.Record{
		ID:       7,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"site":          "cn",
			"pat":           "cn-pat",
			"machine_id":    "machine-cn",
			"machine_token": "machine-token-cn",
			"machine_type":  "5",
		},
	}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{provider}},
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{
		providerRepo:         repo,
		cache:                providercore.NewOAuthUsageCache(),
		httpUpstream:         upstream,
		qoderSessionProvider: tokenSource,
	})

	usage, err := svc.GetUsage(context.Background(), provider.ID)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.QoderQuota == nil || usage.QoderQuota.UserQuota == nil || usage.QoderQuota.UserQuota.Used != 2 {
		t.Fatalf("unexpected qoder quota after PAT session rebuild: %#v", usage)
	}
	if exchangeCalls != 2 {
		t.Fatalf("PAT exchange calls = %d, want 2", exchangeCalls)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 2 {
		t.Fatalf("quota calls = %d, want 2", got)
	}
}

func TestProviderUsageService_QoderUsageForceBypassesCache(t *testing.T) {
	t.Parallel()

	upstream := &qoderUsageHTTPUpstreamStub{bodies: []string{
		`{
			"userType":"teams",
			"usageType":"credits",
			"totalUsagePercentage":1,
			"isQuotaExceeded":false,
			"expiresAt":1783875207000,
			"userQuota":{"total":100,"used":1,"remaining":99,"percentage":1,"unit":"credits"}
		}`,
		`{
			"userType":"teams",
			"usageType":"credits",
			"totalUsagePercentage":2,
			"isQuotaExceeded":false,
			"expiresAt":1783875207000,
			"userQuota":{"total":100,"used":2,"remaining":98,"percentage":2,"unit":"credits"}
		}`,
	}}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:          4,
			Platform:    capability.PlatformQoder,
			Type:        capability.ProviderTypeCosy,
			Credentials: qoderUsageCredentials("sec-token"),
		}}},
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	first, err := svc.GetUsage(context.Background(), 4)
	if err != nil {
		t.Fatalf("first GetUsage() error = %v", err)
	}
	if first.QoderQuota == nil || first.QoderQuota.UserQuota == nil || first.QoderQuota.UserQuota.Used != 1 {
		t.Fatalf("first used = %#v, want 1", first.QoderQuota)
	}

	cached, err := svc.GetUsage(context.Background(), 4)
	if err != nil {
		t.Fatalf("cached GetUsage() error = %v", err)
	}
	if cached.QoderQuota == nil || cached.QoderQuota.UserQuota == nil || cached.QoderQuota.UserQuota.Used != 1 {
		t.Fatalf("cached used = %#v, want cached 1", cached.QoderQuota)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 1 {
		t.Fatalf("calls after cached request = %d, want 1", got)
	}

	forced, err := svc.GetUsage(context.Background(), 4, true)
	if err != nil {
		t.Fatalf("forced GetUsage() error = %v", err)
	}
	if forced.QoderQuota == nil || forced.QoderQuota.UserQuota == nil || forced.QoderQuota.UserQuota.Used != 2 {
		t.Fatalf("forced used = %#v, want 2", forced.QoderQuota)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 2 {
		t.Fatalf("calls after forced request = %d, want 2", got)
	}
}

func TestProviderUsageService_QoderQuotaExceededSetsRateLimited(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	upstream := &qoderUsageHTTPUpstreamStub{body: fmt.Sprintf(`{
		"userType":"teams",
		"usageType":"credits",
		"totalUsagePercentage":100,
		"isQuotaExceeded":true,
		"expiresAt":%d,
		"userQuota":{"total":100,"used":100,"remaining":0,"percentage":100,"unit":"credits"}
	}`, expiresAt)}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:          2,
			Platform:    capability.PlatformQoder,
			Type:        capability.ProviderTypeCosy,
			Credentials: qoderUsageCredentials("sec-token"),
		}}},
		updateExtraCh: make(chan map[string]any, 1),
		rateLimitCh:   make(chan time.Time, 1),
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	usage, err := svc.GetUsage(context.Background(), 2)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.QoderQuota == nil || !usage.QoderQuota.IsQuotaExceeded {
		t.Fatalf("expected exceeded qoder quota: %#v", usage.QoderQuota)
	}
	select {
	case resetAt := <-repo.rateLimitCh:
		if resetAt.UnixMilli() != expiresAt {
			t.Fatalf("resetAt = %d, want %d", resetAt.UnixMilli(), expiresAt)
		}
	case <-time.After(time.Second):
		t.Fatal("expected SetRateLimited call")
	}
}

func TestProviderUsageService_QoderAddOnQuotaRemainingPreventsUserQuotaRateLimit(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	upstream := &qoderUsageHTTPUpstreamStub{body: fmt.Sprintf(`{
		"userType":"teams",
		"usageType":"credits",
		"totalUsagePercentage":90,
		"isQuotaExceeded":false,
		"expiresAt":%d,
		"userQuota":{"total":100,"used":100,"remaining":0,"percentage":100,"unit":"credits"},
		"addOnQuota":{"total":50,"used":10,"remaining":40,"percentage":20,"unit":"credits","detailUrl":"https://qoder.example/addon"}
	}`, expiresAt.UnixMilli())}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:               12,
			Platform:         capability.PlatformQoder,
			Type:             capability.ProviderTypeCosy,
			Credentials:      qoderUsageCredentials("sec-token"),
			RateLimitResetAt: &expiresAt,
		}}},
		rateLimitCh:  make(chan time.Time, 1),
		clearLimitCh: make(chan int64, 1),
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	usage, err := svc.GetUsage(context.Background(), 12)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.QoderQuota == nil || usage.QoderQuota.AddOnQuota == nil || usage.QoderQuota.AddOnQuota.Remaining != 40 {
		t.Fatalf("expected add-on quota remaining in qoder usage, got %#v", usage.QoderQuota)
	}
	select {
	case resetAt := <-repo.rateLimitCh:
		t.Fatalf("unexpected SetRateLimited call while add-on quota remains: %v", resetAt)
	default:
	}
	select {
	case id := <-repo.clearLimitCh:
		if id != 12 {
			t.Fatalf("ClearRateLimit id = %d, want 12", id)
		}
	case <-time.After(time.Second):
		t.Fatal("expected ClearRateLimit call for matching stale quota lock")
	}
}

func TestProviderUsageService_QoderQuotaLockedProviderBypassesCachedUsage(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	upstream := &qoderUsageHTTPUpstreamStub{bodies: []string{
		fmt.Sprintf(`{
			"userType":"teams",
			"usageType":"credits",
			"totalUsagePercentage":100,
			"isQuotaExceeded":true,
			"expiresAt":%d,
			"userQuota":{"total":100,"used":100,"remaining":0,"percentage":100,"unit":"credits"}
		}`, expiresAt.UnixMilli()),
		fmt.Sprintf(`{
			"userType":"teams",
			"usageType":"credits",
			"totalUsagePercentage":50,
			"isQuotaExceeded":false,
			"expiresAt":%d,
			"userQuota":{"total":100,"used":50,"remaining":50,"percentage":50,"unit":"credits"}
		}`, expiresAt.UnixMilli()),
	}}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:          9,
			Platform:    capability.PlatformQoder,
			Type:        capability.ProviderTypeCosy,
			Credentials: qoderUsageCredentials("sec-token"),
		}}},
		rateLimitCh:  make(chan time.Time, 1),
		clearLimitCh: make(chan int64, 1),
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	first, err := svc.GetUsage(context.Background(), 9)
	if err != nil {
		t.Fatalf("first GetUsage() error = %v", err)
	}
	if first.QoderQuota == nil || !first.QoderQuota.IsQuotaExceeded {
		t.Fatalf("expected cached exceeded quota: %#v", first.QoderQuota)
	}
	select {
	case <-repo.rateLimitCh:
	case <-time.After(time.Second):
		t.Fatal("expected SetRateLimited call")
	}
	repo.providers[0].RateLimitResetAt = &expiresAt

	second, err := svc.GetUsage(context.Background(), 9)
	if err != nil {
		t.Fatalf("second GetUsage() error = %v", err)
	}
	if second.QoderQuota == nil || second.QoderQuota.IsQuotaExceeded || second.QoderQuota.UserQuota == nil || second.QoderQuota.UserQuota.Remaining != 50 {
		t.Fatalf("expected refreshed available quota, got %#v", second.QoderQuota)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 2 {
		t.Fatalf("upstream calls = %d, want 2; quota-locked provider must bypass cached exceeded usage", got)
	}
	select {
	case id := <-repo.clearLimitCh:
		if id != 9 {
			t.Fatalf("ClearRateLimit id = %d, want 9", id)
		}
	case <-time.After(time.Second):
		t.Fatal("expected ClearRateLimit call after refreshed quota became available")
	}
}

func TestProviderUsageService_QoderQuotaAvailableClearsMatchingQuotaRateLimit(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	upstream := &qoderUsageHTTPUpstreamStub{body: fmt.Sprintf(`{
		"userType":"teams",
		"usageType":"credits",
		"totalUsagePercentage":50,
		"isQuotaExceeded":false,
		"expiresAt":%d,
		"userQuota":{"total":100,"used":50,"remaining":50,"percentage":50,"unit":"credits"}
	}`, expiresAt.UnixMilli())}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:               7,
			Platform:         capability.PlatformQoder,
			Type:             capability.ProviderTypeCosy,
			Credentials:      qoderUsageCredentials("sec-token"),
			RateLimitResetAt: &expiresAt,
		}}},
		rateLimitCh:  make(chan time.Time, 1),
		clearLimitCh: make(chan int64, 1),
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	usage, err := svc.GetUsage(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.QoderQuota == nil || usage.QoderQuota.IsQuotaExceeded {
		t.Fatalf("expected available qoder quota: %#v", usage.QoderQuota)
	}
	select {
	case id := <-repo.clearLimitCh:
		if id != 7 {
			t.Fatalf("ClearRateLimit id = %d, want 7", id)
		}
	case <-time.After(time.Second):
		t.Fatal("expected ClearRateLimit call")
	}
	select {
	case resetAt := <-repo.rateLimitCh:
		t.Fatalf("unexpected SetRateLimited call: %v", resetAt)
	default:
	}
}

func TestProviderUsageService_QoderQuotaLockedProviderKeepsDegradedCache(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	upstream := &qoderUsageHTTPUpstreamStub{body: fmt.Sprintf(`{
		"userType":"teams",
		"usageType":"credits",
		"totalUsagePercentage":50,
		"isQuotaExceeded":false,
		"expiresAt":%d,
		"userQuota":{"total":100,"used":50,"remaining":50,"percentage":50,"unit":"credits"}
	}`, expiresAt.UnixMilli())}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:               11,
			Platform:         capability.PlatformQoder,
			Type:             capability.ProviderTypeCosy,
			Credentials:      qoderUsageCredentials("sec-token"),
			RateLimitResetAt: &expiresAt,
		}}},
		clearLimitCh: make(chan int64, 1),
	}
	cache := providercore.NewOAuthUsageCache()
	cache.StoreQoder(int64(11), &providercore.OAuthQoderUsageCache{
		Identity: providercore.UsageCacheIdentity(&repo.providers[0]),
		UsageInfo: &providercore.UsageInfo{
			Error:     "usage API error: temporary network error",
			ErrorCode: providercore.ErrorCodeNetworkError,
			QoderQuota: &providercore.QoderQuotaInfo{
				UserType:        "teams",
				IsQuotaExceeded: true,
				ExpiresAt:       &expiresAt,
				UserQuota:       &providercore.QoderQuotaProgress{Total: 100, Used: 100, Remaining: 0, Percentage: 100, Unit: "credits"},
			},
		},
		Timestamp: time.Now(),
	})
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: cache, httpUpstream: upstream})

	usage, err := svc.GetUsage(context.Background(), 11)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage == nil || usage.ErrorCode != providercore.ErrorCodeNetworkError {
		t.Fatalf("expected cached degraded usage, got %#v", usage)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0 while degraded cache TTL is valid", got)
	}
	select {
	case id := <-repo.clearLimitCh:
		t.Fatalf("unexpected ClearRateLimit(%d) from degraded cache", id)
	default:
	}
}

func TestProviderUsageService_QoderQuotaAvailableDoesNotClearActiveOverload(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	overloadUntil := time.Now().Add(5 * time.Minute)
	upstream := &qoderUsageHTTPUpstreamStub{body: fmt.Sprintf(`{
		"userType":"teams",
		"usageType":"credits",
		"totalUsagePercentage":20,
		"isQuotaExceeded":false,
		"expiresAt":%d,
		"userQuota":{"total":100,"used":20,"remaining":80,"percentage":20,"unit":"credits"}
	}`, expiresAt.UnixMilli())}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:               10,
			Platform:         capability.PlatformQoder,
			Type:             capability.ProviderTypeCosy,
			Credentials:      qoderUsageCredentials("sec-token"),
			RateLimitResetAt: &expiresAt,
			OverloadUntil:    &overloadUntil,
		}}},
		clearLimitCh: make(chan int64, 1),
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	usage, err := svc.GetUsage(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.QoderQuota == nil || usage.QoderQuota.IsQuotaExceeded {
		t.Fatalf("expected available qoder quota: %#v", usage.QoderQuota)
	}
	select {
	case id := <-repo.clearLimitCh:
		t.Fatalf("unexpected ClearRateLimit(%d) while active overload is present", id)
	default:
	}
}

func TestProviderUsageService_QoderQuotaAvailableDoesNotClearUnrelatedRateLimit(t *testing.T) {
	t.Parallel()

	resetAt := time.Now().Add(30 * time.Second)
	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	upstream := &qoderUsageHTTPUpstreamStub{body: fmt.Sprintf(`{
		"userType":"teams",
		"usageType":"credits",
		"totalUsagePercentage":10,
		"isQuotaExceeded":false,
		"expiresAt":%d,
		"userQuota":{"total":100,"used":10,"remaining":90,"percentage":10,"unit":"credits"}
	}`, expiresAt)}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:               8,
			Platform:         capability.PlatformQoder,
			Type:             capability.ProviderTypeCosy,
			Credentials:      qoderUsageCredentials("sec-token"),
			RateLimitResetAt: &resetAt,
		}}},
		clearLimitCh: make(chan int64, 1),
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	usage, err := svc.GetUsage(context.Background(), 8)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.QoderQuota == nil || usage.QoderQuota.IsQuotaExceeded {
		t.Fatalf("expected available qoder quota: %#v", usage.QoderQuota)
	}
	select {
	case id := <-repo.clearLimitCh:
		t.Fatalf("unexpected ClearRateLimit(%d) for unrelated reset", id)
	default:
	}
}

func TestProviderUsageService_QoderPersonalZeroQuotaDoesNotSetRateLimited(t *testing.T) {
	t.Parallel()

	upstream := &qoderUsageHTTPUpstreamStub{body: `{
		"userType":"personal_standard",
		"usageType":"credits",
		"totalUsagePercentage":0,
		"isQuotaExceeded":true,
		"expiresAt":253402214400000,
		"userQuota":{"total":0,"used":0,"remaining":0,"percentage":0,"unit":"credits"}
	}`}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:          3,
			Platform:    capability.PlatformQoder,
			Type:        capability.ProviderTypeCosy,
			Credentials: qoderUsageCredentials("sec-token"),
		}}},
		rateLimitCh: make(chan time.Time, 1),
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})

	usage, err := svc.GetUsage(context.Background(), 3)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.QoderQuota == nil || !usage.QoderQuota.IsQuotaExceeded {
		t.Fatalf("expected display-only exceeded quota: %#v", usage.QoderQuota)
	}
	select {
	case resetAt := <-repo.rateLimitCh:
		t.Fatalf("unexpected SetRateLimited call: %v", resetAt)
	default:
	}
}

func TestProviderUsageService_QoderUsageDegradedUsesLastKnownSnapshot(t *testing.T) {
	t.Parallel()

	upstream := &qoderUsageHTTPUpstreamStub{statusCode: http.StatusTooManyRequests, body: `rate limited`}
	updatedAt := time.Now().UTC().Format(time.RFC3339)
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:          1,
			Platform:    capability.PlatformQoder,
			Type:        capability.ProviderTypeCosy,
			Credentials: qoderUsageCredentials("sec-token"),
			Extra: map[string]any{
				providercore.QoderUsageQuotaUpdatedAtExtraKey: updatedAt,
				providercore.QoderUsageQuotaSnapshotExtraKey: map[string]any{
					"user_type":              "teams",
					"usage_type":             "credits",
					"total_usage_percentage": 50,
					"is_quota_exceeded":      false,
					"user_quota": map[string]any{
						"total":     10,
						"used":      5,
						"remaining": 5,
						"unit":      "credits",
					},
				},
			},
		}}},
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{
		providerRepo: repo,
		cache:        providercore.NewOAuthUsageCache(),
		httpUpstream: upstream,
	})

	usage, err := svc.GetUsage(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.ErrorCode != providercore.ErrorCodeRateLimited {
		t.Fatalf("ErrorCode = %q, want rate_limited", usage.ErrorCode)
	}
	if usage.QoderQuota == nil || !usage.QoderQuota.SnapshotFromProvider {
		t.Fatalf("expected snapshot qoder quota: %#v", usage.QoderQuota)
	}
	if usage.QoderQuota.UserQuota == nil || usage.QoderQuota.UserQuota.Used != 5 {
		t.Fatalf("unexpected snapshot quota: %#v", usage.QoderQuota.UserQuota)
	}
}

func TestProviderUsageService_QoderUsageDegradedDoesNotClearProviderError(t *testing.T) {
	t.Parallel()

	upstream := &qoderUsageHTTPUpstreamStub{statusCode: http.StatusUnauthorized, body: `unauthenticated`}
	repo := &providerUsageCodexProbeRepo{
		usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{
			ID:           1,
			Platform:     capability.PlatformQoder,
			Type:         capability.ProviderTypeCosy,
			Status:       providercore.StatusError,
			ErrorMessage: "unauthenticated",
			Credentials:  qoderUsageCredentials("sec-token"),
		}}},
		clearErrorCh: make(chan int64, 1),
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{
		providerRepo: repo,
		cache:        providercore.NewOAuthUsageCache(),
		httpUpstream: upstream,
	})

	usage, err := svc.GetUsage(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage == nil || usage.ErrorCode != providercore.ErrorCodeUnauthenticated {
		t.Fatalf("expected degraded unauthenticated usage, got %#v", usage)
	}
	select {
	case id := <-repo.clearErrorCh:
		t.Fatalf("ClearError(%d) called for degraded usage", id)
	default:
	}
}

// TestQoderUsageCacheDoesNotCrossCredentialIdentity checks that credential changes trigger a query with the new
// identity.
func TestQoderUsageCacheDoesNotCrossCredentialIdentity(t *testing.T) {
	repo := &providerUsageCodexProbeRepo{usageRecordFixture: usageRecordFixture{providers: []providercore.Record{{ID: 919, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: qoderUsageCredentials("first")}}}}
	upstream := &qoderUsageHTTPUpstreamStub{bodies: []string{`{"userType":"teams","userQuota":{"total":100,"used":1,"remaining":99}}`, `{"userType":"teams","userQuota":{"total":100,"used":2,"remaining":98}}`}}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: upstream})
	first, err := svc.GetUsage(context.Background(), 919)
	require.NoError(t, err)
	require.Equal(t, float64(1), first.QoderQuota.UserQuota.Used)
	repo.providers[0].Credentials = qoderUsageCredentials("second")
	second, err := svc.GetUsage(context.Background(), 919)
	require.NoError(t, err)
	require.Equal(t, float64(2), second.QoderQuota.UserQuota.Used)
	require.Equal(t, int32(2), upstream.calls)
}

func TestQoderObservationCannotWriteNewAdministratorIdentity(t *testing.T) {
	repo := &qoderObservationIdentityRepo{current: &providercore.Record{ID: 917, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Status: providercore.StatusActive, Credentials: qoderUsageCredentials("observed")}}
	u := &qoderObservationIdentityUpstream{qoderUsageHTTPUpstreamStub: qoderUsageHTTPUpstreamStub{body: fmt.Sprintf(`{"userType":"teams","usageType":"credits","isQuotaExceeded":true,"expiresAt":%d,"userQuota":{"total":100,"used":100,"remaining":0}}`, time.Now().Add(time.Hour).UnixMilli())}, beforeReturn: func() { repo.current.Credentials = qoderUsageCredentials("administrator") }}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, cache: providercore.NewOAuthUsageCache(), httpUpstream: u})
	_, _ = svc.GetUsage(context.Background(), 917)
	require.Zero(t, repo.writes, "旧观测不能给新凭据写入额度快照或停调")
}

func TestIsQoderAuthenticationError(t *testing.T) {
	for _, statusCode := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		err := fmt.Errorf("wrapped: %w", &qoder.APIError{StatusCode: statusCode})
		if !isQoderAuthenticationError(err) {
			t.Fatalf("status %d should be treated as authentication failure", statusCode)
		}
	}
	if isQoderAuthenticationError(&qoder.APIError{StatusCode: http.StatusInternalServerError}) {
		t.Fatal("status 500 should not trigger PAT session rebuild")
	}
}

func TestQoderQuotaInfoFromResponseInfersAddOnQuotaRemainingFromCap(t *testing.T) {
	t.Parallel()

	quota := qoderQuotaInfoFromResponse(&qoder.QuotaUsageResponse{
		UserType:        "teams",
		UsageType:       "credits",
		IsQuotaExceeded: true,
		ExpiresAt:       qoder.FlexibleInt64(time.Now().Add(time.Hour).UnixMilli()),
		UserQuota:       &qoder.QuotaProgress{Total: 100, Used: 100, Remaining: 0, Unit: "credits"},
		AddOnQuota: &qoder.QuotaProgress{
			Cap:  50,
			Used: 10,
			Unit: "credits",
		},
	}, time.Now(), false)

	if quota.AddOnQuota == nil {
		t.Fatalf("expected add-on quota: %#v", quota)
	}
	if quota.AddOnQuota.Total != 50 {
		t.Fatalf("add-on total = %v, want 50", quota.AddOnQuota.Total)
	}
	if quota.AddOnQuota.Remaining != 40 {
		t.Fatalf("add-on remaining = %v, want 40", quota.AddOnQuota.Remaining)
	}
	if _, limited := providercore.QoderQuotaRateLimitResetAt(quota, time.Now()); limited {
		t.Fatalf("add-on cap-derived remaining credits should prevent quota rate limit")
	}
}

func TestQoderQuotaInfoFromResponseInfersOrgResourcePackageTotalFromUsedRemaining(t *testing.T) {
	t.Parallel()

	quota := qoderQuotaInfoFromResponse(&qoder.QuotaUsageResponse{
		UserType:        "teams",
		UsageType:       "credits",
		IsQuotaExceeded: true,
		ExpiresAt:       qoder.FlexibleInt64(time.Now().Add(time.Hour).UnixMilli()),
		UserQuota:       &qoder.QuotaProgress{Total: 100, Used: 100, Remaining: 0, Unit: "credits"},
		OrgResourcePackage: &qoder.QuotaProgress{
			Used:      25,
			Remaining: 75,
			Unit:      "credits",
		},
	}, time.Now(), false)

	if quota.OrgResourcePackage == nil {
		t.Fatalf("expected org resource package quota: %#v", quota)
	}
	if quota.OrgResourcePackage.Total != 100 {
		t.Fatalf("org resource total = %v, want 100", quota.OrgResourcePackage.Total)
	}
	if capacity := providercore.QoderQuotaTotalCapacity(quota); capacity != 200 {
		t.Fatalf("total capacity = %v, want 200", capacity)
	}
	if remaining, ok := providercore.QoderQuotaTotalRemaining(quota); !ok || remaining != 75 {
		t.Fatalf("total remaining = (%v, %v), want (75, true)", remaining, ok)
	}
	if _, limited := providercore.QoderQuotaRateLimitResetAt(quota, time.Now()); limited {
		t.Fatalf("org resource remaining credits should prevent quota rate limit")
	}
}

func TestQoderQuotaProgressFromJSONInfersOnlyMissingRemaining(t *testing.T) {
	t.Parallel()

	var explicit qoder.QuotaUsageResponse
	if err := json.Unmarshal([]byte(`{
		"userType":"teams",
		"isQuotaExceeded":true,
		"expiresAt":4102444800000,
		"userQuota":{"total":100,"used":50,"remaining":0,"percentage":0,"unit":"credits"}
	}`), &explicit); err != nil {
		t.Fatalf("unmarshal explicit quota response: %v", err)
	}
	quota := qoderQuotaInfoFromResponse(&explicit, time.Now(), false)
	if quota == nil || quota.UserQuota == nil {
		t.Fatalf("expected user quota: %#v", quota)
	}
	if quota.UserQuota.Remaining != 0 {
		t.Fatalf("explicit remaining=0 must not be inferred to positive balance, got %v", quota.UserQuota.Remaining)
	}
	if quota.UserQuota.Percentage != 0 {
		t.Fatalf("explicit percentage=0 must be preserved, got %v", quota.UserQuota.Percentage)
	}
	if _, limited := providercore.QoderQuotaRateLimitResetAt(quota, time.Now()); !limited {
		t.Fatalf("explicit zero remaining with exceeded quota should set quota rate limit")
	}

	var missing qoder.QuotaUsageResponse
	if err := json.Unmarshal([]byte(`{
		"userType":"teams",
		"isQuotaExceeded":false,
		"expiresAt":4102444800000,
		"userQuota":{"total":100,"used":40,"unit":"credits"}
	}`), &missing); err != nil {
		t.Fatalf("unmarshal missing quota response: %v", err)
	}
	missingQuota := qoderQuotaInfoFromResponse(&missing, time.Now(), false)
	if missingQuota == nil || missingQuota.UserQuota == nil {
		t.Fatalf("expected missing user quota: %#v", missingQuota)
	}
	if missingQuota.UserQuota.Remaining != 60 {
		t.Fatalf("missing remaining should be inferred from total-used, got %v", missingQuota.UserQuota.Remaining)
	}
	if missingQuota.UserQuota.Percentage != 40 {
		t.Fatalf("missing percentage should be inferred from used/total, got %v", missingQuota.UserQuota.Percentage)
	}
}

func qoderUsageCredentials(token string) map[string]any {
	return map[string]any{
		"security_oauth_token": token,
		"machine_id":           "machine-usage",
		"machine_token":        "machine-token-usage",
		"machine_type":         "machine-type-usage",
		"uid":                  "uid-usage",
		"organization_id":      "org-usage",
	}
}

func (s *qoderUsageHTTPUpstreamStub) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

func (s *qoderUsageHTTPUpstreamStub) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	s.req = req
	call := atomic.AddInt32(&s.calls, 1)
	status := s.statusCode
	if idx := int(call) - 1; idx >= 0 && idx < len(s.statusCodes) {
		status = s.statusCodes[idx]
	}
	if status == 0 {
		status = http.StatusOK
	}
	body := s.body
	if idx := int(call) - 1; idx >= 0 && idx < len(s.bodies) {
		body = s.bodies[idx]
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (u *qoderObservationIdentityUpstream) DoWithTLS(req *http.Request, proxy string, id int64, n int, p *tlsfingerprint.Profile) (*http.Response, error) {
	resp, err := u.qoderUsageHTTPUpstreamStub.DoWithTLS(req, proxy, id, n, p)
	u.beforeReturn()
	return resp, err
}

func (u *qoderObservationIdentityUpstream) Do(req *http.Request, proxy string, id int64, n int) (*http.Response, error) {
	return u.DoWithTLS(req, proxy, id, n, nil)
}
