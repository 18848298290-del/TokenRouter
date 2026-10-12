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

type testCapacityProviders struct {
	Repository capacityFixtureRepository
	Settings   capacitySettingsReader
}

type testtestCapacityProviderBatchReader interface {
	ListSchedulableCapacityByGroupIDs(context.Context, []int64) ([]provider.GroupProviderCapacityRow, error)
}

type testCapacityProviderBatch struct {
	testCapacityProviders
	Reader testtestCapacityProviderBatchReader
}

type testCapacityGroups struct{ routing.GroupRepository }

// capacityFixtureRepository supplies schedulable group providers for capacity tests.
type capacityFixtureRepository interface {
	ListSchedulableByGroupID(context.Context, int64) ([]provider.Record, error)
}

type capacitySettingsReader interface {
	GetOpenAIQuotaAutoPauseSettings(context.Context) provider.QuotaAutoPauseSettings
}

type groupCapacityProviderRepoStub struct {
	providers []provider.Record
	rows      []provider.GroupProviderCapacityRow
	requested []int64
}

type groupCapacitySettingsStub struct {
	settings provider.QuotaAutoPauseSettings
}

type groupCapacityGroupRepoStub struct {
	routing.GroupRepository

	groupIDs  []int64
	listCalls int
}

type groupCapacityConcurrencyCacheStub struct {
	scheduler.ConcurrencyCache
	counts    map[int64]int
	requested []int64
}

type groupCapacitySessionCacheStub struct {
	scheduler.SessionLimitCache
	counts       map[int64]int
	requested    []int64
	idleTimeouts map[int64]time.Duration
}

type groupCapacityRPMCacheStub struct {
	scheduler.RPMCache
	counts    map[int64]int
	requested []int64
}

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

func (r testCapacityProviders) ListSchedulableByGroupID(ctx context.Context, id int64) ([]provider.CapacitySnapshot, error) {
	values, err := r.Repository.ListSchedulableByGroupID(ctx, id)
	if err != nil {
		return nil, err
	}
	return capacitySnapshots(ctx, values, r.Settings), nil
}

func (r testCapacityProviderBatch) ListSchedulableCapacityByGroupIDs(ctx context.Context, ids []int64) ([]routing.CapacityProviderRow, error) {
	values, err := r.Reader.ListSchedulableCapacityByGroupIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return capacityRows(ctx, values, r.Settings), nil
}

func newTestCapacityProviders(repo capacityFixtureRepository, settings capacitySettingsReader) routing.CapacityProviders {
	base := testCapacityProviders{Repository: repo, Settings: settings}
	if batch, ok := repo.(testtestCapacityProviderBatchReader); ok {
		return testCapacityProviderBatch{testCapacityProviders: base, Reader: batch}
	}
	return base
}

func (r testCapacityGroups) ListActiveIDs(ctx context.Context) ([]int64, error) {
	if actual, ok := r.GroupRepository.(interface {
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

func newTestGroupCapacityService(providers capacityFixtureRepository, groups routing.GroupRepository, concurrency *scheduler.ConcurrencyService, sessions scheduler.SessionLimitCache, rpm scheduler.RPMCache, settings capacitySettingsReader) *routing.CapacityService {
	var counters routing.CapacityConcurrency
	if concurrency != nil {
		counters = concurrency
	}
	return routing.NewCapacityService(newTestCapacityProviders(providers, settings), testCapacityGroups{groups}, counters, sessions, rpm)
}

func capacitySettings(ctx context.Context, reader capacitySettingsReader) provider.QuotaAutoPauseSettings {
	if reader != nil {
		return reader.GetOpenAIQuotaAutoPauseSettings(ctx)
	}
	return provider.QuotaAutoPauseSettings{}
}

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

func (s *groupCapacityProviderRepoStub) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]provider.Record, error) {
	out := make([]provider.Record, len(s.providers))
	copy(out, s.providers)
	return out, nil
}

func (s *groupCapacityProviderRepoStub) ListSchedulableCapacityByGroupIDs(_ context.Context, groupIDs []int64) ([]provider.GroupProviderCapacityRow, error) {
	s.requested = append([]int64(nil), groupIDs...)
	return append([]provider.GroupProviderCapacityRow(nil), s.rows...), nil
}

func (s groupCapacitySettingsStub) GetOpenAIQuotaAutoPauseSettings(ctx context.Context) provider.QuotaAutoPauseSettings {
	return s.settings
}

func (s *groupCapacityGroupRepoStub) ListActiveIDs(context.Context) ([]int64, error) {
	s.listCalls++
	return append([]int64(nil), s.groupIDs...), nil
}

func (s *groupCapacityConcurrencyCacheStub) GetProviderConcurrencyBatch(_ context.Context, providerIDs []int64) (map[int64]int, error) {
	s.requested = append([]int64(nil), providerIDs...)
	out := make(map[int64]int, len(providerIDs))
	for _, id := range providerIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

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

func (s *groupCapacityRPMCacheStub) GetRPMBatch(_ context.Context, providerIDs []int64) (map[int64]int, error) {
	s.requested = append([]int64(nil), providerIDs...)
	out := make(map[int64]int, len(providerIDs))
	for _, id := range providerIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}
