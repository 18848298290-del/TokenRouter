package provider

import (
	"context"
	"sync"
	"time"
)

// DeferredRepository batch-writes provider activity timestamps.
type DeferredRepository interface {
	BatchUpdateLastUsed(context.Context, map[int64]time.Time) error
}

// DeferredSchedule registers and cancels recurring work.
type DeferredSchedule interface {
	ScheduleRecurring(string, time.Duration, func())
	Cancel(string)
}
type DeferredOptions struct {
	Interval time.Duration
	Now      func() time.Time
	Observe  func(string, ...any)
}

// DeferredService owns the activity queue, rejects enqueueing after stop, and bounds the final flush.
type DeferredService struct {
	providerRepo     DeferredRepository
	timingWheel      DeferredSchedule
	options          DeferredOptions
	lastUsedUpdates  sync.Map
	flushMu          *RefreshLock
	lifecycleMu      sync.Mutex
	started, stopped bool
	waitCtx          context.Context
	cancelWait       context.CancelFunc
	stopDone         chan struct{}
	stopErr          error
}

func NewDeferredService(repo DeferredRepository, wheel DeferredSchedule, options DeferredOptions) *DeferredService {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Observe == nil {
		options.Observe = func(string, ...any) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &DeferredService{providerRepo: repo, timingWheel: wheel, options: options, flushMu: NewRefreshLock(), waitCtx: ctx, cancelWait: cancel}
}

func (s *DeferredService) Start() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped || s.started {
		return
	}
	s.started = true
	s.timingWheel.ScheduleRecurring("deferred:last_used", s.options.Interval, s.flushLastUsed)
}

func (s *DeferredService) ScheduleLastUsedUpdate(id int64) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.stopped {
		now := s.options.Now
		if now == nil {
			now = time.Now
		}
		s.lastUsedUpdates.Store(id, now())
	}
}

// StopContext cancels waiting periodic flushes, waits for an active write's budget, and performs a final batch.
func (s *DeferredService) StopContext(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if s.stopped {
		done := s.stopDone
		s.lifecycleMu.Unlock()
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
	s.stopped = true
	s.stopDone = make(chan struct{})
	done := s.stopDone
	s.cancelWait()
	s.lifecycleMu.Unlock()
	s.timingWheel.Cancel("deferred:last_used")
	err := s.flush(ctx, true)
	if err != nil {
		s.options.Observe("[DeferredService] Stop incomplete: %v", err)
	} else {
		s.options.Observe("[DeferredService] Service stopped")
	}
	s.lifecycleMu.Lock()
	s.stopErr = err
	close(done)
	s.lifecycleMu.Unlock()
	return err
}
func (s *DeferredService) flushLastUsed()          { _ = s.flushLastUsedErr() }
func (s *DeferredService) flushLastUsedErr() error { return s.flush(s.waitCtx, false) }

// flush removes a batch under the enqueue lock; failed writes restore entries only if no newer value exists.
func (s *DeferredService) flush(ctx context.Context, final bool) error {
	if err := s.flushMu.Lock(ctx); err != nil {
		return err
	}
	defer s.flushMu.Unlock()
	s.lifecycleMu.Lock()
	if s.stopped && !final {
		s.lifecycleMu.Unlock()
		return nil
	}
	updates := make(map[int64]time.Time)
	s.lastUsedUpdates.Range(func(key, value any) bool {
		id, ok := key.(int64)
		if !ok {
			return true
		}
		ts, ok := value.(time.Time)
		if !ok {
			return true
		}
		updates[id] = ts
		s.lastUsedUpdates.Delete(key)
		return true
	})
	s.lifecycleMu.Unlock()
	if len(updates) == 0 {
		return nil
	}
	// Periodic writes have a ten-second budget; the final write also respects the remaining shutdown budget.
	parent := context.Background()
	if final {
		parent = ctx
	}
	writeCtx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if err := s.providerRepo.BatchUpdateLastUsed(writeCtx, updates); err != nil {
		s.options.Observe("[DeferredService] BatchUpdateLastUsed failed (%d providers): %v", len(updates), err)
		for id, ts := range updates {
			s.lastUsedUpdates.LoadOrStore(id, ts)
		}
		return err
	}
	s.options.Observe("[DeferredService] BatchUpdateLastUsed flushed %d providers", len(updates))
	return nil
}
