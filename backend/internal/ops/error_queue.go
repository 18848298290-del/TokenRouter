package ops

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// error_queue.go queues request-error observations and coordinates bounded
// admission, batch workers, and shutdown.
// Request error observations wait in a bounded channel and use an additional
// estimated payload-byte budget. Workers release queue reservations as they
// take jobs into local batches, so the budget measures waiting payloads rather
// than total live memory. Batch processing groups entries by OpsService and
// suppresses persistence errors; Processed records attempted entries rather
// than confirmed stored rows.
//
// Startup is lazy and serialized with queue shutdown. Enqueue holds the queue
// read lock while reserving and sending, while Shutdown removes and closes the
// shared channel under its write lock. Workers retain their captured channel
// and consume its remaining jobs before a join publishes completion. Each
// caller's context governs its later completion wait, but shutdown setup
// happens before that select and background batch writes keep their independent
// timeouts. Entries remain shared mutable pointers through sanitization and
// asynchronous persistence.

// These budgets and bounds govern error-worker counts, waiting capacity,
// batching delays, shutdown waits, and persistent UserAgent normalization.
const (
	opsErrorLogTimeout      = 5 * time.Second        // per-service batch write budget
	opsErrorLogDrainTimeout = 10 * time.Second       // Stop completion-wait context lifetime
	opsErrorLogBatchWindow  = 200 * time.Millisecond // batch assembly timer interval

	opsErrorLogMinWorkerCount = 4  // minimum lazy worker count
	opsErrorLogMaxWorkerCount = 32 // maximum lazy worker count

	opsErrorLogQueueSizePerWorker = 128              // channel slots per selected worker
	opsErrorLogMinQueueSize       = 256              // channel capacity lower clamp
	opsErrorLogMaxQueueSize       = 8192             // channel capacity upper clamp
	opsErrorLogBatchSize          = 32               // jobs per assembled worker batch
	opsErrorLogMaxQueueBytes      = 32 * 1024 * 1024 // estimated waiting payload bytes limit
	opsErrorLogMaxUserAgentBytes  = 512              // normalized UserAgent byte limit
)

// ErrorLogQueueOptions supplies worker sizing and optional diagnostic callbacks
// for one queue.
type ErrorLogQueueOptions struct {
	// Processors is called during first worker setup while the queue write lock is
	// held. Nil selects one; the callback cannot reenter locking queue accessors.
	Processors func() int // lazy startup sizing callback
	// Logf receives drop and recovered-panic diagnostics from request callers or
	// workers. Nil suppresses output; concurrent callers can invoke it without
	// queue-level serialization.
	Logf func(string, ...any) // optional queue diagnostic callback
	// Stack supplies bytes when batch processing recovers a panic. Nil selects no
	// stack bytes, and callback failures receive no additional recovery here.
	Stack func() []byte // optional panic stack callback
}

// ErrorLogQueue owns lazy error workers, waiting-budget accounting, and shared
// shutdown completion.
type ErrorLogQueue struct {
	options ErrorLogQueueOptions // retained sizing and diagnostic callbacks
	// opsErrorLogOnce triggers setup during the first admitted enqueue. Closing
	// the queue before first use leaves workers unstarted.
	opsErrorLogOnce sync.Once // single worker setup barrier
	// opsErrorLogQueue is published and removed under opsErrorLogMu. Workers
	// capture the created channel, so clearing this shared reference does not
	// prevent draining its remaining jobs.
	opsErrorLogQueue chan opsErrorLogJob // shared waiting job channel
	// opsErrorLogStopOnce performs shutdown setup once. Later callers share its
	// completion signal and can use another wait context after a timeout.
	opsErrorLogStopOnce sync.Once // single shutdown setup barrier
	// opsErrorLogStopDone is created during shutdown and closed after
	// opsErrorLogWorkersWg completes. Shutdown callers wait on that shared
	// publication after the setup barrier returns.
	opsErrorLogStopDone  chan struct{}  // shared worker completion signal
	opsErrorLogWorkersWg sync.WaitGroup // admitted worker completion join
	// opsErrorLogMu protects opsErrorLogQueue and opsErrorLogStopping. Enqueue
	// holds its read lock through reservation and send, while shutdown closes the
	// channel with exclusive access.
	opsErrorLogMu       sync.RWMutex // queue lifecycle access mutex
	opsErrorLogStopping bool         // permanent queue admission closure
	// These atomic observations are updated independently, so Health need not
	// describe one instant. Reservation counters exclude jobs after worker
	// receipt, while cumulative counters can include attempts that never reach
	// storage. opsErrorLogQueueLen counts reserved waiting jobs.
	// opsErrorLogQueueBytes counts estimated waiting bytes. opsErrorLogEnqueued
	// counts successful channel sends. opsErrorLogDropped counts rejected payload
	// or capacity attempts. opsErrorLogProcessed counts entries attempted by batch
	// processing. opsErrorLogSanitized counts ErrorBody changes reported before
	// admission. opsErrorLogLastDropLogAt stores Unix seconds for diagnostic
	// throttling.
	opsErrorLogQueueLen, opsErrorLogQueueBytes, opsErrorLogEnqueued, opsErrorLogDropped, opsErrorLogProcessed, opsErrorLogSanitized, opsErrorLogLastDropLogAt atomic.Int64
	// opsErrorLogShutdownCh and opsErrorLogShutdownOnce reject later enqueue
	// attempts before lazy setup. The final locked stopping check covers an
	// enqueue that passed this early signal.
	opsErrorLogShutdownCh   chan struct{} // early enqueue shutdown signal
	opsErrorLogShutdownOnce sync.Once     // single shutdown signal close
	// opsErrorLogDrained becomes true after every worker has exited and queue
	// accounting is reset. It does not assert that persistence succeeded.
	opsErrorLogDrained atomic.Bool // published worker join completion
}

// ErrorLogQueueHealth combines independently sampled waiting capacity and
// cumulative queue counters.
type ErrorLogQueueHealth struct {
	// Length and Bytes sample outstanding reservations, while BytesCapacity is the
	// fixed byte budget. Worker-local batches have already released these
	// reservations and are excluded. Length is reserved waiting jobs. Bytes is
	// estimated waiting bytes. BytesCapacity is estimated waiting-byte limit.
	Length, Bytes, BytesCapacity int64
	Capacity                     int // current shared channel slot count
	// Dropped, Enqueued, Processed, and Sanitized are independent cumulative
	// counters; processed attempts can fail storage, and sanitization occurs
	// before checking whether enqueue admission remains open. Dropped is rejected
	// attempt count. Enqueued is successful send count. Processed is
	// batch-attempted entry count. Sanitized is changed ErrorBody count.
	Dropped, Enqueued, Processed, Sanitized int64
}

// opsErrorLogJob couples a mutable observation with its service and
// waiting-byte reservation.
type opsErrorLogJob struct {
	ops *OpsService // error persistence service dependency
	// entry is sanitized before enqueue and retained directly for asynchronous
	// preparation and writing. The submitting caller stops mutating it after
	// submission.
	entry       *OpsInsertErrorLogInput // shared queued error observation
	queuedBytes int64                   // estimated reserved payload bytes
}

// NewErrorLogQueue retains callbacks and creates the early shutdown signal
// without starting workers. Use the constructed queue for Enqueue and Shutdown;
// the zero value lacks the channel needed by shutdown setup.
func NewErrorLogQueue(options ErrorLogQueueOptions) *ErrorLogQueue {
	return &ErrorLogQueue{options: options, opsErrorLogShutdownCh: make(chan struct{})}
}

// report invokes the configured diagnostic callback with the supplied
// arguments. A nil callback omits output, and callback blocking or panic
// behavior belongs to its implementation.
func (q *ErrorLogQueue) report(format string, args ...any) {
	if q.options.Logf != nil {
		q.options.Logf(format, args...)
	}
}

// stack obtains diagnostic stack bytes from the optional callback. A missing
// callback returns nil, and the returned bytes are passed directly to the
// reporting callback.
func (q *ErrorLogQueue) stack() []byte {
	if q.options.Stack != nil {
		return q.options.Stack()
	}
	return nil
}

// processors reads the sizing callback when available and otherwise returns
// one. Worker configuration later clamps this value; the helper itself performs
// no normalization or caching.
func (q *ErrorLogQueue) processors() int {
	if q.options.Processors != nil {
		return q.options.Processors()
	}
	return 1
}

// Health returns separate samples of channel capacity, waiting reservations,
// and cumulative counters. Workers release reservations before persistence, and
// a shutdown-cleared channel reports zero capacity while captured workers may
// still be finishing.
func (q *ErrorLogQueue) Health() ErrorLogQueueHealth {
	return ErrorLogQueueHealth{q.OpsErrorLogQueueLength(), q.OpsErrorLogQueueBytes(), q.OpsErrorLogQueueBytesCapacity(), q.OpsErrorLogQueueCapacity(), q.OpsErrorLogDroppedTotal(), q.OpsErrorLogEnqueuedTotal(), q.OpsErrorLogProcessedTotal(), q.OpsErrorLogSanitizedTotal()}
}

// PrepareErrorLogInput mutates a nonnil observation to normalize UserAgent,
// process ErrorBody, and serialize a present upstream event slice before
// admission. Preexisting UpstreamErrorsJSON is left as supplied when that slice
// is empty. Its boolean reports ErrorBody change or truncation alone; UserAgent
// and upstream-event changes do not contribute. It returns the upstream
// sanitizer's error.
func PrepareErrorLogInput(entry *OpsInsertErrorLogInput) (bool, error) {
	// Normalize UserAgent and process ErrorBody before queue admission. These
	// changes and event serialization can occur even when a later shutdown guard
	// omits the job.
	entry.UserAgent = normalizeOpsPersistentUserAgent(entry.UserAgent)
	sanitized := false
	if entry.ErrorBody != "" {
		before := entry.ErrorBody
		body, truncated := SanitizeOpsErrorBodyForQueue(before)
		entry.ErrorBody = body
		sanitized = truncated || body != before
	}
	return sanitized, SanitizeOpsUpstreamErrorsForQueue(entry)
}

// opsErrorLogConfig selects twice the supplied processor count clamped to four
// through thirty-two workers. Channel capacity uses one hundred twenty-eight
// slots per selected worker with its own clamps; the current worker bounds
// yield five hundred twelve through four thousand ninety-six slots.
func opsErrorLogConfig(processors int) (workerCount int, queueSize int) {
	// Scale workers from the processor callback, then bound concurrency before
	// deriving waiting capacity from the selected worker count.
	workerCount = min(max(processors*2, opsErrorLogMinWorkerCount), opsErrorLogMaxWorkerCount)

	queueSize = min(max(workerCount*opsErrorLogQueueSizePerWorker, opsErrorLogMinQueueSize), opsErrorLogMaxQueueSize)

	return workerCount, queueSize
}

// estimateOpsErrorLogJobBytes estimates waiting payload weight from selected
// string lengths and fixed overhead. A nil entry weighs one byte; this estimate
// excludes allocations and worker-local batches and does not measure total heap
// usage.
func estimateOpsErrorLogJobBytes(entry *OpsInsertErrorLogInput) int64 {
	if entry == nil {
		return 1
	}
	// Approximate fixed record weight alongside counted variable string bytes.
	const fixedOverhead = 512
	size := fixedOverhead + len(entry.RequestID) + len(entry.ClientRequestID) +
		len(entry.Platform) + len(entry.Model) + len(entry.RequestPath) +
		len(entry.InboundEndpoint) + len(entry.UpstreamEndpoint) +
		len(entry.RequestedModel) + len(entry.UpstreamModel) + len(entry.UserAgent) +
		len(entry.ErrorPhase) + len(entry.ErrorType) + len(entry.Severity) +
		len(entry.ErrorMessage) + len(entry.ErrorBody) + len(entry.ErrorSource) +
		len(entry.ErrorOwner) + len(entry.APIKeyPrefix)
	if entry.UpstreamErrorMessage != nil {
		size += len(*entry.UpstreamErrorMessage)
	}
	if entry.UpstreamErrorDetail != nil {
		size += len(*entry.UpstreamErrorDetail)
	}
	if entry.UpstreamErrorsJSON != nil {
		size += len(*entry.UpstreamErrorsJSON)
	}
	return int64(size)
}

// reserveOpsErrorLogQueueBytes claims waiting bytes and increments reserved job
// count when the byte budget permits. Nonpositive sizes are normalized to one;
// callers undo both reservations when the channel send fails.
//
// Compare-and-swap retries when another reservation or worker receipt changes
// the byte total. The byte and length counters are separate atomic updates, so
// health can see transitional combinations. Jobs release their reservation when
// taken into a worker batch, allowing more waiting jobs while earlier entries
// are still being written.
func (q *ErrorLogQueue) reserveOpsErrorLogQueueBytes(size int64) bool {
	if size < 1 {
		size = 1
	}
	// Reserve bytes through compare-and-swap before publishing a job-count
	// increment; other reservations and worker receipts can change the compared
	// value.
	for {
		current := q.opsErrorLogQueueBytes.Load()
		if current > opsErrorLogMaxQueueBytes-size {
			return false
		}
		if q.opsErrorLogQueueBytes.CompareAndSwap(current, current+size) {
			q.opsErrorLogQueueLen.Add(1)
			return true
		}
	}
}

// maybeLogOpsErrorLogDrop selects one drop-diagnostic attempt per wall-clock
// minute using an atomic Unix-second timestamp. The first selected attempt runs
// immediately, and overlapping callers retry the timestamp comparison until one
// wins.
//
// The timestamp advances even when Logf is absent, and a backward wall-clock
// movement can extend suppression. Queue capacity and counters are sampled
// afterward, so the diagnostic fields can reflect different moments. The
// caller's rejected attempt has already affected dropped accounting before this
// optional report is selected.
func (q *ErrorLogQueue) maybeLogOpsErrorLogDrop() {
	now := time.Now().Unix()

	// Claim the diagnostic timestamp atomically so concurrent rejected submissions
	// select one report attempt within the wall-clock suppression interval.
	for {
		last := q.opsErrorLogLastDropLogAt.Load()
		if last != 0 && now-last < 60 {
			return
		}
		if q.opsErrorLogLastDropLogAt.CompareAndSwap(last, now) {
			break
		}
	}

	queued := q.opsErrorLogQueueLen.Load()
	queuedBytes := q.opsErrorLogQueueBytes.Load()
	// This accessor acquires a read lock even when Enqueue holds one; a pending
	// Shutdown writer blocks that recursive acquisition.
	queueCap := q.OpsErrorLogQueueCapacity()

	q.report(
		"[OpsErrorLogger] queue is full; dropping logs (queued=%d cap=%d queued_bytes=%d bytes_cap=%d enqueued_total=%d dropped_total=%d processed_total=%d sanitized_total=%d)",
		queued,
		queueCap,
		queuedBytes,
		opsErrorLogMaxQueueBytes,
		q.opsErrorLogEnqueued.Load(),
		q.opsErrorLogDropped.Load(),
		q.opsErrorLogProcessed.Load(),
		q.opsErrorLogSanitized.Load(),
	)
}

// OpsErrorLogSanitizedTotal returns the cumulative number of ErrorBody
// sanitization changes.
func (q *ErrorLogQueue) OpsErrorLogSanitizedTotal() int64 {
	return q.opsErrorLogSanitized.Load()
}

// OpsErrorLogProcessedTotal returns entries counted after batch processing
// calls finish.
func (q *ErrorLogQueue) OpsErrorLogProcessedTotal() int64 {
	return q.opsErrorLogProcessed.Load()
}

// OpsErrorLogEnqueuedTotal returns the cumulative number of successful channel
// sends.
func (q *ErrorLogQueue) OpsErrorLogEnqueuedTotal() int64 {
	return q.opsErrorLogEnqueued.Load()
}

// OpsErrorLogDroppedTotal returns attempts rejected during payload preparation
// or capacity admission.
func (q *ErrorLogQueue) OpsErrorLogDroppedTotal() int64 {
	return q.opsErrorLogDropped.Load()
}

// OpsErrorLogQueueCapacity returns the shared channel's current slot capacity
// under its lifecycle lock. An unstarted or shutdown-cleared channel reports
// zero, even when workers still hold a captured channel. A nested call from a
// locked enqueue can wait behind Shutdown's pending writer.
func (q *ErrorLogQueue) OpsErrorLogQueueCapacity() int {
	q.opsErrorLogMu.RLock()
	ch := q.opsErrorLogQueue
	q.opsErrorLogMu.RUnlock()
	if ch == nil {
		return 0
	}
	return cap(ch)
}

// OpsErrorLogQueueBytesCapacity returns the fixed estimated waiting-payload
// byte budget.
func (q *ErrorLogQueue) OpsErrorLogQueueBytesCapacity() int64 {
	return opsErrorLogMaxQueueBytes
}

// OpsErrorLogQueueBytes returns the independently sampled estimated
// waiting-payload byte total.
func (q *ErrorLogQueue) OpsErrorLogQueueBytes() int64 {
	return q.opsErrorLogQueueBytes.Load()
}

// OpsErrorLogQueueLength returns the independently sampled count of waiting job
// reservations.
func (q *ErrorLogQueue) OpsErrorLogQueueLength() int64 {
	return q.opsErrorLogQueueLen.Load()
}

// Shutdown closes enqueue admission once and waits for the captured workers to
// finish using a required nonnil context. Each subsequent caller shares
// completion but can choose another wait budget.
//
// The shutdown signal closes before exclusive queue access removes and closes
// the channel, allowing workers to consume remaining buffered jobs. Setup
// happens inside sync.Once before the context select, so blocking setup is not
// bounded by that wait context. A background join resets waiting counters and
// publishes Drained after worker exit. Completion describes processing
// attempts; batch write failures have no retry or successful-storage guarantee.
func (q *ErrorLogQueue) Shutdown(ctx context.Context) error {
	// Publish early shutdown, exclusively close the shared channel, then join
	// workers asynchronously; their captured channel retains buffered jobs until
	// processing completes.
	q.opsErrorLogStopOnce.Do(func() {
		q.opsErrorLogShutdownOnce.Do(func() { close(q.opsErrorLogShutdownCh) })
		q.opsErrorLogMu.Lock()
		q.opsErrorLogStopping = true
		queue := q.opsErrorLogQueue
		q.opsErrorLogQueue = nil
		q.opsErrorLogStopDone = make(chan struct{})
		if queue != nil {
			close(queue)
		}
		q.opsErrorLogMu.Unlock()
		// After all workers exit, reset outstanding reservations and publish
		// completion in that order so another Shutdown waiter can return.
		go func() {
			q.opsErrorLogWorkersWg.Wait()
			q.opsErrorLogQueueLen.Store(0)
			q.opsErrorLogQueueBytes.Store(0)
			q.opsErrorLogDrained.Store(true)
			close(q.opsErrorLogStopDone)
		}()
	})
	select {
	case <-q.opsErrorLogStopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop reports whether Shutdown returns success using a ten-second
// background-derived context. A false result reports its wait-context error;
// the background join can still finish or already be ready when the
// cancellation case wins. Callers can await the shared completion through
// Shutdown again.
func (q *ErrorLogQueue) Stop() bool {
	ctx, cancel := context.WithTimeout(context.Background(), opsErrorLogDrainTimeout)
	defer cancel()
	return q.Shutdown(ctx) == nil
}

// normalizeOpsPersistentUserAgent removes invalid UTF-8 bytes, trims
// surrounding whitespace, and bounds the result to five hundred twelve bytes.
// Truncation keeps a complete UTF-8 prefix so persistent UserAgent strings
// remain valid.
func normalizeOpsPersistentUserAgent(value string) string {
	// Remove invalid bytes before trimming and UTF-8 prefix truncation.
	return truncateString(strings.TrimSpace(strings.ToValidUTF8(value, "")), opsErrorLogMaxUserAgentBytes)
}

// Enqueue prepares and attempts a nonblocking submission of a service
// observation to the lazy queue. Nil service or entry is ignored; preparation
// mutates the entry before shutdown checks, and callers relinquish further
// mutation after submission because workers retain that pointer.
//
// An admitted enqueue initializes workers once, then holds the lifecycle read
// lock through byte reservation and channel send. Byte refusal or channel
// fullness increments dropped, and a failed send compensates its reservation.
// Closed admission simply returns after preparation.
//
// A selected drop diagnostic acquires another read lock to sample capacity
// while this outer lock is held. If Shutdown's writer is already waiting, that
// inner read can block while the writer waits for the outer reader. Diagnostic
// callbacks can also block, so the channel's nonblocking send does not bound
// the whole call.
// @project-doc docs/operations/observability_and_data_lifecycle.md#background_runtimes
func (q *ErrorLogQueue) Enqueue(ops *OpsService, entry *OpsInsertErrorLogInput) {
	if ops == nil || entry == nil {
		return
	}
	sanitized, err := PrepareErrorLogInput(entry)
	if sanitized {
		q.opsErrorLogSanitized.Add(1)
	}
	// Treat a preparation error as a dropped observation before worker admission,
	// using the same throttled diagnostic path as capacity refusal.
	if err != nil {
		q.opsErrorLogDropped.Add(1)
		q.maybeLogOpsErrorLogDrop()
		return
	}

	// Check the early shutdown signal before lazy initialization, then recheck
	// locked admission because shutdown can begin after this first check.
	select {
	case <-q.opsErrorLogShutdownCh:
		return
	default:
	}

	q.opsErrorLogMu.RLock()
	stopping := q.opsErrorLogStopping
	q.opsErrorLogMu.RUnlock()
	if stopping {
		return
	}

	// The once barrier serializes first-use setup; setup takes the write lock
	// before Enqueue acquires the read lock for sending.
	q.opsErrorLogOnce.Do(q.startOpsErrorLogWorkers)

	q.opsErrorLogMu.RLock()
	defer q.opsErrorLogMu.RUnlock()
	if q.opsErrorLogStopping || q.opsErrorLogQueue == nil {
		return
	}
	// Hold the read lock while reserving and attempting the send so shutdown
	// cannot close the channel during admission or compensation.
	queuedBytes := estimateOpsErrorLogJobBytes(entry)
	if !q.reserveOpsErrorLogQueueBytes(queuedBytes) {
		q.opsErrorLogDropped.Add(1)
		q.maybeLogOpsErrorLogDrop()
		return
	}

	select {
	case q.opsErrorLogQueue <- opsErrorLogJob{ops: ops, entry: entry, queuedBytes: queuedBytes}:
		q.opsErrorLogEnqueued.Add(1)
	default:
		q.opsErrorLogQueueLen.Add(-1)
		q.opsErrorLogQueueBytes.Add(-queuedBytes)
		// Channel fullness drops the job after undoing its byte and length
		// reservations, keeping the pending budget available to later submissions.
		q.opsErrorLogDropped.Add(1)
		q.maybeLogOpsErrorLogDrop()
	}
}

// flushOpsErrorLogBatch groups nonnil jobs by their OpsService and attempts
// each group's error preparation and persistence. Empty input and groups with
// no valid jobs are omitted.
//
// Every service group receives its own background-derived timeout, and map
// iteration leaves group order unspecified. RecordErrorBatch errors are
// suppressed and are not retried. Processed increases for valid jobs after all
// calls return, including services that skipped or failed persistence. Recovery
// reports a batch panic and stops that invocation before the final increment
// when reached; earlier groups can already have performed writes.
func (q *ErrorLogQueue) flushOpsErrorLogBatch(batch []opsErrorLogJob) {
	if len(batch) == 0 {
		return
	}
	// Recover a panic for this batch invocation and report it; earlier service
	// groups can already have written before recovery returns.
	defer func() {
		if r := recover(); r != nil {
			q.report("[OpsErrorLogger] worker panic: %v\n%s", r, q.stack())
		}
	}()

	grouped := make(map[*OpsService][]*OpsInsertErrorLogInput, len(batch))
	var processed int64
	for _, job := range batch {
		if job.ops == nil || job.entry == nil {
			continue
		}
		grouped[job.ops] = append(grouped[job.ops], job.entry)
		processed++
	}
	if processed == 0 {
		return
	}

	for opsSvc, entries := range grouped {
		if opsSvc == nil || len(entries) == 0 {
			continue
		}
		// Keep each service group's write budget independent of queue shutdown
		// waiting. Errors are suppressed here, so attempted entries are counted
		// despite persistence failure.
		ctx, cancel := context.WithTimeout(context.Background(), opsErrorLogTimeout)
		_ = opsSvc.RecordErrorBatch(ctx, entries)
		cancel()
	}
	q.opsErrorLogProcessed.Add(processed)
}

// startOpsErrorLogWorkers initializes one waiting channel and registers workers
// while holding the lifecycle write lock. Enqueue calls it through its once
// barrier; a stopping queue skips setup.
//
// Each worker captures the created channel, releases reservations on receipt,
// and assembles up to thirty-two jobs until its two-hundred-millisecond timer
// case is selected or the channel closes. Timer readiness can race with another
// queued job. Channel closure flushes that worker's partial batch before exit.
// Workers acknowledge completion through Shutdown's WaitGroup, while database
// attempts use contexts independent of shutdown waiting.
func (q *ErrorLogQueue) startOpsErrorLogWorkers() {
	// Register workers and publish their channel while holding the write lock,
	// preventing shutdown from closing an incompletely initialized queue.
	q.opsErrorLogMu.Lock()
	defer q.opsErrorLogMu.Unlock()

	if q.opsErrorLogStopping {
		return
	}

	workerCount, queueSize := opsErrorLogConfig(q.processors())
	q.opsErrorLogQueue = make(chan opsErrorLogJob, queueSize)
	q.opsErrorLogQueueLen.Store(0)
	q.opsErrorLogQueueBytes.Store(0)

	// Capture the channel before launching registered workers; shutdown later
	// clears the shared reference while these workers consume the captured buffer.
	queue := q.opsErrorLogQueue
	q.opsErrorLogWorkersWg.Add(workerCount)
	for range workerCount {
		go func() {
			defer q.opsErrorLogWorkersWg.Done()
			for {
				job, ok := <-queue
				if !ok {
					return
				}
				// Release waiting reservations when taking the first job, then assemble a
				// local batch whose payload bytes are outside the waiting budget.
				q.opsErrorLogQueueLen.Add(-1)
				q.opsErrorLogQueueBytes.Add(-job.queuedBytes)
				batch := make([]opsErrorLogJob, 0, opsErrorLogBatchSize)
				batch = append(batch, job)

				// Bound partial-batch assembly with one timer; channel closure flushes
				// collected jobs before exit, and full batches stop the timer before
				// processing.
				timer := time.NewTimer(opsErrorLogBatchWindow)
			batchLoop:
				for len(batch) < opsErrorLogBatchSize {
					select {
					case nextJob, ok := <-queue:
						if !ok {
							// Stop and nonblockingly consume any available timer notification before
							// flushing the partial batch after channel closure.
							if !timer.Stop() {
								select {
								case <-timer.C:
								default:
								}
							}
							q.flushOpsErrorLogBatch(batch)
							return
						}
						q.opsErrorLogQueueLen.Add(-1)
						q.opsErrorLogQueueBytes.Add(-nextJob.queuedBytes)
						batch = append(batch, nextJob)
					case <-timer.C:
						break batchLoop
					}
				}
				// Stop or discard the assembly timer's available notification before this
				// worker writes its completed batch and starts another timer.
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				q.flushOpsErrorLogBatch(batch)
			}
		}()
	}
}
