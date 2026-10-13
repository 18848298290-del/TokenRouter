package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Free-tier selection must admit an unobserved provider before its local
// token count is available. These fixtures reuse the production gate with
// asynchronous loads, then check later filtering, rolling-window recovery,
// and cache eviction while keeping other provider tiers eligible.

// grokFreeQuotaUsageRepoStub records asynchronous usage requests and returns
// controlled per-provider token observations.
type grokFreeQuotaUsageRepoStub struct {
	// mu protects stats, err, calls, lastIDs, and start during background loads.
	// Tests acquire it when changing observations while work is active.
	mu      sync.Mutex                // usage fixture state lock
	stats   map[int64]*quotaTestStats // configured provider token observations
	err     error                     // configured usage read failure
	calls   int                       // usage fixture call count
	lastIDs []int64                   // copied latest request identifiers
	start   time.Time                 // latest query lookback instant
}

// quotaTestStats supplies the scalar token total copied by the usage fixture.
type quotaTestStats struct {
	Tokens int64 // observed provider token total
}

// quotaTestRuntime adapts records into eligibility inputs for the production
// free-quota gate.
type quotaTestRuntime struct {
	gate *FreeQuotaGate // production free-quota gate instance
}

// GetProviderWindowStatsBatch counts the request and records a copied ID list
// under the fixture mutex. A configured error returns no observations;
// successful calls allocate a map and copy each present token structure so
// later fixture replacement does not change the result.
func (r *grokFreeQuotaUsageRepoStub) GetProviderWindowStatsBatch(_ context.Context, providerIDs []int64, start time.Time) (map[int64]*quotaTestStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.lastIDs = append([]int64(nil), providerIDs...)
	r.start = start
	if r.err != nil {
		return nil, r.err
	}
	result := make(map[int64]*quotaTestStats, len(providerIDs))
	for _, providerID := range providerIDs {
		if stats := r.stats[providerID]; stats != nil {
			copyStats := *stats
			result[providerID] = &copyStats
		}
	}
	return result, nil
}

// grokFreeQuotaTestConfig returns the fixed budget and timing options used by
// these selection tests.
func grokFreeQuotaTestConfig() FreeQuotaOptions {
	return FreeQuotaOptions{Enabled: true, TokenLimit: 500_000, Percent: 95, WindowHours: 24, CacheSeconds: 60}
}

// TestFilterGrokFreeQuotaProvidersOnlyBlocksExplicitFreeOAuth mixes free,
// paid, unknown, and API Key records at the soft-budget threshold. It
// verifies first-miss admission, later free-only exclusion, eligible-only
// loading, and the requested daily lookback.
func TestFilterGrokFreeQuotaProvidersOnlyBlocksExplicitFreeOAuth(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*quotaTestStats{
		1: {Tokens: 475_000},
	}}
	scheduler := newQuotaTestRuntime(repo)
	t.Cleanup(func() { waitGrokFreeQuotaTestGate(t, scheduler.gate) })
	providers := []Record{
		{ID: 1, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "FREE"}},
		{ID: 2, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "PRO"}},
		{ID: 3, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth},
		{ID: 4, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"subscription_tier": "FREE"}},
	}

	// Assert admission before waiting for the asynchronous query. This checks
	// that selection can return while the local usage observation is missing.
	filtered := scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Equal(t, []int64{1, 2, 3, 4}, providerIDs(filtered), "miss fails open on hot path")

	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return repo.calls >= 1
	}, 2*time.Second, 10*time.Millisecond)
	waitGrokFreeQuotaTestGate(t, scheduler.gate)

	// Join the background observation before checking its effect so the free
	// provider's removal is attributed to a completed usage refresh.
	filtered = scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Equal(t, []int64{2, 3, 4}, providerIDs(filtered), "paid and unknown fail-open; API-key free marker is not gated")
	require.Equal(t, []int64{1}, repo.lastIDs, "paid, unknown, and API-key providers must not enter the local free-tier query")
	require.WithinDuration(t, time.Now().UTC().Add(-24*time.Hour), repo.start, time.Second)
}

// TestFilterGrokFreeQuotaProvidersStatsFailureFailsOpen makes the usage store
// fail for a free OAuth candidate. It verifies admission before and after
// refresh and that the cached unknown result suppresses a second read.
func TestFilterGrokFreeQuotaProvidersStatsFailureFailsOpen(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{err: errors.New("usage database unavailable")}
	scheduler := newQuotaTestRuntime(repo)
	t.Cleanup(func() { waitGrokFreeQuotaTestGate(t, scheduler.gate) })
	providers := []Record{{
		ID: 1, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "free"},
	}}

	filtered := scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Equal(t, []int64{1}, providerIDs(filtered))
	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return repo.calls >= 1
	}, 2*time.Second, 10*time.Millisecond)
	waitGrokFreeQuotaTestGate(t, scheduler.gate)
	// Wait for publication before checking a second selection, separating the
	// cached failure's retry suppression from the initial unknown observation.
	filtered = scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Equal(t, []int64{1}, providerIDs(filtered))
	require.Equal(t, 1, repo.calls)
}

// TestFilterGrokFreeQuotaProvidersUnknownTierFailOpen supplies missing,
// unknown, and paid tier markers with an over-budget fixture count. It
// verifies all candidates remain admitted and no usage query begins.
func TestFilterGrokFreeQuotaProvidersUnknownTierFailOpen(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*quotaTestStats{
		1: {Tokens: 9_999_999},
	}}
	scheduler := newQuotaTestRuntime(repo)
	t.Cleanup(func() { waitGrokFreeQuotaTestGate(t, scheduler.gate) })
	providers := []Record{
		{ID: 1, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth},
		{ID: 2, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "unknown"}},
		{ID: 3, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Extra: map[string]any{"subscription_tier": "pro"}},
	}

	filtered := scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Equal(t, []int64{1, 2, 3}, providerIDs(filtered))
	require.Zero(t, repo.calls, "unknown/paid tiers must not query free-quota stats")
}

// TestFilterGrokFreeQuotaProvidersRecoversAfterRollingUsageFalls reduces
// recorded usage after a cached blocking observation. It verifies the fresh
// hold persists, an injected expired entry admits requests, and another usage
// query begins while that provider remains admitted.
func TestFilterGrokFreeQuotaProvidersRecoversAfterRollingUsageFalls(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*quotaTestStats{
		1: {Tokens: 490_000},
	}}
	scheduler := newQuotaTestRuntime(repo)
	t.Cleanup(func() { waitGrokFreeQuotaTestGate(t, scheduler.gate) })
	providers := []Record{{
		ID: 1, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth,
		Credentials: map[string]any{"plan_type": "free"},
	}}

	// Start from missing usage, then wait for a blocking observation. This
	// establishes a cached hold before the fixture reports recovered usage.
	require.Equal(t, []int64{1}, providerIDs(scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)))
	require.Eventually(t, func() bool {
		filtered := scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
		return len(filtered) == 0
	}, 2*time.Second, 10*time.Millisecond)

	repo.mu.Lock()
	repo.stats[1] = &quotaTestStats{Tokens: 100_000}
	repo.mu.Unlock()
	// Keep the earlier observation fresh so replacing the fixture's count alone
	// cannot release the existing scheduling hold before another query.
	require.Empty(t, scheduler.filterGrokFreeQuotaProviders(context.Background(), providers), "fresh cache keeps the soft-gate hold")

	// Drain current refreshes before replacing their entry with an old sample;
	// otherwise a late task could overwrite the intended expiration setup.
	waitGrokFreeQuotaTestGate(t, scheduler.gate)
	repo.mu.Lock()
	callsBeforeExpire := repo.calls
	repo.mu.Unlock()
	scheduler.gate.cache.Store(int64(1), grokFreeQuotaGateCacheEntry{
		tokens: 490_000, checkedAt: time.Now().Add(-2 * time.Minute), known: true,
	})
	// Expiration removes the known usage from this check. Immediate admission
	// then remains observable while another query obtains the recovered count.
	require.Equal(t, []int64{1}, providerIDs(scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)))
	require.Eventually(t, func() bool {
		filtered := scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return len(filtered) == 1 && filtered[0].ID == 1 &&
			repo.calls > callsBeforeExpire
	}, 2*time.Second, 10*time.Millisecond)
}

// TestResolveGrokFreeQuotaGateSettingsDefaultsToNinetyFivePercent resolves
// the explicit options returned by the test configuration helper. It verifies
// the token budget, whole-token ninety-five-percent threshold, and daily
// window supplied by that fixture.
func TestResolveGrokFreeQuotaGateSettingsDefaultsToNinetyFivePercent(t *testing.T) {
	settings, ok := resolveGrokFreeQuotaGateSettings(grokFreeQuotaTestConfig())
	require.True(t, ok)
	require.Equal(t, int64(500_000), settings.limitTokens)
	require.Equal(t, int64(475_000), settings.gateTokens)
	require.Equal(t, 24*time.Hour, settings.window)
}

// TestIsExplicitGrokFreeOAuthProvider_OnlyExactFree compares authentication
// types and credential tier markers. It verifies case-insensitive free
// markers qualify while nil, API Key, basic, and absent tier inputs do not.
func TestIsExplicitGrokFreeOAuthProvider_OnlyExactFree(t *testing.T) {
	t.Parallel()
	require.False(t, IsExplicitGrokFreeOAuthProvider(nil))
	require.False(t, IsExplicitGrokFreeOAuthProvider(&Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"subscription_tier": "free"}}))
	require.True(t, IsExplicitGrokFreeOAuthProvider(&Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "FREE"}}))
	require.True(t, IsExplicitGrokFreeOAuthProvider(&Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"plan_type": "free"}}))
	// Require an explicit free marker before sending an ID to the usage store,
	// keeping basic and absent tier values outside this local token gate.
	require.False(t, IsExplicitGrokFreeOAuthProvider(&Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "basic"}}))
	require.False(t, IsExplicitGrokFreeOAuthProvider(&Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}))
}

// TestGrokFreeQuotaGateIsSchedulerOnlyAdminPathUnfiltered removes an
// over-budget record through the scheduling fixture. It verifies that the
// caller's original record retains its ID and explicit free eligibility for
// other consumers.
func TestGrokFreeQuotaGateIsSchedulerOnlyAdminPathUnfiltered(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*quotaTestStats{
		9: {Tokens: 500_000},
	}}
	scheduler := newQuotaTestRuntime(repo)
	t.Cleanup(func() { waitGrokFreeQuotaTestGate(t, scheduler.gate) })
	overGate := Record{ID: 9, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "FREE"}}
	require.Eventually(t, func() bool {
		_ = scheduler.filterGrokFreeQuotaProviders(context.Background(), []Record{overGate})
		return len(scheduler.filterGrokFreeQuotaProviders(context.Background(), []Record{overGate})) == 0
	}, 2*time.Second, 10*time.Millisecond)
	require.True(t, IsExplicitGrokFreeOAuthProvider(&overGate))
	require.Equal(t, int64(9), overGate.ID)
}

// TestSweepGrokFreeQuotaGateCacheDropsStaleEntries combines recent, older,
// and malformed cache entries under a short lifetime. It verifies recent
// entries survive, older and malformed entries are removed, and zero lifetime
// skips sweeping.
func TestSweepGrokFreeQuotaGateCacheDropsStaleEntries(t *testing.T) {
	now := time.Now().UTC()
	cacheTTL := 5 * time.Second
	// The five-minute minimum keeps these short-lifetime samples available; the
	// hour-old sample and malformed value exercise separate eviction paths.
	var cache sync.Map
	cache.Store(int64(1), grokFreeQuotaGateCacheEntry{tokens: 10, checkedAt: now, known: true})
	cache.Store(int64(2), grokFreeQuotaGateCacheEntry{tokens: 20, checkedAt: now.Add(-time.Minute), known: true})
	cache.Store(int64(3), grokFreeQuotaGateCacheEntry{tokens: 30, checkedAt: now.Add(-time.Hour), known: true})
	cache.Store(int64(4), "not-an-entry")

	sweepGrokFreeQuotaGateCache(&cache, now, cacheTTL)

	remaining := make([]int64, 0, 4)
	cache.Range(func(key, _ any) bool {
		if id, ok := key.(int64); ok {
			remaining = append(remaining, id)
		}
		return true
	})
	require.ElementsMatch(t, []int64{1, 2}, remaining)

	// Zero lifetime also disables age-based cleanup, so an hour-old valid
	// observation remains available even after an explicit sweep call.
	var untouched sync.Map
	untouched.Store(int64(7), grokFreeQuotaGateCacheEntry{checkedAt: now.Add(-time.Hour), known: true})
	sweepGrokFreeQuotaGateCache(&untouched, now, 0)
	_, stillThere := untouched.Load(int64(7))
	require.True(t, stillThere)
}

// TestFilterGrokFreeQuotaProvidersEvictsDepartedProviders leaves an old
// observation for an ID absent from the new pool. It verifies another
// provider's background refresh evicts that observation while caching and
// admitting the current candidate.
func TestFilterGrokFreeQuotaProvidersEvictsDepartedProviders(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*quotaTestStats{
		1: {Tokens: 1_000},
	}}
	runtime := newQuotaTestRuntime(repo)
	cache := &runtime.gate.cache
	// Keep a departed provider's observation in the gate because selection
	// changes do not remove entries directly; another completed refresh sweeps
	// it.
	cache.Store(int64(99), grokFreeQuotaGateCacheEntry{tokens: 5, checkedAt: time.Now().UTC().Add(-2 * time.Hour), known: true})

	providers := []Record{
		{ID: 1, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "FREE"}},
	}
	// Wait for both the fresh entry and departed-ID removal because selection
	// returns before the background loader publishes its observation and sweeps.
	_ = runtime.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Eventually(t, func() bool {
		_, departedStillCached := cache.Load(int64(99))
		_, freshCached := cache.Load(int64(1))
		return !departedStillCached && freshCached
	}, 2*time.Second, 10*time.Millisecond)
	filtered := runtime.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Equal(t, []int64{1}, providerIDs(filtered))
}

// providerIDs returns fixture record identifiers in their existing slice
// order.
func providerIDs(providers []Record) []int64 {
	ids := make([]int64, 0, len(providers))
	for i := range providers {
		ids = append(ids, providers[i].ID)
	}
	return ids
}

// newQuotaTestRuntime binds the usage fixture to an asynchronously executing
// production gate. The test executor always accepts work; callers wait for
// the gate's in-flight markers before mutating shared cache state or
// inspecting completed request fields.
func newQuotaTestRuntime(repo *grokFreeQuotaUsageRepoStub) *quotaTestRuntime {
	return &quotaTestRuntime{gate: NewFreeQuotaGate(grokFreeQuotaTestConfig, func(ctx context.Context, ids []int64, start time.Time) (map[int64]int64, error) {
		stats, err := repo.GetProviderWindowStatsBatch(ctx, ids, start)
		result := make(map[int64]int64, len(stats))
		for id, v := range stats {
			if v != nil {
				result[id] = v.Tokens
			}
		}
		return result, err
	}, func(_ string, fn func()) bool {
		// This executor admits every callback; in-flight marker polling gives each
		// test the completion point needed before inspecting shared results.
		go fn()
		return true
	}, time.Now, nil, nil)}
}

// filterGrokFreeQuotaProviders computes explicit Free OAuth eligibility and
// returns admitted records in input order. The result has a new slice, while
// nested record data remains shared with the supplied records.
func (r *quotaTestRuntime) filterGrokFreeQuotaProviders(_ context.Context, values []Record) []Record {
	candidates := make([]FreeQuotaCandidate, len(values))
	for i := range values {
		candidates[i] = FreeQuotaCandidate{ID: values[i].ID, Eligible: IsExplicitGrokFreeOAuthProvider(&values[i])}
	}
	blocked := r.gate.Blocked(candidates)
	result := make([]Record, 0, len(values))
	for i, c := range candidates {
		if !c.Eligible || !blocked[c.ID] {
			result = append(result, values[i])
		}
	}
	return result
}

// waitGrokFreeQuotaTestGate polls until no refresh ID remains reserved,
// failing the test after two seconds. Call it before changing gate cache
// entries or reading fixture fields written by completed background loads.
func waitGrokFreeQuotaTestGate(t *testing.T, gate *FreeQuotaGate) {
	t.Helper()
	require.Eventually(t, func() bool {
		empty := true
		gate.inFlight.Range(func(_, _ any) bool { empty = false; return false })
		return empty
	}, 2*time.Second, time.Millisecond)
}
