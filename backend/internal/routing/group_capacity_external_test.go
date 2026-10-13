package routing_test

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// A provider shared by two groups contributes its full limits and observed
// usage to each summary. These fixtures convert stored provider rows into
// scalar capacity snapshots, while counter spies show which provider IDs and
// session timeouts the batch service actually requests.

// testCapacityProviders adapts stored provider records into observed capacity
// snapshots for tests.
type testCapacityProviders struct {
	// Repository supplies records for the single-group path. Callers provide a
	// usable reader before querying; the adapter forwards repository errors
	// before capacity conversion.
	Repository capacityFixtureRepository // stored provider fixture reader
	// Settings is consulted once for a nonempty conversion batch. Nil supplies
	// zero-valued quota settings before provider observation calculates each
	// row's pause state.
	Settings capacitySettingsReader // optional quota settings reader
}

// testtestCapacityProviderBatchReader exposes optional batch rows supplied by
// the provider fixture.
type testtestCapacityProviderBatchReader interface {
	// ListSchedulableCapacityByGroupIDs returns stored capacity rows for the
	// supplied groups. The adapter forwards any error and otherwise converts
	// those rows using its configured quota settings reader.
	ListSchedulableCapacityByGroupIDs(context.Context, []int64) ([]provider.GroupProviderCapacityRow, error) // batch membership row query
}

// testCapacityProviderBatch adds batch capacity reads to the single-group
// fixture adapter.
type testCapacityProviderBatch struct {
	testCapacityProviders // shared single-group conversion adapter
	// Reader is installed after the optional batch-interface check succeeds.
	// Direct fixture construction provides a nonnil reader before batch queries.
	Reader testtestCapacityProviderBatchReader // stored batch membership reader
}

// testCapacityGroups adapts group fixtures to the capacity service's
// active-ID reader.
type testCapacityGroups struct {
	// GroupRepository can implement ListActiveIDs directly; otherwise the
	// adapter calls ListActive and extracts IDs in returned order for the
	// capacity service.
	routing.GroupRepository // wrapped active group fixture
}

// capacityFixtureRepository supplies stored provider records for the
// single-group test adapter.
type capacityFixtureRepository interface {
	// ListSchedulableByGroupID supplies provider records for one group using the
	// supplied context. The test adapter observes runtime limits and quota state
	// after this read, returning storage errors directly.
	ListSchedulableByGroupID(context.Context, int64) ([]provider.Record, error) // single-group provider record query
}

// capacitySettingsReader supplies quota thresholds when test rows become
// capacity snapshots.
type capacitySettingsReader interface {
	// GetOpenAIQuotaAutoPauseSettings returns thresholds used when observing
	// stored capacity rows. A nil reader is handled by capacitySettings and
	// supplies zero-valued settings to the observation function.
	GetOpenAIQuotaAutoPauseSettings(context.Context) provider.QuotaAutoPauseSettings // quota observation settings query
}

// groupCapacityProviderRepoStub returns configured single-group records and
// batch membership rows.
type groupCapacityProviderRepoStub struct {
	// providers and rows supply separate query paths. Readers copy their outer
	// slices, while nested Extra maps and optional window pointers remain
	// attached to the fixture values.
	providers []provider.Record                   // configured single-group provider records
	rows      []provider.GroupProviderCapacityRow // configured batch membership rows
	// requested copies each query's ID slice. Later caller mutations cannot
	// change the group identities retained for this fixture's assertions.
	requested []int64 // copied requested batch group IDs
}

// groupCapacitySettingsStub supplies fixed quota settings for provider
// observation in capacity tests.
type groupCapacitySettingsStub struct {
	settings provider.QuotaAutoPauseSettings // fixed quota threshold settings
}

// groupCapacityGroupRepoStub records active-ID reads and returns the
// configured group selection.
type groupCapacityGroupRepoStub struct {
	// GroupRepository supplies unused group repository methods through a nil
	// embedded interface. Invoking a method without a fixture override panics.
	routing.GroupRepository // unused group repository methods

	groupIDs  []int64 // configured active group IDs
	listCalls int     // recorded active-ID query count
}

// groupCapacityConcurrencyCacheStub records batch occupancy reads and
// supplies per-provider counts.
type groupCapacityConcurrencyCacheStub struct {
	// ConcurrencyCache supplies unused concurrency cache methods through a nil
	// embedded interface. Invoking a method without a fixture override panics.
	scheduler.ConcurrencyCache               // unused concurrency cache methods
	counts                     map[int64]int // supplied occupied slots by provider
	// requested copies each query's ID slice. Later caller mutations cannot
	// change the provider identities retained for this fixture's assertions.
	requested []int64 // copied queried occupancy provider IDs
}

// groupCapacitySessionCacheStub records requested providers and idle
// durations for session counting.
type groupCapacitySessionCacheStub struct {
	// SessionLimitCache supplies unused session cache methods through a nil
	// embedded interface. Invoking a method without a fixture override panics.
	scheduler.SessionLimitCache               // unused session cache methods
	counts                      map[int64]int // supplied active sessions by provider
	// requested copies each query's ID slice. Later caller mutations cannot
	// change the provider identities retained for this fixture's assertions.
	requested []int64 // copied queried session provider IDs
	// idleTimeouts copies each request's provider-duration map so the test can
	// inspect minute conversion independently of later caller mutations to that
	// map.
	idleTimeouts map[int64]time.Duration // copied supplied session idle durations
}

// groupCapacityRPMCacheStub records batch request-count reads and supplies
// per-provider RPM usage.
type groupCapacityRPMCacheStub struct {
	// RPMCache supplies unused request-count cache methods through a nil
	// embedded interface. Invoking a method without a fixture override panics.
	scheduler.RPMCache               // unused request-count cache methods
	counts             map[int64]int // supplied requests per provider
	// requested copies each query's ID slice. Later caller mutations cannot
	// change the provider identities retained for this fixture's assertions.
	requested []int64 // copied queried RPM provider IDs
}

// TestGroupCapacityService_ExcludesOpenAIQuotaAutoPausedProviders supplies
// two OAuth providers on opposite sides of the configured quota threshold.
// The summary and requested occupancy IDs include the eligible provider
// alone.
func TestGroupCapacityService_ExcludesOpenAIQuotaAutoPausedProviders(t *testing.T) {
	providers := []provider.Record{
		{
			ID:          1,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 7,
			Extra: map[string]any{
				"codex_5h_used_percent": 96.0,
			},
		},
		{
			ID:          2,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 3,
			Extra: map[string]any{
				"codex_5h_used_percent": 30.0,
			},
		},
	}
	concurrencyCache := &groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 5, 2: 2}}
	svc := newTestGroupCapacityService(
		&groupCapacityProviderRepoStub{providers: providers},
		nil, scheduler.NewConcurrencyService(concurrencyCache, scheduler.Diagnostics{
			Logf: logging.LegacyPrintf,

			Event: logging.Event,
		},
		),
		nil,
		nil,
		groupCapacitySettingsStub{settings: provider.QuotaAutoPauseSettings{DefaultThreshold5h: 0.95}},
	)

	capacity, err := svc.GetGroupCapacity(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 3, capacity.ConcurrencyMax)
	require.Equal(t, 2, capacity.ConcurrencyUsed)
	require.Equal(t, []int64{2}, concurrencyCache.requested)
}

// TestGetAllGroupCapacityBatchExcludesOpenAIQuotaAutoPausedProviders supplies
// two batch rows with different quota observations. It checks the remaining
// provider's concurrency limit and usage and the IDs passed to occupancy
// lookup.
func TestGetAllGroupCapacityBatchExcludesOpenAIQuotaAutoPausedProviders(t *testing.T) {
	providerRepo := &groupCapacityProviderRepoStub{rows: []provider.GroupProviderCapacityRow{
		{
			GroupID:     10,
			ProviderID:  1,
			Platform:    capability.PlatformOpenAI,
			Concurrency: 7,
			Extra:       map[string]any{"codex_5h_used_percent": 96.0},
		},
		{
			GroupID:     10,
			ProviderID:  2,
			Platform:    capability.PlatformOpenAI,
			Concurrency: 3,
			Extra:       map[string]any{"codex_5h_used_percent": 30.0},
		},
	}}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10}}
	concurrencyCache := &groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 5, 2: 2}}
	svc := newTestGroupCapacityService(
		providerRepo,
		groupRepo, scheduler.NewConcurrencyService(concurrencyCache, scheduler.Diagnostics{
			Logf: logging.LegacyPrintf,

			Event: logging.Event,
		},
		),
		nil,
		nil,
		groupCapacitySettingsStub{settings: provider.QuotaAutoPauseSettings{DefaultThreshold5h: 0.95}},
	)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)
	require.Equal(t, []routing.GroupCapacitySummary{{
		GroupID:         10,
		ConcurrencyUsed: 2,
		ConcurrencyMax:  3,
	}}, results)
	require.Equal(t, []int64{2}, concurrencyCache.requested)
}

// TestGetAllGroupCapacityBatchAggregatesRuntimeAndLimits shares one provider
// between two groups and adds another to the second. It checks per-group
// totals, deduplicated counter queries, active-ID loading, and minute-based
// session durations.
func TestGetAllGroupCapacityBatchAggregatesRuntimeAndLimits(t *testing.T) {
	providerRepo := &groupCapacityProviderRepoStub{
		rows: []provider.GroupProviderCapacityRow{
			{
				GroupID:     10,
				ProviderID:  1,
				Concurrency: 2,
				Extra: map[string]any{
					"max_sessions":                 3,
					"session_idle_timeout_minutes": 7,
					"base_rpm":                     11,
				},
			},
			{
				GroupID:     20,
				ProviderID:  1,
				Concurrency: 2,
				Extra: map[string]any{
					"max_sessions":                 3,
					"session_idle_timeout_minutes": 7,
					"base_rpm":                     11,
				},
			},
			{
				GroupID:     20,
				ProviderID:  2,
				Concurrency: 4,
				Extra: map[string]any{
					"max_sessions":                 1,
					"session_idle_timeout_minutes": 9,
					"base_rpm":                     13,
				},
			},
		},
	}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}
	concurrencyCache := &groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 1, 2: 2}}
	sessionCache := &groupCapacitySessionCacheStub{counts: map[int64]int{1: 2, 2: 1}}
	rpmCache := &groupCapacityRPMCacheStub{counts: map[int64]int{1: 5, 2: 7}}
	svc := newTestGroupCapacityService(
		providerRepo,
		groupRepo, scheduler.NewConcurrencyService(concurrencyCache, scheduler.Diagnostics{
			Logf: logging.LegacyPrintf,

			Event: logging.Event,
		},
		),
		sessionCache,
		rpmCache,
		nil,
	)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, groupRepo.listCalls)
	require.Equal(t, []int64{10, 20}, providerRepo.requested)
	require.Equal(t, []int64{1, 2}, concurrencyCache.requested)
	require.ElementsMatch(t, []int64{1, 2}, sessionCache.requested)
	require.ElementsMatch(t, []int64{1, 2}, rpmCache.requested)
	// Stored minute-based idle settings become duration arguments for the
	// session counter.
	require.Equal(t, 7*time.Minute, sessionCache.idleTimeouts[1])
	require.Equal(t, 9*time.Minute, sessionCache.idleTimeouts[2])

	require.Equal(t, []routing.GroupCapacitySummary{
		{
			GroupID:         10,
			ConcurrencyUsed: 1,
			ConcurrencyMax:  2,
			SessionsUsed:    2,
			SessionsMax:     3,
			RPMUsed:         5,
			RPMMax:          11,
		},
		{
			GroupID:         20,
			ConcurrencyUsed: 3,
			ConcurrencyMax:  6,
			SessionsUsed:    3,
			SessionsMax:     4,
			RPMUsed:         12,
			RPMMax:          24,
		},
	}, results)
}

// TestGetAllGroupCapacityBatchKeepsEmptyGroupRows lists two active groups but
// supplies provider rows for one. Both ordered summaries remain present, with
// zero capacity for the empty group.
func TestGetAllGroupCapacityBatchKeepsEmptyGroupRows(t *testing.T) {
	providerRepo := &groupCapacityProviderRepoStub{
		rows: []provider.GroupProviderCapacityRow{
			{GroupID: 20, ProviderID: 2, Concurrency: 4},
		},
	}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}
	svc := newTestGroupCapacityService(providerRepo, groupRepo, nil, nil, nil, nil)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)

	require.Equal(t, []routing.GroupCapacitySummary{
		{GroupID: 10},
		{GroupID: 20, ConcurrencyMax: 4},
	}, results)
}

// TestGetGroupCapacityByIDsUsesBatchPathAndDeduplicatesIDs supplies repeated,
// zero, negative, and empty-row group IDs to a batch-capable reader. It
// checks positive distinct query order and a retained zero-capacity group.
func TestGetGroupCapacityByIDsUsesBatchPathAndDeduplicatesIDs(t *testing.T) {
	providerRepo := &groupCapacityProviderRepoStub{rows: []provider.GroupProviderCapacityRow{
		{GroupID: 20, ProviderID: 2, Concurrency: 4},
	}}
	svc := newTestGroupCapacityService(providerRepo, nil, nil, nil, nil, nil)

	results, err := svc.GetGroupCapacityByIDs(context.Background(), []int64{20, 10, 20, 0, -1})
	require.NoError(t, err)
	require.Equal(t, []int64{20, 10}, providerRepo.requested)
	require.Equal(t, map[int64]routing.GroupCapacitySummary{
		10: {GroupID: 10},
		20: {GroupID: 20, ConcurrencyMax: 4},
	}, results)
}

// ListSchedulableByGroupID reads the configured stored-record fixture and
// converts its results into capacity snapshots. Repository errors propagate
// directly; nonempty conversion reads quota settings once before observing
// each record.
func (r testCapacityProviders) ListSchedulableByGroupID(ctx context.Context, id int64) ([]provider.CapacitySnapshot, error) {
	values, err := r.Repository.ListSchedulableByGroupID(ctx, id)
	if err != nil {
		return nil, err
	}
	return capacitySnapshots(ctx, values, r.Settings), nil
}

// ListSchedulableCapacityByGroupIDs reads stored membership rows and converts
// them into routing capacity rows. Repository errors propagate directly, and
// conversion retains group IDs while observing each provider's scalar
// capacity.
func (r testCapacityProviderBatch) ListSchedulableCapacityByGroupIDs(ctx context.Context, ids []int64) ([]routing.CapacityProviderRow, error) {
	values, err := r.Reader.ListSchedulableCapacityByGroupIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return capacityRows(ctx, values, r.Settings), nil
}

// newTestCapacityProviders selects a batch-capable adapter when the
// repository implements testtestCapacityProviderBatchReader. Other
// repositories keep the single-group adapter, and both forms retain the
// supplied quota settings reader.
func newTestCapacityProviders(repo capacityFixtureRepository, settings capacitySettingsReader) routing.CapacityProviders {
	base := testCapacityProviders{Repository: repo, Settings: settings}
	// Retain optional batch support so CapacityService can share one membership
	// query and runtime-count reads across the selected groups in these fixtures.
	if batch, ok := repo.(testtestCapacityProviderBatchReader); ok {
		return testCapacityProviderBatch{testCapacityProviders: base, Reader: batch}
	}
	return base
}

// ListActiveIDs prefers a wrapped repository's direct active-ID method.
// Otherwise it extracts IDs from ListActive in returned order, propagating a
// listing error before constructing the ID slice.
func (r testCapacityGroups) ListActiveIDs(ctx context.Context) ([]int64, error) {
	// Use a fixture's direct active-ID query when available so the batch test
	// can inspect that narrow read; fixtures exposing full active groups can
	// supply their IDs through the alternative ListActive path.
	if actual, ok := r.GroupRepository.(interface {
		// ListActiveIDs supplies active group identities for the capacity adapter.
		ListActiveIDs(context.Context) ([]int64, error)
	}); ok {
		return actual.ListActiveIDs(ctx)
	}
	values, err := r.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(values))
	for i := range values {
		ids = append(ids, values[i].ID)
	}
	return ids, nil
}

// newTestGroupCapacityService connects provider and group adapters with
// optional runtime readers. A nil concurrency pointer becomes a nil interface
// so service queries can omit occupancy reads safely.
func newTestGroupCapacityService(providers capacityFixtureRepository, groups routing.GroupRepository, concurrency *scheduler.ConcurrencyService, sessions scheduler.SessionLimitCache, rpm scheduler.RPMCache, settings capacitySettingsReader) *routing.CapacityService {
	var counters routing.CapacityConcurrency
	if concurrency != nil {
		counters = concurrency
	}
	return routing.NewCapacityService(newTestCapacityProviders(providers, settings), testCapacityGroups{groups}, counters, sessions, rpm)
}

// capacitySettings reads quota thresholds from a supplied reader or returns
// zero-valued settings. Nil selects that fixture default; a nonnil reader
// receives the current conversion context.
func capacitySettings(ctx context.Context, reader capacitySettingsReader) provider.QuotaAutoPauseSettings {
	if reader != nil {
		return reader.GetOpenAIQuotaAutoPauseSettings(ctx)
	}
	return provider.QuotaAutoPauseSettings{}
}

// capacitySnapshots returns nil for empty records and observes each nonempty
// record through provider.ProjectObservedCapacity. It reads settings once and
// samples time.Now separately for each row before returning scalar snapshots.
func capacitySnapshots(ctx context.Context, values []provider.Record, settings capacitySettingsReader) []provider.CapacitySnapshot {
	if len(values) == 0 {
		return nil
	}
	snapshot := capacitySettings(ctx, settings)
	out := make([]provider.CapacitySnapshot, len(values))
	for i, a := range values {
		out[i] = provider.ProjectObservedCapacity(provider.GroupProviderCapacityRow{ProviderID: a.ID, Platform: a.Platform, Concurrency: a.Concurrency, Extra: a.Extra, SessionWindowStart: a.SessionWindowStart, SessionWindowEnd: a.SessionWindowEnd}, snapshot, time.Now())
	}
	return out
}

// capacityRows returns nil for empty input and observes each membership row
// through provider.ProjectObservedCapacity. It retains each group ID, reads
// settings once, and samples time.Now for each provider row.
func capacityRows(ctx context.Context, rows []provider.GroupProviderCapacityRow, settings capacitySettingsReader) []routing.CapacityProviderRow {
	if len(rows) == 0 {
		return nil
	}
	snapshot := capacitySettings(ctx, settings)
	out := make([]routing.CapacityProviderRow, len(rows))
	for i, row := range rows {
		out[i] = routing.CapacityProviderRow{GroupID: row.GroupID, Provider: provider.ProjectObservedCapacity(row, snapshot, time.Now())}
	}
	return out
}

// ListSchedulableByGroupID returns an outer-slice copy of configured provider
// records and ignores the group ID. Nested record maps and pointers remain
// shared while observation reads them.
func (s *groupCapacityProviderRepoStub) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]provider.Record, error) {
	out := make([]provider.Record, len(s.providers))
	copy(out, s.providers)
	return out, nil
}

// ListSchedulableCapacityByGroupIDs records a copied group-ID request and
// returns an outer-slice copy of configured rows. Nested row maps and
// pointers remain shared with the fixture inputs.
func (s *groupCapacityProviderRepoStub) ListSchedulableCapacityByGroupIDs(_ context.Context, groupIDs []int64) ([]provider.GroupProviderCapacityRow, error) {
	s.requested = append([]int64(nil), groupIDs...)
	return append([]provider.GroupProviderCapacityRow(nil), s.rows...), nil
}

// GetOpenAIQuotaAutoPauseSettings returns the fixed quota settings supplied
// to this fixture.
func (s groupCapacitySettingsStub) GetOpenAIQuotaAutoPauseSettings(ctx context.Context) provider.QuotaAutoPauseSettings {
	return s.settings
}

// ListActiveIDs counts the active-ID read and returns a copied configured
// slice. Tests can inspect the call count while callers can change returned
// ID storage independently.
func (s *groupCapacityGroupRepoStub) ListActiveIDs(context.Context) ([]int64, error) {
	s.listCalls++
	return append([]int64(nil), s.groupIDs...), nil
}

// GetProviderConcurrencyBatch records a copied provider-ID request and
// returns a new count map. Missing configured counts contribute zero, and
// caller map changes do not alter the fixture.
func (s *groupCapacityConcurrencyCacheStub) GetProviderConcurrencyBatch(_ context.Context, providerIDs []int64) (map[int64]int, error) {
	s.requested = append([]int64(nil), providerIDs...)
	out := make(map[int64]int, len(providerIDs))
	for _, id := range providerIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

// GetActiveSessionCountBatch copies the requested provider IDs and
// idle-duration map, then returns a new count map. Missing configured counts
// contribute zero, leaving both captured request collections stable for
// assertions.
func (s *groupCapacitySessionCacheStub) GetActiveSessionCountBatch(_ context.Context, providerIDs []int64, idleTimeouts map[int64]time.Duration) (map[int64]int, error) {
	s.requested = append([]int64(nil), providerIDs...)
	s.idleTimeouts = make(map[int64]time.Duration, len(idleTimeouts))
	maps.Copy(s.idleTimeouts, idleTimeouts)
	out := make(map[int64]int, len(providerIDs))
	for _, id := range providerIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

// GetRPMBatch records a copied provider-ID request and returns a new map of
// configured request counts. Missing providers contribute zero, and callers
// can change the result independently.
func (s *groupCapacityRPMCacheStub) GetRPMBatch(_ context.Context, providerIDs []int64) (map[int64]int, error) {
	s.requested = append([]int64(nil), providerIDs...)
	out := make(map[int64]int, len(providerIDs))
	for _, id := range providerIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}
