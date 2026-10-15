package completion

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alitto/pond/v2"
)

// worker.go runs bounded completion work and coordinates admission, overflow,
// scaling, and shutdown. Usage tasks can finish after the response and still
// need their storage services during shutdown. Admission registers work before
// releasing the state lock, then hands that registration to a queued worker or
// keeps it in the submitting caller for synchronous overflow. A stopped pool
// rejects new admission with a distinct result so entrypoint policy can decide
// whether to execute outside the pool.
//
// Stopping first seals admission and cancels scaling, then waits for the scaler,
// pond's queued work, and all registered work including inline overflow. Caller
// cancellation limits that wait while draining continues. Task contexts have their
// own cooperative timeout and recovered panics; pool success metrics therefore
// describe callback completion rather than committed billing or successful log
// writes. Scaling and diagnostic counters use independently read observations.

// Queue and scaling defaults select capacity, cooperative task budgets, congestion
// handling, and diagnostic intervals when normalized options omit valid values.
const (
	defaultUsageRecordWorkerCount        = 128   // initial worker capacity
	defaultUsageRecordQueueSize          = 16384 // waiting task capacity
	defaultUsageRecordTaskTimeoutSeconds = 5     // seconds
	// Default synchronous overflow keeps accepted billing work in the submitting
	// goroutine; StopContext includes its registration while waiting for completion.
	defaultUsageRecordOverflowPolicy       = "sync"           // inline overflow policy
	defaultUsageRecordOverflowSampleRatio  = 10               // sampled overflow percent
	defaultUsageRecordAutoScaleEnabled     = true             // default scaler enabled
	defaultUsageRecordAutoScaleMinWorkers  = 128              // minimum worker capacity
	defaultUsageRecordAutoScaleMaxWorkers  = 512              // maximum worker capacity
	defaultUsageRecordAutoScaleUpPercent   = 70               // growth queue percent
	defaultUsageRecordAutoScaleDownPercent = 15               // shrink occupancy percent
	defaultUsageRecordAutoScaleUpStep      = 32               // growth worker increment
	defaultUsageRecordAutoScaleDownStep    = 16               // shrink worker decrement
	defaultUsageRecordAutoScaleInterval    = 3 * time.Second  // scaler observation interval
	defaultUsageRecordAutoScaleCooldown    = 10 * time.Second // minimum resize interval
	usageRecordDropLogInterval             = 5 * time.Second  // minimum drop-log interval

	// Submission modes distinguish accepted queued or inline work from rejected work,
	// letting entrypoint policy decide whether rejected billing work needs its own retry.
	UsageRecordSubmitModeEnqueued UsageRecordSubmitMode = "enqueued" // accepted pond submission
	UsageRecordSubmitModeDropped  UsageRecordSubmitMode = "dropped"  // rejected queue overflow
	// UsageRecordSubmitModeDroppedStopped indicates rejection by a stopped pool.
	// Callers can execute billing tasks synchronously during shutdown while retaining
	// configured overflow policies.
	UsageRecordSubmitModeDroppedStopped UsageRecordSubmitMode = "dropped_stopped"
	UsageRecordSubmitModeSync           UsageRecordSubmitMode = "sync_fallback" // completed inline overflow
)

// UsageRecordTask is a usage-recording job.
// Tasks observe their timeout context and report business errors themselves.
// Returning normally, including after recovered task panic, can count as pond
// success even when funds or analysis persistence failed.
type UsageRecordTask func(ctx context.Context)

// UsageRecordSubmitMode reports the admission or execution outcome of one submission.
type UsageRecordSubmitMode string

// UsageRecordWorkerPoolOptions configures capacity, task budgets, queueing, overflow,
// and worker scaling.
type UsageRecordWorkerPoolOptions struct {
	// Observe receives synchronous queue, panic, and scale diagnostics; nil skips
	// emission. Shared pools can call it concurrently, so the callback supports
	// concurrent use. Callback panics can escape submission; the scaler goroutine
	// has no recovery around diagnostics, and an escaping panic terminates the process.
	Observe func(Event) // executor diagnostic callback
	// WorkerCount selects initial capacity; nonpositive values use the default,
	// and enabled scaling further clamps it inside the normalized worker range.
	WorkerCount int // initial worker capacity
	// QueueSize bounds waiting pond submissions. Nonpositive values select the
	// default capacity, which the occupancy calculations also use as their denominator.
	QueueSize int // waiting task capacity
	// TaskTimeout supplies each execution's independent context budget; nonpositive
	// values select the default. Tasks cooperate with cancellation because the pool
	// does not forcibly interrupt a running callback.
	TaskTimeout time.Duration // execution context lifetime
	// OverflowPolicy selects drop, sample, or sync after a failed queue admission.
	// Normalization trims and lowercases supported names; unknown input selects synchronous overflow.
	OverflowPolicy string // configured congestion policy
	// OverflowSamplePercent controls deterministic inline execution among full-queue
	// sample-policy submissions. Normalization bounds it to zero through one hundred;
	// sample policy replaces zero with its default percentage.
	OverflowSamplePercent int // sampled overflow percent
	// AutoScaleEnabled starts periodic observation when Start runs. Its supplied
	// false value stays disabled during normalization, while DefaultOptions chooses true.
	AutoScaleEnabled bool // periodic scaler enabled
	// AutoScaleMinWorkers bounds shrinkage when scaling is enabled. Nonpositive
	// values select the default, and disabled scaling uses normalized WorkerCount.
	AutoScaleMinWorkers int // minimum worker capacity
	// AutoScaleMaxWorkers bounds growth when scaling is enabled. Values below the
	// normalized minimum become that minimum; disabled scaling uses normalized WorkerCount.
	AutoScaleMaxWorkers int // maximum worker capacity
	// AutoScaleUpPercent triggers growth from integer queue occupancy; nonpositive
	// or greater-than-one-hundred values select the default while scaling is enabled.
	AutoScaleUpPercent int // growth queue percentage
	// AutoScaleDownPercent limits both queue and worker occupancy for shrinkage;
	// the queue must additionally be empty. Invalid or overlapping thresholds are
	// normalized below the growth threshold.
	AutoScaleDownPercent int // shrink occupancy percentage
	// AutoScaleUpStep increases capacity during a growth decision, bounded by the
	// maximum. Nonpositive increments use the default when scaling is enabled.
	AutoScaleUpStep int // growth worker increment
	// AutoScaleDownStep decreases capacity during a shrink decision, bounded by the
	// minimum. Nonpositive decrements use the default when scaling is enabled.
	AutoScaleDownStep int // shrink worker decrement
	// AutoScaleInterval spaces scaler observations after Start; nonpositive values
	// select the default when enabled, and stopping cancels the observer goroutine.
	AutoScaleInterval time.Duration // scaler observation interval
	// AutoScaleCooldown limits successive resizes by recorded wall-clock time.
	// Negative durations use the default when enabled; zero permits a resize on
	// every otherwise eligible observation.
	AutoScaleCooldown time.Duration // minimum resize interval
}

// UsageRecordWorkerPoolStats contains current pool state and counters.
// MaxConcurrency, RunningWorkers, WaitingTasks, SubmittedTasks, CompletedTasks,
// SuccessfulTasks, FailedTasks, and DroppedTasks are independent pond observations.
// DroppedQueueFull, DroppedPoolStopped, and SyncFallbackTasks are separately read
// completion counters, so the result can describe several different instants.
// Inline execution contributes to SyncFallbackTasks rather than pond completion.
type UsageRecordWorkerPoolStats struct {
	MaxConcurrency  int    // pond configured worker capacity
	RunningWorkers  int64  // pond active worker count
	WaitingTasks    uint64 // pond queued callback count
	SubmittedTasks  uint64 // pond admission attempt count
	CompletedTasks  uint64 // pond completed callback count
	SuccessfulTasks uint64 // pond successful callback count
	FailedTasks     uint64 // pond failed callback count
	// DroppedTasks counts pond rejection even when completion subsequently executes
	// the callback inline; that inline success does not remove the rejection count.
	DroppedTasks       uint64 // pond rejected submission count
	DroppedQueueFull   uint64 // completion full-queue loss count
	DroppedPoolStopped uint64 // completion stopped-pool rejection count
	SyncFallbackTasks  uint64 // inline overflow execution count
}

// Event carries executor diagnostics to the app logging callback.
type Event struct {
	// Level: diagnostic severity label. Message: stable diagnostic event text.
	Level, Message string
	// Fields retains supplied attributes and any nested references until Observe
	// finishes its synchronous read; callers serialize mutation during emission.
	Fields map[string]any
}

// UsageRecordWorkerPool tracks bounded queue admission, inline overflow, and one-way stop.
// stateMu protects started, stopped, autoScaleCancel, and stopDone publication, plus
// active registration before stop begins waiting. Atomic counters expose separately
// read diagnostic state; pond owns worker and queue synchronization.
type UsageRecordWorkerPool struct {
	observe               func(Event)        // shared diagnostic callback
	pool                  pond.Pool          // bounded pond execution pool
	taskTimeout           time.Duration      // cooperative execution context lifetime
	overflowPolicy        string             // normalized congestion handling mode
	overflowSamplePercent int                // deterministic inline sample percentage
	overflowCounter       atomic.Uint64      // sample overflow ordinal counter
	droppedQueueFull      atomic.Uint64      // full-queue task loss count
	droppedPoolStopped    atomic.Uint64      // stopped-pool admission rejection count
	syncFallback          atomic.Uint64      // inline overflow execution count
	lastDropLogNanos      atomic.Int64       // last elected diagnostic Unix nanoseconds
	autoScaleEnabled      bool               // configured periodic scaling enabled
	autoScaleMinWorkers   int                // normalized minimum worker capacity
	autoScaleMaxWorkers   int                // normalized maximum worker capacity
	autoScaleUpPercent    int                // normalized growth queue percentage
	autoScaleDownPercent  int                // normalized shrink occupancy percentage
	autoScaleUpStep       int                // normalized growth worker increment
	autoScaleDownStep     int                // normalized shrink worker decrement
	autoScaleInterval     time.Duration      // configured scaler observation interval
	autoScaleCooldown     time.Duration      // configured resize cooldown duration
	lastScaleNanos        atomic.Int64       // last resize Unix nanoseconds
	autoScaleCancel       context.CancelFunc // published scaler cancellation function
	lifecycleWg           sync.WaitGroup     // started scaler lifetime tracking
	// stateMu, active, and activeCount register admission before stopped can seal
	// the pool. Each registration transfers to one worker or is completed by the
	// submitter, allowing StopContext to wait for both queued and inline execution.
	stateMu sync.Mutex // admission and stop publication lock
	// started: scaler start requested. stopped: admission permanently sealed.
	started, stopped bool
	active           sync.WaitGroup // registered queued and inline tasks
	activeCount      atomic.Int64   // registered unfinished task count
	stopDone         chan struct{}  // shared completed-drain signal
}

// Dropped reports whether the mode names a rejected submission.
// Queue congestion and stopped-pool rejection return true; queued, inline, and
// unrecognized labels return false so submission policy can decide about retrying.
func (m UsageRecordSubmitMode) Dropped() bool {
	return m == UsageRecordSubmitModeDropped || m == UsageRecordSubmitModeDroppedStopped
}

// NewUsageRecordWorkerPoolWithOptions normalizes capacity and policy, then creates
// the bounded pond pool. Submission can execute before Start, while periodic
// scaling waits for Start. The caller owns stopping this pool before closing its
// tasks' storage dependencies.
func NewUsageRecordWorkerPoolWithOptions(opts UsageRecordWorkerPoolOptions) *UsageRecordWorkerPool {
	opts = NormalizeOptions(opts)

	p := &UsageRecordWorkerPool{
		observe:               opts.Observe,
		taskTimeout:           opts.TaskTimeout,
		overflowPolicy:        opts.OverflowPolicy,
		overflowSamplePercent: opts.OverflowSamplePercent,
		autoScaleEnabled:      opts.AutoScaleEnabled,
		autoScaleMinWorkers:   opts.AutoScaleMinWorkers,
		autoScaleMaxWorkers:   opts.AutoScaleMaxWorkers,
		autoScaleUpPercent:    opts.AutoScaleUpPercent,
		autoScaleDownPercent:  opts.AutoScaleDownPercent,
		autoScaleUpStep:       opts.AutoScaleUpStep,
		autoScaleDownStep:     opts.AutoScaleDownStep,
		autoScaleInterval:     opts.AutoScaleInterval,
		autoScaleCooldown:     opts.AutoScaleCooldown,
	}

	p.pool = pond.NewPool(
		opts.WorkerCount,
		pond.WithQueueSize(opts.QueueSize),
	)

	return p
}

// Submit admits a task or applies the configured congestion policy.
// Nil receiver/task returns the ordinary dropped mode. A sealed or stopped pool
// returns its distinct stopped mode so entrypoint submission policy can react.
//
// Registration happens under stateMu before stop can wait. An accepted queued
// worker releases it on completion; the submitter releases it after inline work
// or rejection. Failed queue admission rechecks pond's stopped state before
// selecting sync, deterministic sample, or drop. Inline execution runs before
// returning and remains included in StopContext's accepted-work wait.
func (p *UsageRecordWorkerPool) Submit(task UsageRecordTask) UsageRecordSubmitMode {
	if p == nil || task == nil {
		return UsageRecordSubmitModeDropped
	}
	p.stateMu.Lock()
	if p.stopped || p.pool == nil || p.pool.Stopped() {
		p.stateMu.Unlock()
		p.droppedPoolStopped.Add(1)
		p.logDrop("stopped")
		return UsageRecordSubmitModeDroppedStopped
	}

	p.active.Add(1)
	p.activeCount.Add(1)
	p.stateMu.Unlock()
	// One registration survives the unlocked pond admission attempt. Transfer its
	// cleanup to an accepted worker; every rejection or inline path finishes it here.
	transferred := false
	defer func() {
		if !transferred {
			p.finishTask()
		}
	}()
	_, ok := p.pool.TrySubmit(func() {
		defer p.finishTask()
		p.execute(task)
	})
	if ok {
		transferred = true
		return UsageRecordSubmitModeEnqueued
	}

	// A stop can win after registration but before pond acceptance; preserve its
	// distinct rejection mode before any configured full-queue policy is applied.
	if p.pool.Stopped() {
		p.droppedPoolStopped.Add(1)
		p.logDrop("stopped")
		return UsageRecordSubmitModeDroppedStopped
	}

	switch p.overflowPolicy {
	case "sync":
		p.syncFallback.Add(1)
		p.execute(task)
		return UsageRecordSubmitModeSync
	case "sample":
		if p.shouldSyncFallback() {
			p.syncFallback.Add(1)
			p.execute(task)
			return UsageRecordSubmitModeSync
		}
	}

	p.droppedQueueFull.Add(1)
	p.logDrop("full")
	return UsageRecordSubmitModeDropped
}

// Stats reads pond metrics and separate completion counters for diagnostics.
// Nil receiver or missing pond pool returns zeros. Values are read independently;
// pond callback counts exclude inline overflow, and recovered/business task failures
// can still count as successful callback returns.
func (p *UsageRecordWorkerPool) Stats() UsageRecordWorkerPoolStats {
	if p == nil || p.pool == nil {
		return UsageRecordWorkerPoolStats{}
	}
	return UsageRecordWorkerPoolStats{
		MaxConcurrency:     p.pool.MaxConcurrency(),
		RunningWorkers:     p.pool.RunningWorkers(),
		WaitingTasks:       p.pool.WaitingTasks(),
		SubmittedTasks:     p.pool.SubmittedTasks(),
		CompletedTasks:     p.pool.CompletedTasks(),
		SuccessfulTasks:    p.pool.SuccessfulTasks(),
		FailedTasks:        p.pool.FailedTasks(),
		DroppedTasks:       p.pool.DroppedTasks(),
		DroppedQueueFull:   p.droppedQueueFull.Load(),
		DroppedPoolStopped: p.droppedPoolStopped.Load(),
		SyncFallbackTasks:  p.syncFallback.Load(),
	}
}

// finishTask releases one registration from activeCount and active.
// Call it once for every successful registration in Submit, from the accepted
// worker or from the submitter when ownership was not transferred.
func (p *UsageRecordWorkerPool) finishTask() { p.activeCount.Add(-1); p.active.Done() }

// Stop waits for this pool to drain using a background context.
// It has no caller deadline and discards the delegated error; application shutdown
// uses StopContext with its remaining cleanup budget.
func (p *UsageRecordWorkerPool) Stop() { _ = p.StopContext(context.Background()) }

// StopContext seals admission and waits for scaling, queued work, and registered
// inline overflow to finish. Nil receiver or absent pond pool returns nil;
// other callers supply a nonnil context for their waiting budget.
//
// The first caller publishes stopped and one completion channel while holding
// stateMu. Its background drain waits for the scaler before pond shutdown, then
// for all registrations before closing that channel. Repeated callers share the
// same drain, and a completed drain returns success even with an expired caller
// context. An expired wait returns the unfinished count while draining continues;
// it does not cancel the accepted tasks' independent execution contexts.
func (p *UsageRecordWorkerPool) StopContext(ctx context.Context) error {
	if p == nil || p.pool == nil {
		return nil
	}
	p.stateMu.Lock()
	if !p.stopped {
		p.stopped = true
		p.stopDone = make(chan struct{})
		if p.autoScaleCancel != nil {
			p.autoScaleCancel()
		}
		// Seal registration before waiting, then finish the scaler before pond stop
		// so a final resize cannot race shutdown; inline registrations drain last.
		go func() {
			p.lifecycleWg.Wait()
			p.pool.StopAndWait()
			p.active.Wait()
			close(p.stopDone)
		}()
	}
	done := p.stopDone
	p.stateMu.Unlock()
	// Check completed drain first so a previously finished stop is successful even
	// when a repeated caller arrives with an already expired waiting context.
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("completion stop: %d unfinished tasks: %w", p.activeCount.Load(), ctx.Err())
	}
}

// startAutoScaler starts one ticker-driven observer with independent cancellation.
// Start calls it under stateMu and publishes its cancel function before stop can
// wait. The lifecycle wait group keeps this observer ahead of pond shutdown.
func (p *UsageRecordWorkerPool) startAutoScaler() {
	ctx, cancel := context.WithCancel(context.Background())
	p.autoScaleCancel = cancel

	// Register the observer while Start holds stateMu; Stop waits for ticker cleanup
	// and the last selected tick before pond can be sealed and drained.
	p.lifecycleWg.Go(func() {
		ticker := time.NewTicker(p.autoScaleInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.autoScaleTick()
			}
		}
	})
}

// autoScaleTick evaluates current queue and worker observations for a resize.
// Missing/stopped pools, invalid capacities, or an active cooldown skip decisions;
// percentages use integer arithmetic over independently read counters.
//
// Growth is considered first when waiting occupancy reaches its threshold and
// capacity is below maximum. Shrinkage also requires no waiting callbacks and low
// worker occupancy, so an empty queue alone cannot reduce a busy pool. Both paths
// move by one bounded configured step. The stop drain waits for this observer
// before stopping pond, allowing an in-flight observation to finish its decision.
func (p *UsageRecordWorkerPool) autoScaleTick() {
	if p == nil || p.pool == nil || p.pool.Stopped() {
		return
	}
	queueSize := p.pool.QueueSize()
	if queueSize <= 0 {
		return
	}
	current := p.pool.MaxConcurrency()
	waiting := int(p.pool.WaitingTasks())
	running := int(p.pool.RunningWorkers())
	if current <= 0 || waiting < 0 {
		return
	}
	// Integer percentages approximate independently read queue and worker occupancy;
	// they decide a bounded capacity step rather than represent one atomic pool sample.
	queuePercent := waiting * 100 / queueSize
	runningPercent := 0
	if current > 0 {
		runningPercent = running * 100 / current
	}

	now := time.Now()
	lastScaleNanos := p.lastScaleNanos.Load()
	if lastScaleNanos > 0 && now.Sub(time.Unix(0, lastScaleNanos)) < p.autoScaleCooldown {
		return
	}

	// Waiting callbacks supply the growth signal; bound the chosen increment so
	// sustained queue pressure cannot take concurrency beyond the configured maximum.
	if queuePercent >= p.autoScaleUpPercent && current < p.autoScaleMaxWorkers {
		target := min(current+p.autoScaleUpStep, p.autoScaleMaxWorkers)
		p.resizePool(current, target, queuePercent, waiting, runningPercent, queueSize, "scale_up")
		return
	}

	// An empty waiting queue can still have occupied workers; require low worker
	// occupancy as well so the scaler does not shrink solely after dispatching work.
	if queuePercent <= p.autoScaleDownPercent && waiting == 0 &&
		runningPercent <= p.autoScaleDownPercent &&
		current > p.autoScaleMinWorkers {
		target := max(current-p.autoScaleDownStep, p.autoScaleMinWorkers)
		p.resizePool(current, target, queuePercent, waiting, runningPercent, queueSize, "scale_down")
	}
}

// resizePool applies one selected capacity and records its wall-clock timestamp.
// An unchanged target returns immediately. The live scaler supplies positive
// bounded targets, and callers serialize its lifetime before pond stop; emitted
// fields retain the observations used for that resize decision.
func (p *UsageRecordWorkerPool) resizePool(current, target, queuePercent, waiting, runningPercent, queueSize int, action string) {
	if target == current {
		return
	}
	// Publish capacity before its cooldown timestamp and diagnostic; the scaler
	// can then compare later observations against the newly selected worker limit.
	p.pool.Resize(target)
	p.lastScaleNanos.Store(time.Now().UnixNano())

	p.emit(Event{Level: "info", Message: "usage_record.auto_scale", Fields: map[string]any{"component": "service.usage_record_worker_pool", "action": action, "from_workers": current, "to_workers": target, "queue_percent": queuePercent, "waiting_tasks": waiting, "running_percent": runningPercent, "queue_size": queueSize}})
}

// shouldSyncFallback selects the configured first percentage of a hundred-count
// cycle using an atomic overflow ordinal. Nonpositive percentages return false
// without advancing; Submit calls it only for sample-policy full-queue overflow.
func (p *UsageRecordWorkerPool) shouldSyncFallback() bool {
	if p.overflowSamplePercent <= 0 {
		return false
	}
	// Sampling is deterministic from the overflow ordinal, so the first eligible
	// rejection in a new cycle executes inline even for a one-percent policy.
	n := p.overflowCounter.Add(1)
	return int((n-1)%100) < p.overflowSamplePercent
}

// execute runs one callback with an independent cooperative timeout context.
// It cancels that context after return and consumes task panics through the
// optional diagnostic observer. Callback business errors remain callback-owned,
// and an observer panic can escape the recovery handler.
func (p *UsageRecordWorkerPool) execute(task UsageRecordTask) {
	ctx, cancel := context.WithTimeout(context.Background(), p.taskTimeout)
	defer cancel()

	// Consume callback panics here for both queued and inline tasks; completion
	// errors logged inside the callback remain separate from pond callback success.
	defer func() {
		if recovered := recover(); recovered != nil {
			p.emit(Event{Level: "error", Message: "usage_record.task_panic", Fields: map[string]any{"component": "service.usage_record_worker_pool", "panic": recovered}})
		}
	}()

	task(ctx)
}

// logDrop elects at most one diagnostic emitter inside its wall-clock interval.
// Drop counters are updated by callers before invoking it, so suppressed logs
// still leave rejected-work evidence available through Stats.
//
// The timestamp compare-and-swap resolves concurrent attempts to emit from the
// same prior value. The elected caller publishes the timestamp before reading
// metrics and invoking Observe, and those separately read counters can advance
// in the meantime. Callback failure can follow that timestamp publication;
// subsequent callers still observe the chosen suppression window.
func (p *UsageRecordWorkerPool) logDrop(reason string) {
	now := time.Now().UnixNano()
	last := p.lastDropLogNanos.Load()
	if now-last < int64(usageRecordDropLogInterval) {
		return
	}
	// Competing drop reports can share the same previous timestamp; one elected
	// emitter publishes the window while all callers keep their already incremented counts.
	if !p.lastDropLogNanos.CompareAndSwap(last, now) {
		return
	}

	stats := p.Stats()
	p.emit(Event{Level: "warn", Message: "usage_record.task_dropped", Fields: map[string]any{"component": "service.usage_record_worker_pool", "reason": reason, "overflow_policy": p.overflowPolicy, "running_workers": stats.RunningWorkers, "waiting_tasks": stats.WaitingTasks, "dropped_queue_full": stats.DroppedQueueFull, "dropped_pool_stopped": stats.DroppedPoolStopped, "sync_fallback_tasks": stats.SyncFallbackTasks}})
}

// NormalizeOptions fills missing capacity and budget settings and validates policy
// ranges. It retains the supplied scaler-enabled flag; DefaultOptions is the
// entrypoint that chooses enabled scaling by default.
//
// Enabled scaling repairs minimum/maximum order, clamps initial workers inside
// that range, and separates growth from shrink thresholds. Queue-sample percentages
// are clamped, with zero under sample policy selecting its configured default.
// Nonpositive observation intervals receive a default, negative cooldowns receive
// a default, and zero cooldown remains valid. Disabled scaling fixes its worker
// range to the normalized initial capacity.
func NormalizeOptions(opts UsageRecordWorkerPoolOptions) UsageRecordWorkerPoolOptions {
	if opts.WorkerCount <= 0 {
		opts.WorkerCount = defaultUsageRecordWorkerCount
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultUsageRecordQueueSize
	}
	if opts.TaskTimeout <= 0 {
		opts.TaskTimeout = time.Duration(defaultUsageRecordTaskTimeoutSeconds) * time.Second
	}
	switch strings.ToLower(strings.TrimSpace(opts.OverflowPolicy)) {
	case "drop", "sample", "sync":
		opts.OverflowPolicy = strings.ToLower(strings.TrimSpace(opts.OverflowPolicy))
	default:
		opts.OverflowPolicy = defaultUsageRecordOverflowPolicy
	}
	if opts.OverflowSamplePercent < 0 {
		opts.OverflowSamplePercent = 0
	}
	if opts.OverflowSamplePercent > 100 {
		opts.OverflowSamplePercent = 100
	}
	if opts.OverflowPolicy == "sample" && opts.OverflowSamplePercent == 0 {
		opts.OverflowSamplePercent = defaultUsageRecordOverflowSampleRatio
	}
	if opts.AutoScaleEnabled {
		if opts.AutoScaleMinWorkers <= 0 {
			opts.AutoScaleMinWorkers = defaultUsageRecordAutoScaleMinWorkers
		}
		if opts.AutoScaleMaxWorkers <= 0 {
			opts.AutoScaleMaxWorkers = defaultUsageRecordAutoScaleMaxWorkers
		}
		if opts.AutoScaleMaxWorkers < opts.AutoScaleMinWorkers {
			opts.AutoScaleMaxWorkers = opts.AutoScaleMinWorkers
		}
		if opts.WorkerCount < opts.AutoScaleMinWorkers {
			opts.WorkerCount = opts.AutoScaleMinWorkers
		}
		if opts.WorkerCount > opts.AutoScaleMaxWorkers {
			opts.WorkerCount = opts.AutoScaleMaxWorkers
		}
		if opts.AutoScaleUpPercent <= 0 || opts.AutoScaleUpPercent > 100 {
			opts.AutoScaleUpPercent = defaultUsageRecordAutoScaleUpPercent
		}
		if opts.AutoScaleDownPercent < 0 || opts.AutoScaleDownPercent >= 100 {
			opts.AutoScaleDownPercent = defaultUsageRecordAutoScaleDownPercent
		}
		// Overlapping thresholds would make growth and shrink decisions compete; halve
		// the repaired growth threshold to keep the shrink condition below it.
		if opts.AutoScaleDownPercent >= opts.AutoScaleUpPercent {
			opts.AutoScaleDownPercent = max(0, opts.AutoScaleUpPercent/2)
		}
		if opts.AutoScaleUpStep <= 0 {
			opts.AutoScaleUpStep = defaultUsageRecordAutoScaleUpStep
		}
		if opts.AutoScaleDownStep <= 0 {
			opts.AutoScaleDownStep = defaultUsageRecordAutoScaleDownStep
		}
		if opts.AutoScaleInterval <= 0 {
			opts.AutoScaleInterval = defaultUsageRecordAutoScaleInterval
		}
		if opts.AutoScaleCooldown < 0 {
			opts.AutoScaleCooldown = defaultUsageRecordAutoScaleCooldown
		}
	} else {
		opts.AutoScaleMinWorkers = opts.WorkerCount
		opts.AutoScaleMaxWorkers = opts.WorkerCount
	}
	return opts
}

// String returns the submission mode label used in diagnostics and assertions.
func (m UsageRecordSubmitMode) String() string {
	return string(m)
}

// String formats selected queue and task observations for a compact diagnostic
// summary using this already captured statistics value.
func (s UsageRecordWorkerPoolStats) String() string {
	return fmt.Sprintf("running=%d waiting=%d submitted=%d dropped=%d", s.RunningWorkers, s.WaitingTasks, s.SubmittedTasks, s.DroppedTasks)
}

// Start publishes one scaler start under the same state lock used by StopContext.
// Use a constructed nonnil pool. Repeated starts and starts after admission is
// sealed do nothing; enabled scaling registers its lifecycle before releasing the lock.
func (p *UsageRecordWorkerPool) Start() {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.started || p.stopped {
		return
	}
	p.started = true
	if p.autoScaleEnabled {
		p.startAutoScaler()
	}
}

// DefaultOptions returns the default worker, queue, timeout, congestion, and
// enabled-scaling settings for callers that want the complete default policy.
func DefaultOptions() UsageRecordWorkerPoolOptions {
	return UsageRecordWorkerPoolOptions{
		WorkerCount:           defaultUsageRecordWorkerCount,
		QueueSize:             defaultUsageRecordQueueSize,
		TaskTimeout:           time.Duration(defaultUsageRecordTaskTimeoutSeconds) * time.Second,
		OverflowPolicy:        defaultUsageRecordOverflowPolicy,
		OverflowSamplePercent: defaultUsageRecordOverflowSampleRatio,
		AutoScaleEnabled:      defaultUsageRecordAutoScaleEnabled,
		AutoScaleMinWorkers:   defaultUsageRecordAutoScaleMinWorkers,
		AutoScaleMaxWorkers:   defaultUsageRecordAutoScaleMaxWorkers,
		AutoScaleUpPercent:    defaultUsageRecordAutoScaleUpPercent,
		AutoScaleDownPercent:  defaultUsageRecordAutoScaleDownPercent,
		AutoScaleUpStep:       defaultUsageRecordAutoScaleUpStep,
		AutoScaleDownStep:     defaultUsageRecordAutoScaleDownStep,
		AutoScaleInterval:     defaultUsageRecordAutoScaleInterval,
		AutoScaleCooldown:     defaultUsageRecordAutoScaleCooldown,
	}
}

// emit passes a diagnostic to the optional shared observer synchronously.
// A nil observer skips emission. The event retains its Fields map during the
// call, and callback concurrency or panic handling belongs to the supplied observer.
func (p *UsageRecordWorkerPool) emit(event Event) {
	if p.observe != nil {
		p.observe(event)
	}
}
