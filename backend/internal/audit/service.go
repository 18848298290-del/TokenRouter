package audit

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Management requests enqueue audit records while a separate writer persists
// batches. Queue saturation drops observations rather than delaying the request,
// and failed batches are counted and discarded. Accepted records share their
// pointers with the writer, so producers finish populating each record before
// handing it to Record. Startup remains separate from construction so app can
// finish wiring dependencies before either background worker runs.
//
// Shutdown marks the service stopped under the same lock used by enqueueing,
// then cancels the writer and retention worker. The writer drains accepted
// records with independent database timeouts when it has been started. A caller's
// shutdown deadline limits waiting for that drain rather than cancelling a flush.
// Retention performs idempotent bounded deletes, allowing concurrent instances to
// remove expired records without coordinating a leader.

const (
	// Writer limits bound queued observations, size database batches, and flush
	// partially filled batches even when incoming audit traffic becomes quiet.
	auditLogQueueCapacity = 4096
	auditLogBatchSize     = 100
	auditLogFlushInterval = time.Second

	// Retention timing schedules the first cleanup after a startup delay and later
	// runs once per day; the batch size bounds each deletion within a cleanup run.
	auditRetentionCheckInterval = 24 * time.Hour
	auditRetentionStartupDelay  = 5 * time.Minute
	auditRetentionBatchSize     = 5000
)

// AuditLogService records and queries management-operation audit logs.
// Record enqueues asynchronously and drops entries when the bounded queue is full.
// Callers authorize ClearAll with TOTP before invoking it.
type AuditLogService struct {
	repo      AuditLogRepository // audit persistence and query store
	retention RetentionReader    // current retention policy reader

	// queue retains accepted pointers until the writer consumes them. Producers
	// leave the record and referenced values unchanged after calling Record.
	queue chan *AuditLog
	// lifecycleMu serializes started and stopped changes with enqueue admission.
	// stopOnce begins one shutdown, and stopDone closes after all workers finish.
	lifecycleMu sync.RWMutex // startup and enqueue admission lock
	// started records worker startup; stopped permanently rejects new queue admission
	// after StopContext begins shutdown. Both flags are protected by lifecycleMu.
	started, stopped bool          // worker launch and terminal admission flags
	stopOnce         sync.Once     // one shutdown initiation
	stopDone         chan struct{} // worker completion notification

	// ctx and cancel stop both workers; wg covers their entire lifetime, including
	// the writer's final drain. Flushes create independent bounded database contexts.
	ctx    context.Context    // shared worker lifetime context
	cancel context.CancelFunc // shared worker cancellation function
	wg     sync.WaitGroup     // active background worker count

	// droppedCount, writeFailed, and writtenCount are updated atomically by enqueue
	// and flush paths. Queue drops and failed writes count different lost-record paths.
	droppedCount uint64 // observations rejected at capacity
	writeFailed  uint64 // records in failed batches
	writtenCount uint64 // successfully inserted audit records
}

// RetentionReader returns retention days for each cleanup run; zero means indefinite retention.
type RetentionReader func(context.Context) int

// NewAuditLogService creates an inactive audit recorder with its own worker
// context and bounded queue. Call Start after binding dependencies; a nil
// retention reader leaves scheduled deletion disabled, and Stop releases the context.
func NewAuditLogService(repo AuditLogRepository, retention RetentionReader) *AuditLogService {
	ctx, cancel := context.WithCancel(context.Background())
	return &AuditLogService{
		repo:      repo,
		retention: retention,
		queue:     make(chan *AuditLog, auditLogQueueCapacity),
		stopDone:  make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start launches the batch writer and retention worker once for a configured
// repository. Nil services or missing repositories leave the service inactive.
//
// The lifecycle lock serializes launches against shutdown and repeated starts.
// Worker accounting is registered before either goroutine begins, so StopContext
// can wait for both workers after cancelling their shared context. A stopped
// service remains stopped; callers construct another service for a new lifetime.
func (s *AuditLogService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.stopped {
		return
	}
	// Publish started and register both workers before launching them under the
	// lifecycle lock, so shutdown cannot wait on an empty group during startup.
	s.started = true
	s.wg.Add(2)
	go s.runWriter()
	go s.runRetentionLoop()
}

// Stop begins shutdown and waits for both workers using an unlimited waiting
// context. Callers with an application shutdown budget use StopContext instead;
// the started writer drains accepted records through its bounded batch operations.
func (s *AuditLogService) Stop() { _ = s.StopContext(context.Background()) }

// StopContext stops enqueue admission, cancels the workers, and waits for their
// completion or the caller's context. A nil service completes immediately.
//
// One caller initiates shutdown under the lifecycle lock; later calls wait on the
// same completion channel. Synchronous shutdown setup and lifecycle-lock
// acquisition occur before the context wait and do not observe its deadline.
// Accepted records remain available to a started writer's
// final drain. If ctx expires, its error describes an incomplete wait, while the
// drain and completion notification continue independently for subsequent callers.
func (s *AuditLogService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		// Stop enqueue admission before cancelling the writer. Holding this lock
		// prevents an accepted record from arriving after the drain observes an empty queue.
		s.lifecycleMu.Lock()
		s.stopped = true
		s.cancel()
		s.lifecycleMu.Unlock()
		// Completion has its own goroutine so a waiting caller's deadline can expire
		// while the writer finishes database flushes under their independent timeout contexts.
		go func() { s.wg.Wait(); close(s.stopDone) }()
	})
	select {
	case <-s.stopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Record attempts to enqueue an audit record without waiting for queue capacity.
// It fills a missing CreatedAt and shares the supplied pointer with the writer;
// callers finish mutating the record and its referenced metadata before this call.
//
// Admission holds the lifecycle read lock so shutdown cannot finish its stop
// transition while an enqueue is in progress. A full queue increments droppedCount,
// while stopped services and nil entries are ignored. Start is separate from
// recording, so records accepted before startup remain queued until the writer runs.
func (s *AuditLogService) Record(entry *AuditLog) {
	if s == nil || entry == nil {
		return
	}
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	if s.stopped {
		return
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	select {
	case <-s.ctx.Done():
		return
	default:
	}
	select {
	case s.queue <- entry:
	default:
		// An observation that cannot fit is dropped immediately, allowing the management
		// response to proceed while the counter records lost audit coverage under load.
		atomic.AddUint64(&s.droppedCount, 1)
	}
}

// List returns the repository's filtered page of management audit records.
// A nil filter uses repository pagination defaults, and bodies are omitted from
// list results so callers fetch a record's details separately.
func (s *AuditLogService) List(ctx context.Context, filter *AuditLogFilter) (*AuditLogList, error) {
	return s.repo.List(ctx, filter)
}

// GetByID returns one stored audit record including its stored request body.
// The repository returns ErrAuditLogNotFound for a missing identifier and propagates
// query failures for the caller's HTTP error handling.
func (s *AuditLogService) GetByID(ctx context.Context, id int64) (*AuditLog, error) {
	return s.repo.GetByID(ctx, id)
}

// ClearAll asks the repository to clear audit history and persist the supplied
// trace in the same transaction. The caller verifies fresh TOTP authorization
// first and supplies a nonnil trace describing the authenticated operation.
// ClearAll assigns the clearing action and fills a missing timestamp before
// delegating to the synchronous repository path. An error leaves success unknown
// to the caller; the returned count is usable when the transaction succeeds.
//
// @project-doc docs/operations/observability_and_data_lifecycle.md#data_cleanup
func (s *AuditLogService) ClearAll(ctx context.Context, trace *AuditLog) (int64, error) {
	if trace == nil {
		return 0, fmt.Errorf("audit clear trace is required")
	}
	trace.Action = AuditActionAuditLogClear
	if trace.CreatedAt.IsZero() {
		trace.CreatedAt = time.Now().UTC()
	}
	return s.repo.ClearWithTrace(ctx, trace)
}

// runWriter persists accepted records in size- or time-triggered batches until
// its worker context is cancelled. It accounts for each completed batch and
// discards failed batches after reporting them to stderr.
//
// Cancellation switches the writer to draining the queue after enqueue admission
// has stopped. Each flush uses a fresh five-second context so worker cancellation
// does not prevent the final database attempt. The wait group covers this drain,
// letting StopContext distinguish worker completion from its caller's expired wait.
func (s *AuditLogService) runWriter() {
	defer s.wg.Done()

	ticker := time.NewTicker(auditLogFlushInterval)
	defer ticker.Stop()

	batch := make([]*AuditLog, 0, auditLogBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		// Flushes outlive worker cancellation so shutdown can attempt accepted records;
		// the independent timeout still bounds a stalled database call for each batch.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		inserted, err := s.repo.BatchInsert(ctx, batch)
		cancel()
		if err != nil {
			// Failed batches are discarded after accounting for their records. Reporting
			// directly to stderr avoids feeding the failed persistence path back into itself.
			atomic.AddUint64(&s.writeFailed, uint64(len(batch)))
			_, _ = fmt.Fprintf(os.Stderr, "time=%s level=WARN msg=\"audit log flush failed\" err=%v batch=%d\n",
				time.Now().Format(time.RFC3339Nano), err, len(batch))
		} else {
			atomic.AddUint64(&s.writtenCount, uint64(inserted))
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-s.ctx.Done():
			// Enqueue admission has stopped before cancellation. Drain the accepted queue
			// and final partial batch before signalling completion to the shutdown waiters.
			for {
				select {
				case item := <-s.queue:
					if item == nil {
						continue
					}
					batch = append(batch, item)
					if len(batch) >= auditLogBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case item := <-s.queue:
			if item == nil {
				continue
			}
			batch = append(batch, item)
			if len(batch) >= auditLogBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// runRetentionLoop runs cleanup after the startup delay and once each daily
// interval. Worker cancellation interrupts waiting and the active cleanup context;
// idempotent repository deletes allow instances to run cleanup without leader election.
func (s *AuditLogService) runRetentionLoop() {
	defer s.wg.Done()

	startupTimer := time.NewTimer(auditRetentionStartupDelay)
	defer startupTimer.Stop()
	// Observe shutdown while the startup timer is pending, allowing this worker to
	// exit before its first cleanup even when application startup is still settling.
	select {
	case <-s.ctx.Done():
		return
	case <-startupTimer.C:
	}

	ticker := time.NewTicker(auditRetentionCheckInterval)
	defer ticker.Stop()

	s.runRetentionOnce()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.runRetentionOnce()
		}
	}
}

// runRetentionOnce removes records older than the current retention period in
// bounded batches. A nil reader or nonpositive day count leaves records intact;
// the ten-minute worker-derived context bounds the full run and follows shutdown.
func (s *AuditLogService) runRetentionOnce() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Minute)
	defer cancel()

	days := 0
	if s.retention != nil {
		days = s.retention(ctx)
	}
	if days <= 0 {
		return // Nonpositive values retain records indefinitely until manually cleared.
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	for {
		// Repeat bounded deletes against one cutoff until no expired records remain;
		// an error stops this run so the daily loop can attempt remaining records later.
		deleted, err := s.repo.DeleteBefore(ctx, cutoff, auditRetentionBatchSize)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "time=%s level=WARN msg=\"audit log retention cleanup failed\" err=%v\n",
				time.Now().Format(time.RFC3339Nano), err)
			return
		}
		if deleted == 0 {
			return
		}
	}
}
