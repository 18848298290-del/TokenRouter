package ops

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// opsMetricsProjectionRepo records whether queue sampling chooses dedicated
// load rows or provider observations.
type opsMetricsProjectionRepo struct {
	// ProviderLoadSource remains nil; these tests call overridden listing methods,
	// and invoking other embedded methods would use that nil dependency.
	ProviderLoadSource // unused listing methods placeholder
	// providers is returned directly by ListSchedulable. Test setup owns its slice
	// and leaves it unchanged during collection.
	providers []ProviderObservation // schedulable provider observation fixture
	// providerLoads is returned directly by the dedicated listing. Queue sampling
	// passes these fixture limits onward without cloning them here.
	providerLoads   []ProviderWithConcurrency // dedicated provider load fixture
	listCalls       int                       // observation listing call count
	projectionCalls int                       // dedicated listing call count
}

// opsMetricsFallbackRepo exposes observation listing without implementing the
// dedicated provider-load method.
type opsMetricsFallbackRepo struct {
	// ProviderLoadSource remains nil; these tests call overridden listing methods,
	// and invoking other embedded methods would use that nil dependency.
	ProviderLoadSource // unused listing methods placeholder
	// providers is returned directly to the observation path. The fixture is
	// populated before collection and reused without mutation.
	providers []ProviderObservation // schedulable provider observation fixture
	listCalls int                   // observation listing call count
}

// opsMetricsLoadCache records forwarded provider limits and returns
// predetermined scheduler load values.
type opsMetricsLoadCache struct {
	// ConcurrencyCache remains nil because these tests use the overridden batch
	// method. Other embedded cache methods cannot be invoked by this fixture.
	scheduler.ConcurrencyCache // unused cache methods placeholder
	// loads and its pointed-to observations are returned directly. The test keeps
	// the map unchanged while the scheduler reads it.
	loads map[int64]*ProviderLoadInfo // provider load response fixture
	// got retains the most recently supplied slice for assertions.
	// GetProvidersLoadBatch performs no defensive copy of provider limits.
	got []ProviderWithConcurrency // last forwarded provider limits
}

// ListSchedulable records an observation-list call and returns the stored
// provider slice directly. It ignores context and succeeds, letting assertions
// detect if the dedicated-load collector accidentally chooses this method.
func (r *opsMetricsProjectionRepo) ListSchedulable(context.Context) ([]ProviderObservation, error) {
	r.listCalls++
	return r.providers, nil
}

// ListSchedulableProviderLoads records a dedicated-list call and returns the
// stored limit slice directly. It ignores context and succeeds; the test
// compares those forwarded limits with the observation-based result.
func (r *opsMetricsProjectionRepo) ListSchedulableProviderLoads(context.Context) ([]ProviderWithConcurrency, error) {
	r.projectionCalls++
	return r.providerLoads, nil
}

// ListSchedulable records a call and returns its provider fixture directly.
// This repository lacks the optional dedicated listing, so the collector
// derives limits from these observations during the test.
func (r *opsMetricsFallbackRepo) ListSchedulable(context.Context) ([]ProviderObservation, error) {
	r.listCalls++
	return r.providers, nil
}

// GetProvidersLoadBatch retains the supplied provider slice for assertions and
// returns the stored load map directly. It ignores context and succeeds,
// allowing the real scheduler reader to exercise collector forwarding.
func (c *opsMetricsLoadCache) GetProvidersLoadBatch(_ context.Context, providers []ProviderWithConcurrency) (map[int64]*ProviderLoadInfo, error) {
	c.got = providers
	return c.loads, nil
}

// TestCollectConcurrencyQueueDepthUsesProjectionAndPreservesFallbackResult
// gives both listing paths the same resolved limits and waiting counts. It
// checks a depth of five, dedicated-method selection, and the limits forwarded
// to the scheduler cache.
func TestCollectConcurrencyQueueDepthUsesProjectionAndPreservesFallbackResult(t *testing.T) {
	loadFactor := 7
	providers := []ProviderObservation{
		{ID: 11, Concurrency: 2, LoadFactor: loadFactor},
		{ID: 12, Concurrency: 3, LoadFactor: 3},
		{ID: 13, LoadFactor: 1},
	}
	providerLoads := []ProviderWithConcurrency{
		{ID: 11, MaxConcurrency: 7},
		{ID: 12, MaxConcurrency: 3},
		{ID: 13, MaxConcurrency: 1},
	}
	loads := map[int64]*ProviderLoadInfo{
		11: {ProviderID: 11, WaitingCount: 2},
		12: {ProviderID: 12, WaitingCount: 3},
		13: {ProviderID: 13, WaitingCount: 0},
	}

	projectionRepo := &opsMetricsProjectionRepo{providers: providers, providerLoads: providerLoads}
	projectionCache := &opsMetricsLoadCache{loads: loads}
	projectionConcurrency := scheduler.NewConcurrencyService(projectionCache)
	// Disable scheduler result reuse so this collection reaches the fixture and
	// records the dedicated limits used by the assertion.
	projectionConcurrency.SetProviderLoadBatchCacheTTL(0)
	projectionCollector := &OpsMetricsCollector{
		providerRepo:       projectionRepo,
		concurrencyService: projectionConcurrency,
	}

	fallbackRepo := &opsMetricsFallbackRepo{providers: providers}
	fallbackCache := &opsMetricsLoadCache{loads: loads}
	fallbackConcurrency := scheduler.NewConcurrencyService(fallbackCache)
	// Disable result reuse for the observation path as well, making both collected
	// results depend on the supplied load-map fixture.
	fallbackConcurrency.SetProviderLoadBatchCacheTTL(0)
	fallbackCollector := &OpsMetricsCollector{
		providerRepo:       fallbackRepo,
		concurrencyService: fallbackConcurrency,
	}

	projectionDepth := projectionCollector.collectConcurrencyQueueDepth(context.Background())
	fallbackDepth := fallbackCollector.collectConcurrencyQueueDepth(context.Background())

	require.NotNil(t, projectionDepth)
	require.NotNil(t, fallbackDepth)
	require.Equal(t, 5, *projectionDepth)
	require.Equal(t, *fallbackDepth, *projectionDepth)
	require.Equal(t, 1, projectionRepo.projectionCalls)
	require.Zero(t, projectionRepo.listCalls)
	require.Equal(t, 1, fallbackRepo.listCalls)
	require.Equal(t, providerLoads, projectionCache.got)
	require.Equal(t, fallbackCache.got, projectionCache.got)
}

// BenchmarkOpsMetricsCollectorCollectConcurrencyQueueDepth samples one thousand
// dedicated provider-load rows through a scheduler fixture returning no waiting
// loads. It reports allocations for that path and checks each iteration returns
// an observed zero.
func BenchmarkOpsMetricsCollectorCollectConcurrencyQueueDepth(b *testing.B) {
	// Use one thousand providers as the benchmark fixture size.
	const providerCount = 1000
	loadFactor := 8
	providers := make([]ProviderObservation, providerCount)
	providerLoads := make([]ProviderWithConcurrency, providerCount)
	for i := range providerCount {
		id := int64(i + 1)
		providers[i] = ProviderObservation{
			ID:          id,
			Concurrency: 4,
			LoadFactor:  loadFactor,
		}
		providerLoads[i] = ProviderWithConcurrency{ID: id, MaxConcurrency: loadFactor}
	}

	repo := &opsMetricsProjectionRepo{providers: providers, providerLoads: providerLoads}
	cache := &opsMetricsLoadCache{loads: map[int64]*ProviderLoadInfo{}}
	concurrency := scheduler.NewConcurrencyService(cache)
	// Disable scheduler result reuse so each timed iteration reads the fixture's
	// load map rather than reusing an earlier result.
	concurrency.SetProviderLoadBatchCacheTTL(0)
	collector := &OpsMetricsCollector{providerRepo: repo, concurrencyService: concurrency}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if depth := collector.collectConcurrencyQueueDepth(context.Background()); depth == nil || *depth != 0 {
			b.Fatalf("unexpected queue depth: %v", depth)
		}
	}
}
