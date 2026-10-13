package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Expiration lifecycle fixtures block inside the repository after the immediate
// scan begins. They separate cancellation-aware scans from deliberately
// unfinished scans, checking permanent stop admission and retention of a first
// timeout after late completion.

// expiryLifecycleRepository exposes scan entry and completion controls for
// expiration lifecycle tests.
type expiryLifecycleRepository struct {
	calls atomic.Int32 // recorded scan calls
	// entered and release distinguish actual scan entry from forced completion.
	// ignoreCancel chooses whether the scan waits for release or acknowledges its
	// supplied context cancellation.
	entered, release chan struct{} // entered: first scan; release: forced completion
	ignoreCancel     bool          // external release required
}

// providerExpiryRepo reports an unexpected scan after a service has already
// stopped.
type providerExpiryRepo struct {
	// ExpiryRepository remains unset because the fixture exercises its overridden
	// scan alone; an unexpected inherited method would fail through the nil
	// interface.
	ExpiryRepository               // unused repository methods
	started          chan struct{} // unexpected scan signal
}

// AutoPauseExpiredProviders records a scan and signals the first entry before
// waiting for completion. The atomic call count lets the first scan close
// entered once even if callers overlap. Initialize entered before use.
//
// Cancellation-aware mode waits for ctx and returns its error. The ignoring
// mode waits for the initialized release channel and returns success after the
// test opens it. This deliberate unfinished operation lets the test distinguish
// worker completion from the service's cancellation request.
func (r *expiryLifecycleRepository) AutoPauseExpiredProviders(ctx context.Context, _ time.Time) (int64, error) {
	if r.calls.Add(1) == 1 {
		close(r.entered)
	}
	// The ignoring fixture waits for a test-owned release signal, making a
	// shutdown deadline observable while repository work is still active despite
	// context cancellation.
	if r.ignoreCancel {
		<-r.release
		return 0, nil
	}
	<-ctx.Done()
	return 0, ctx.Err()
}

// TestExpiryConstructStartAndStopBoundaries starts an idle constructor twice
// while its first scan waits for cancellation. It checks one repository call,
// successful repeated stops and permanently rejected restart.
func TestExpiryConstructStartAndStopBoundaries(t *testing.T) {
	repo := &expiryLifecycleRepository{entered: make(chan struct{})}
	svc := NewExpiryService(repo, ExpiryOptions{Interval: time.Hour})
	require.Zero(t, repo.calls.Load())
	svc.Start()
	svc.Start()
	<-repo.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.StopContext(ctx))
	require.Equal(t, int32(1), repo.calls.Load())
	require.NoError(t, svc.StopContext(context.Background()))
	svc.Start()
	require.Equal(t, int32(1), repo.calls.Load())
}

// TestExpiryStopBudgetDoesNotReportUnfinishedScanAsComplete holds a scan after
// it ignores cancellation. It checks the first stop returns DeadlineExceeded
// and that late worker completion leaves that result retained.
func TestExpiryStopBudgetDoesNotReportUnfinishedScanAsComplete(t *testing.T) {
	repo := &expiryLifecycleRepository{entered: make(chan struct{}), release: make(chan struct{}), ignoreCancel: true}
	svc := NewExpiryService(repo, ExpiryOptions{Interval: time.Hour})
	svc.Start()
	<-repo.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, svc.StopContext(ctx), context.DeadlineExceeded)
	// Release the repository only after the stop timeout is recorded; waiting for
	// runDone then establishes late completion before checking the retained error.
	close(repo.release)
	<-svc.runDone
	require.ErrorIs(t, svc.StopContext(context.Background()), context.DeadlineExceeded)
}

// AutoPauseExpiredProviders closes the fixture's started signal if an
// unexpected scan runs. Initialize that channel and invoke this override once;
// the stopped-service test treats any invocation as failed admission.
func (r *providerExpiryRepo) AutoPauseExpiredProviders(context.Context, time.Time) (int64, error) {
	close(r.started)
	return 0, nil
}

// TestProviderExpiryCannotRestartAfterStop stops the service before its first
// Start. It checks no repository-entry signal arrives during the observation
// window, protecting permanent rejection of later worker admission.
func TestProviderExpiryCannotRestartAfterStop(t *testing.T) {
	repo := &providerExpiryRepo{started: make(chan struct{})}
	svc := NewExpiryService(repo, ExpiryOptions{Interval: time.Hour})
	svc.Stop()
	svc.Start()
	defer svc.Stop()
	select {
	case <-repo.started:
		t.Fatal("stopped expiry worker executed another scan")
	case <-time.After(30 * time.Millisecond):
	}
}
