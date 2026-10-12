package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Maintenance shutdown needs to cancel both periodic scan calls and
// administrative reconciliation before their pager returns. Blocking fixtures
// expose entry and completion counts, and a separate startup fixture keeps the
// initial scan busy so a repeated start can be checked for duplicate work.

// backgroundRefreshBlockingPager records two registered entry points while
// holding reads until cancellation.
type backgroundRefreshBlockingPager struct {
	// entered signals admitted page reads before they wait for cancellation.
	// Tests buffer both entry points or receive promptly so signaling can finish.
	entered  chan struct{}
	released atomic.Int32 // canceled read completion count
	calls    atomic.Int32 // observed page read count
}

// backgroundRefreshUnusedProvider admits every fixture candidate and supplies an
// empty successful exchange.
type backgroundRefreshUnusedProvider struct{}

// refreshStartPager holds the initial scan so repeated startup can be observed.
type refreshStartPager struct {
	calls atomic.Int32 // observed initial scan count
	// entered lets the test wait for initial paging before repeating startup;
	// its channel is buffered so the fixture can reach cancellation waiting.
	entered chan struct{}
}

// tokenRefreshTestRefresher supplies successful fixture credentials or an
// injected exchange error.
type tokenRefreshTestRefresher struct {
	err error // injected exchange failure
}

// ListOAuthRefreshCandidatePage signals each read and waits for its context to
// be canceled before returning. Tests provide a signal channel with capacity for
// both entry points and join completion before reading counters.
func (p *backgroundRefreshBlockingPager) ListOAuthRefreshCandidatePage(ctx context.Context, _ OAuthRefreshPageOptions) (*OAuthRefreshCandidatePage, error) {
	p.calls.Add(1)
	// Signal entry before awaiting shutdown so the test cancels only after
	// both callers are inside their reads, then counts actual completed waits.
	p.entered <- struct{}{}
	<-ctx.Done()
	p.released.Add(1)
	return nil, ctx.Err()
}

// CanRefresh admits every candidate supplied to the background shutdown fixture.
func (backgroundRefreshUnusedProvider) CanRefresh(*Record) bool { return true }

// NeedsRefresh marks every fixture candidate eligible for an exchange attempt.
func (backgroundRefreshUnusedProvider) NeedsRefresh(*Record, time.Duration) bool { return true }

// Refresh returns nil credentials and no error for the unused exchange fixture.
func (backgroundRefreshUnusedProvider) Refresh(context.Context, *Record) (map[string]any, error) {
	return nil, nil
}

// TestBackgroundRefreshStopsBothEntrypoints blocks a scan and Grok dry-run
// reconciliation inside the same pager. Shutdown cancels both, joins their
// exits, and prevents later entry points from increasing its call count.
func TestBackgroundRefreshStopsBothEntrypoints(t *testing.T) {
	pager := &backgroundRefreshBlockingPager{entered: make(chan struct{}, 2)}
	core := NewBackgroundRefreshService(BackgroundRefreshOptions{Tuning: &RefreshTuning{Enabled: true}, Pager: pager, Registrations: []RefreshRegistration{{Platform: PlatformGrok, Refresher: backgroundRefreshUnusedProvider{}}}, Reconciliation: GrokReconciliationOptions{Skew: time.Minute}})
	require.Zero(t, pager.calls.Load())
	// Run both entry points and join their separate results after stopping, so
	// pager completion counts represent exited calls rather than delivered
	// cancellation.
	scanDone := make(chan struct{})
	go func() { core.ScanCycle(context.Background()); close(scanDone) }()
	reconcileDone := make(chan error, 1)
	go func() {
		_, err := core.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{DryRun: true})
		reconcileDone <- err
	}()
	<-pager.entered
	<-pager.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, core.StopContext(ctx))
	<-scanDone
	require.ErrorIs(t, <-reconcileDone, context.Canceled)
	require.Equal(t, int32(2), pager.released.Load())
	require.NoError(t, core.StopContext(context.Background()))
	require.NoError(t, core.StartContext(context.Background()))
	core.ScanCycle(context.Background())
	_, err := core.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{})
	require.ErrorIs(t, err, ErrRefreshStopped)
	require.Equal(t, int32(2), pager.calls.Load())
}

// ListOAuthRefreshCandidatePage records entry and remains blocked until scan
// cancellation. Tests supply a usable signal channel and call service shutdown
// to let the initial scan return.
func (p *refreshStartPager) ListOAuthRefreshCandidatePage(ctx context.Context, _ OAuthRefreshPageOptions) (*OAuthRefreshCandidatePage, error) {
	p.calls.Add(1)
	// Signal before waiting so the repeated-start fixture reaches an active
	// initial scan; shutdown releases this wait before its cleanup completes.
	p.entered <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestTokenRefreshStartIsIdempotent starts maintenance while the initial pager
// call remains blocked. A second start leaves the observed call count at one
// during the fixture window, and cleanup joins the scan.
func TestTokenRefreshStartIsIdempotent(t *testing.T) {
	p := &refreshStartPager{entered: make(chan struct{}, 2)}
	s := NewBackgroundRefreshService(BackgroundRefreshOptions{Tuning: &RefreshTuning{Enabled: true}, Pager: p, Registrations: []RefreshRegistration{{Platform: PlatformOpenAI, Refresher: &tokenRefreshTestRefresher{}}}})
	defer func() { require.NoError(t, s.StopContext(context.Background())) }()
	require.Zero(t, p.calls.Load())
	require.NoError(t, s.StartContext(context.Background()))
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("首轮扫描未启动")
	}
	require.NoError(t, s.StartContext(context.Background()))
	// Allow a short second-entry window while the initial scan remains blocked,
	// so duplicate startup would increase the pager count observed below.
	select {
	case <-p.entered:
	case <-time.After(30 * time.Millisecond):
	}
	require.Equal(t, int32(1), p.calls.Load(), "重复启动产生并行扫描")
}

// CanRefresh admits every candidate used by the repeated-start fixture.
func (r *tokenRefreshTestRefresher) CanRefresh(*Record) bool { return true }

// NeedsRefresh marks every repeated-start fixture candidate as requiring
// credential exchange.
func (r *tokenRefreshTestRefresher) NeedsRefresh(*Record, time.Duration) bool {
	return true
}

// Refresh returns fixed fixture tokens unless an injected error is present. Each
// successful call creates a fresh top-level map, and the method ignores the
// supplied record and context.
func (r *tokenRefreshTestRefresher) Refresh(context.Context, *Record) (map[string]any, error) {
	if r.err != nil {
		return nil, r.err
	}
	return map[string]any{"access_token": "new-access-token", "refresh_token": "new-refresh-token"}, nil
}
