package routing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Probe shutdown changes whether later ticks can enter the repository and
// whether an active claim can finish. The controllable scheduler exposes its
// recorded callback directly, and the claim fixtures let tests choose between
// a cooperative context wait and work that ignores cancellation.
//
// The tests wait for claim entry before stopping a runner so shutdown
// observes active claim work. Completion channels join the launched round,
// and the uncancellable fixture is released after the deadline result is
// captured so the test leaves no blocked goroutine. Assertions cover one
// registration attempt, one scheduler stop, rejected later admission, and the
// waiting error when a claim remains unfinished.

// probeScheduleStub records sequential scheduler calls and retains the
// callback supplied to Start.
type probeScheduleStub struct {
	starts int // recorded schedule start count
	stops  int // recorded schedule stop count
	// run is assigned by every Start call, including a failing one, so tests can
	// invoke the retained callback directly.
	run func() // captured scheduled round callback
	// err is returned after Start records its attempt and callback, so an
	// injected registration failure still leaves both observations available.
	err error // injected schedule start error
}

// probeExecutorStub supplies a fixed provider and successful execution for
// lifecycle setup.
type probeExecutorStub struct{}

// uncancellableProbeRepo holds a claim until the test releases it,
// irrespective of context cancellation. Its start signal lets the test
// establish active work before imposing a shutdown wait budget.
type uncancellableProbeRepo struct {
	groupAvailabilityProbeRunnerRepoStub // unused repository operation implementations
	// started and release bracket the deliberately uncooperative claim. ClaimDue
	// closes started once and waits for release; the test closes release after
	// observing shutdown's result.
	started chan struct{} // uncancellable claim entry signal
	release chan struct{} // test-controlled claim exit gate
	// once protects the started channel from repeated closure if the fixture
	// receives another claim request.
	once sync.Once // claim entry closure guard
}

// Start records a registration attempt and retains the supplied callback,
// then returns the configured error. Tests call it sequentially through
// runner startup and invoke the callback themselves.
func (s *probeScheduleStub) Start(run func()) error { s.starts++; s.run = run; return s.err }

// Stop records a dispatch-stop request and returns an already-canceled
// background context. That completion signal lets lifecycle tests exercise
// runner work waiting without a real scheduler callback still running.
func (s *probeScheduleStub) Stop() context.Context {
	s.stops++
	// Return a completed scheduling signal immediately so the runner's shutdown
	// wait concerns its active repository round rather than an external timer.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// Select supplies provider 1 with the requested model as its execution
// target. It succeeds for every input so lifecycle tests can construct a
// runner with an available executor.
func (probeExecutorStub) Select(_ context.Context, _ GroupAvailabilityProbeDueGroup, model string) (GroupProbeTarget, error) {
	return GroupProbeTarget{ProviderID: 1, ModelID: model}, nil
}

// Test returns successful status with zero-valued execution metadata for
// every provider request. Lifecycle assertions use registration and claim
// observations, so this fixture supplies no measured upstream timing.
func (probeExecutorStub) Test(context.Context, GroupProbeTarget, string, string) (*ProbeExecutionResult, error) {
	return &ProbeExecutionResult{Status: GroupAvailabilityProbeStatusSuccess}, nil
}

// TestProbeStopPreventsLaterStart stops an idle runner before attempting
// registration. Construction and the later Start leave the schedule
// untouched, and a repeated StopContext completes successfully.
func TestProbeStopPreventsLaterStart(t *testing.T) {
	schedule := &probeScheduleStub{}
	runner := NewGroupAvailabilityProbeRunnerService(&groupAvailabilityProbeRunnerRepoStub{}, probeExecutorStub{}, GroupProbeOptions{Schedule: schedule})
	require.Zero(t, schedule.starts)
	runner.Stop()
	runner.Start()
	require.Zero(t, schedule.starts)
	require.NoError(t, runner.StopContext(context.Background()))
}

// TestProbeRepeatedStartAndStop repeats startup and shutdown on one runner.
// It checks one registration, one schedule stop, and zero repository claims
// when the captured callback runs after shutdown.
func TestProbeRepeatedStartAndStop(t *testing.T) {
	schedule := &probeScheduleStub{}
	repo := &groupAvailabilityProbeRunnerRepoStub{}
	runner := NewGroupAvailabilityProbeRunnerService(repo, probeExecutorStub{}, GroupProbeOptions{Schedule: schedule})
	runner.Start()
	runner.Start()
	require.Equal(t, 1, schedule.starts)
	runner.Stop()
	schedule.run()
	calls, _ := repo.claimSnapshot()
	require.Zero(t, calls)
	runner.Stop()
	require.Equal(t, 1, schedule.stops)
}

// TestProbeStopCancelsClaimAndWaitsForRun stops a round blocked in a
// context-aware claim. Shutdown returns successfully, the round exits, and a
// later direct run makes no additional claim.
func TestProbeStopCancelsClaimAndWaitsForRun(t *testing.T) {
	started := make(chan struct{})
	repo := &groupAvailabilityProbeRunnerRepoStub{claimStarted: started, releaseClaim: make(chan struct{})}
	runner := NewGroupAvailabilityProbeRunnerService(repo, probeExecutorStub{}, GroupProbeOptions{})
	done := make(chan struct{})
	// Launch the round separately and close done on return, allowing shutdown to
	// wait while the test still controls the blocked claim.
	go func() { defer close(done); runner.runDue() }()
	// Wait for the cooperative claim to announce entry before stopping;
	// otherwise shutdown could succeed merely because no round had been
	// admitted.
	<-started
	// Bound this test's shutdown wait and release its timer on return, making a
	// missed claim cancellation fail instead of leaving shutdown blocked
	// indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runner.StopContext(ctx))
	// Wait for the launched round's completion with a test deadline so a missed
	// wakeup becomes an assertion failure instead of a blocked test.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("停止后领取仍未退出")
	}
	runner.runDue()
	calls, _ := repo.claimSnapshot()
	require.Equal(t, 1, calls)
}

// ClaimDue announces entry once and holds the claim until the test closes
// release, then returns an empty batch with no error. Tests initialize both
// channels before launching the round and close the release gate once during
// cleanup.
//
// The entry signal lets TestProbeStopBudgetReportsUnfinishedClaim establish
// admitted work before creating its shutdown deadline. The claim ignores
// context cancellation. once prevents another call from closing started again,
// while release lets the test join the round after inspecting the stop result.
func (r *uncancellableProbeRepo) ClaimDue(context.Context, time.Time, time.Time, string, int) ([]GroupAvailabilityProbeDueGroup, error) {
	// Publish claim entry once before blocking so the test can impose its
	// shutdown deadline after admission and repeated calls cannot double-close
	// the signal.
	r.once.Do(func() { close(r.started) })
	// Hold this gate after cancellation so an expired StopContext waiting budget
	// reports unfinished work. Releasing the gate lets the round signal idle.
	<-r.release
	return nil, nil
}

// TestProbeStopBudgetReportsUnfinishedClaim holds a claim beyond the
// StopContext waiting deadline. It checks DeadlineExceeded, releases and
// joins that round, then verifies a subsequent unbounded wait succeeds.
func TestProbeStopBudgetReportsUnfinishedClaim(t *testing.T) {
	repo := &uncancellableProbeRepo{started: make(chan struct{}), release: make(chan struct{})}
	runner := NewGroupAvailabilityProbeRunnerService(repo, probeExecutorStub{}, GroupProbeOptions{})
	done := make(chan struct{})
	// Launch the round separately and close done on return, allowing shutdown to
	// wait while the test still controls the blocked claim.
	go func() { defer close(done); runner.runDue() }()
	// Wait until the uncooperative repository call is active before creating the
	// deadline, so timeout exercises shutdown waiting rather than idle shutdown.
	<-repo.started
	// Use a short waiting budget after claim entry to expose unfinished work
	// promptly; the deferred cancel releases the deadline timer after the test.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := runner.StopContext(ctx)
	// Release and join the deliberately blocked round before checking errors,
	// allowing later StopContext calls to observe the runner's completed idle
	// signal.
	close(repo.release)
	<-done
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, runner.StopContext(context.Background()))
}

// TestProbeInvalidScheduleDoesNotRetryStart injects a registration error
// before repeated Start calls. The scheduler records one attempt,
// establishing that a failed startup still consumes this runner's
// registration opportunity.
func TestProbeInvalidScheduleDoesNotRetryStart(t *testing.T) {
	schedule := &probeScheduleStub{err: errors.New("invalid schedule")}
	runner := NewGroupAvailabilityProbeRunnerService(&groupAvailabilityProbeRunnerRepoStub{}, probeExecutorStub{}, GroupProbeOptions{Schedule: schedule})
	runner.Start()
	runner.Start()
	runner.Stop()
	require.Equal(t, 1, schedule.starts)
}
