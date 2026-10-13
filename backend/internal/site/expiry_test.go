package site

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// announcementExpiryRepoStub records archive counts and sampled scan timestamps for tests.
type announcementExpiryRepoStub struct {
	AnnouncementRepository // unused repository method set
	// calls counts every scan independently of called's capacity. called exposes
	// timestamps when space is available, so test observation cannot block the worker.
	calls  atomic.Int64   // observed archive count
	called chan time.Time // optional scan timestamps
}

// blockingExpiryRepository blocks archiving so tests can check shutdown waiting.
type blockingExpiryRepository struct {
	AnnouncementRepository // unused repository method set
	// entered reports that archiving began, while release lets the test complete
	// that call. The repository context can also terminate the held scan.
	// entered: archive start notification.
	// release: archive completion permission.
	entered, release chan struct{}
}

// ArchiveExpired counts a scan and publishes its timestamp when observation has capacity.
// A nil or full timestamp channel does not block the worker; the atomic
// counter remains available for checking calls after shutdown.
func (r *announcementExpiryRepoStub) ArchiveExpired(_ context.Context, now time.Time) (int64, error) {
	// Worker scans and test assertions progress independently. Count every call
	// atomically and publish timestamps without blocking so observation does not alter shutdown behavior.
	r.calls.Add(1)
	if r.called != nil {
		select {
		case r.called <- now:
		default:
		}
	}
	return 1, nil
}

// TestAnnouncementExpiryServiceRunOnceArchivesExpiredAnnouncements invokes one synchronous archive scan.
// It verifies that exactly one repository call receives a timestamp within
// the wall-clock interval bracketing that scan.
func TestAnnouncementExpiryServiceRunOnceArchivesExpiredAnnouncements(t *testing.T) {
	repo := &announcementExpiryRepoStub{called: make(chan time.Time, 1)}
	svc := NewAnnouncementExpiryService(repo, time.Minute)
	before := time.Now()

	svc.runOnce()

	archivedAt := <-repo.called
	require.EqualValues(t, 1, repo.calls.Load())
	require.False(t, archivedAt.Before(before))
	require.False(t, archivedAt.After(time.Now()))
}

// TestAnnouncementExpiryServiceStartRunsImmediatelyAndPeriodically starts a worker with a short interval.
// It observes immediate and later scans, then verifies the archive count stays
// unchanged across additional intervals after Stop returns.
func TestAnnouncementExpiryServiceStartRunsImmediatelyAndPeriodically(t *testing.T) {
	interval := 10 * time.Millisecond
	repo := &announcementExpiryRepoStub{called: make(chan time.Time, 8)}
	svc := NewAnnouncementExpiryService(repo, interval)
	svc.Start()
	t.Cleanup(svc.Stop)

	waitForAnnouncementExpiryCall(t, repo.called)
	waitForAnnouncementExpiryCall(t, repo.called)
	svc.Stop()

	stoppedCalls := repo.calls.Load()
	// Wait beyond several ticker opportunities after Stop returns. A surviving
	// worker would then change the counter, exposing an incomplete shutdown to this assertion.
	time.Sleep(3 * interval)
	require.Equal(t, stoppedCalls, repo.calls.Load())
}

// waitForAnnouncementExpiryCall waits for one observed scan or fails the calling test.
// Its one-second limit bounds synchronization with the worker, and Helper keeps
// a missing observation failure attributed to the owning test.
func waitForAnnouncementExpiryCall(t *testing.T, called <-chan time.Time) {
	t.Helper()
	// The worker must produce a timestamp to advance this scenario. Bound the
	// wait so a missing scan reports a test failure rather than holding the test indefinitely.
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("等待公告到期扫描超时")
	}
}

// ArchiveExpired reports scan entry and waits for release or context cancellation.
// The test controls normal completion through release, while cancellation
// returns the context error to exercise the worker's repository-call lifetime.
//
// entered closes before the completion wait so a Stop goroutine can be started
// while the scan is held. The release signal and context race through one
// select; the chosen branch determines whether that call completes normally or
// reports cancellation, with no separate worker state to clear afterward.
func (r *blockingExpiryRepository) ArchiveExpired(ctx context.Context, _ time.Time) (int64, error) {
	// Entry notification precedes the wait so the test can start Stop while
	// this repository call still owns the worker's in-progress scan.
	close(r.entered)
	select {
	case <-r.release:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// TestExpiryStopWaitsForScan holds the repository call while another goroutine invokes Stop.
// It verifies shutdown waits for release, then exercises repeated Start and
// Stop calls and a Start call after terminal shutdown.
func TestExpiryStopWaitsForScan(t *testing.T) {
	repo := &blockingExpiryRepository{entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewAnnouncementExpiryService(repo, time.Minute)
	svc.Start()
	svc.Start()
	<-repo.entered
	// Observe Stop completion independently while the scan is held. Release the
	// repository call only after confirming shutdown has not returned ahead of its worker.
	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("归档尚未完成")
	case <-time.After(20 * time.Millisecond):
	}
	close(repo.release)
	<-stopped
	svc.Stop()
	svc.Start()
}

// TestExpiryStopBeforeStart shuts down a worker before any Start call registers work.
// It verifies that later Start and repeated Stop calls leave the repository
// call count at zero.
func TestExpiryStopBeforeStart(t *testing.T) {
	repo := &announcementExpiryRepoStub{}
	svc := NewAnnouncementExpiryService(repo, time.Minute)
	svc.Stop()
	svc.Start()
	svc.Stop()
	require.Zero(t, repo.calls.Load())
}
