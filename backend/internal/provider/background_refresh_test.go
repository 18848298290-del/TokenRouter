package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// backgroundRefreshBlockingPager lets tests verify shared cancellation before dependencies are released.
type backgroundRefreshBlockingPager struct {
	entered  chan struct{}
	released atomic.Int32
	calls    atomic.Int32
}

type backgroundRefreshUnusedProvider struct{}

type refreshStartPager struct {
	calls   atomic.Int32
	entered chan struct{}
}

type tokenRefreshTestRefresher struct {
	err error
}

func (p *backgroundRefreshBlockingPager) ListOAuthRefreshCandidatePage(ctx context.Context, _ OAuthRefreshPageOptions) (*OAuthRefreshCandidatePage, error) {
	p.calls.Add(1)
	p.entered <- struct{}{}
	<-ctx.Done()
	p.released.Add(1)
	return nil, ctx.Err()
}

func (backgroundRefreshUnusedProvider) CanRefresh(*Record) bool { return true }

func (backgroundRefreshUnusedProvider) NeedsRefresh(*Record, time.Duration) bool { return true }

func (backgroundRefreshUnusedProvider) Refresh(context.Context, *Record) (map[string]any, error) {
	return nil, nil
}

func TestBackgroundRefreshStopsBothEntrypoints(t *testing.T) {
	pager := &backgroundRefreshBlockingPager{entered: make(chan struct{}, 2)}
	core := NewBackgroundRefreshService(BackgroundRefreshOptions{Tuning: &RefreshTuning{Enabled: true}, Pager: pager, Registrations: []RefreshRegistration{{Platform: PlatformGrok, Refresher: backgroundRefreshUnusedProvider{}}}, Reconciliation: GrokReconciliationOptions{Skew: time.Minute}})
	require.Zero(t, pager.calls.Load())
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

func (p *refreshStartPager) ListOAuthRefreshCandidatePage(ctx context.Context, _ OAuthRefreshPageOptions) (*OAuthRefreshCandidatePage, error) {
	p.calls.Add(1)
	p.entered <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestTokenRefreshStartIsIdempotent checks that repeated Start calls cannot launch concurrent initial scans.
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
	select {
	case <-p.entered:
	case <-time.After(30 * time.Millisecond):
	}
	require.Equal(t, int32(1), p.calls.Load(), "重复启动产生并行扫描")
}

func (r *tokenRefreshTestRefresher) CanRefresh(*Record) bool { return true }

func (r *tokenRefreshTestRefresher) NeedsRefresh(*Record, time.Duration) bool {
	return true
}

func (r *tokenRefreshTestRefresher) Refresh(context.Context, *Record) (map[string]any, error) {
	if r.err != nil {
		return nil, r.err
	}
	return map[string]any{"access_token": "new-access-token", "refresh_token": "new-refresh-token"}, nil
}
