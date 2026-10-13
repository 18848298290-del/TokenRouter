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

// usageBatchRecordFixture retains serial source records and clones selected
// batch observations.
type usageBatchRecordFixture struct {
	OAuthUsageReader // unused embedded reader operations
	// providers retains source records with borrowed nested maps. Keep them stable
	// while GetByIDs copies supported containers for requested observation rows.
	providers []Record
}

// usageBatchStatisticsFixture returns independent empty statistics for window
// and daily queries.
type usageBatchStatisticsFixture struct{}

// usageFableWriteFixture publishes copied scalar-compatible patches for
// synchronous Fable assertions.
type usageFableWriteFixture struct {
	OAuthUsageReader // unused embedded reader operations
	// updates receives the copied patch before the synchronous fixture call
	// returns. Provide buffer capacity or a receiver, since context cancellation
	// does not interrupt this capture send.
	updates chan map[string]any
}

// activePassiveIdentityRepo compares current credentials before recording
// serial conditional observations.
type activePassiveIdentityRepo struct {
	sessionWindowSyncRepo // synchronized write argument capture
	// current is the serial stored identity used by comparison fixtures. Tests
	// replace its credential map before queries; comparison is not protected by
	// the embedded write-capture mutex.
	current Record
}

// oauthUsageLifecycleReader atomically counts ID reads for shared-query
// shutdown assertions.
type oauthUsageLifecycleReader struct {
	OAuthUsageReader              // unused embedded reader operations
	reads            atomic.Int32 // concurrent record read count
}

// sessionWindowSyncRepo protects captured metadata and reset writes with a
// fixture mutex.
type sessionWindowSyncRepo struct {
	OAuthUsageReader // unused embedded reader operations
	// mu protects extraUpdates and sessionWindowEnds during append and assertions.
	// Conditional identity fixtures compare current separately, so this capture
	// lock alone does not simulate an atomic version check with persistence.
	mu sync.Mutex // fixture capture mutex
	// extraUpdates holds shallow top-level patch copies created during capture.
	// Nested values stay borrowed and must remain stable when assertions inspect
	// those maps under mu.
	extraUpdates []map[string]any // captured metadata patch maps
	// captured session reset arguments
	sessionWindowEnds []sessionWindowEndCall
}

// sessionWindowEndCall captures a provider row and its accepted session reset.
type sessionWindowEndCall struct {
	ProviderID int64     // captured provider row identifier
	End        time.Time // captured session reset timestamp
}

// usageBatchRaceRepo supplies unsupported even rows while odd identifiers
// remain missing.
type usageBatchRaceRepo struct {
	OAuthUsageReader // unused embedded reader operations
}

// TestProviderUsageService_GetUsageBatch_BestEffortByProvider supplies
// Anthropic OAuth, OpenAI snapshot, and unsupported API-key rows with a
// duplicate requested ID. It checks passive Anthropic source, retained OpenAI
// percentage, and the unsupported row's collected error.
func TestProviderUsageService_GetUsageBatch_BestEffortByProvider(t *testing.T) {
	t.Parallel()

	// Align fixture reset text to whole seconds before display parsing.
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

// GetByIDs returns supported-container copies for matching requested IDs in
// input order. Missing IDs are omitted, source records remain stable during
// copying, and opaque unsupported values can retain their CloneRecord sharing
// behavior.
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

// GetProviderWindowStats returns a new empty local statistics observation for
// fixtures.
func (usageBatchStatisticsFixture) GetProviderWindowStats(context.Context, int64, time.Time) (*WindowStats, error) {
	return &WindowStats{}, nil
}

// GetProviderTodayStats returns a new empty daily statistics observation for
// fixtures.
func (usageBatchStatisticsFixture) GetProviderTodayStats(context.Context, int64) (*WindowStats, error) {
	return &WindowStats{}, nil
}

// TestSyncActiveToPassive_WritesFableExtras synchronizes a Fable percentage
// and reset through a capture channel. It checks the fractional Extra value,
// exact Unix-second reset, and presence of sampled time in the captured patch.
func TestSyncActiveToPassive_WritesFableExtras(t *testing.T) {
	repo := &usageFableWriteFixture{updates: make(chan map[string]any, 1)}
	svc := NewOAuthUsageService(repo, nil, nil, OAuthUsageOptions{})

	// Match whole-second reset storage when comparing the captured Unix value.
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
		// Allow floating representation error in percentage-to-fraction conversion.
		require.InDelta(t, 0.87, updates["passive_usage_7d_oi_utilization"], 1e-9)
		require.Equal(t, resetAt.Unix(), updates["passive_usage_7d_oi_reset"])
		require.Contains(t, updates, "passive_usage_sampled_at")
	default:
		t.Fatal("expected UpdateExtra to be called with fable extras")
	}
}

// UpdateUsageExtraIfUnchanged sends supported-container patch copies through
// the configured channel and reports acceptance. It ignores context/version
// comparison; callers provide capacity or a receiver so this synchronous
// fixture send can complete.
func (r *usageFableWriteFixture) UpdateUsageExtraIfUnchanged(_ context.Context, _ UsageObservationVersion, updates map[string]any) (bool, error) {
	// Capture supported values before sending so later source-map edits cannot
	// alter the fixture patch. The channel can still block independently of
	// context, and this fixture does not compare a stored identity.
	r.updates <- CloneValues(updates)
	return true, nil
}

// TestAnthropicNegativeUsageCacheDoesNotCrossCredentialIdentity prepares an
// old cached failure and a different credential with an already canceled
// context. It checks early context.Canceled admission, before cache identity
// matching or an active upstream query is reached.
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

// GetByID returns a shallow copy of the serial current fixture record. Nested
// credentials/Extra remain borrowed, provider ID and context are ignored, and
// tests keep shared values stable while inspecting conditional captures.
func (r *activePassiveIdentityRepo) GetByID(context.Context, int64) (*Record, error) {
	v := r.current
	return &v, nil
}

// TestAnthropicActiveUsageDoesNotWriteNewCredentialIdentity reuses a cached
// old-credential response after fixture storage credentials change. It checks
// the live query returns normally while the serial conditional fixture records
// neither Extra nor session-end writes.
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

// UpdateUsageExtraIfUnchanged compares current credential version before
// recording a patch. A mismatch returns false without capture; tests serialize
// current-record mutation because this comparison is outside the capture mutex
// and does not model atomic storage.
func (r *activePassiveIdentityRepo) UpdateUsageExtraIfUnchanged(ctx context.Context, v UsageObservationVersion, updates map[string]any) (bool, error) {
	// Compare before delegated capture, retaining a clean miss without writes.
	// The fixture separates this serial identity check from locking its captures,
	// so it does not establish database atomicity under concurrent replacement.
	if !MatchesCredentialVersion(&r.current, v.CredentialVersion) {
		return false, nil
	}
	return true, r.UpdateExtra(ctx, v.ID, updates)
}

// UpdateUsageSessionWindowEndIfUnchanged compares current credential version
// before recording the reset. Prior-end comparison is ignored; mismatch
// returns false, and tests serialize identity mutation because capture locking
// does not make this comparison atomic.
func (r *activePassiveIdentityRepo) UpdateUsageSessionWindowEndIfUnchanged(ctx context.Context, v UsageObservationVersion, _ *time.Time, end time.Time) (bool, error) {
	// Reject a changed credential before capture while ignoring prior-end checks.
	// This serial comparison is outside the capture mutex and cannot promise
	// atomic check/write behavior under a concurrent fixture identity replacement.
	if !MatchesCredentialVersion(&r.current, v.CredentialVersion) {
		return false, nil
	}
	return true, r.UpdateSessionWindowEnd(ctx, v.ID, end)
}

// TestAnthropicUsageNegativeCacheIdentityAndTTL repeats a failing query,
// replaces access credentials, then advances a fixed clock to the error TTL.
// It checks cached reuse, credential-triggered refetch, and another fetch at
// that exact age boundary.
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

// GetByID atomically counts a read and returns a new active Qoder fixture row.
// It ignores requested ID/context and supplies no real persistent credentials,
// letting lifecycle tests isolate callback ownership and shutdown admission.
func (r *oauthUsageLifecycleReader) GetByID(context.Context, int64) (*Record, error) {
	r.reads.Add(1)
	return &Record{ID: 1, Platform: PlatformQoder, Type: ProviderTypeOAuth, Status: StatusActive}, nil
}

// TestOAuthUsageLifecycleOwnsDetachedSharedQueries starts two callers on one
// blocked detached Qoder fetch. It checks five admitted activities,
// caller-cancellation survival, shutdown cancellation of both callers, one
// fetch, and later rejection before another read.
func TestOAuthUsageLifecycleOwnsDetachedSharedQueries(t *testing.T) {
	reader := &oauthUsageLifecycleReader{}
	cache := NewOAuthUsageCache()
	entered := make(chan struct{})
	var calls atomic.Int32
	var core *OAuthUsageService
	var detached context.Context
	// Signal the detached context before waiting so the controller can test that
	// caller cancellation leaves shared work alive, then release it through
	// service cancellation and join both outer query completions.
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
	// Start the first caller and await detached callback entry before admitting a
	// second caller, so both lifecycle/cancellation assertions refer to the same
	// already progressing row-shared upstream fetch.
	go func() { _, err := core.GetUsage(caller, 1); done <- err }()
	<-entered
	// Admit a peer before caller cancellation, then wait for both outer/dispatch
	// registrations plus detached ownership below. Its result channel joins both
	// callers after service shutdown interrupts their shared work.
	go func() { _, err := core.GetUsage(context.Background(), 1); done <- err }()
	// Observe five registrations under the activity mutex before cancellation:
	// two outer loads, two dispatch calls, and one detached fetch. This fixes the
	// peer dispatch admission; joining the row flight can follow that observation.
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

// TestOAuthUsageLifecycleReportsUnfinishedProvider holds a Qoder callback past
// cancellation until explicit release. It checks bounded unfinished-work stop,
// joins the outer request after release, and retains the same first timeout on
// repeated stop.
func TestOAuthUsageLifecycleReportsUnfinishedProvider(t *testing.T) {
	reader := &oauthUsageLifecycleReader{}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	// Hold the shared callback on explicit release rather than context, allowing
	// the test to observe a bounded unfinished-work stop before releasing its
	// actual completion and the outer request.
	core := NewOAuthUsageService(reader, nil, nil, OAuthUsageOptions{Qoder: QoderUsageOptions{Fetch: func(context.Context, *Record, func() time.Time) (*UsageInfo, error) {
		close(entered)
		<-release
		return &UsageInfo{}, nil
	}}})
	// Dispatch and await callback entry before bounded stop. Explicit release
	// controls completion despite cancellation, and joining done afterward keeps
	// the deliberately held request within this test lifetime.
	go func() { defer close(done); _, _ = core.GetUsage(context.Background(), 1) }()
	<-entered
	// Bound shutdown while the fetch still waits for release, so timeout reports
	// unfinished ownership separately from already delivered cancellation. The
	// test then releases and joins completion before checking retained stop error.
	budget, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := core.StopContext(budget)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "unfinished")
	close(release)
	<-done
	require.Same(t, err, core.StopContext(context.Background()))
}

// UpdateExtra locks fixture capture and appends a fresh top-level patch map.
// Nested values remain borrowed through maps.Copy, context/row are ignored,
// and assertions acquire the same mutex before inspecting the retained slice.
func (r *sessionWindowSyncRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	// Serialize capture append with test inspection so batch/synchronization
	// callbacks cannot race slice access. Only top-level map entries are copied
	// below; nested sharing remains a separate fixture ownership rule.
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make(map[string]any, len(updates))
	maps.Copy(copied, updates)
	r.extraUpdates = append(r.extraUpdates, copied)
	return nil
}

// UpdateSessionWindowEnd appends scalar row/reset arguments under the fixture
// mutex and reports success. It ignores context and performs no storage or
// previous-end check; assertions use the same capture lock.
func (r *sessionWindowSyncRepo) UpdateSessionWindowEnd(_ context.Context, id int64, end time.Time) error {
	// Serialize reset capture with assertions through the shared fixture mutex,
	// publishing complete scalar arguments before another reader inspects the
	// accumulated call slice.
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionWindowEnds = append(r.sessionWindowEnds, sessionWindowEndCall{ProviderID: id, End: end})
	return nil
}

// TestSyncActiveToPassive_WritesFiveHourSessionWindowEnd synchronizes a
// FiveHour view with a future reset. It checks one captured session-end write
// carries the expected row and exact reset time, without asserting Extra
// persistence contents.
func TestSyncActiveToPassive_WritesFiveHourSessionWindowEnd(t *testing.T) {
	t.Parallel()

	repo := &sessionWindowSyncRepo{}
	svc := NewOAuthUsageService(repo, nil, nil, OAuthUsageOptions{})
	// Match reset text precision before exact session-end comparison.
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

// TestSyncActiveToPassive_SkipsSessionWindowEndWhenResetMissing synchronizes
// FiveHour utilization without reset timing. It checks no session-end call is
// captured for that view while leaving its Extra-write outcome outside these
// assertions.
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

// UpdateUsageExtraIfUnchanged reports acceptance and delegates patch capture
// without comparing identity. Tests use it as a successful synchronization
// dependency, with the delegated method owning locking and shallow top-level
// copying.
func (r *sessionWindowSyncRepo) UpdateUsageExtraIfUnchanged(ctx context.Context, v UsageObservationVersion, updates map[string]any) (bool, error) {
	return true, r.UpdateExtra(ctx, v.ID, updates)
}

// UpdateUsageSessionWindowEndIfUnchanged reports acceptance and captures the
// supplied row/reset without identity or prior-end comparison. Tests use its
// delegated mutex-protected arguments rather than simulate conditional storage
// behavior.
func (r *sessionWindowSyncRepo) UpdateUsageSessionWindowEndIfUnchanged(ctx context.Context, v UsageObservationVersion, _ *time.Time, end time.Time) (bool, error) {
	return true, r.UpdateSessionWindowEnd(ctx, v.ID, end)
}

// GetByIDs returns new unsupported API-key rows for even requested IDs and
// omits odd IDs. It ignores context and reports success, letting the batch
// combine missing-row errors with concurrent worker failures.
func (usageBatchRaceRepo) GetByIDs(_ context.Context, ids []int64) ([]*Record, error) {
	out := make([]*Record, 0, len(ids)/2)
	for _, id := range ids {
		if id%2 == 0 {
			out = append(out, &Record{ID: id, Type: capability.ProviderTypeAPIKey})
		}
	}
	return out, nil
}

// TestUsageBatchMissingAndFailedProvidersShareSafeResultMap supplies 601
// positive IDs with alternating missing/unsupported rows. It checks a nil
// batch error, empty successful values, and one collected failure per
// requested ID during bounded concurrent dispatch.
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
