package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Scheduled provider tests read due plans after a tick offset, then execute them
// with a five-minute round context. Each round admits up to ten tests at once
// and waits for every admitted worker before finishing. Per-minute callbacks can
// overlap, and reading due plans leaves their next-run timestamps unchanged
// until each completed test reaches its later update.
//
// Shutdown closes round admission, cancels the runner context, stops the
// schedule, and waits for active rounds within the caller's budget. A schedule
// or executor that ignores cancellation can remain active after a timeout; the
// first stop result stays recorded. Result saving, optional recovery, and plan
// time advancement are separate calls, so their failures can leave earlier work
// applied and a plan still due.

// ScheduledTestExecutor runs a background test and returns its result.
type ScheduledTestExecutor interface {
	// RunTestBackground executes the requested provider and model with ctx and
	// returns a nonnil result on nil error. TestService encodes test failures in
	// that result; a returned Go error makes the runner skip saving and schedule
	// advancement.
	RunTestBackground(context.Context, int64, string) (*ScheduledTestResult, error)
}

// SuccessfulTestRecovery reports primary cleanup calls completed after a
// successful test.
type SuccessfulTestRecovery struct {
	// ClearedError and ClearedRateLimit record the primary cleanup calls selected
	// by RecoveryService. The runner logs those flags; auxiliary cache and counter
	// outcomes are handled separately by recovery.
	ClearedError     bool // successful error cleanup call
	ClearedRateLimit bool // successful runtime cleanup call
}

// ScheduledTestSchedule registers recurring callbacks and waits for their
// execution to stop.
type ScheduledTestSchedule interface {
	// Start registers the runner callback using ctx for the startup attempt. The
	// runner cancels this temporary context after return; ongoing callbacks use the
	// runner's own lifetime, and implementations coordinate concurrent Start and
	// Stop calls.
	Start(context.Context, func()) error
	// Stop closes scheduling and waits for callbacks within the supplied context.
	// The runner bounds this call separately from active-round completion and
	// reports an unfinished schedule when that wait expires.
	Stop(context.Context) error
}

// ScheduledRunnerOptions binds scheduling, time, diagnostics, and optional
// successful-test recovery callbacks.
type ScheduledRunnerOptions struct {
	// Schedule is required for startup and supports concurrent lifecycle calls.
	// StopContext calls Stop even when startup failed after acquiring schedule
	// resources.
	Schedule ScheduledTestSchedule
	// Now and NextRun are supplied before callbacks start. Now timestamps due
	// selection and completion separately; NextRun computes the next time after
	// testing, and its failure leaves that plan's stored times unchanged.
	Now     func() time.Time                           // current runner observation time
	NextRun func(string, time.Time) (time.Time, error) // cron next-time calculation callback
	Offset  time.Duration                              // post-tick execution delay
	// Observe receives lifecycle and per-plan diagnostics when configured. Rounds
	// can call it concurrently, so its owner supplies concurrency-safe handling.
	Observe func(string, ...any)
	// Recover is optional and runs for exact success results with AutoRecover. Its
	// errors are logged while the runner continues to calculate the next run.
	Recover func(context.Context, int64) (*SuccessfulTestRecovery, error)
}

// ScheduledTestRunnerService owns admitted test rounds and closes their shared
// lifetime permanently at stop.
type ScheduledTestRunnerService struct {
	// planRepo, scheduledSvc, and providerTestSvc are retained across overlapping
	// rounds. Their owner supports concurrent calls and keeps shared dependencies
	// available until all admitted rounds finish.
	planRepo        ScheduledTestPlanRepository // due-plan and timestamp storage
	scheduledSvc    *ScheduledTestService       // result insertion and retention service
	providerTestSvc ScheduledTestExecutor       // background provider test executor
	options         ScheduledRunnerOptions      // captured runner callback options
	// startOnce retains the first startup attempt, including failure or an already
	// stopped runner. Later StartContext calls return the recorded startup result.
	startOnce sync.Once
	// mu protects stopped, startErr, active, idle, stopDone, and stopErr.
	// Admission, round completion, and stop publication use this lock to share one
	// lifecycle.
	mu sync.Mutex // runner lifecycle state lock
	// ctx and cancel are constructed together for the runner's lifetime. Every
	// admitted round derives cancellation from ctx, and the first stop invokes
	// cancel.
	ctx    context.Context    // shared runner lifetime context
	cancel context.CancelFunc // shared runner lifetime cancellation
	// stopped closes round admission permanently before cancellation begins.
	// StartContext skips startup after this flag is observed under mu.
	stopped bool
	// startErr retains the first attempted schedule startup result. A partial
	// failure can still leave callbacks for StopContext to cancel and await.
	startErr error
	// active counts admitted rounds, and idle signals when the count reaches zero.
	// The first round opens a new idle channel; its final overlapping round closes
	// it.
	active int           // admitted unfinished round count
	idle   chan struct{} // active-round completion signal
	// stopDone publishes stopErr after the first stop finishes its bounded waits.
	// Later callers share that result, including a timeout after eventual worker
	// completion.
	stopDone chan struct{} // first stop publication signal
	stopErr  error         // retained first stop result
}

// NewScheduledTestRunnerService retains plan, result, and test services with an
// idle runner lifetime. Configure the required clock and next-run callbacks
// before starting; StartContext registers scheduling, and StopContext cancels
// the retained lifetime and waits for admitted rounds.
func NewScheduledTestRunnerService(plans ScheduledTestPlanRepository, service *ScheduledTestService, tests ScheduledTestExecutor, options ScheduledRunnerOptions) *ScheduledTestRunnerService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ScheduledTestRunnerService{planRepo: plans, scheduledSvc: service, providerTestSvc: tests, options: options, ctx: ctx, cancel: cancel}
}

// observe forwards a formatted runner diagnostic when Observe is configured.
// Concurrent rounds may invoke the callback together, so the callback owner
// handles synchronization for its destination.
func (s *ScheduledTestRunnerService) observe(format string, args ...any) {
	if s.options.Observe != nil {
		s.options.Observe(format, args...)
	}
}

// StartContext attempts schedule registration once and returns the recorded
// startup result. A nil service succeeds; a runner stopped before this attempt
// skips registration. Missing Schedule returns a startup error, and a failed
// attempt is retained for subsequent calls.
//
// A temporary startup context combines runner cancellation with the caller's
// context, then is cancelled when Schedule.Start returns. Recurring callbacks
// use the runner lifetime instead. StopContext may run during registration, so
// the schedule handles that lifecycle overlap; a partially failed registration
// still needs StopContext to cancel callbacks and release the schedule's
// resources.
func (s *ScheduledTestRunnerService) StartContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	// Retain registration and its error as one startup attempt. A failed schedule
	// may still have started callbacks, so its later stop remains required.
	s.startOnce.Do(func() {
		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			return
		}
		var err error
		if s.options.Schedule == nil {
			err = errors.New("scheduled test schedule is not configured")
		} else {
			// The caller controls startup while recurring work uses s.ctx. Detach the
			// caller cancellation callback after registration and cancel this temporary
			// context before returning.
			startCtx, cancel := context.WithCancel(s.ctx)
			defer cancel()
			after := context.AfterFunc(ctx, cancel)
			defer after()
			err = s.options.Schedule.Start(startCtx, s.runScheduled)
		}
		s.mu.Lock()
		s.startErr = err
		stopped = s.stopped
		s.mu.Unlock()
		if err != nil {
			s.observe("[ScheduledTestRunner] not started (invalid schedule): %v", err)
		} else if !stopped {
			s.observe("[ScheduledTestRunner] started (tick=every minute)")
		}
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startErr
}

// Stop performs StopContext with an unbounded Background context and discards
// its error. It can wait indefinitely for uncooperative schedule or test work
// unless an earlier StopContext already published a retained result.
func (s *ScheduledTestRunnerService) Stop() { _ = s.StopContext(context.Background()) }

// StopContext closes round admission, cancels execution, and retains the first
// shutdown result. A nil service succeeds. It bounds both schedule stopping and
// admitted-round completion with ctx, reporting unfinished stages when their
// waits expire.
//
// The stop flag and captured idle signal are set under the admission mutex
// before schedule cleanup starts. Schedule.Stop runs through a buffered result
// channel so an unresponsive implementation cannot hold this caller beyond its
// budget. Worker callbacks may still be running after timeout, and their
// dependencies remain needed until they finish. Later callers wait for the first
// result or their own cancellation, then reuse the published result even after
// late completion.
func (s *ScheduledTestRunnerService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	// An earlier stop can still be waiting for completion. Prefer its published
	// result, otherwise apply this caller's budget while preserving the first
	// stop's eventual result.
	if s.stopped {
		done := s.stopDone
		s.mu.Unlock()
		select {
		case <-done:
			return s.stopErr
		default:
		}
		select {
		case <-done:
			return s.stopErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Close admission and capture the active-round signal before cancelling the
	// runner. An admitted round then stays represented until its workers actually
	// return.
	s.stopped = true
	s.stopDone = make(chan struct{})
	done := s.stopDone
	idle := s.idle
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	var stopErr error
	if s.options.Schedule != nil {
		// Buffer the schedule result so its late return can finish after this caller
		// reports timeout; the same budget also governs the remaining round wait.
		scheduleDone := make(chan error, 1)
		go func() { scheduleDone <- s.options.Schedule.Stop(ctx) }()
		select {
		case stopErr = <-scheduleDone:
		case <-ctx.Done():
			stopErr = fmt.Errorf("scheduled cron stop remains unfinished: %w", ctx.Err())
		}
	}
	// Check completed rounds before consulting the budget. If rounds remain active
	// after the schedule wait, join their unfinished-stage error with the schedule
	// outcome.
	if idle != nil {
		select {
		case <-idle:
		default:
			select {
			case <-idle:
			case <-ctx.Done():
				stopErr = errors.Join(stopErr, fmt.Errorf("scheduled tests remain unfinished: %w", ctx.Err()))
			}
		}
	}
	s.mu.Lock()
	// Publish the first stop outcome before closing its completion channel. Later
	// stops observe this fixed result even when a blocked schedule or worker
	// subsequently returns.
	s.stopErr = stopErr
	close(done)
	s.mu.Unlock()
	return stopErr
}

// runScheduled admits one triggered round, waits its offset, and tests due plans
// under a five-minute context. Callers keep returned plan pointers stable and
// nonnil during execution. A stopped runner admits no further rounds.
//
// Each round has ten execution slots and may process more than ten plans as
// workers finish. Concurrent schedule callbacks have separate slot pools and can
// read the same still-due plan. ListDue supplies a read result; runOnePlan
// advances each plan after execution reaches its timestamp update.
//
// Admission and active-round accounting use the stop mutex. Cancellation
// interrupts the offset and slot wait, and a second context check returns an
// acquired slot when cancellation raced with admission. The round waits for all
// launched workers before decrementing its activity count; callbacks that ignore
// cancellation can keep that count active beyond the round deadline.
func (s *ScheduledTestRunnerService) runScheduled() {
	// Admit and count this round under the stop mutex so shutdown either rejects it
	// or captures the signal its deferred completion will close.
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.active++
	if s.active == 1 {
		s.idle = make(chan struct{})
	}
	parent := s.ctx
	s.mu.Unlock()
	// Keep the round active until every launched worker has returned. Only the
	// final overlapping round closes idle, allowing StopContext to observe actual
	// completion.
	defer func() {
		s.mu.Lock()
		s.active--
		if s.active == 0 {
			close(s.idle)
		}
		s.mu.Unlock()
	}()
	// The tick offset belongs to the admitted round, so shutdown interrupts its
	// timer before due plans are read and still waits for round accounting to
	// finish.
	delay := time.NewTimer(s.options.Offset)
	defer delay.Stop()
	select {
	case <-parent.Done():
		return
	case <-delay.C:
	}
	if parent.Err() != nil {
		return
	}
	// Use one bounded context for due-plan reads, slot admission, and tests.
	// Dependencies cooperate with its cancellation; the activity count remains
	// until launched work returns.
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	plans, err := s.planRepo.ListDue(ctx, s.options.Now())
	if err != nil {
		s.observe("[ScheduledTestRunner] ListDue error: %v", err)
		return
	}
	if len(plans) == 0 {
		return
	}
	s.observe("[ScheduledTestRunner] found %d due plans", len(plans))
	// Limit this round to ten concurrent tests while continuing through the due
	// list as slots return. Overlapping rounds each retain their own capacity.
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup
	for _, plan := range plans {
		if ctx.Err() != nil {
			break
		}
		// Wait for capacity with cancellation, and await already launched workers
		// before returning from a cancelled admission wait so the round's idle signal
		// remains meaningful.
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		// Cancellation can race with a ready slot. Return that newly acquired capacity
		// before leaving admission, because no worker will own its deferred release.
		if ctx.Err() != nil {
			<-sem
			break
		}
		// Register each worker before launching it, then return its slot before
		// signalling completion. The round cannot publish idle while its test callback
		// is still running.
		wg.Add(1)
		go func(p *ScheduledTestPlan) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() == nil {
				s.runOnePlan(ctx, p)
			}
		}(plan)
	}
	wg.Wait()
}

// runOnePlan executes a nonnil stable plan and applies result saving, optional
// recovery, and schedule advancement in order. A Go error from the executor
// returns before those steps; a nil error supplies a nonnil result, whose failed
// status is still saved.
//
// SaveResult failure is logged and execution continues. An exact success result
// with AutoRecover requests recovery even when result saving failed. Recovery
// failure is also logged, then the next run is calculated using a fresh clock
// reading. Calculation failure leaves the old schedule times, while
// UpdateAfterRun failure retains earlier result and recovery outcomes. The
// last-run clock is sampled separately from the next-run calculation.
func (s *ScheduledTestRunnerService) runOnePlan(ctx context.Context, plan *ScheduledTestPlan) {
	result, err := s.providerTestSvc.RunTestBackground(ctx, plan.ProviderID, plan.ModelID)
	if err != nil {
		s.observe("[ScheduledTestRunner] plan=%d RunTestBackground error: %v", plan.ID, err)
		return
	}

	// Save the observation before recovery and time advancement. Failure is
	// diagnostic here, so those later operations can still run with their own
	// independent outcomes.
	if err := s.scheduledSvc.SaveResult(ctx, plan.ID, plan.MaxResults, result); err != nil {
		s.observe("[ScheduledTestRunner] plan=%d SaveResult error: %v", plan.ID, err)
	}

	// Use the test status to decide recovery even after a result-storage failure.
	// Recovery does not depend on the history entry having been retained.
	if result.Status == "success" && plan.AutoRecover {
		s.tryRecoverProvider(ctx, plan.ProviderID, plan.ID)
	}

	// Advance the schedule after saving and the recovery attempt. Failed
	// calculation leaves the old next-run time due while earlier successful side
	// effects remain applied.
	nextRun, err := s.options.NextRun(plan.CronExpression, s.options.Now())
	if err != nil {
		s.observe("[ScheduledTestRunner] plan=%d computeNextRun error: %v", plan.ID, err)
		return
	}

	// Write last and next times together through the plan repository. This separate
	// update can fail after result history and recovery have already changed.
	if err := s.planRepo.UpdateAfterRun(ctx, plan.ID, s.options.Now(), nextRun); err != nil {
		s.observe("[ScheduledTestRunner] plan=%d UpdateAfterRun error: %v", plan.ID, err)
	}
}

// tryRecoverProvider invokes optional recovery and logs the returned cleanup
// flags. A missing callback or nil description produces no diagnostic; callback
// failure is logged and completes the attempt here, allowing runOnePlan to
// continue schedule calculation.
func (s *ScheduledTestRunnerService) tryRecoverProvider(ctx context.Context, providerID int64, planID int64) {
	if s.options.Recover == nil {
		return
	}

	recovery, err := s.options.Recover(ctx, providerID)
	if err != nil {
		// Treat recovery failure as a logged attempt here, so runOnePlan can still
		// calculate and persist its next schedule time after this helper returns.
		s.observe("[ScheduledTestRunner] plan=%d auto-recover failed: %v", planID, err)
		return
	}
	if recovery == nil {
		return
	}

	if recovery.ClearedError {
		s.observe("[ScheduledTestRunner] plan=%d auto-recover: provider=%d recovered from error status", planID, providerID)
	}
	if recovery.ClearedRateLimit {
		s.observe("[ScheduledTestRunner] plan=%d auto-recover: provider=%d cleared rate-limit/runtime state", planID, providerID)
	}
}
