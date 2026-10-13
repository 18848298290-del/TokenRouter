package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// openAIUsageWriteOwner captures one conditional write while explicit release
// controls completion.
type openAIUsageWriteOwner struct {
	OAuthUsageReader // unused embedded reader operations
	// entered, release, and exited order capture, explicit completion, and cleanup
	// joining for one writer call. Cancellation cannot replace release, so tests
	// can inspect a timed-out stop before allowing the retained work to finish.
	// entered: completed capture signal; release: writer completion gate;
	// exited: completed writer signal.
	entered, release, exited chan struct{}
	// version retains the observer's supported-container identity copy. Tests read
	// it after entered and keep it stable until release permits writer completion.
	version UsageObservationVersion
	// updates retains the observer's supplied patch without fixture-side copying.
	// Entry signalling publishes it for assertions before the blocked write exits.
	updates map[string]any
}

// TestShouldRefreshOpenAICodexSnapshot supplies complete, missing-window,
// rate-limited, and stale WS observations. It checks the specified refresh
// decisions without asserting an upstream request, timestamp reservation, or
// persistence outcome.
func TestShouldRefreshOpenAICodexSnapshot(t *testing.T) {
	t.Parallel()

	rateLimitedUntil := time.Now().Add(5 * time.Minute)
	now := time.Now()
	usage := &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 0},
		SevenDay: &UsageProgress{Utilization: 0},
	}

	if !ShouldRefreshOpenAICodexSnapshot(&Record{RateLimitResetAt: &rateLimitedUntil}, usage, now) {
		t.Fatal("expected rate-limited provider to force codex snapshot refresh")
	}

	if ShouldRefreshOpenAICodexSnapshot(&Record{}, usage, now) {
		t.Fatal("expected complete non-rate-limited usage to skip codex snapshot refresh")
	}

	if !ShouldRefreshOpenAICodexSnapshot(&Record{}, &UsageInfo{FiveHour: nil, SevenDay: &UsageProgress{}}, now) {
		t.Fatal("expected missing 5h snapshot to require refresh")
	}

	staleAt := now.Add(-(OAuthUsageOpenAIProbeCacheTTL + time.Minute)).Format(time.RFC3339)
	if !ShouldRefreshOpenAICodexSnapshot(&Record{
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_enabled": true,
			"codex_usage_updated_at":                       staleAt,
		},
	}, usage, now) {
		t.Fatal("expected stale ws snapshot to trigger refresh")
	}
}

// TestShouldRefreshOpenAICodexSnapshot_SparkShadowIgnoresWSv2 varies
// stale/fresh shadow timestamps and a complete ordinary non-WS view. It checks
// shadow age eligibility without WS and ordinary capability gating for
// timestamp-only refresh.
func TestShouldRefreshOpenAICodexSnapshot_SparkShadowIgnoresWSv2(t *testing.T) {
	t.Parallel()

	now := time.Now()
	usage := &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 0},
		SevenDay: &UsageProgress{Utilization: 0},
	}
	staleAt := now.Add(-(OAuthUsageOpenAIProbeCacheTTL + time.Minute)).Format(time.RFC3339)
	freshAt := now.Add(-time.Minute).Format(time.RFC3339)
	parentID := int64(7001)

	// A complete shadow snapshot still needs refreshing once its timestamp
	// expires.
	shadowStale := &Record{
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &parentID,
		QuotaDimension:   QuotaDimensionSpark,
		Extra:            map[string]any{"codex_usage_updated_at": staleAt},
	}
	if !ShouldRefreshOpenAICodexSnapshot(shadowStale, usage, now) {
		t.Fatal("expected stale spark shadow (no WSv2) to trigger refresh")
	}

	// A shadow snapshot within the TTL can be reused.
	shadowFresh := &Record{
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &parentID,
		QuotaDimension:   QuotaDimensionSpark,
		Extra:            map[string]any{"codex_usage_updated_at": freshAt},
	}
	if ShouldRefreshOpenAICodexSnapshot(shadowFresh, usage, now) {
		t.Fatal("expected fresh spark shadow to skip refresh (TTL not elapsed)")
	}

	// Regular providers without upstream WS capability do not trigger this probe
	// on expiration alone.
	normalNoWS := &Record{
		Credentials: map[string]any{"upstream_protocols": []string{"openai_responses"}},
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Extra:       map[string]any{"codex_usage_updated_at": staleAt},
	}
	if ShouldRefreshOpenAICodexSnapshot(normalNoWS, usage, now) {
		t.Fatal("expected non-WSv2 normal provider to skip codex probe refresh")
	}
}

// UpdateUsageExtraIfUnchanged captures supplied version and patch, signals
// entry, then waits for explicit release. Tests use one call with initialized
// open channels and inspect captures only after entry.
//
// The fixture ignores cancellation while blocked, returning false with the
// eventual context error after release. Its captured maps remain borrowed from
// the observer arguments rather than cloned by this fixture. Closing exited at
// return lets cleanup join the writer, separating snapshot-copy assertions
// from actual conditional persistence and the service's bounded shutdown wait.
func (w *openAIUsageWriteOwner) UpdateUsageExtraIfUnchanged(ctx context.Context, v UsageObservationVersion, updates map[string]any) (bool, error) {
	// Publish captures before entry so the waiting test can inspect them without
	// racing assignment. Explicit release still controls completion even after
	// shutdown cancels the supplied writer context.
	w.version = v
	w.updates = updates
	close(w.entered)
	defer close(w.exited)
	<-w.release
	return false, ctx.Err()
}

// TestOAuthUsageOpenAIWritebackHasBoundedOwner blocks a detached writer before
// mutating source credential and nested maps. It checks captured supported
// values remain unchanged, stop times out with unfinished diagnostics, and
// repeated stop retains that error.
func TestOAuthUsageOpenAIWritebackHasBoundedOwner(t *testing.T) {
	store := &openAIUsageWriteOwner{entered: make(chan struct{}), release: make(chan struct{}), exited: make(chan struct{})}
	// Release and join the blocked writer during cleanup after bounded-stop
	// assertions. The writer return signal prevents its callback from remaining
	// blocked; observer activity cleanup follows that return in its goroutine.
	t.Cleanup(func() { close(store.release); <-store.exited })
	core := NewOAuthUsageService(store, nil, nil, OAuthUsageOptions{})
	observed := &Record{ID: 2, Credentials: map[string]any{"access_token": "observed"}}
	updates := map[string]any{"nested": map[string]any{"used": float64(1)}}
	// Wait for asynchronous capture before mutating the source maps, so assertions
	// distinguish observer-owned supported copies from the original caller data
	// without racing fixture capture assignments.
	core.PersistOpenAICodexProbeSnapshot(observed, updates)
	<-store.entered
	observed.Credentials["access_token"] = "changed"
	nested, ok := updates["nested"].(map[string]any)
	require.True(t, ok)
	nested["used"] = float64(2)
	require.Equal(t, "observed", store.version.Credentials["access_token"])
	copied, ok := store.updates["nested"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(1), copied["used"])
	// Bound stop while explicit release remains pending, exposing
	// unfinished work even after cancellation. Cleanup later releases actual
	// completion, while repeated stop must retain the first timeout result.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := core.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "unfinished")
	require.Same(t, err, core.StopContext(context.Background()))
}

// TestOpenAIUsesOneWindowQuery supplies stored windows with future reset times
// and a pair-statistics fixture. It checks one combined query, reset-based
// starts, no single queries, and independent short/weekly request totals after
// caller mutation.
func TestOpenAIUsesOneWindowQuery(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	reset5 := now.Add(time.Hour)
	reset7 := now.Add(24 * time.Hour)
	reader := &windowPairReader{}
	stats := NewLocalUsageStatistics(reader, NewOAuthUsageCache(), LocalUsageStatisticsOptions{Now: func() time.Time { return now }})
	service := NewOAuthUsageService(nil, nil, stats, OAuthUsageOptions{Now: func() time.Time { return now }})
	value, err := service.GetOpenAIUsage(context.Background(), &Record{ID: 1, Platform: PlatformOpenAI, Type: ProviderTypeOAuth, Extra: map[string]any{
		"codex_5h_used_percent": 10.0, "codex_7d_used_percent": 20.0,
		"codex_5h_reset_at": reset5.Format(time.RFC3339), "codex_7d_reset_at": reset7.Format(time.RFC3339),
	}}, false)
	require.NoError(t, err)
	require.Equal(t, 1, reader.pairs)
	require.Zero(t, reader.singles)
	require.Equal(t, []time.Time{reset5.Add(-5 * time.Hour), reset7.Add(-7 * 24 * time.Hour)}, reader.starts)
	value.FiveHour.WindowStats.Requests = 99
	require.EqualValues(t, 7, value.SevenDay.WindowStats.Requests)
}
