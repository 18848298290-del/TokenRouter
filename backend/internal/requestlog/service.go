package requestlog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

const (
	requestBufferCapacity = 4096
	requestBatchSize      = 256
	requestFlushInterval  = 20 * time.Millisecond
	requestRetryInterval  = time.Second
	requestWriteTimeout   = 5 * time.Second
)

// ErrInvalidRecord marks snapshot content that cannot be stored by retrying the same version.
var ErrInvalidRecord = errors.New("invalid request record")

// Repository stores request summaries and restricts queries to the caller's access.
type Repository interface {
	Save(context.Context, []telemetry.RequestRecord) error
	Find(context.Context, string, int64, bool) ([]Detail, error)
	Cleanup(context.Context, time.Time) error
}

// Usage summarizes cost and tokens in request details.
type Usage struct {
	ID           int64   `json:"id"`
	Model        string  `json:"model"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	ActualCost   float64 `json:"actual_cost"`
}

// Failure references an error record whose ownership has been checked.
type Failure struct {
	ID     int64  `json:"id"`
	Status int    `json:"status_code"`
	Phase  string `json:"phase"`
}

// Detail combines a request summary with retained business records.
type Detail struct {
	telemetry.RequestRecord
	Usage    []Usage   `json:"usage"`
	Errors   []Failure `json:"errors"`
	AuditIDs []int64   `json:"audit_ids,omitempty"`
	Legacy   bool      `json:"legacy"`
	Pending  bool      `json:"pending,omitempty"`
}

// Health reports pending snapshots and cumulative write failures.
type Health struct {
	Pending  int    `json:"pending"`
	Failures uint64 `json:"failures"`
}

// Service merges request snapshots in memory and writes them in background batches.
type Service struct {
	repo      Repository
	retention time.Duration
	logf      func(string, ...any)
	failures  atomic.Uint64

	mu      sync.Mutex
	pending map[string]telemetry.RequestRecord
	stopped bool
	writes  sync.WaitGroup
	flushMu sync.Mutex

	ctx       context.Context
	cancel    context.CancelFunc
	startOnce sync.Once
	stopOnce  sync.Once
	done      chan struct{}
	stopDone  chan struct{}
	stopErr   error
}

// NewService configures request-summary batching and retention; nonpositive retention defaults to 30 days.
// @project-doc docs/operations/request_lookup.md#request_storage
func NewService(repo Repository, retentionDays int, logf func(string, ...any)) *Service {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		repo: repo, retention: time.Duration(retentionDays) * 24 * time.Hour, logf: logf,
		pending: make(map[string]telemetry.RequestRecord), ctx: ctx, cancel: cancel,
		done: make(chan struct{}), stopDone: make(chan struct{}),
	}
}

// Observe merges pending snapshots for a request and writes synchronously when full or stopped.
func (s *Service) Observe(record telemetry.RequestRecord) {
	if s == nil || s.repo == nil {
		return
	}
	if record.RequestID == "" {
		s.report("write request record", fmt.Errorf("missing request ID"))
		return
	}
	record = telemetry.NormalizeRequestRecord(record)
	s.mu.Lock()
	if !s.stopped {
		if previous, ok := s.pending[record.RequestID]; ok {
			s.pending[record.RequestID] = telemetry.MergeRequestRecord(previous, record)
			s.mu.Unlock()
			return
		}
		if len(s.pending) < requestBufferCapacity {
			s.pending[record.RequestID] = record
			s.mu.Unlock()
			return
		}
		// Stop waits for synchronous writes already started here; calls arriving after shutdown finish their own writes.
		s.writes.Add(1)
		defer s.writes.Done()
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), requestWriteTimeout)
	defer cancel()
	if err := s.repo.Save(ctx, []telemetry.RequestRecord{record}); err != nil {
		s.report("write request "+record.RequestID, err)
	}
}

func (s *Service) Start(context.Context) error {
	s.startOnce.Do(func() { go s.run() })
	return nil
}

func (s *Service) run() {
	defer close(s.done)
	ticker := time.NewTicker(requestFlushInterval)
	defer ticker.Stop()
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, requestWriteTimeout)
			if err := s.Flush(ctx); err != nil {
				if s.ctx.Err() == nil {
					s.report("flush request records", err)
				}
				// Slow retries after failures and restore the normal batch interval after a successful flush.
				ticker.Reset(requestRetryInterval)
			} else {
				ticker.Reset(requestFlushInterval)
			}
			cancel()
		case <-cleanup.C:
			ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
			if err := s.repo.Cleanup(ctx, time.Now().Add(-s.retention)); err != nil && s.ctx.Err() == nil {
				s.report("clean request records", err)
			}
			cancel()
		}
	}
}

// Stop closes memory admission and drains pending records within the supplied shutdown context.
func (s *Service) Stop(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		s.mu.Unlock()
		s.cancel()
		s.startOnce.Do(func() { close(s.done) })
		go func() {
			defer close(s.stopDone)
			<-s.done
			s.writes.Wait()
			s.stopErr = s.drain(ctx)
		}()
	})
	select {
	case <-s.stopDone:
		return s.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) drain(ctx context.Context) error {
	for {
		health, _ := s.Health()
		if health.Pending == 0 {
			return nil
		}
		if err := s.Flush(ctx); err != nil {
			s.report("drain request records", err)
			return err
		}
	}
}

// Flush writes a snapshot batch and retries records individually within the same timeout after batch failure.
func (s *Service) Flush(ctx context.Context) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	batch := make([]telemetry.RequestRecord, 0, min(len(s.pending), requestBatchSize))
	for _, record := range s.pending {
		batch = append(batch, record)
		if len(batch) == requestBatchSize {
			break
		}
	}
	s.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	writeCtx, cancel := context.WithTimeout(ctx, requestWriteTimeout)
	defer cancel()
	if err := s.repo.Save(writeCtx, batch); err != nil {
		if len(batch) == 1 {
			if s.discardInvalid(batch[0], err) {
				return nil
			}
			return err
		}
		if writeCtx.Err() != nil {
			return err
		}
		s.report("batch write request records", err)
		var failures []error
		for _, record := range batch {
			if err := writeCtx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			if err := s.repo.Save(writeCtx, []telemetry.RequestRecord{record}); err != nil {
				if !s.discardInvalid(record, err) {
					failures = append(failures, fmt.Errorf("request %s: %w", record.RequestID, err))
				}
			} else {
				s.ack(record)
			}
		}
		return errors.Join(failures...)
	}
	for _, record := range batch {
		s.ack(record)
	}
	return nil
}

// discardInvalid removes a confirmed invalid version and reports the discard through logs and failure counts.
func (s *Service) discardInvalid(record telemetry.RequestRecord, err error) bool {
	if !errors.Is(err, ErrInvalidRecord) {
		return false
	}
	s.ack(record)
	s.report("discard invalid request "+record.RequestID, err)
	return true
}

// ack removes the acknowledged version; updates received during the write remain pending.
func (s *Service) ack(record telemetry.RequestRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.pending[record.RequestID]; ok && current.UpdatedAt.Equal(record.UpdatedAt) {
		delete(s.pending, record.RequestID)
	}
}

func (s *Service) Find(ctx context.Context, id string, userID int64, admin bool) ([]Detail, error) {
	items, err := s.repo.Find(ctx, id, userID, admin)
	s.mu.Lock()
	record, found := s.pending[id]
	s.mu.Unlock()
	if found && (admin || record.UserID > 0 && record.UserID == userID) {
		record = telemetry.CloneRequestRecord(record)
		for i := range items {
			if items[i].RequestID == id {
				items[i].RequestRecord = telemetry.MergeRequestRecord(items[i].RequestRecord, record)
				items[i].Pending = true
				return items, nil
			}
		}
		return append(items, Detail{RequestRecord: record, Pending: true}), nil
	}
	return items, err
}

func (s *Service) Health() (Health, error) {
	s.mu.Lock()
	pending := len(s.pending)
	s.mu.Unlock()
	return Health{Pending: pending, Failures: s.failures.Load()}, nil
}

func (s *Service) report(action string, err error) {
	s.failures.Add(1)
	if s.logf != nil {
		s.logf("%s: %v", action, fmt.Errorf("request records: %w", err))
	}
}
