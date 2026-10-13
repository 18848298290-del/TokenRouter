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

// response429Clock supplies an atomically adjustable clock for runtime 429
// recovery tests.
type response429Clock struct {
	// nanos uses zero to select the real clock. Nonzero values provide the
	// runtime state with a controlled test timestamp.
	nanos atomic.Int64 // Unix nanoseconds
}

// TestOpenAI429FastPath_BlocksOAuthOnlyAfterRetryWindow expires an OAuth
// recovery window before a transient 429 observation. The fast path must
// install a runtime block and deny another retry.
func TestOpenAI429FastPath_BlocksOAuthOnlyAfterRetryWindow(t *testing.T) {
	svc := &OpenAIResponseHealth{Runtime: providercore.NewRuntimeBlockState(time.Now)}
	provider := &providercore.Record{LoadLocation: time.LoadLocation, ID: 420, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
	expireResponseRetryForTest(svc, provider.ID)

	svc.markOAuth429(context.Background(), provider, http.Header{}, nil)

	require.True(t, svc.Runtime.Blocked(provider.ID, func() string { return providercore.RefreshCredentialIdentity(provider) }))
	require.False(t, svc.RetryOAuth429(provider, http.StatusTooManyRequests, false, nil, nil))
}

// TestOpenAIHTTP429StillUsesQuotaResetHeaders observes a nonexhausted weekly
// window with a reported reset after retry expiry. The runtime block must
// remain active six days later under that header deadline.
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

// TestOpenAI429FastPath_SkipsSparkShadow compares a Spark shadow and ordinary
// OAuth record under exhausted global quota headers. The ordinary provider must
// receive a runtime block while the shadow's provider block remains absent.
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

// Now returns the stored test time or the real clock when its nanosecond value
// is zero. Atomic loading permits runtime queries to read a time supplied by
// Set.
func (c *response429Clock) Now() time.Time {
	if n := c.nanos.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Now()
}

// Set atomically publishes the supplied time as Unix nanoseconds.
func (c *response429Clock) Set(now time.Time) { c.nanos.Store(now.UnixNano()) }

// expireResponseRetryForTest replaces runtime state and seeds a retry window
// beyond its allowed age. It then restores real-time reads through the clock's
// zero sentinel while returning the clock for later advancement.
func expireResponseRetryForTest(h *OpenAIResponseHealth, id int64) *response429Clock {
	clock := &response429Clock{}
	h.Runtime = providercore.NewRuntimeBlockState(clock.Now)
	// Seed admission before the allowed recovery interval so markOAuth429 observes
	// an expired window; reset the clock sentinel afterward to keep normal reads
	// on wall time.
	clock.Set(time.Now().Add(-providercore.RuntimeRetryWindow - time.Second))
	h.Runtime.RetryWindowActive(id)
	clock.nanos.Store(0)
	return clock
}
