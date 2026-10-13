package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Upstream usage callers can cancel while a shared query remains active. A
// fixture execution signals when its independent context is cancelled, letting
// tests distinguish caller departure from service shutdown. A release-held
// variant delays the final return after cancellation, making unfinished shared
// work observable at the shutdown deadline.
//
// The first stop error must remain available after that deadline, and stopped
// admission must prevent later repository reads. Separate copy assertions edit
// nested amount, availability, subscription, and time values while checking the
// original scalars remain unchanged. Matching outer/internal balance references
// and nil versus present empty collections are also observed, limiting the alias
// claim to relationships actually asserted by this fixture.

// usageQueryReader supplies cloned provider snapshots and counts query lifecycle
// reads.
type usageQueryReader struct {
	// value is configured before use and kept stable while GetByID clones it. Tests
	// retain that source to inspect lifecycle reads without changing query
	// identity.
	value *Record
	reads atomic.Int32 // provider snapshot read count
}

// usageQueryExecution models shared-context cancellation with an optional
// independently blocked return.
type usageQueryExecution struct {
	// started signals one query entry and cancelled signals its context
	// cancellation. When ignore is true, release holds the callback afterward so
	// shutdown can time out before actual return. These channels are configured for
	// one invocation.
	started   chan struct{} // query entry notification signal
	cancelled chan struct{} // shared cancellation notification signal
	release   chan struct{} // test-controlled query release signal
	ignore    bool          // cancellation-delayed return fixture flag
	calls     atomic.Int32  // upstream callback invocation count
}

// GetByID counts a fixture read and returns CloneRecord of its stable configured
// source. CloneRecord detaches its supported values and retains its documented
// unsupported references; requested ID and context do not change this fixture
// selection.
func (r *usageQueryReader) GetByID(context.Context, int64) (*Record, error) {
	r.reads.Add(1)
	return CloneRecord(r.value), nil
}

// Available reports enabled execution for this upstream query cancellation
// fixture.
func (*usageQueryExecution) Available() bool { return true }

// Supports accepts every adapter name for this cancellation fixture.
func (*usageQueryExecution) Supports(string) bool { return true }

// BaseURL returns the fixed usage example root for fixture flight identity.
func (*usageQueryExecution) BaseURL(*Record) string { return "https://usage.example" }

// Query signals entry, waits for shared-context cancellation, and returns that
// context error. It increments the fixture call count and ignores provider and
// query configuration because these tests observe lifetime handling.
//
// After ctx is cancelled, cancelled closes so the test can observe delivery.
// With ignore enabled, release then holds the callback independently until the
// test closes it, leaving work registered beyond cancellation and the stop
// deadline. Each fixture uses its notification channels for one invocation; the
// shared query result remains pending until this callback returns.
func (e *usageQueryExecution) Query(ctx context.Context, _ *Record, _ UpstreamUsageQueryConfig) (*UpstreamUsageInfo, error) {
	e.calls.Add(1)
	// Signal actual callback entry before waiting for cancellation, then hold its
	// return separately when requested. Tests can distinguish cancelled context
	// delivery from completed shared execution.
	close(e.started)
	<-ctx.Done()
	close(e.cancelled)
	if e.ignore {
		<-e.release
	}
	return nil, ctx.Err()
}

// newUsageQueryTest constructs an idle service with an active OpenAI API Key
// fixture and independent lifetime signals. ignore selects release-held return
// after cancellation; callers stop the service and release that held callback
// for completion.
func newUsageQueryTest(t *testing.T, ignore bool) (*UpstreamUsageService, *usageQueryReader, *usageQueryExecution) {
	t.Helper()
	r := &usageQueryReader{value: &Record{ID: 1, Type: ProviderTypeAPIKey, Platform: PlatformOpenAI, Status: StatusActive}}
	e := &usageQueryExecution{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), ignore: ignore}
	return NewUpstreamUsageService(r, e, UpstreamUsageOptions{}), r, e
}

// TestUpstreamQueriesStopOwnsDetachedWork cancels a caller after its shared
// query enters execution. It verifies the query survives that cancellation, stop
// cancels and drains it, and later admission fails before additional provider
// reads.
func TestUpstreamQueriesStopOwnsDetachedWork(t *testing.T) {
	s, r, e := newUsageQueryTest(t, false)
	require.Zero(t, r.reads.Load())
	require.Zero(t, e.calls.Load())
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	// Wait for execution entry before cancelling the caller so the assertion
	// observes a shared query that has already detached its context and registered
	// its lifetime.
	go func() { _, err := s.QueryProvider(ctx, 1); result <- err }()
	waitUsageSignal(t, e.started)
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	select {
	case <-e.cancelled:
		t.Fatal("等待方取消提前终止共享查询")
	default:
	}
	// Bound the cooperative shutdown wait so missing completion accounting fails
	// this test instead of leaving it blocked indefinitely.
	stopCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	require.NoError(t, s.StopContext(stopCtx))
	waitUsageSignal(t, e.cancelled)
	require.NoError(t, s.StopContext(stopCtx))
	reads := r.reads.Load()
	_, err := s.QueryProvider(context.Background(), 1)
	require.ErrorIs(t, err, ErrUpstreamUsageStopped)
	require.Equal(t, reads, r.reads.Load())
}

// TestUpstreamQueriesStopTimeoutRemainsFailure holds shared execution after
// cancellation delivery. It requires an unfinished-work timeout and a cancelled
// caller wait, then releases the callback and verifies a later stop returns the
// identical first error.
func TestUpstreamQueriesStopTimeoutRemainsFailure(t *testing.T) {
	s, _, e := newUsageQueryTest(t, true)
	waiter := make(chan error, 1)
	// Keep the caller uncancelled while stopping the service so its returned
	// cancellation is attributable to the activity tracker rather than a
	// caller-owned cancel function.
	go func() { _, err := s.QueryProvider(context.Background(), 1); waiter <- err }()
	waitUsageSignal(t, e.started)
	// Expire the stop budget while release holds the shared callback, exposing
	// unfinished work even after its context cancellation has been delivered.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := s.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "upstream usage work remains unfinished")
	require.ErrorIs(t, <-waiter, context.Canceled)
	waitUsageSignal(t, e.cancelled)
	// Release the held callback after checking the unfinished stage. The following
	// stop reads the published error immediately, so this test does not await
	// callback completion there.
	close(e.release)
	require.Same(t, err, s.StopContext(context.Background()))
}

// TestUpstreamUsageResultCopiesAllValues edits nested numeric, boolean, and time
// values in a cloned observation. It verifies source scalar isolation, matching
// outer/internal balance alias, and distinct nil versus present empty balance
// collections.
func TestUpstreamUsageResultCopiesAllValues(t *testing.T) {
	now := time.Now()
	amount := 3.0
	flag := true
	usage := &UpstreamUsageInfo{Balance: &UpstreamUsageAmount{Remaining: &amount}, Balances: []UpstreamUsageBalanceEntry{}, Available: &flag, Limits: []UpstreamUsageLimit{{Name: "day", Used: &amount, ResetAt: &now}}, Subscription: &UpstreamUsageSubscription{PlanName: "test", Remaining: &amount, ExpiresAt: &now}, ExpiresAt: &now}
	source := &UpstreamUsageQueryResult{Usage: usage, Balance: usage.Balance, Balances: usage.Balances, Available: usage.Available, Limits: usage.Limits, Subscription: usage.Subscription, ExpiresAt: &now}
	// Reuse amount and time pointers across source fields, then edit separate
	// copied fields to check scalar isolation and the one outer/internal balance
	// alias asserted below.
	out := CloneUpstreamUsageResult(source)
	require.Equal(t, source, out)
	require.Same(t, out.Balance, out.Usage.Balance)
	*out.Balance.Remaining = 91
	*out.Limits[0].Used = 92
	*out.Limits[0].ResetAt = time.Time{}
	*out.Available = false
	*out.Subscription.Remaining = 93
	*out.ExpiresAt = time.Time{}
	require.Equal(t, 3.0, amount)
	require.True(t, flag)
	require.False(t, now.IsZero())
	require.Empty(t, out.Balances)
	require.NotNil(t, out.Balances)
	require.Nil(t, CloneUpstreamUsageResult(nil))
	require.Nil(t, CloneUpstreamUsageResult(&UpstreamUsageQueryResult{}).Balances)
}

// TestUpstreamQueriesNilCompatibility queries a nil service and reads its
// metrics. It verifies the unavailable error identity and an empty count map for
// these compatibility entry points.
func TestUpstreamQueriesNilCompatibility(t *testing.T) {
	var s *UpstreamUsageService
	_, err := s.QueryProvider(context.Background(), 1)
	require.True(t, errors.Is(err, ErrUpstreamUsageUnavailable))
	require.Empty(t, s.SnapshotMetrics().Counts)
}
