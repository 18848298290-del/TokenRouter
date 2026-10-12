package provider

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// response429Clock lets tests advance provider runtime time.
type response429Clock struct{ nanos atomic.Int64 }

func TestOpenAI429FastPath_BlocksOAuthOnlyAfterRetryWindow(t *testing.T) {
	svc := &OpenAIResponseHealth{Runtime: providercore.NewRuntimeBlockState(time.Now)}
	provider := &providercore.Record{LoadLocation: time.LoadLocation, ID: 420, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
	expireResponseRetryForTest(svc, provider.ID)

	svc.markOAuth429(context.Background(), provider, http.Header{}, nil)

	require.True(t, svc.Runtime.Blocked(provider.ID, func() string { return providercore.RefreshCredentialIdentity(provider) }))
	require.False(t, svc.RetryOAuth429(provider, http.StatusTooManyRequests, false, nil, nil))
}

func TestOpenAIHTTP429StillUsesQuotaResetHeaders(t *testing.T) {
	svc := &OpenAIResponseHealth{Runtime: providercore.NewRuntimeBlockState(time.Now), Health: &UpstreamHealth{Core: providercore.NewHealthService(nil, nil, providercore.HealthOptions{Now: time.Now})}}
	provider := &providercore.Record{LoadLocation: time.LoadLocation, ID: 422, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
	clock := expireResponseRetryForTest(svc, provider.ID)
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "37")
	headers.Set("x-codex-primary-reset-after-seconds", "604800")
	headers.Set("x-codex-primary-window-minutes", "10080")

	svc.markOAuth429(context.Background(), provider, headers, nil)

	clock.Set(time.Now().Add(6 * 24 * time.Hour))
	require.True(t, svc.Runtime.Blocked(provider.ID, func() string { return providercore.RefreshCredentialIdentity(provider) }), "HTTP 429 保留上游配额重置边界")
}

// TestOpenAI429FastPath_SkipsSparkShadow keeps global /responses quota signals
// from pausing a Spark shadow until the global reset, which could exhaust a single-shadow pool.
func TestOpenAI429FastPath_SkipsSparkShadow(t *testing.T) {
	svc := &OpenAIResponseHealth{Runtime: providercore.NewRuntimeBlockState(time.Now)}
	parentID := int64(800)
	shadow := &providercore.Record{
		LoadLocation: time.LoadLocation, ID: 801,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &parentID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
	}
	normal := &providercore.Record{LoadLocation: time.LoadLocation, ID: 802, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}

	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "18000")
	headers.Set("x-codex-primary-window-minutes", "300")

	svc.markOAuth429(context.Background(), shadow, headers, nil)
	svc.markOAuth429(context.Background(), normal, headers, nil)

	require.False(t, svc.Runtime.Blocked(shadow.ID, func() string { return providercore.RefreshCredentialIdentity(shadow) }), "spark shadow must not be runtime-blocked by /responses global 429")
	require.True(t, svc.Runtime.Blocked(normal.ID, func() string { return providercore.RefreshCredentialIdentity(normal) }), "normal OpenAI OAuth provider with an exhausted 5h window must be paused")
}

func (c *response429Clock) Now() time.Time {
	if n := c.nanos.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Now()
}

func (c *response429Clock) Set(now time.Time) { c.nanos.Store(now.UnixNano()) }

func expireResponseRetryForTest(h *OpenAIResponseHealth, id int64) *response429Clock {
	clock := &response429Clock{}
	h.Runtime = providercore.NewRuntimeBlockState(clock.Now)
	clock.Set(time.Now().Add(-providercore.RuntimeRetryWindow - time.Second))
	h.Runtime.RetryWindowActive(id)
	clock.nanos.Store(0)
	return clock
}
