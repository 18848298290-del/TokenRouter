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

const (
	defaultUsageRecordWorkerCount        = 128
	defaultUsageRecordQueueSize          = 16384
	defaultUsageRecordTaskTimeoutSeconds = 5
	// Synchronous overflow runs in the submitting goroutine so billing tasks are not silently dropped (issue #3656).
	defaultUsageRecordOverflowPolicy       = "sync"
	defaultUsageRecordOverflowSampleRatio  = 10
	defaultUsageRecordAutoScaleEnabled     = true
	defaultUsageRecordAutoScaleMinWorkers  = 128
	defaultUsageRecordAutoScaleMaxWorkers  = 512
	defaultUsageRecordAutoScaleUpPercent   = 70
	defaultUsageRecordAutoScaleDownPercent = 15
	defaultUsageRecordAutoScaleUpStep      = 32
	defaultUsageRecordAutoScaleDownStep    = 16
	defaultUsageRecordAutoScaleInterval    = 3 * time.Second
	defaultUsageRecordAutoScaleCooldown    = 10 * time.Second
	usageRecordDropLogInterval             = 5 * time.Second

	UsageRecordSubmitModeEnqueued UsageRecordSubmitMode = "enqueued"
	UsageRecordSubmitModeDropped  UsageRecordSubmitMode = "dropped"
	// UsageRecordSubmitModeDroppedStopped indicates rejection by a stopped pool.
	// Callers can execute billing tasks synchronously during shutdown while retaining configured overflow policies.
	UsageRecordSubmitModeDroppedStopped UsageRecordSubmitMode = "dropped_stopped"
	UsageRecordSubmitModeSync           UsageRecordSubmitMode = "sync_fallback"
)

// UsageRecordTask is a usage-recording job.
// Tasks log their own business errors; the pool controls scheduling and supplies a timeout context.
type UsageRecordTask func(ctx context.Context)

// UsageRecordSubmitMode reports how a submission was handled.
type UsageRecordSubmitMode string

// UsageRecordWorkerPoolOptions configures queueing, overflow, and worker scaling.
type UsageRecordWorkerPoolOptions struct {
	Observe               func(Event)
	WorkerCount           int
	QueueSize             int
	TaskTimeout           time.Duration
	OverflowPolicy        string
	OverflowSamplePercent int
	AutoScaleEnabled      bool
	AutoScaleMinWorkers   int
	AutoScaleMaxWorkers   int
	AutoScaleUpPercent    int
	AutoScaleDownPercent  int
	AutoScaleUpStep       int
	AutoScaleDownStep     int
	AutoScaleInterval     time.Duration
	AutoScaleCooldown     time.Duration
}

// UsageRecordWorkerPoolStats contains current pool state and counters.
type UsageRecordWorkerPoolStats struct {
	MaxConcurrency     int
	RunningWorkers     int64
	WaitingTasks       uint64
	SubmittedTasks     uint64
	CompletedTasks     uint64
	SuccessfulTasks    uint64
	FailedTasks        uint64
	DroppedTasks       uint64
	DroppedQueueFull   uint64
	DroppedPoolStopped uint64
	SyncFallbackTasks  uint64
}

// Event carries executor diagnostics to the app logging callback.
type Event struct {
	Level, Message string
	Fields         map[string]any
}

// UsageRecordWorkerPool executes asynchronous tasks through a bounded queue.
type UsageRecordWorkerPool struct {
	observe               func(Event)
	pool                  pond.Pool
	taskTimeout           time.Duration
	overflowPolicy        string
	overflowSamplePercent int
	overflowCounter       atomic.Uint64
	droppedQueueFull      atomic.Uint64
	droppedPoolStopped    atomic.Uint64
	syncFallback          atomic.Uint64
	lastDropLogNanos      atomic.Int64
	autoScaleEnabled      bool
	autoScaleMinWorkers   int
	autoScaleMaxWorkers   int
	autoScaleUpPercent    int
	autoScaleDownPercent  int
	autoScaleUpStep       int
	autoScaleDownStep     int
	autoScaleInterval     time.Duration
	autoScaleCooldown     time.Duration
	lastScaleNanos        atomic.Int64
	autoScaleCancel       context.CancelFunc
	lifecycleWg           sync.WaitGroup
	// stateMu serializes task registration with stopping so Add completes before Wait starts.
	stateMu          sync.Mutex
	started, stopped bool
	active           sync.WaitGroup
	activeCount      atomic.Int64
	stopDone         chan struct{}
}

// Dropped reports whether the task was neither queued nor executed synchronously.
func (m UsageRecordSubmitMode) Dropped() bool {
	return m == UsageRecordSubmitModeDropped || m == UsageRecordSubmitModeDroppedStopped
}

// NewUsageRecordWorkerPoolWithOptions normalizes options and constructs the worker pool.
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

// Submit queues a usage task or applies the overflow policy.
// A full queue uses drop, sample, or synchronous execution according to configuration.
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
	// Queued workers release their registration; the submitter releases it on synchronous execution, drop, or stop races.
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

// Stats returns current pool state and counters.
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

// finishTask releases a registration. Each accepted task must call it once.
func (p *UsageRecordWorkerPool) finishTask() { p.activeCount.Add(-1); p.active.Done() }

// Stop waits without a deadline; application shutdown should pass its remaining budget to StopContext.
func (p *UsageRecordWorkerPool) Stop() { _ = p.StopContext(context.Background()) }

// StopContext waits for queued tasks, scaling, and synchronous overflow, returning an error if its context expires.
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
		go func() {
			p.lifecycleWg.Wait()
			p.pool.StopAndWait()
			p.active.Wait()
			close(p.stopDone)
		}()
	}
	done := p.stopDone
	p.stateMu.Unlock()
	// A completed stop returns success even if this caller context has since expired.
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

func (p *UsageRecordWorkerPool) startAutoScaler() {
	ctx, cancel := context.WithCancel(context.Background())
	p.autoScaleCancel = cancel

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

	// Grow by one configured step when queue occupancy reaches the threshold.
	if queuePercent >= p.autoScaleUpPercent && current < p.autoScaleMaxWorkers {
		target := min(current+p.autoScaleUpStep, p.autoScaleMaxWorkers)
		p.resizePool(current, target, queuePercent, waiting, runningPercent, queueSize, "scale_up")
		return
	}

	// Shrink only with an empty queue and low worker utilization.
	if queuePercent <= p.autoScaleDownPercent && waiting == 0 &&
		runningPercent <= p.autoScaleDownPercent &&
		current > p.autoScaleMinWorkers {
		target := max(current-p.autoScaleDownStep, p.autoScaleMinWorkers)
		p.resizePool(current, target, queuePercent, waiting, runningPercent, queueSize, "scale_down")
	}
}

func (p *UsageRecordWorkerPool) resizePool(current, target, queuePercent, waiting, runningPercent, queueSize int, action string) {
	if target == current {
		return
	}
	p.pool.Resize(target)
	p.lastScaleNanos.Store(time.Now().UnixNano())

	p.emit(Event{Level: "info", Message: "usage_record.auto_scale", Fields: map[string]any{"component": "service.usage_record_worker_pool", "action": action, "from_workers": current, "to_workers": target, "queue_percent": queuePercent, "waiting_tasks": waiting, "running_percent": runningPercent, "queue_size": queueSize}})
}

func (p *UsageRecordWorkerPool) shouldSyncFallback() bool {
	if p.overflowSamplePercent <= 0 {
		return false
	}
	n := p.overflowCounter.Add(1)
	return int((n-1)%100) < p.overflowSamplePercent
}

func (p *UsageRecordWorkerPool) execute(task UsageRecordTask) {
	ctx, cancel := context.WithTimeout(context.Background(), p.taskTimeout)
	defer cancel()

	defer func() {
		if recovered := recover(); recovered != nil {
			p.emit(Event{Level: "error", Message: "usage_record.task_panic", Fields: map[string]any{"component": "service.usage_record_worker_pool", "panic": recovered}})
		}
	}()

	task(ctx)
}

func (p *UsageRecordWorkerPool) logDrop(reason string) {
	now := time.Now().UnixNano()
	last := p.lastDropLogNanos.Load()
	if now-last < int64(usageRecordDropLogInterval) {
		return
	}
	if !p.lastDropLogNanos.CompareAndSwap(last, now) {
		return
	}

	stats := p.Stats()
	p.emit(Event{Level: "warn", Message: "usage_record.task_dropped", Fields: map[string]any{"component": "service.usage_record_worker_pool", "reason": reason, "overflow_policy": p.overflowPolicy, "running_workers": stats.RunningWorkers, "waiting_tasks": stats.WaitingTasks, "dropped_queue_full": stats.DroppedQueueFull, "dropped_pool_stopped": stats.DroppedPoolStopped, "sync_fallback_tasks": stats.SyncFallbackTasks}})
}

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

func (m UsageRecordSubmitMode) String() string {
	return string(m)
}

func (s UsageRecordWorkerPoolStats) String() string {
	return fmt.Sprintf("running=%d waiting=%d submitted=%d dropped=%d", s.RunningWorkers, s.WaitingTasks, s.SubmittedTasks, s.DroppedTasks)
}

// Start serializes with Stop and cannot restart scaling after shutdown.
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

// DefaultOptions returns the default queue, timeout, overflow, and scaling settings.
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

func (p *UsageRecordWorkerPool) emit(event Event) {
	if p.observe != nil {
		p.observe(event)
	}
}
