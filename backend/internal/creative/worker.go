package creative

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

const (
	DefaultCreativeWorkerLockTTL             = 5 * time.Minute
	DefaultCreativeWorkerLockConflictDelay   = 5 * time.Second
	DefaultCreativeWorkerErrorRetryDelay     = time.Minute
	DefaultCreativeWorkerRequeueDelay        = 30 * time.Second
	DefaultCreativeWorkerDelayedPollInterval = 5 * time.Second
	DefaultCreativeWorkerRecoveryInterval    = 5 * time.Minute
	DefaultCreativeWorkerStaleActiveAfter    = 10 * time.Minute
	DefaultCreativeWorkerDelayedMoveLimit    = 100
	DefaultCreativeWorkerRecoverLimit        = 100
	DefaultCreativeWorkerErrorBackoff        = time.Second
	DefaultCreativeWorkerReserveBlockTimeout = 5 * time.Second
	DefaultCreativeConcurrencyRequeueDelay   = time.Second

	defaultCreativeMaxAttempts = 3
)

// ErrCreativeExecutionPending requests a short requeue when concurrency admission is temporarily unavailable.
var ErrCreativeExecutionPending = errors.New("creative execution is pending concurrency admission")

type CreativeOutput = upstream.ImageOutput

// CreativeExecuteResult contains the selected provider's image outputs and reported cost.
type CreativeExecuteResult struct {
	Outputs      []CreativeOutput
	ProviderID   int64
	ProviderCost float64
}

// CreativeWorkerOptions controls reservation, locking, recovery intervals, and execution retries.
type CreativeWorkerOptions struct {
	ReserveBlockTimeout time.Duration
	JobLockTTL          time.Duration
	LockConflictDelay   time.Duration
	DefaultRequeueDelay time.Duration
	ErrorRetryDelay     time.Duration
	ErrorBackoff        time.Duration
	DelayedPollInterval time.Duration
	RecoveryInterval    time.Duration
	StaleActiveAfter    time.Duration
	DelayedMoveLimit    int
	RecoverLimit        int
	MaxAttempts         int
}

// CreativeProcessResult requests acknowledgment when Terminal is true, otherwise a delayed requeue.
type CreativeProcessResult struct {
	RequeueAfter time.Duration
	Terminal     bool
}

// CreativeRunWorker reserves and locks tasks, executes them, recovers funding, and acknowledges or requeues
// them.
type CreativeRunWorker struct {
	queue    CreativeRunQueue
	repo     CreativeRunRepository
	store    CreativeTransientStore
	executor CreativeRunExecutor
	service  *Results
	ports    WorkerPorts
	opts     CreativeWorkerOptions
	// busy counts concurrent Process calls made through this worker's queue loop.
	busy atomic.Int32
}

type creativeLeaseStateKey struct{}

type creativeLeaseState struct {
	lost atomic.Bool
}

// ExecutionTarget executes requests using the selected provider's bound credentials and platform transport.
type ExecutionTarget interface {
	Execute(context.Context, CreativeRun, CreativeRunPayload) (*CreativeExecuteResult, error)
}
type CreativeExecution struct {
	Platform      string
	ProviderID    int64
	UpstreamModel string
	Target        ExecutionTarget
	ReleaseFunc   func()
}
type CreativeRunExecutor interface {
	Prepare(context.Context, CreativeRun) (*CreativeExecution, error)
	IsRetryable(error) bool
}
type WorkerPorts struct {
	AcquireUser func(context.Context, int64) (func(), bool, error)
	UserMissing func(error) bool
	Observe     func(string, ...any)
}

func NormalizeCreativeWorkerOptions(opts CreativeWorkerOptions) CreativeWorkerOptions {
	if opts.ReserveBlockTimeout <= 0 {
		opts.ReserveBlockTimeout = DefaultCreativeWorkerReserveBlockTimeout
	}
	if opts.JobLockTTL <= 0 {
		opts.JobLockTTL = DefaultCreativeWorkerLockTTL
	}
	if opts.LockConflictDelay <= 0 {
		opts.LockConflictDelay = DefaultCreativeWorkerLockConflictDelay
	}
	if opts.DefaultRequeueDelay <= 0 {
		opts.DefaultRequeueDelay = DefaultCreativeWorkerRequeueDelay
	}
	if opts.ErrorRetryDelay <= 0 {
		opts.ErrorRetryDelay = DefaultCreativeWorkerErrorRetryDelay
	}
	if opts.ErrorBackoff <= 0 {
		opts.ErrorBackoff = DefaultCreativeWorkerErrorBackoff
	}
	if opts.DelayedPollInterval <= 0 {
		opts.DelayedPollInterval = DefaultCreativeWorkerDelayedPollInterval
	}
	if opts.RecoveryInterval <= 0 {
		opts.RecoveryInterval = DefaultCreativeWorkerRecoveryInterval
	}
	if opts.StaleActiveAfter <= 0 {
		opts.StaleActiveAfter = DefaultCreativeWorkerStaleActiveAfter
	}
	if opts.DelayedMoveLimit <= 0 {
		opts.DelayedMoveLimit = DefaultCreativeWorkerDelayedMoveLimit
	}
	if opts.RecoverLimit <= 0 {
		opts.RecoverLimit = DefaultCreativeWorkerRecoverLimit
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = defaultCreativeMaxAttempts
	}
	return opts
}

func NewCreativeRunWorker(queue CreativeRunQueue, repo CreativeRunRepository, store CreativeTransientStore, executor CreativeRunExecutor, results *Results, opts CreativeWorkerOptions, ports WorkerPorts) *CreativeRunWorker {
	return &CreativeRunWorker{queue: queue, repo: repo, store: store, executor: executor, service: results, opts: NormalizeCreativeWorkerOptions(opts), ports: ports}
}

// RunUntilStopped drains after a stop signal; a reservation that races the signal is returned to ready.
func (w *CreativeRunWorker) RunUntilStopped(ctx context.Context, stop <-chan struct{}) {
	if w == nil {
		return
	}
	for {
		if ctx.Err() != nil || creativeWorkerStopRequested(stop) {
			return
		}
		if err := w.runOnce(ctx, stop); err != nil && ctx.Err() == nil {
			if !sleepOrCreativeWorkerStop(ctx, w.opts.ErrorBackoff, stop) {
				return
			}
		}
	}
}

func creativeWorkerStopRequested(stop <-chan struct{}) bool {
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func sleepOrCreativeWorkerStop(ctx context.Context, delay time.Duration, stop <-chan struct{}) bool {
	if delay <= 0 {
		return ctx.Err() == nil && !creativeWorkerStopRequested(stop)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}

// BusyCount returns the number of queue workers currently processing tasks.
func (w *CreativeRunWorker) BusyCount() int {
	if w == nil {
		return 0
	}
	return int(w.busy.Load())
}

func (w *CreativeRunWorker) runOnce(ctx context.Context, stop <-chan struct{}) error {
	if w == nil || w.queue == nil || w.repo == nil || w.service == nil || w.executor == nil {
		return nil
	}

	reserved, err := w.queue.Reserve(ctx, w.opts.ReserveBlockTimeout)
	if errors.Is(err, ErrCreativeQueueEmpty) {
		return nil
	}
	if err != nil {
		return err
	}
	if creativeWorkerStopRequested(stop) {
		// Return the reservation immediately if scale-down arrived while Reserve was waiting.
		return w.queue.RequeueAfter(ctx, reserved.RunID, reserved.LeaseToken, 0)
	}

	lock, ok, err := w.queue.TryAcquireJobLock(ctx, reserved.RunID, w.opts.JobLockTTL)
	if err != nil {
		if requeueErr := w.queue.RequeueAfter(ctx, reserved.RunID, reserved.LeaseToken, w.opts.LockConflictDelay); requeueErr != nil {
			return requeueErr
		}
		return err
	}
	if !ok {
		// Requeue after a lock conflict so the task does not remain stranded in active.
		return w.queue.RequeueAfter(ctx, reserved.RunID, reserved.LeaseToken, w.opts.LockConflictDelay)
	}
	defer func() {
		_ = lock.Release(ctx)
	}()

	// Refresh active timestamps and lock TTLs while processing so recovery does not reclaim live work.
	hbStop := make(chan struct{})
	hbDone := make(chan struct{})
	leaseLost := make(chan struct{}, 1)
	processCtx, cancelProcess := context.WithCancel(ctx)
	leaseState := &creativeLeaseState{}
	processCtx = context.WithValue(processCtx, creativeLeaseStateKey{}, leaseState)
	go w.runJobHeartbeat(processCtx, reserved.RunID, reserved.LeaseToken, lock, hbStop, hbDone, leaseLost, cancelProcess, leaseState)

	w.busy.Add(1)
	result, processErr := w.Process(processCtx, reserved.RunID)
	w.busy.Add(-1)
	close(hbStop)
	<-hbDone
	cancelProcess()
	select {
	case <-leaseLost:
		// Lease ownership is lost; leave acknowledgment and requeueing to the current owner.
		return ErrCreativeLeaseLost
	default:
	}
	if processErr != nil {
		w.warn("creative.worker_process_failed",
			"run_id", reserved.RunID,
			"error", processErr,
		)
		return w.queue.RequeueAfter(ctx, reserved.RunID, reserved.LeaseToken, w.opts.ErrorRetryDelay)
	}
	if result.Terminal {
		return w.queue.Ack(ctx, reserved.RunID, reserved.LeaseToken)
	}
	delay := result.RequeueAfter
	if delay <= 0 {
		delay = w.opts.DefaultRequeueDelay
	}
	return w.queue.RequeueAfter(ctx, reserved.RunID, reserved.LeaseToken, delay)
}

// Process executes one task or resumes its pending funding recovery and returns the required queue action.
func (w *CreativeRunWorker) Process(ctx context.Context, runID string) (CreativeProcessResult, error) {
	run, err := w.repo.GetCreativeRunByRunID(ctx, runID)
	if err != nil {
		if errors.Is(err, ErrCreativeRunNotFound) {
			// A deleted task can be acknowledged and removed from the queue.
			return CreativeProcessResult{Terminal: true}, nil
		}
		return CreativeProcessResult{}, err
	}
	if run == nil {
		return CreativeProcessResult{}, errors.New("creative run is unavailable")
	}
	if IsTerminalCreativeRunStatus(run.Status) {
		return CreativeProcessResult{Terminal: true}, nil
	}
	if run.Status == CreativeRunStatusReleasePending {
		if err := w.service.ReleaseRun(ctx, runID); err != nil {
			if _, incErr := w.repo.IncrementCreativeRunReleaseAttempt(ctx, runID); incErr != nil {
				return CreativeProcessResult{}, incErr
			}
			_ = w.repo.SetCreativeRunReconcileError(ctx, runID, SanitizeCreativeMessage(err.Error()), time.Now().Add(w.opts.ErrorRetryDelay))
			return CreativeProcessResult{}, err
		}
		_ = w.repo.SetCreativeRunReconcileError(ctx, runID, "", time.Time{})
		return CreativeProcessResult{Terminal: true}, nil
	}
	if IsCreativeRunSettlementPending(run.Status) {
		if err := w.service.SettleRun(ctx, runID); err != nil {
			attempts, incErr := w.repo.IncrementCreativeRunSettlementAttempt(ctx, runID)
			if incErr != nil {
				return CreativeProcessResult{}, incErr
			}
			_ = w.repo.SetCreativeRunReconcileError(ctx, runID, SanitizeCreativeMessage(err.Error()), time.Now().Add(w.opts.ErrorRetryDelay))
			if attempts >= w.opts.MaxAttempts*2 {
				w.warn("creative.worker_settlement_pending_retries_exhausted",
					"run_id", runID,
					"attempts", attempts,
					"error", err,
				)
			}
			return CreativeProcessResult{RequeueAfter: w.opts.ErrorRetryDelay}, nil
		}
		_ = w.repo.SetCreativeRunReconcileError(ctx, runID, "", time.Time{})
		return CreativeProcessResult{Terminal: true}, nil
	}
	if recovered, recoverErr := w.recoverPersistedProviderResult(ctx, run); recoverErr != nil {
		return CreativeProcessResult{}, recoverErr
	} else if recovered {
		return CreativeProcessResult{Terminal: true}, nil
	}

	payload, err := w.loadPayload(ctx, runID)
	if err != nil {
		if errors.Is(err, ErrCreativeTransientUnavailable) {
			// Storage outages remain retryable; the caller requeues the task.
			return CreativeProcessResult{}, err
		}
		// Expired or corrupt input prevents execution, so release the hold and record result_lost.
		if markErr := w.service.MarkResultLost(ctx, runID, false); markErr != nil {
			w.warn("creative.worker_mark_result_lost_failed",
				"run_id", runID,
				"error", markErr,
			)
			return CreativeProcessResult{}, markErr
		}
		return CreativeProcessResult{Terminal: true}, nil
	}
	// Acquire user capacity for generation; if unavailable, requeue promptly to free the worker.
	slots := scheduler.NewLease(ctx, scheduler.ReleaseOnCompletion)
	defer slots.Release()
	if w.ports.AcquireUser != nil {
		release, acquired, err := w.ports.AcquireUser(ctx, run.UserID)
		if err != nil {
			if w.ports.UserMissing != nil && w.ports.UserMissing(err) {
				_ = w.service.MarkResultLost(ctx, runID, false)
				return CreativeProcessResult{Terminal: true}, nil
			}
			return CreativeProcessResult{}, err
		}
		if !acquired {
			return CreativeProcessResult{RequeueAfter: DefaultCreativeConcurrencyRequeueDelay}, nil
		}
		slots.Own(release)
	}

	execution, err := w.executor.Prepare(ctx, *run)
	if errors.Is(err, ErrCreativeExecutionPending) {
		return CreativeProcessResult{RequeueAfter: DefaultCreativeConcurrencyRequeueDelay}, nil
	}
	if err != nil {
		return w.handleExecuteError(ctx, runID, err)
	}
	if execution == nil || execution.Target == nil {
		return w.handleExecuteError(ctx, runID, errors.New("creative execution provider is unavailable"))
	}

	// Release provider and user slots in reverse acquisition order when generation ends.
	slots.Own(execution.ReleaseFunc)
	releaseSlots := slots.Release

	// Provider admission is complete; advance to running before recording the selected provider below.
	if err := w.service.MarkRunning(ctx, runID, 0); err != nil {
		return CreativeProcessResult{}, err
	}
	// Persist provider identity before the call so saved output metadata can recover settlement.
	if execution.ProviderID > 0 {
		if err := w.repo.SetCreativeRunExecution(ctx, runID, execution.ProviderID, execution.Platform, time.Now()); err != nil {
			return CreativeProcessResult{}, err
		}
	}
	// Recheck terminal status before invoking the provider, including cancellation races.
	current, err := w.repo.GetCreativeRunByRunID(ctx, runID)
	if err != nil {
		return CreativeProcessResult{}, err
	}
	if current == nil {
		return CreativeProcessResult{}, errors.New("creative run is unavailable")
	}
	if current.Status == CreativeRunStatusCancelled || IsTerminalCreativeRunStatus(current.Status) {
		if err := w.service.CancelRunByWorker(ctx, runID); err != nil {
			return CreativeProcessResult{}, err
		}
		return CreativeProcessResult{Terminal: true}, nil
	}

	result, err := execution.Target.Execute(ctx, *current, *payload)
	releaseSlots()
	if err != nil {
		return w.handleExecuteError(ctx, runID, err)
	}
	if leaseState, _ := ctx.Value(creativeLeaseStateKey{}).(*creativeLeaseState); leaseState != nil && leaseState.lost.Load() {
		return CreativeProcessResult{}, ErrCreativeLeaseLost
	}
	if result == nil {
		return w.handleExecuteError(ctx, runID, errors.New("creative executor returned no result"))
	}
	results := make([]ProviderOutput, 0, len(result.Outputs))
	for _, output := range result.Outputs {
		results = append(results, ProviderOutput{
			Index:   output.Index,
			Success: true,
			Bytes:   output.Bytes,
			Mime:    output.Mime,
		})
	}
	// Retry recording the confirmed result while retaining its bytes in memory.
	for {
		_, recordErr := w.service.SucceedRun(ctx, runID, result.ProviderID, results)
		if recordErr == nil {
			break
		}
		if ctx.Err() != nil {
			return CreativeProcessResult{}, ctx.Err()
		}
		recorded, readErr := w.repo.GetCreativeRunByRunID(ctx, runID)
		if readErr == nil && recorded.ProviderResultRecordedAt != nil {
			return CreativeProcessResult{RequeueAfter: w.opts.ErrorRetryDelay}, nil
		}
		if errors.Is(readErr, ErrCreativeRunNotFound) {
			return CreativeProcessResult{}, readErr
		}
		valid := len(results) > 0
		for _, output := range results {
			if len(output.Bytes) == 0 {
				valid = false
			}
		}
		if !valid {
			return CreativeProcessResult{}, recordErr
		}
		timer := time.NewTimer(w.opts.ErrorRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return CreativeProcessResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	// Try settlement within this lease; the recorded provider outcome lets later attempts resume settlement.
	if err := w.service.SettleRun(ctx, runID); err != nil {
		return CreativeProcessResult{}, err
	}
	return CreativeProcessResult{Terminal: true}, nil
}

// loadPayload reads temporary payload and input bytes, returning storage errors for Process to classify.
func (w *CreativeRunWorker) loadPayload(ctx context.Context, runID string) (*CreativeRunPayload, error) {
	if w.store == nil {
		return nil, errors.New("creative transient store is not configured")
	}
	payload, err := w.store.LoadPayload(ctx, runID)
	if err != nil {
		return nil, err
	}
	if payload.SourceCount > 0 {
		inputs, err := w.store.LoadInputs(ctx, runID, payload.SourceCount)
		if err != nil {
			return nil, err
		}
		payload.Sources = make([]CreativeInputImage, 0, len(inputs))
		// Input keys contain bytes without MIME metadata, so infer MIME types from file signatures.
		for _, data := range inputs {
			payload.Sources = append(payload.Sources, CreativeInputImage{Bytes: data, Mime: upstream.SniffImageMIME(data)})
		}
	}
	if payload.HasMask {
		mask, err := w.store.LoadMask(ctx, runID)
		if err != nil {
			return nil, err
		}
		payload.Mask = &CreativeInputImage{Bytes: mask, Mime: upstream.SniffImageMIME(mask)}
	}
	return payload, nil
}

// recoverPersistedProviderResult detects completed output metadata on a task still marked running.
// It records provider completion and resumes settlement without repeating generation.
func (w *CreativeRunWorker) recoverPersistedProviderResult(ctx context.Context, run *CreativeRun) (bool, error) {
	if w == nil || run == nil || run.Status != CreativeRunStatusRunning {
		return false, nil
	}
	outputs, err := w.repo.ListCreativeRunOutputs(ctx, run.RunID)
	if err != nil {
		return false, err
	}
	if len(outputs) == 0 {
		return false, nil
	}
	successCount := 0
	for _, output := range outputs {
		if output == nil || output.Status == CreativeRunOutputStatusPending {
			return false, nil
		}
		if output.Status == CreativeRunOutputStatusSucceeded || output.Status == CreativeRunOutputStatusAcked {
			successCount++
		}
	}
	if successCount == 0 || run.ProviderID == nil || *run.ProviderID <= 0 {
		return false, nil
	}
	if err := w.repo.MarkCreativeRunProviderSucceeded(ctx, run.RunID, *run.ProviderID, time.Now()); err != nil {
		return false, err
	}
	if err := w.service.EnsureCreativeOutbox(ctx, run.RunID, CreativeRunOutboxSettle); err != nil {
		return false, err
	}
	if err := w.service.SettleRun(ctx, run.RunID); err != nil {
		return false, err
	}
	return true, nil
}

// handleExecuteError counts retryable failures and requeues below the limit; other failures enter release
// recovery.
func (w *CreativeRunWorker) handleExecuteError(ctx context.Context, runID string, execErr error) (CreativeProcessResult, error) {
	if w.executor.IsRetryable(execErr) {
		attempts, err := w.repo.IncrementCreativeRunAttempt(ctx, runID)
		if err != nil {
			return CreativeProcessResult{}, err
		}
		if attempts < w.opts.MaxAttempts {
			return CreativeProcessResult{RequeueAfter: w.opts.ErrorRetryDelay}, nil
		}
		w.warn("creative.worker_attempts_exhausted",
			"run_id", runID,
			"attempts", attempts,
			"error", execErr,
		)
	}
	code, message := creativeExecuteErrorParts(execErr)
	if err := w.service.FailRun(ctx, runID, code, message); err != nil {
		return CreativeProcessResult{}, err
	}
	return CreativeProcessResult{Terminal: true}, nil
}

// creativeExecuteErrorParts derives the persisted error code and message from an execution error.
func creativeExecuteErrorParts(err error) (string, string) {
	if upstreamErr, ok := errors.AsType[*CreativeUpstreamError](err); ok {
		if upstreamErr.Code != "" {
			return upstreamErr.Code, upstreamErr.Message
		}
		code := "PROVIDER_FAILED"
		if upstreamErr.StatusCode > 0 {
			code = "UPSTREAM_STATUS_" + itoaPositive(upstreamErr.StatusCode)
		}
		return code, upstreamErr.Message
	}
	return "PROVIDER_FAILED", SanitizeCreativeMessage(err.Error())
}

func itoaPositive(v int) string {
	if v <= 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

// runJobHeartbeat refreshes active timestamps and renewable locks, cancelling processing if ownership is lost.
func (w *CreativeRunWorker) runJobHeartbeat(ctx context.Context, runID, leaseToken string, lock CreativeRunJobLock, stop <-chan struct{}, done chan<- struct{}, leaseLost chan<- struct{}, cancel context.CancelFunc, leaseState *creativeLeaseState) {
	defer close(done)
	ticker := time.NewTicker(w.heartbeatInterval())
	defer ticker.Stop()
	var lost atomic.Bool
	markLost := func() {
		if lost.CompareAndSwap(false, true) {
			if leaseState != nil {
				leaseState.lost.Store(true)
			}
			leaseLost <- struct{}{}
			if cancel != nil {
				cancel()
			}
		}
	}
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			owned, err := w.queue.Heartbeat(ctx, runID, leaseToken)
			if err != nil && ctx.Err() == nil {
				w.warn("creative.worker_heartbeat_failed",
					"run_id", runID,
					"error", err,
				)
				// Uncertain ownership cancels processing so an old worker stops writing during a Redis outage.
				markLost()
				return
			} else if err == nil && !owned {
				w.warn("creative.worker_lease_lost",
					"run_id", runID,
				)
				markLost()
				return
			}
			if refresher, ok := lock.(CreativeRunJobLockRefresher); ok {
				owned, err := refresher.Refresh(ctx, w.opts.JobLockTTL)
				if err != nil && ctx.Err() == nil {
					w.warn("creative.worker_lock_refresh_failed",
						"run_id", runID,
						"error", err,
					)
					markLost()
					return
				} else if err == nil && !owned {
					w.warn("creative.worker_lock_lost",
						"run_id", runID,
					)
					markLost()
					return
				}
			}
		}
	}
}

func (w *CreativeRunWorker) heartbeatInterval() time.Duration {
	interval := min(w.opts.StaleActiveAfter, w.opts.JobLockTTL)
	interval /= 3
	if interval < time.Second {
		interval = time.Second
	}
	return interval
}

// MoveDueDelayedOnce returns one batch of due delayed tasks to ready.
func (w *CreativeRunWorker) MoveDueDelayedOnce(ctx context.Context) (int, error) {
	if w == nil || w.queue == nil {
		return 0, nil
	}
	return w.queue.MoveDueDelayedToReady(ctx, w.opts.DelayedMoveLimit)
}

// RunDelayedMover polls delayed tasks until cancellation, retrying immediately after a nonempty batch.
func (w *CreativeRunWorker) RunDelayedMover(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		moved, _ := w.MoveDueDelayedOnce(ctx)
		if moved > 0 {
			continue
		}
		sleepOrDone(ctx, w.opts.DelayedPollInterval)
	}
}

// RecoverStaleActiveOnce returns one batch of tasks with expired heartbeats to ready.
func (w *CreativeRunWorker) RecoverStaleActiveOnce(ctx context.Context) (int, error) {
	if w == nil || w.queue == nil {
		return 0, nil
	}
	return w.queue.RecoverStaleActive(ctx, w.opts.StaleActiveAfter, w.opts.RecoverLimit)
}

// RunStaleActiveRecovery periodically requeues stale active tasks until cancellation.
func (w *CreativeRunWorker) RunStaleActiveRecovery(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		_, _ = w.RecoverStaleActiveOnce(ctx)
		sleepOrDone(ctx, w.opts.RecoveryInterval)
	}
}

func (w *CreativeRunWorker) Options() CreativeWorkerOptions { return w.opts }
func (w *CreativeRunWorker) warn(event string, values ...any) {
	if w.ports.Observe != nil {
		w.ports.Observe(event, values...)
	}
}

func sleepOrDone(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// RunOnce reserves and processes at most one task, then acknowledges or requeues it.
func (w *CreativeRunWorker) RunOnce(ctx context.Context) error {
	return w.runOnce(ctx, nil)
}
