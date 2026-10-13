package provider

import (
	"context"
	"errors"
	"sync"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Grok import probing begins after provider creation and runs through a bounded
// queue. Only OAuth Grok routing snapshots are admitted; the queue retains a
// prober and provider ID rather than the supplied snapshot. Pending and
// executing IDs share a deduplication decision, while completed IDs can be
// scheduled again.
//
// Workers are created on demand and exit when no queued task remains. Queue
// admission, pending-to-executing transfer, and worker counts change under one
// mutex so scheduling cannot lose an ID between states. Shutdown closes
// admission before discarding pending tasks and cancelling tracked workers. A
// probe's timeout begins after dequeue, and a worker that ignores cancellation
// can outlive the caller's shutdown budget.

const (
	// Import probing caps pending tasks independently of worker concurrency and
	// applies the default timeout only after a worker takes a task.
	grokImportProbeTimeout    = 25 * time.Second
	grokImportProbeQueueLimit = 64
)

// ErrImportProbeStopped rejects worker registration after the shared probe
// activity has entered its shutdown state and closed admission to new work.
var ErrImportProbeStopped = errors.New("provider import probes are stopped")

// GrokImportProbeResult contains the minimal upstream observation used in
// import logs.
type GrokImportProbeResult struct {
	Model           string // observed upstream model identifier
	StatusCode      int    // observed HTTP response status
	HeadersObserved bool   // quota response headers observed
}

// GrokImportProbeOptions selects worker limits, task budgets, and import
// observation logging.
type GrokImportProbeOptions struct {
	// Concurrency caps lazy worker creation. NewGrokImportProbeScheduler replaces
	// nonpositive values with one before retaining the effective scheduler limit.
	Concurrency int
	// Timeout is the budget for each dequeued probe. Nonpositive values select
	// twenty-five seconds, and queue waiting consumes none of that budget.
	// QueryQuota must honor its context deadline for execution to end on time.
	Timeout time.Duration
	Debug,  // rejected queue admission logging
	Info, // completed probe observation logging
	Warn, // failed probe observation logging
	Error func(string, ...any) // recovered probe panic logging
}

// GrokImportProber queries quota for one imported provider under a supplied
// context.
type GrokImportProber interface {
	// QueryQuota observes one provider under the supplied execution and shutdown
	// context. Implementations return a minimal log summary and respond to
	// cancellation so scheduler shutdown can wait for their work.
	QueryQuota(ctx context.Context, providerID int64) (*GrokImportProbeResult, error)
}

// grokImportProbeTask pairs an imported provider ID with its quota-query
// dependency.
type grokImportProbeTask struct {
	// prober remains available while this task is queued or executing. The
	// scheduler releases the queue reference as the task is dequeued.
	prober     GrokImportProber
	providerID int64 // imported local provider identifier
}

// GrokImportProbeScheduler coordinates bounded import probes with deduplication
// and tracked shutdown.
type GrokImportProbeScheduler struct {
	options GrokImportProbeOptions // normalized import logging callbacks
	// mu protects stopped, queue, pending, inFlight, workers, and maxWorkers.
	// pending becomes inFlight in the same critical section, while activity
	// separately tracks launched worker lifetimes for shutdown.
	stopped     bool                  // queue admission closed flag
	activity    operationActivity     // tracked worker shutdown state
	mu          sync.Mutex            // queue admission state lock
	queue       []grokImportProbeTask // pending probe task order
	pending     map[int64]struct{}    // queued provider identity set
	inFlight    map[int64]struct{}    // executing provider identity set
	concurrency int                   // configured lazy worker ceiling
	// workers counts queue consumers. nextTask reduces it before the goroutine's
	// deferred activity completion, so a retiring worker can still be tracked by
	// operationActivity after leaving this count.
	workers    int           // current queue worker count
	maxWorkers int           // highest queue worker count
	timeout    time.Duration // dequeued probe execution budget
}

// NewGrokImportProbeScheduler creates an idle queue with lazy workers.
// Nonpositive concurrency selects one, nonpositive timeout selects twenty-five
// seconds, and missing log callbacks become no-ops; Schedule starts execution
// when work is admitted.
func NewGrokImportProbeScheduler(options GrokImportProbeOptions) *GrokImportProbeScheduler {
	concurrency, timeout := options.Concurrency, options.Timeout
	if options.Debug == nil {
		options.Debug = func(string, ...any) {}
	}
	if options.Info == nil {
		options.Info = func(string, ...any) {}
	}
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	if options.Error == nil {
		options.Error = func(string, ...any) {}
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	if timeout <= 0 {
		timeout = grokImportProbeTimeout
	}
	return &GrokImportProbeScheduler{
		options:     options,
		concurrency: concurrency,
		timeout:     timeout,
		pending:     make(map[int64]struct{}),
		inFlight:    make(map[int64]struct{}),
	}
}

// Schedule admits a valid Grok OAuth provider unless its ID is queued,
// executing, or the queue is full. Invalid inputs and a stopped scheduler are
// skipped. The supplied snapshot is read during admission; the prober is
// retained until execution finishes.
//
// Admission records the pending ID and starts at most the configured worker
// count under the queue mutex. Workers are registered with operationActivity
// before launch so shutdown can cancel and wait for them. Finished IDs leave
// the deduplication set and may be admitted by a later call.
func (s *GrokImportProbeScheduler) Schedule(prober GrokImportProber, provider *ProviderSnapshot) {
	if s == nil || prober == nil || provider == nil || provider.ID <= 0 {
		return
	}
	if provider.Platform != PlatformGrok || provider.Type != ProviderTypeOAuth {
		return
	}

	// Admission and both deduplication states share this lock. Keep pending
	// insertion and worker registration together so shutdown can account for
	// admitted work.
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if _, exists := s.pending[provider.ID]; exists {
		s.mu.Unlock()
		return
	}
	if _, exists := s.inFlight[provider.ID]; exists {
		s.mu.Unlock()
		return
	}
	// Full pending capacity drops this best-effort observation. Dropping admission
	// does not undo the import's provider write, and executing tasks retain their
	// own concurrency slots.
	if len(s.queue) >= grokImportProbeQueueLimit {
		s.mu.Unlock()
		s.options.Debug("grok_import_active_probe_dropped", "provider_id", provider.ID, "reason", "queue_full")
		return
	}
	s.queue = append(s.queue, grokImportProbeTask{prober: prober, providerID: provider.ID})
	s.pending[provider.ID] = struct{}{}
	if s.workers < s.concurrency {
		s.workers++
		if s.workers > s.maxWorkers {
			s.maxWorkers = s.workers
		}
		// Register before launching so StopContext can cancel and wait for
		// every accepted worker, including one that has not started consuming
		// its first task.
		ctx, done, err := s.activity.begin(context.Background(), ErrImportProbeStopped)
		if err != nil {
			s.workers--
			s.mu.Unlock()
			return
		}
		go func() { defer done(); s.worker(ctx) }()
	}
	s.mu.Unlock()
}

// worker processes queued probes until the queue is empty or stopped. It
// finishes each executing ID after run returns, including skipped or failed
// probes, so that ID can be scheduled again.
func (s *GrokImportProbeScheduler) worker(ctx context.Context) {
	for {
		task, ok := s.nextTask()
		if !ok {
			return
		}
		s.run(ctx, task.prober, task.providerID)
		s.finish(task.providerID)
	}
}

// nextTask transfers the next queued ID into the executing set under the
// admission mutex. The pending marker is removed in the same critical section,
// so Schedule sees the ID in one deduplication state throughout that
// transition.
//
// An empty or stopped queue retires the calling worker by reducing its worker
// count. Cleared queue slots release retained prober references, and consuming
// the last queued entry releases the queue slice while the selected task
// remains executing.
func (s *GrokImportProbeScheduler) nextTask() (grokImportProbeTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || len(s.queue) == 0 {
		s.workers--
		return grokImportProbeTask{}, false
	}
	// Move the ID between deduplication sets before releasing admission.
	// Clearing the consumed slot also drops its queued prober reference while
	// this task retains it.
	task := s.queue[0]
	s.queue[0] = grokImportProbeTask{}
	s.queue = s.queue[1:]
	if len(s.queue) == 0 {
		s.queue = nil
	}
	delete(s.pending, task.providerID)
	s.inFlight[task.providerID] = struct{}{}
	return task, true
}

// finish removes an executing provider ID after its probe has returned. The
// admission mutex coordinates that removal with Schedule, allowing later
// requests for the ID to start another probe.
func (s *GrokImportProbeScheduler) finish(providerID int64) {
	s.mu.Lock()
	// Completion reopens admission for this ID under the same lock that guards
	// pending checks, so a subsequent Schedule sees the updated deduplication
	// state.
	delete(s.inFlight, providerID)
	s.mu.Unlock()
}

// run executes one best-effort probe with a timeout that starts after dequeue.
// Cancellation observed before preparation skips the query; failures, empty
// results, and panic categories are logged. QueryQuota must honor its context
// deadline and cancellation; a noncooperative call can return after the budget
// and still have its result logged.
func (s *GrokImportProbeScheduler) run(parent context.Context, prober GrokImportProber, providerID int64) {
	// Check cancellation after dequeue, before preparing the query. Cancellation
	// observed here skips this task; QueryQuota receives the context for any
	// cancellation racing with that check.
	if parent.Err() != nil {
		return
	}
	defer func() {
		// Report the panic category while keeping its raw value out of import logs.
		// Recovery returns control to the worker so it can finish this provider ID.
		if recovered := recover(); recovered != nil {
			s.options.Error(
				"grok_import_active_probe_panic",
				"provider_id", providerID,
				"recovery_type", panicType(recovered),
			)
		}
	}()

	// Start the task budget after queue waiting has finished. Query
	// preparation, quota coordination waits, and the upstream request share
	// this execution deadline.
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	result, err := prober.QueryQuota(ctx, providerID)
	// Probing is a best-effort observation after import creation. Log structured
	// status and reason so an upstream error's raw message stays out of routine
	// import diagnostics.
	if err != nil {
		s.options.Warn(
			"grok_import_active_probe_failed",
			"provider_id", providerID,
			"status", int(infraerrors.FromError(err).Code),
			"reason", infraerrors.Reason(err),
		)
		return
	}
	// A successful query with no summary supplies no completed observation.
	// Classify it as empty_result while keeping the import's creation outcome
	// independent from this probe.
	if result == nil {
		s.options.Warn(
			"grok_import_active_probe_failed",
			"provider_id", providerID,
			"reason", "empty_result",
		)
		return
	}

	s.options.Info(
		"grok_import_active_probe_completed",
		"provider_id", providerID,
		"model", result.Model,
		"status", result.StatusCode,
		"headers_observed", result.HeadersObserved,
	)
}

// panicType classifies a recovered panic without returning its error message or
// value.
func panicType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case error:
		return "error"
	default:
		return "unknown"
	}
}

// StopContext closes admission, discards pending probes, and cancels tracked
// workers. Its worker wait observes the supplied context after synchronous
// mutex acquisition and cancellation preparation, which that context cannot
// interrupt. A nil scheduler succeeds immediately.
//
// The admission mutex closes scheduling before worker cancellation begins.
// Executing probes must honor cancellation for the wait to drain them. Once
// operationActivity's first stop finishes, later calls reuse its result,
// including an unfinished-work timeout; a concurrent caller can return its own
// context error while that first stop is still pending. Supply a nonnil context
// for those waits. A timeout leaves noncooperative workers able to continue.
func (s *GrokImportProbeScheduler) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	// Close admission before dropping pending work, then cancel the separately
	// tracked workers. This ordering prevents shutdown from missing a
	// concurrent worker registration.
	s.mu.Lock()
	s.stopped = true
	s.queue = nil
	clear(s.pending)
	s.mu.Unlock()
	return s.activity.stop(ctx, "provider import probes")
}
