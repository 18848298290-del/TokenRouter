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

type grokFreeQuotaUsageRepoStub struct {
	mu      sync.Mutex
	stats   map[int64]*quotaTestStats
	err     error
	calls   int
	lastIDs []int64
	start   time.Time
}

type quotaTestStats struct{ Tokens int64 }

// quotaTestRuntime supplies eligibility to the gate and returns admitted providers in input order.
type quotaTestRuntime struct{ gate *FreeQuotaGate }

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

func grokFreeQuotaTestConfig() FreeQuotaOptions {
	return FreeQuotaOptions{Enabled: true, TokenLimit: 500_000, Percent: 95, WindowHours: 24, CacheSeconds: 60}
}

func TestFilterGrokFreeQuotaProvidersOnlyBlocksExplicitFreeOAuth(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*quotaTestStats{
		1: {Tokens: 475_000}, // 95% of 500k
	}}
	scheduler := newQuotaTestRuntime(repo)
	t.Cleanup(func() { waitGrokFreeQuotaTestGate(t, scheduler.gate) })
	providers := []Record{
		{ID: 1, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "FREE"}},
		{ID: 2, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "PRO"}},
		{ID: 3, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth},
		{ID: 4, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"subscription_tier": "FREE"}},
	}

	// The first miss allows admission and schedules a background refresh.
	filtered := scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Equal(t, []int64{1, 2, 3, 4}, providerIDs(filtered), "miss fails open on hot path")

	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return repo.calls >= 1
	}, 2*time.Second, 10*time.Millisecond)
	waitGrokFreeQuotaTestGate(t, scheduler.gate)

	// The refreshed cache blocks a free OAuth provider above the threshold.
	filtered = scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Equal(t, []int64{2, 3, 4}, providerIDs(filtered), "paid and unknown fail-open; API-key free marker is not gated")
	require.Equal(t, []int64{1}, repo.lastIDs, "paid, unknown, and API-key providers must not enter the local free-tier query")
	require.WithinDuration(t, time.Now().UTC().Add(-24*time.Hour), repo.start, time.Second)
}

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
	// A cached query failure allows admission and suppresses retries until the entry expires.
	filtered = scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Equal(t, []int64{1}, providerIDs(filtered))
	require.Equal(t, 1, repo.calls)
}

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

	// The initial miss allows admission; populated usage then blocks the provider.
	require.Equal(t, []int64{1}, providerIDs(scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)))
	require.Eventually(t, func() bool {
		filtered := scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
		return len(filtered) == 0
	}, 2*time.Second, 10*time.Millisecond)

	repo.mu.Lock()
	repo.stats[1] = &quotaTestStats{Tokens: 100_000}
	repo.mu.Unlock()
	// Fresh usage continues to enforce the gate until the TTL expires.
	require.Empty(t, scheduler.filterGrokFreeQuotaProviders(context.Background(), providers), "fresh cache keeps the soft-gate hold")

	// Expired usage allows admission and starts a refresh using the recovered usage.
	waitGrokFreeQuotaTestGate(t, scheduler.gate)
	repo.mu.Lock()
	callsBeforeExpire := repo.calls
	repo.mu.Unlock()
	scheduler.gate.cache.Store(int64(1), grokFreeQuotaGateCacheEntry{
		tokens: 490_000, checkedAt: time.Now().Add(-2 * time.Minute), known: true, // Beyond the 60-second TTL.
	})
	// Requests remain admitted while the refresh runs.
	require.Equal(t, []int64{1}, providerIDs(scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)))
	require.Eventually(t, func() bool {
		filtered := scheduler.filterGrokFreeQuotaProviders(context.Background(), providers)
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return len(filtered) == 1 && filtered[0].ID == 1 &&
			repo.calls > callsBeforeExpire
	}, 2*time.Second, 10*time.Millisecond)
}

func TestResolveGrokFreeQuotaGateSettingsDefaultsToNinetyFivePercent(t *testing.T) {
	settings, ok := resolveGrokFreeQuotaGateSettings(grokFreeQuotaTestConfig())
	require.True(t, ok)
	require.Equal(t, int64(500_000), settings.limitTokens)
	require.Equal(t, int64(475_000), settings.gateTokens) // 95% of 500k
	require.Equal(t, 24*time.Hour, settings.window)
}

func TestIsExplicitGrokFreeOAuthProvider_OnlyExactFree(t *testing.T) {
	t.Parallel()
	require.False(t, IsExplicitGrokFreeOAuthProvider(nil))
	require.False(t, IsExplicitGrokFreeOAuthProvider(&Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"subscription_tier": "free"}}))
	require.True(t, IsExplicitGrokFreeOAuthProvider(&Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "FREE"}}))
	require.True(t, IsExplicitGrokFreeOAuthProvider(&Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"plan_type": "free"}}))
	// Only an explicit free tier qualifies; basic and inferred free tiers do not.
	require.False(t, IsExplicitGrokFreeOAuthProvider(&Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "basic"}}))
	require.False(t, IsExplicitGrokFreeOAuthProvider(&Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}))
}

// TestGrokFreeQuotaGateIsSchedulerOnlyAdminPathUnfiltered checks that selection filtering
// leaves the provider record unchanged for other callers.
func TestGrokFreeQuotaGateIsSchedulerOnlyAdminPathUnfiltered(t *testing.T) {
	// An over-limit free provider is excluded by the scheduling fixture.
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
	// Filtering leaves the source record available to other callers.
	require.True(t, IsExplicitGrokFreeOAuthProvider(&overGate))
	require.Equal(t, int64(9), overGate.ID)
}

func TestSweepGrokFreeQuotaGateCacheDropsStaleEntries(t *testing.T) {
	now := time.Now().UTC()
	cacheTTL := 5 * time.Second
	// Entries younger than grokFreeQuotaGateCacheMinSweepAge survive the sweep.
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

	// A zero TTL disables expiry, so the sweep retains existing entries.
	var untouched sync.Map
	untouched.Store(int64(7), grokFreeQuotaGateCacheEntry{checkedAt: now.Add(-time.Hour), known: true})
	sweepGrokFreeQuotaGateCache(&untouched, now, 0)
	_, stillThere := untouched.Load(int64(7))
	require.True(t, stillThere)
}

func TestFilterGrokFreeQuotaProvidersEvictsDepartedProviders(t *testing.T) {
	repo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*quotaTestStats{
		1: {Tokens: 1_000},
	}}
	runtime := newQuotaTestRuntime(repo)
	cache := &runtime.gate.cache
	// Provider 99 has left the current selection and has a stale cache entry.
	// Refreshing another provider also evicts this stale entry.
	cache.Store(int64(99), grokFreeQuotaGateCacheEntry{tokens: 5, checkedAt: time.Now().UTC().Add(-2 * time.Hour), known: true})

	providers := []Record{
		{ID: 1, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"subscription_tier": "FREE"}},
	}
	// The initial refresh is asynchronous; eviction may still be pending when the call returns.
	_ = runtime.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Eventually(t, func() bool {
		_, departedStillCached := cache.Load(int64(99))
		_, freshCached := cache.Load(int64(1))
		return !departedStillCached && freshCached
	}, 2*time.Second, 10*time.Millisecond)
	filtered := runtime.filterGrokFreeQuotaProviders(context.Background(), providers)
	require.Equal(t, []int64{1}, providerIDs(filtered))
}

func providerIDs(providers []Record) []int64 {
	ids := make([]int64, 0, len(providers))
	for i := range providers {
		ids = append(ids, providers[i].ID)
	}
	return ids
}

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
	}, func(_ string, fn func()) bool { go fn(); return true }, time.Now, nil, nil)}
}

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

// waitGrokFreeQuotaTestGate waits for background refreshes before tests mutate the cache.
func waitGrokFreeQuotaTestGate(t *testing.T, gate *FreeQuotaGate) {
	t.Helper()
	require.Eventually(t, func() bool {
		empty := true
		gate.inFlight.Range(func(_, _ any) bool { empty = false; return false })
		return empty
	}, 2*time.Second, time.Millisecond)
}
