package provider

import (
	"context"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// openAICodexSnapshotAsyncRepo captures snapshot patches and unexpected
// provider-rate-limit writes for observer tests.
type openAICodexSnapshotAsyncRepo struct {
	// updateExtraCh optionally captures copied Extra patches from background
	// writes. A nil channel makes UpdateExtra finish without sending.
	updateExtraCh chan map[string]any
	// rateLimitCh optionally captures provider recovery deadlines. A nil channel
	// makes SetRateLimited finish without sending.
	rateLimitCh chan time.Time
}

// snapshotUpdateProviderRepo exposes copied Extra patches to quota-header
// observer assertions.
type snapshotUpdateProviderRepo struct {
	// updateExtraCalls optionally captures copied header-derived patches. A nil
	// channel skips capture, and tests buffer it for background writes.
	updateExtraCalls chan map[string]any
}

// SetRateLimited sends the observed reset time to the optional test channel. A
// nil channel discards it, letting tests detect any forbidden provider-limit
// side effect when configured.
func (r *openAICodexSnapshotAsyncRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	if r.rateLimitCh != nil {
		// Send the observed reset to the assertion channel so the snapshot tests can
		// detect an unexpected provider-wide limit from the background path.
		r.rateLimitCh <- resetAt
	}
	return nil
}

// UpdateExtra sends a shallow copy of the snapshot patch to the optional
// channel. A nil channel skips capture; buffered test channels let background
// persistence finish independently of assertions.
func (r *openAICodexSnapshotAsyncRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if r.updateExtraCh != nil {
		copied := make(map[string]any, len(updates))
		maps.Copy(copied, updates)
		// Publish the copied map to the assertion channel after capturing it, so the
		// test observes a complete patch from the asynchronous writer.
		r.updateExtraCh <- copied
	}
	return nil
}

// TestOpenAIGatewayService_UpdateCodexUsageSnapshot_ExhaustedSnapshotDoesNotSetRateLimit
// supplies exhausted weekly quota and a low-use short window. Extra must
// receive the snapshot while no provider-wide rate-limit write occurs.
func TestOpenAIGatewayService_UpdateCodexUsageSnapshot_ExhaustedSnapshotDoesNotSetRateLimit(t *testing.T) {
	repo := &openAICodexSnapshotAsyncRepo{
		updateExtraCh: make(chan map[string]any, 1),
		rateLimitCh:   make(chan time.Time, 1),
	}
	svc := &CodexUsageObserver{Store: repo, Throttle: providercore.NewWriteThrottle(30 * time.Second)}
	snapshot := &openai.OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:         snapshotFloat(100),
		PrimaryResetAfterSeconds:   snapshotInt(3600),
		PrimaryWindowMinutes:       snapshotInt(10080),
		SecondaryUsedPercent:       snapshotFloat(12),
		SecondaryResetAfterSeconds: snapshotInt(1200),
		SecondaryWindowMinutes:     snapshotInt(300),
	}
	svc.Observe(context.Background(), 601, snapshot)

	select {
	case updates := <-repo.updateExtraCh:
		require.Equal(t, 100.0, updates["codex_7d_used_percent"])
	case <-time.After(2 * time.Second):
		t.Fatal("等待 codex 快照落库超时")
	}

	select {
	case resetAt := <-repo.rateLimitCh:
		t.Fatalf("不应因仅写入快照而生成运行时限流时间: %v", resetAt)
	case <-time.After(2 * time.Second):
	}
}

// TestOpenAIGatewayService_UpdateCodexUsageSnapshot_NonExhaustedSnapshotDoesNotSetRateLimit
// supplies two nonexhausted windows. Snapshot persistence must complete while
// the observer leaves provider-wide rate-limit state untouched during the
// assertion interval.
func TestOpenAIGatewayService_UpdateCodexUsageSnapshot_NonExhaustedSnapshotDoesNotSetRateLimit(t *testing.T) {
	repo := &openAICodexSnapshotAsyncRepo{
		updateExtraCh: make(chan map[string]any, 1),
		rateLimitCh:   make(chan time.Time, 1),
	}
	svc := &CodexUsageObserver{Store: repo, Throttle: providercore.NewWriteThrottle(30 * time.Second)}
	snapshot := &openai.OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:         snapshotFloat(94),
		PrimaryResetAfterSeconds:   snapshotInt(3600),
		PrimaryWindowMinutes:       snapshotInt(10080),
		SecondaryUsedPercent:       snapshotFloat(22),
		SecondaryResetAfterSeconds: snapshotInt(1200),
		SecondaryWindowMinutes:     snapshotInt(300),
	}
	svc.Observe(context.Background(), 602, snapshot)

	select {
	case <-repo.updateExtraCh:
	case <-time.After(2 * time.Second):
		t.Fatal("等待 codex 快照落库超时")
	}

	select {
	case resetAt := <-repo.rateLimitCh:
		t.Fatalf("不应写入运行时限流时间: %v", resetAt)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestOpenAIGatewayService_UpdateCodexUsageSnapshot_ThrottlesExtraWrites
// observes one provider twice within its configured interval. The first patch
// must arrive and the second observation must produce no additional captured
// write.
func TestOpenAIGatewayService_UpdateCodexUsageSnapshot_ThrottlesExtraWrites(t *testing.T) {
	repo := &openAICodexSnapshotAsyncRepo{
		updateExtraCh: make(chan map[string]any, 2),
	}
	svc := &CodexUsageObserver{Store: repo, Throttle: providercore.NewWriteThrottle(time.Hour)}
	snapshot := &openai.OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:         snapshotFloat(94),
		PrimaryResetAfterSeconds:   snapshotInt(3600),
		PrimaryWindowMinutes:       snapshotInt(10080),
		SecondaryUsedPercent:       snapshotFloat(22),
		SecondaryResetAfterSeconds: snapshotInt(1200),
		SecondaryWindowMinutes:     snapshotInt(300),
	}

	svc.Observe(context.Background(), 777, snapshot)
	svc.Observe(context.Background(), 777, snapshot)

	select {
	case <-repo.updateExtraCh:
	case <-time.After(2 * time.Second):
		t.Fatal("等待第一次 codex 快照落库超时")
	}

	select {
	case updates := <-repo.updateExtraCh:
		t.Fatalf("unexpected second codex snapshot write: %v", updates)
	case <-time.After(200 * time.Millisecond):
	}
}

// UpdateExtra publishes a shallow-copied Extra patch to the optional capture
// channel. Nil channels skip capture, and callers provide enough buffering for
// the observer's background write to complete.
func (r *snapshotUpdateProviderRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	if r.updateExtraCalls != nil {
		copied := make(map[string]any, len(updates))
		maps.Copy(copied, updates)
		// Publish the copied map to the assertion channel after capturing it, so the
		// test observes a complete patch from the asynchronous writer.
		r.updateExtraCalls <- copied
	}
	return nil
}

// TestOpenAIUpdateCodexUsageSnapshotFromHeaders supplies Codex percentages,
// durations, and relative resets through HTTP headers. The observer must
// persist correctly associated five-hour and seven-day measurements in Extra.
func TestOpenAIUpdateCodexUsageSnapshotFromHeaders(t *testing.T) {
	repo := &snapshotUpdateProviderRepo{updateExtraCalls: make(chan map[string]any, 1)}
	svc := &CodexUsageObserver{Store: repo, Throttle: providercore.NewWriteThrottle(30 * time.Second)}
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "12")
	headers.Set("x-codex-secondary-used-percent", "34")
	headers.Set("x-codex-primary-window-minutes", "300")
	headers.Set("x-codex-secondary-window-minutes", "10080")
	headers.Set("x-codex-primary-reset-after-seconds", "600")
	headers.Set("x-codex-secondary-reset-after-seconds", "86400")

	svc.Headers(context.Background(), 123, headers)

	select {
	case updates := <-repo.updateExtraCalls:
		require.Equal(t, 12.0, updates["codex_5h_used_percent"])
		require.Equal(t, 34.0, updates["codex_7d_used_percent"])
		require.Equal(t, 600, updates["codex_5h_reset_after_seconds"])
		require.Equal(t, 86400, updates["codex_7d_reset_after_seconds"])
	case <-time.After(2 * time.Second):
		t.Fatal("expected UpdateExtra to be called")
	}
}

// snapshotFloat returns a fresh pointer to a floating-point quota measurement.
func snapshotFloat(v float64) *float64 { return &v }

// snapshotInt returns a fresh pointer to an integer quota measurement.
func snapshotInt(v int) *int { return &v }
