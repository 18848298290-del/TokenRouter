package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// grokReconcileBlockingPager exposes page entry and delayed exit for shutdown
// tests.
type grokReconcileBlockingPager struct {
	// entered, release, and exited delimit one page callback for these fixtures.
	// Tests wait for entry before stopping; release permits delayed completion,
	// and exited closes when the callback returns.
	entered, release, exited chan struct{} // page entry signal, explicit release gate, and page exit signal
	// ignoreCancel keeps the page blocked after ctx cancellation until release
	// closes. This fixture flag is set before callback use and remains stable
	// throughout the test.
	ignoreCancel bool         // use release instead of cancellation
	calls        atomic.Int32 // atomic page invocation count
}

// ListOAuthRefreshCandidatePage records entry, then returns only after
// cancellation or the configured explicit release. Each fixture supports one
// invocation because entered is closed on entry; deferred exit closure exposes
// actual callback completion.
func (p *grokReconcileBlockingPager) ListOAuthRefreshCandidatePage(ctx context.Context, _ OAuthRefreshPageOptions) (*OAuthRefreshCandidatePage, error) {
	p.calls.Add(1)
	// Signal accepted page entry before waiting so the test can stop only
	// after work is registered; deferred exit reports the callback's actual
	// return rather than cancellation delivery.
	close(p.entered)
	defer close(p.exited)
	if p.ignoreCancel {
		<-p.release
		return nil, ctx.Err()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestGrokReconciliationLifecycleCancelsAcceptedRequest starts a dry-run page
// read and stops after its entry barrier. It asserts cancellation reaches that
// callback, later admission is rejected, one page ran, and repeated stop
// succeeds.
func TestGrokReconciliationLifecycleCancelsAcceptedRequest(t *testing.T) {
	pager := &grokReconcileBlockingPager{entered: make(chan struct{}), exited: make(chan struct{})}
	core := NewGrokReconciliationService(GrokReconciliationOptions{Pager: pager, Skew: time.Minute})
	require.Zero(t, pager.calls.Load())
	done := make(chan error, 1)
	// Launch paging before observing its entry barrier, so stopping exercises
	// an already admitted callback rather than a runtime with no registered
	// page work.
	go func() {
		_, err := core.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{DryRun: true})
		done <- err
	}()
	<-pager.entered
	// Bound shutdown after pager entry so failed cancellation is reported
	// instead of leaving this lifecycle fixture waiting for page completion.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, core.StopContext(ctx))
	require.ErrorIs(t, <-done, context.Canceled)
	_, err := core.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{DryRun: true})
	require.ErrorIs(t, err, ErrRefreshStopped)
	require.Equal(t, int32(1), pager.calls.Load())
	require.NoError(t, core.StopContext(context.Background()))
}

// TestGrokReconciliationLifecycleReportsBlockedRead holds a page callback after
// cancellation until test cleanup releases it. It asserts bounded shutdown
// reports unfinished deadline error and subsequent stop returns that exact
// retained result.
func TestGrokReconciliationLifecycleReportsBlockedRead(t *testing.T) {
	pager := &grokReconcileBlockingPager{entered: make(chan struct{}), exited: make(chan struct{}), release: make(chan struct{}), ignoreCancel: true}
	done := make(chan error, 1)
	// Release cancellation-insensitive work after the stop assertions and join
	// its waiter during cleanup, preventing a surviving page operation after
	// this test returns.
	t.Cleanup(func() { close(pager.release); <-done })
	core := NewGrokReconciliationService(GrokReconciliationOptions{Pager: pager, Skew: time.Minute})
	// Launch paging before observing its entry barrier, so stopping exercises
	// an already admitted callback rather than a runtime with no registered
	// page work.
	go func() {
		_, err := core.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{DryRun: true})
		done <- err
	}()
	<-pager.entered
	// Keep release closed through this short stop budget. Cancellation alone
	// cannot finish the deliberately uncooperative page callback, so timeout
	// remains observable before cleanup permits its return.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := core.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "unfinished")
	require.Same(t, err, core.StopContext(context.Background()))
}
