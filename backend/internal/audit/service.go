package audit

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	auditLogQueueCapacity = 4096
	auditLogBatchSize     = 100
	auditLogFlushInterval = time.Second

	auditRetentionCheckInterval = 24 * time.Hour
	auditRetentionStartupDelay  = 5 * time.Minute
	auditRetentionBatchSize     = 5000
)

// AuditLogService records and queries management-operation audit logs.
// Record enqueues asynchronously and drops entries when the bounded queue is full.
// Callers authorize ClearAll with TOTP before invoking it.
type AuditLogService struct {
	repo      AuditLogRepository
	retention RetentionReader

	queue            chan *AuditLog
	lifecycleMu      sync.RWMutex
	started, stopped bool
	stopOnce         sync.Once
	stopDone         chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	droppedCount uint64
	writeFailed  uint64
	writtenCount uint64
}

// RetentionReader returns retention days for each cleanup run; zero means indefinite retention.
type RetentionReader func(context.Context) int

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

// Start launches the batch writer and retention worker once.
func (s *AuditLogService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	s.wg.Add(2)
	go s.runWriter()
	go s.runRetentionLoop()
}

// Stop stops the workers and attempts to flush queued records.
func (s *AuditLogService) Stop() { _ = s.StopContext(context.Background()) }

// StopContext rejects new enqueues and waits for workers; expiration returns the context error.
func (s *AuditLogService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.stopped = true
		s.cancel()
		s.lifecycleMu.Unlock()
		go func() { s.wg.Wait(); close(s.stopDone) }()
	})
	select {
	case <-s.stopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Record enqueues without waiting for capacity and counts entries dropped when the queue is full.
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
		atomic.AddUint64(&s.droppedCount, 1)
	}
}

// List returns a page of audit logs.
func (s *AuditLogService) List(ctx context.Context, filter *AuditLogFilter) (*AuditLogList, error) {
	return s.repo.List(ctx, filter)
}

// GetByID returns one audit record.
func (s *AuditLogService) GetByID(ctx context.Context, id int64) (*AuditLog, error) {
	return s.repo.GetByID(ctx, id)
}

// ClearAll clears audit logs and persists a trace in the same transaction.
// The caller must verify TOTP first. The repository operation:
//  1. Counts and clears the records.
//  2. Synchronously inserts an "admin.audit_log.clear" trace, bypassing the asynchronous queue.
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

func (s *AuditLogService) runWriter() {
	defer s.wg.Done()

	ticker := time.NewTicker(auditLogFlushInterval)
	defer ticker.Stop()

	batch := make([]*AuditLog, 0, auditLogBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		inserted, err := s.repo.BatchInsert(ctx, batch)
		cancel()
		if err != nil {
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
			// Attempt to flush all queued entries before the writer exits.
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

// runRetentionLoop periodically deletes expired audit records.
// Deletion is idempotent, so instances can run cleanup without leader election.
func (s *AuditLogService) runRetentionLoop() {
	defer s.wg.Done()

	startupTimer := time.NewTimer(auditRetentionStartupDelay)
	defer startupTimer.Stop()
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
