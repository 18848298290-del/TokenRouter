package provider

import (
	"context"
	"errors"
	"log"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// usageBatchRecordFixture returns provider records for requested IDs.
type usageBatchRecordFixture struct {
	OAuthUsageReader
	providers []Record
}

// usageBatchStatisticsFixture supplies empty local statistics to usage scenarios.
type usageBatchStatisticsFixture struct{}

// usageFableWriteFixture captures the updates produced by active-to-passive synchronization.
type usageFableWriteFixture struct {
	OAuthUsageReader
	updates chan map[string]any
}

// activePassiveIdentityRepo rejects writes after the observed credential identity changes.
type activePassiveIdentityRepo struct {
	sessionWindowSyncRepo
	current Record
}

// oauthUsageLifecycleReader counts record reads during shared-query shutdown tests.
type oauthUsageLifecycleReader struct {
	OAuthUsageReader
	reads atomic.Int32
}

// sessionWindowSyncRepo records writes triggered by SyncActiveToPassive.
type sessionWindowSyncRepo struct {
	OAuthUsageReader

	mu                sync.Mutex
	extraUpdates      []map[string]any
	sessionWindowEnds []sessionWindowEndCall
}

type sessionWindowEndCall struct {
	ProviderID int64
	End        time.Time
}

// usageBatchRaceRepo mixes missing and unsupported providers to exercise concurrent error collection.
type usageBatchRaceRepo struct{ OAuthUsageReader }

func TestProviderUsageService_GetUsageBatch_BestEffortByProvider(t *testing.T) {
	t.Parallel()

	resetAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	repo := &usageBatchRecordFixture{
		providers: []Record{
			{
				ID:       7001,
				Platform: capability.PlatformAnthropic,
				Type:     capability.ProviderTypeOAuth,
				Extra: map[string]any{
					"passive_usage_7d_utilization": 0.62,
				},
			},
			{
				ID:       7002,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Extra: map[string]any{
					"codex_usage_updated_at":  time.Now().UTC().Format(time.RFC3339),
					"codex_5h_used_percent":   18.0,
					"codex_5h_reset_at":       resetAt.Format(time.RFC3339),
					"codex_7d_used_percent":   34.0,
					"codex_7d_reset_at":       resetAt.Add(24 * time.Hour).Format(time.RFC3339),
					"workspace_id":            "org-test",
					"chatgpt_account_id":      "acct-test",
					"openai_snapshot_version": "test",
				},
			},
			{
				ID:       7003,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeAPIKey,
			},
		},
	}

	cache := NewOAuthUsageCache()
	stats := NewLocalUsageStatistics(usageBatchStatisticsFixture{}, cache, LocalUsageStatisticsOptions{Now: time.Now, Today: func() time.Time { return time.Now().Truncate(24 * time.Hour) }, Log: log.Printf})
	svc := NewOAuthUsageService(repo, cache, stats, OAuthUsageOptions{OpenAIQuotaPause: func(_ context.Context, value *Record, info *UsageInfo) {
		info.QuotaAutoPaused, _ = EvaluateQuotaAutoPause(value.Platform, value.Extra, QuotaAutoPauseSettings{}, time.Now())
	}})

	usageByProvider, errorsByProvider, err := svc.GetUsageBatch(context.Background(), []int64{7001, 7002, 7003, 7002}, false)
	if err != nil {
		t.Fatalf("GetUsageBatch() error = %v", err)
	}

	if usageByProvider[7001] == nil || usageByProvider[7001].Source != "passive" {
		t.Fatalf("expected anthropic passive usage, got %#v", usageByProvider[7001])
	}

	if usageByProvider[7002] == nil || usageByProvider[7002].FiveHour == nil || usageByProvider[7002].FiveHour.Utilization != 18.0 {
		t.Fatalf("expected openai snapshot usage, got %#v", usageByProvider[7002])
	}

	if !strings.Contains(strings.ToLower(errorsByProvider[7003]), "does not support usage query") {
		t.Fatalf("expected API key provider error to be preserved, got %q", errorsByProvider[7003])
	}
}

func (r usageBatchRecordFixture) GetByIDs(_ context.Context, ids []int64) ([]*Record, error) {
	result := make([]*Record, 0, len(ids))
	for _, id := range ids {
		for index := range r.providers {
			if r.providers[index].ID == id {
				result = append(result, CloneRecord(&r.providers[index]))
				break
			}
		}
	}
	return result, nil
}

func (usageBatchStatisticsFixture) GetProviderWindowStats(context.Context, int64, time.Time) (*WindowStats, error) {
	return &WindowStats{}, nil
}

func (usageBatchStatisticsFixture) GetProviderTodayStats(context.Context, int64) (*WindowStats, error) {
	return &WindowStats{}, nil
}

func TestSyncActiveToPassive_WritesFableExtras(t *testing.T) {
	repo := &usageFableWriteFixture{updates: make(chan map[string]any, 1)}
	svc := NewOAuthUsageService(repo, nil, nil, OAuthUsageOptions{})

	resetAt := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	usage := &UsageInfo{
		SevenDayFable: &UsageProgress{
			Utilization: 87,
			ResetsAt:    &resetAt,
		},
	}

	svc.SyncActiveToPassive(t.Context(), &Record{ID: 1}, usage)

	select {
	case updates := <-repo.updates:
		require.InDelta(t, 0.87, updates["passive_usage_7d_oi_utilization"], 1e-9)
		require.Equal(t, resetAt.Unix(), updates["passive_usage_7d_oi_reset"])
		require.Contains(t, updates, "passive_usage_sampled_at")
	default:
		t.Fatal("expected UpdateExtra to be called with fable extras")
	}
}

func (r *usageFableWriteFixture) UpdateUsageExtraIfUnchanged(_ context.Context, _ UsageObservationVersion, updates map[string]any) (bool, error) {
	r.updates <- CloneValues(updates)
	return true, nil
}

func TestAnthropicNegativeUsageCacheDoesNotCrossCredentialIdentity(t *testing.T) {
	oldErr := errors.New("old identity failed")
	cache := NewOAuthUsageCache()
	svc := NewOAuthUsageService(nil, cache, nil, OAuthUsageOptions{})
	cache.StoreAPI(int64(886), &OAuthAPIUsageCache{Identity: "old identity", Err: oldErr, Timestamp: time.Now()})
	a := &Record{ID: 886, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "second"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.GetUsageForProvider(ctx, a, false)
	require.ErrorIs(t, err, context.Canceled)
}

func (r *activePassiveIdentityRepo) GetByID(context.Context, int64) (*Record, error) {
	v := r.current
	return &v, nil
}

func TestAnthropicActiveUsageDoesNotWriteNewCredentialIdentity(t *testing.T) {
	a := Record{ID: 887, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "old"}}
	repo := &activePassiveIdentityRepo{current: a}
	repo.current.Credentials = map[string]any{"access_token": "administrator"}
	cache := NewOAuthUsageCache()
	stats := NewLocalUsageStatistics(usageBatchStatisticsFixture{}, cache, LocalUsageStatisticsOptions{Now: time.Now, Log: log.Printf})
	svc := NewOAuthUsageService(repo, cache, stats, OAuthUsageOptions{})
	response := &ClaudeUsageResponse{}
	response.FiveHour.Utilization = 31
	response.FiveHour.ResetsAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	cache.StoreAPI(a.ID, &OAuthAPIUsageCache{Identity: UsageCacheIdentity(&a), Response: response, Timestamp: time.Now()})
	_, err := svc.GetUsageForProvider(context.Background(), &a, false)
	require.NoError(t, err)
	require.Empty(t, repo.extraUpdates)
	require.Empty(t, repo.sessionWindowEnds)
}

// UpdateUsageExtraIfUnchanged models credential checks before recording a write.
func (r *activePassiveIdentityRepo) UpdateUsageExtraIfUnchanged(ctx context.Context, v UsageObservationVersion, updates map[string]any) (bool, error) {
	if !MatchesCredentialVersion(&r.current, v.CredentialVersion) {
		return false, nil
	}
	return true, r.UpdateExtra(ctx, v.ID, updates)
}

func (r *activePassiveIdentityRepo) UpdateUsageSessionWindowEndIfUnchanged(ctx context.Context, v UsageObservationVersion, _ *time.Time, end time.Time) (bool, error) {
	if !MatchesCredentialVersion(&r.current, v.CredentialVersion) {
		return false, nil
	}
	return true, r.UpdateSessionWindowEnd(ctx, v.ID, end)
}

// TestAnthropicUsageNegativeCacheIdentityAndTTL checks error-cache invalidation on credential changes and
// expiry.
func TestAnthropicUsageNegativeCacheIdentityAndTTL(t *testing.T) {
	now := time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC)
	cache := NewOAuthUsageCache()
	var calls int
	marker := errors.New("provider failure")
	core := NewOAuthUsageService(nil, cache, nil, OAuthUsageOptions{Now: func() time.Time { return now }, Anthropic: func(context.Context, *Record) (*ClaudeUsageResponse, error) { calls++; return nil, marker }})
	old := &Record{ID: 8, Platform: PlatformAnthropic, Type: ProviderTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "old"}}
	_, err := core.GetUsageForProvider(context.Background(), old, false)
	require.ErrorIs(t, err, marker)
	_, err = core.GetUsageForProvider(context.Background(), old, false)
	require.ErrorIs(t, err, marker)
	require.Equal(t, 1, calls)
	fresh := CloneRecord(old)
	fresh.Credentials = map[string]any{"access_token": "new"}
	_, err = core.GetUsageForProvider(context.Background(), fresh, false)
	require.ErrorIs(t, err, marker)
	require.Equal(t, 2, calls)
	now = now.Add(OAuthUsageAPIErrorCacheTTL)
	_, err = core.GetUsageForProvider(context.Background(), fresh, false)
	require.ErrorIs(t, err, marker)
	require.Equal(t, 3, calls)
}

func (r *oauthUsageLifecycleReader) GetByID(context.Context, int64) (*Record, error) {
	r.reads.Add(1)
	return &Record{ID: 1, Platform: PlatformQoder, Type: ProviderTypeOAuth, Status: StatusActive}, nil
}

func TestOAuthUsageLifecycleOwnsDetachedSharedQueries(t *testing.T) {
	reader := &oauthUsageLifecycleReader{}
	cache := NewOAuthUsageCache()
	entered := make(chan struct{})
	var calls atomic.Int32
	var core *OAuthUsageService
	var detached context.Context
	options := OAuthUsageOptions{Qoder: QoderUsageOptions{Fetch: func(ctx context.Context, _ *Record, _ func() time.Time) (*UsageInfo, error) {
		calls.Add(1)
		detached = ctx
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}}
	core = NewOAuthUsageService(reader, cache, nil, options)
	require.Zero(t, calls.Load())
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	go func() { _, err := core.GetUsage(caller, 1); done <- err }()
	<-entered
	go func() { _, err := core.GetUsage(context.Background(), 1); done <- err }()
	require.Eventually(t, func() bool {
		core.activity.mu.Lock()
		defer core.activity.mu.Unlock()
		return len(core.activity.active) == 5
	}, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, detached.Err(), "调用方取消不得终止原共享查询")
	budget, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	require.NoError(t, core.StopContext(budget))
	for range 2 {
		require.ErrorIs(t, <-done, context.Canceled)
	}
	require.Equal(t, int32(1), calls.Load())
	reads := reader.reads.Load()
	_, err := core.GetUsage(context.Background(), 1)
	require.ErrorIs(t, err, ErrOAuthUsageStopped)
	require.Equal(t, reads, reader.reads.Load())
	require.NoError(t, core.StopContext(context.Background()))
}

func TestOAuthUsageLifecycleReportsUnfinishedProvider(t *testing.T) {
	reader := &oauthUsageLifecycleReader{}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	core := NewOAuthUsageService(reader, nil, nil, OAuthUsageOptions{Qoder: QoderUsageOptions{Fetch: func(context.Context, *Record, func() time.Time) (*UsageInfo, error) {
		close(entered)
		<-release
		return &UsageInfo{}, nil
	}}})
	go func() { defer close(done); _, _ = core.GetUsage(context.Background(), 1) }()
	<-entered
	budget, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := core.StopContext(budget)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "unfinished")
	close(release)
	<-done
	require.Same(t, err, core.StopContext(context.Background()))
}

func (r *sessionWindowSyncRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make(map[string]any, len(updates))
	maps.Copy(copied, updates)
	r.extraUpdates = append(r.extraUpdates, copied)
	return nil
}

func (r *sessionWindowSyncRepo) UpdateSessionWindowEnd(_ context.Context, id int64, end time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionWindowEnds = append(r.sessionWindowEnds, sessionWindowEndCall{ProviderID: id, End: end})
	return nil
}

func TestSyncActiveToPassive_WritesFiveHourSessionWindowEnd(t *testing.T) {
	t.Parallel()

	repo := &sessionWindowSyncRepo{}
	svc := NewOAuthUsageService(repo, nil, nil, OAuthUsageOptions{})
	resetsAt := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	svc.SyncActiveToPassive(context.Background(), &Record{ID: 42}, &UsageInfo{
		FiveHour: &UsageProgress{
			Utilization: 53,
			ResetsAt:    &resetsAt,
		},
	})

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.sessionWindowEnds) != 1 {
		t.Fatalf("expected 1 UpdateSessionWindowEnd call, got %d", len(repo.sessionWindowEnds))
	}
	call := repo.sessionWindowEnds[0]
	if call.ProviderID != 42 {
		t.Fatalf("expected ProviderID=42, got %d", call.ProviderID)
	}
	if !call.End.Equal(resetsAt) {
		t.Fatalf("expected End=%v, got %v", resetsAt, call.End)
	}
}

func TestSyncActiveToPassive_SkipsSessionWindowEndWhenResetMissing(t *testing.T) {
	t.Parallel()

	repo := &sessionWindowSyncRepo{}
	svc := NewOAuthUsageService(repo, nil, nil, OAuthUsageOptions{})
	svc.SyncActiveToPassive(context.Background(), &Record{ID: 99}, &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 10},
	})

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.sessionWindowEnds) != 0 {
		t.Fatalf("expected no UpdateSessionWindowEnd calls when ResetsAt is nil, got %d", len(repo.sessionWindowEnds))
	}
}

// UpdateUsageExtraIfUnchanged records updates as an accepted conditional write.
func (r *sessionWindowSyncRepo) UpdateUsageExtraIfUnchanged(ctx context.Context, v UsageObservationVersion, updates map[string]any) (bool, error) {
	return true, r.UpdateExtra(ctx, v.ID, updates)
}

func (r *sessionWindowSyncRepo) UpdateUsageSessionWindowEndIfUnchanged(ctx context.Context, v UsageObservationVersion, _ *time.Time, end time.Time) (bool, error) {
	return true, r.UpdateSessionWindowEnd(ctx, v.ID, end)
}

func (usageBatchRaceRepo) GetByIDs(_ context.Context, ids []int64) ([]*Record, error) {
	out := make([]*Record, 0, len(ids)/2)
	for _, id := range ids {
		if id%2 == 0 {
			out = append(out, &Record{ID: id, Type: capability.ProviderTypeAPIKey})
		}
	}
	return out, nil
}

func TestUsageBatchMissingAndFailedProvidersShareSafeResultMap(t *testing.T) {
	ids := make([]int64, 601)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	svc := NewOAuthUsageService(usageBatchRaceRepo{}, NewOAuthUsageCache(), nil, OAuthUsageOptions{})
	values, failures, err := svc.GetUsageBatch(context.Background(), ids, false)
	require.NoError(t, err)
	require.Empty(t, values)
	require.Len(t, failures, len(ids))
}
