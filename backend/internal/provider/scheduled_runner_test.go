package provider

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Scheduled-test shutdown must distinguish cancelled admission from unfinished
// execution. An offset-held round lets the tests observe cancellation before any
// due-plan read, while release-held executor and scheduler stubs continue
// independently of their supplied contexts. The assertions require the runner to
// report those unfinished stages within its budget.
//
// Executor entry is signalled before stopping, while the scheduler fixture holds
// its Stop return behind a separate release channel. Later stops reuse the first
// timeout after release permits late work to finish. A startup stub launches the
// callback and still returns a registration error, allowing callback admission
// to race with cleanup. Counters and active-round inspection establish which
// lifecycle calls occurred; the partial-start test observes callback exit,
// suppressed due reads, and one schedule cleanup call.

// scheduledScheduleStub models startup outcomes and counts scheduled-runner
// lifecycle calls.
type scheduledScheduleStub struct {
	// starts and stops count fixture lifecycle calls independently. Tests compare
	// them after repeated runner calls to distinguish registration from permanent
	// shutdown.
	starts atomic.Int32 // schedule startup call count
	stops  atomic.Int32 // schedule stop call count
	// err and start are configured before use. Start invokes start when supplied,
	// then returns err, allowing a callback to exist despite startup failure.
	err   error        // injected schedule startup error
	start func(func()) // optional callback launch hook
}

// scheduledPlansStub supplies stable due plans and counts fixture repository
// reads.
type scheduledPlansStub struct {
	// ScheduledTestPlanRepository leaves unimplemented methods nil. These tests use
	// the ListDue and UpdateAfterRun overrides and keep other repository calls
	// unused.
	ScheduledTestPlanRepository
	reads atomic.Int32 // due-plan read call count
	// plans is returned directly by ListDue. Tests configure and keep its plan
	// pointers stable while a runner round is executing.
	plans []*ScheduledTestPlan
}

// scheduledExecutorStub holds one provider execution beyond its context
// cancellation.
type scheduledExecutorStub struct {
	// entered closes before release is awaited. The test observes execution entry
	// and holds release open through shutdown to model a callback ignoring
	// cancellation.
	entered chan struct{} // executor entry notification signal
	release chan struct{} // test-controlled execution release signal
}

// scheduledResultsStub provides the cancelled result-creation path for runner
// stop tests.
type scheduledResultsStub struct {
	// ScheduledTestResultRepository leaves unused result methods nil. The cancelled
	// execution path returns through Create before retention can reach those
	// methods.
	ScheduledTestResultRepository
}

// scheduledBlockedStop holds scheduler cleanup until the test permits late
// completion.
type scheduledBlockedStop struct {
	scheduledScheduleStub // shared lifecycle counter fixture
	// release holds Stop despite cancellation, and done closes after release lets
	// that call return. Tests retain both signals to observe bounded waiting and
	// late completion.
	release chan struct{} // test-controlled schedule release signal
	done    chan struct{} // late stop completion signal
}

// Start counts registration, invokes the optional launch hook, and returns the
// injected error. The hook runs before that error is reported, so a
// partial-start test can leave a callback requiring cleanup.
func (s *scheduledScheduleStub) Start(_ context.Context, run func()) error {
	s.starts.Add(1)
	if s.start != nil {
		s.start(run)
	}
	return s.err
}

// Stop counts fixture schedule cleanup and reports successful completion.
func (s *scheduledScheduleStub) Stop(context.Context) error { s.stops.Add(1); return nil }

// ListDue counts the fixture read and returns its configured plan slice
// directly. The context and due time are ignored, and the fixture owner keeps
// returned plans stable while the round uses them.
func (s *scheduledPlansStub) ListDue(context.Context, time.Time) ([]*ScheduledTestPlan, error) {
	s.reads.Add(1)
	return s.plans, nil
}

// UpdateAfterRun discards timestamps and returns the supplied context error. The
// cancelled-execution fixture uses this to finish its late update attempt after
// StopContext has already closed the runner lifetime.
func (*scheduledPlansStub) UpdateAfterRun(ctx context.Context, _ int64, _, _ time.Time) error {
	return ctx.Err()
}

// RunTestBackground signals entry and holds this fixture execution until release
// closes. It ignores cancellation so the runner must report unfinished test work
// while the release remains held.
//
// The test waits for entered before stopping the runner and closes release after
// checking the timeout. This callback then returns the fixture error status with
// a nil Go error, allowing the cancelled result-storage and timestamp paths to
// run before round completion. Use the entry channel for one invocation.
func (s scheduledExecutorStub) RunTestBackground(context.Context, int64, string) (*ScheduledTestResult, error) {
	// Signal execution entry before blocking on the independent release channel.
	// Shutdown cancels the supplied context while this fixture deliberately keeps
	// its admitted work unfinished.
	close(s.entered)
	<-s.release
	return &ScheduledTestResult{Status: "error"}, nil
}

// Create discards the result and returns ctx.Err with a nil stored value. These
// fixtures call it after runner cancellation, making SaveResult return before
// its unimplemented retention method is reached.
func (scheduledResultsStub) Create(ctx context.Context, _ *ScheduledTestResult) (*ScheduledTestResult, error) {
	return nil, ctx.Err()
}

// scheduledRunnerOptions supplies the fixture schedule, clock, and requested
// offset. Its next-run callback advances one minute for any expression, letting
// stop tests finish timestamp handling without testing cron validation.
func scheduledRunnerOptions(schedule ScheduledTestSchedule, offset time.Duration) ScheduledRunnerOptions {
	return ScheduledRunnerOptions{
		Schedule: schedule, Offset: offset, Now: time.Now,
		// Use a fixed next-run step for lifecycle fixtures; cron parsing is exercised
		// by the schedule adapter tests.
		NextRun: func(_ string, from time.Time) (time.Time, error) { return from.Add(time.Minute), nil },
	}
}

// waitScheduledRound waits within one second until the runner has admitted a
// round. It reads the activity count under the runner mutex, establishing
// admission before the test triggers offset cancellation.
func waitScheduledRound(t *testing.T, runner *ScheduledTestRunnerService) {
	t.Helper()
	require.Eventually(t, func() bool {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		return runner.active > 0
	}, time.Second, time.Millisecond)
}

// TestScheduledRunnerStopsPermanently stops both an idle runner and one started
// beforehand. It verifies construction is idle, registration occurs at most
// once, schedule cleanup occurs once, and later starts cannot reopen scheduling.
func TestScheduledRunnerStopsPermanently(t *testing.T) {
	for _, beforeStart := range []bool{false, true} {
		schedule := &scheduledScheduleStub{}
		runner := NewScheduledTestRunnerService(&scheduledPlansStub{}, nil, nil, scheduledRunnerOptions(schedule, 10*time.Second))
		require.Zero(t, schedule.starts.Load())
		if beforeStart {
			require.NoError(t, runner.StopContext(context.Background()))
		}
		require.NoError(t, runner.StartContext(context.Background()))
		require.NoError(t, runner.StartContext(context.Background()))
		require.NoError(t, runner.StopContext(context.Background()))
		require.NoError(t, runner.StartContext(context.Background()))
		if beforeStart {
			require.Zero(t, schedule.starts.Load())
		} else {
			require.Equal(t, int32(1), schedule.starts.Load())
		}
		require.Equal(t, int32(1), schedule.stops.Load())
	}
}

// TestScheduledRunnerCancelsOffsetBeforeClaim stops an admitted round during its
// long offset. It verifies the round returns within the stop budget, no due
// plans are read, and a later callback remains rejected.
func TestScheduledRunnerCancelsOffsetBeforeClaim(t *testing.T) {
	plans := &scheduledPlansStub{}
	schedule := &scheduledScheduleStub{}
	runner := NewScheduledTestRunnerService(plans, nil, nil, scheduledRunnerOptions(schedule, 10*time.Second))
	done := make(chan struct{})
	// Run the offset wait concurrently with StopContext and establish active
	// admission before stopping, so cancellation is observed before the first
	// due-plan read.
	go func() { runner.runScheduled(); close(done) }()
	waitScheduledRound(t, runner)
	// Keep the stop budget shorter than the offset, so a successful round exit
	// demonstrates cancellation of the wait rather than natural timer expiry.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	require.NoError(t, runner.StopContext(ctx))
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("停止未结束偏移等待")
	}
	require.Zero(t, plans.reads.Load())
	runner.runScheduled()
	require.Zero(t, plans.reads.Load())
}

// TestScheduledRunnerReportsUnfinishedExecution holds an entered executor beyond
// shutdown cancellation. It requires an unfinished-test timeout, then waits for
// released execution to finish and verifies the first stop error remains
// retained.
func TestScheduledRunnerReportsUnfinishedExecution(t *testing.T) {
	plans := &scheduledPlansStub{plans: []*ScheduledTestPlan{{ID: 1, ProviderID: 2}}}
	executor := scheduledExecutorStub{entered: make(chan struct{}), release: make(chan struct{})}
	// Make explicit release and deferred failure cleanup share one close operation,
	// so the held executor can exit safely on either assertion path.
	release := sync.OnceFunc(func() { close(executor.release) })
	defer release()
	svc := NewScheduledTestService(plans, scheduledResultsStub{}, ScheduledTestOptions{})
	runner := NewScheduledTestRunnerService(plans, svc, executor, scheduledRunnerOptions(&scheduledScheduleStub{}, 0))
	done := make(chan struct{})
	// Hold an admitted executor independently of context cancellation, then await
	// the entire round after releasing it to distinguish cancellation delivery from
	// completed execution.
	go func() { runner.runScheduled(); close(done) }()
	select {
	case <-executor.entered:
	case <-time.After(time.Second):
		t.Fatal("执行器没有取得计划")
	}
	// Expire the stop wait while release still holds execution, making unfinished
	// test work observable before allowing late completion.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := runner.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "scheduled tests remain unfinished")
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("已释放执行器仍未退出")
	}
	require.Equal(t, err, runner.StopContext(context.Background()))
}

// Stop counts scheduler cleanup, then waits for the test's release signal
// despite context cancellation. It closes done immediately before returning
// successful cleanup, letting the test distinguish runner timeout from that
// later return.
//
// The fixture uses one release channel and one completion signal for a single
// stop invocation. Holding release across the first StopContext call makes the
// scheduler stage outlive the caller's budget; releasing it afterward permits
// the buffered runner result send to finish while the retained timeout remains
// unchanged.
func (s *scheduledBlockedStop) Stop(context.Context) error {
	s.stops.Add(1)
	// Hold schedule cleanup through the runner deadline, then signal its late
	// return so the test can check retention of the earlier unfinished-stage
	// result.
	<-s.release
	close(s.done)
	return nil
}

// TestScheduledRunnerBoundsSchedulerStop holds the schedule's Stop call beyond
// the caller budget. It requires an unfinished-cron timeout, releases that call,
// and verifies the first error and single cleanup count remain unchanged.
func TestScheduledRunnerBoundsSchedulerStop(t *testing.T) {
	schedule := &scheduledBlockedStop{release: make(chan struct{}), done: make(chan struct{})}
	// Share the release close between the explicit late-completion step and
	// deferred cleanup, so a failing assertion can still unblock this schedule
	// fixture.
	release := sync.OnceFunc(func() { close(schedule.release) })
	defer release()
	runner := NewScheduledTestRunnerService(&scheduledPlansStub{}, nil, nil, scheduledRunnerOptions(schedule, time.Second))
	require.NoError(t, runner.StartContext(context.Background()))
	// Expire the runner budget while the schedule remains release-blocked, exposing
	// the unfinished cron stage independently of any admitted test workers.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := runner.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "scheduled cron stop remains unfinished")
	release()
	select {
	case <-schedule.done:
	case <-time.After(time.Second):
		t.Fatal("释放后的日历停止调用仍未返回")
	}
	require.Equal(t, err, runner.StopContext(context.Background()))
	require.Equal(t, int32(1), schedule.stops.Load())
}

// TestScheduledRunnerCleansPartialStart launches a callback before injecting a
// startup failure. It verifies stop is followed by callback exit with no
// due-plan read and exactly one schedule cleanup call.
func TestScheduledRunnerCleansPartialStart(t *testing.T) {
	plans := &scheduledPlansStub{}
	failed := errors.New("forced schedule start failure")
	done := make(chan struct{})
	// Allow the callback to race with stop after the startup failure, so either
	// cancelled admitted work or rejected late admission has to finish.
	schedule := &scheduledScheduleStub{err: failed, start: func(run func()) { go func() { run(); close(done) }() }}
	runner := NewScheduledTestRunnerService(plans, nil, nil, scheduledRunnerOptions(schedule, 10*time.Second))
	require.ErrorIs(t, runner.StartContext(context.Background()), failed)
	require.NoError(t, runner.StopContext(context.Background()))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("部分启动的回调没有退出")
	}
	require.Zero(t, plans.reads.Load())
	require.Equal(t, int32(1), schedule.stops.Load())
}
