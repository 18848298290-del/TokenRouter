package provider

import (
	"context"
	"sync"
	"time"
)

// deferred.go queues provider activity timestamps and flushes them through
// periodic and shutdown writes.
// Frequent provider activity replaces one queued last-used timestamp per ID,
// while a periodic writer persists batches independently of the request path.
// A failed older batch must not replace newer activity that arrived while the
// database write was in progress.
//
// The lifecycle mutex protects enqueueing and the drain that removes a batch; a
// separate RefreshLock serializes periodic and final writes. Failure
// restoration uses LoadOrStore so a newer queued timestamp keeps its place.
// Stopping rejects new activity, cancels waiting periodic flushes, and then
// attempts the final batch after any active write releases the flush lock.
// Periodic database calls have their own ten-second context; the final call
// additionally inherits the shutdown budget. The first stop result, including a
// timeout, remains the result of later stops.

// DeferredRepository persists provider activity timestamps removed from the
// pending queue.
type DeferredRepository interface {
	// BatchUpdateLastUsed writes the supplied provider times under ctx's budget.
	// The service retains ownership of updates for failure restoration, so the
	// repository treats this batch as read-only until the call returns.
	BatchUpdateLastUsed(context.Context, map[int64]time.Time) error
}

// DeferredSchedule registers the activity flush and removes its recurring
// callback.
type DeferredSchedule interface {
	// ScheduleRecurring registers a named callback using the supplied interval.
	// DeferredService.Start holds lifecycleMu during this call. Implementations
	// return without waiting for callback execution because draining activity
	// acquires that same mutex.
	ScheduleRecurring(string, time.Duration, func())
	// Cancel removes the named recurring callback from the scheduler.
	Cancel(string)
}

// DeferredOptions supplies the activity clock, flush cadence, and write observer.
type DeferredOptions struct {
	Interval time.Duration // periodic flush scheduling interval
	// Now timestamps queued activity; NewDeferredService supplies time.Now when
	// absent. Enqueueing calls it under lifecycleMu, so it returns without
	// reentering a service method that acquires the same mutex.
	Now func() time.Time
	// Observe receives flush and stop outcomes. NewDeferredService installs a
	// no-op callback when omitted, so database failures can still be returned.
	Observe func(string, ...any)
}

// DeferredService coalesces provider activity and serializes periodic and final
// batch writes.
type DeferredService struct {
	providerRepo DeferredRepository // activity timestamp persistence target
	timingWheel  DeferredSchedule   // recurring flush callback scheduler
	options      DeferredOptions    // clock, cadence, and observation callbacks
	// lastUsedUpdates stores int64 IDs and time.Time values. Enqueueing and
	// extraction use lifecycleMu; failure restoration retains newer entries
	// through LoadOrStore after the database call returns.
	lastUsedUpdates sync.Map
	// flushMu serializes database batches and permits shutdown to cancel a
	// wait for the active batch while retaining queued timestamps.
	flushMu *RefreshLock
	// lifecycleMu protects queue admission, extraction, and lifecycle flags;
	// the active repository call runs after this mutex has been released.
	lifecycleMu sync.Mutex
	// started and stopped prevent duplicate scheduling and later admission.
	// The stopped state also lets queued periodic callbacks skip their drain
	// while the stop owner proceeds with its final flush.
	started, stopped bool // scheduling and shutdown state
	// waitCtx and cancelWait cancel periodic flush lock waits at shutdown.
	// Active periodic writes use their separate database context and keep running
	// until that call returns or its ten-second timeout takes effect.
	waitCtx    context.Context    // periodic lock-wait cancellation context
	cancelWait context.CancelFunc // shutdown cancellation for lock waiters
	// stopDone and stopErr publish the first stop outcome together. Closing
	// stopDone makes stopErr available to later callers, including a retained
	// failure when the final queue did not drain.
	stopDone chan struct{} // first stop completion signal
	stopErr  error         // retained first stop result
}

// NewDeferredService constructs an activity queue with serialized flushes and a
// cancellable wait context. Missing Now and Observe callbacks select time.Now
// and a no-op observer; the repository and recurring scheduler are supplied by
// the caller.
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

// Start registers the recurring last-used flush at the configured interval.
// Repeated calls are ignored, and a stopped service cannot restart.
//
// Registration holds lifecycleMu while publishing started and scheduling the
// callback. A competing Start cannot add another callback and StopContext
// cannot cancel the job before registration completes. Activity updates also
// wait for registration to return because they share lifecycleMu.
// DeferredSchedule implementations return without waiting on a callback that
// needs this same lock, allowing registration and later draining to complete.
func (s *DeferredService) Start() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped || s.started {
		return
	}
	// Mark and register this schedule before releasing lifecycleMu. A concurrent
	// Start cannot create a duplicate callback, and StopContext cannot cancel the
	// job until its registration has completed.
	s.started = true
	s.timingWheel.ScheduleRecurring("deferred:last_used", s.options.Interval, s.flushLastUsed)
}

// ScheduleLastUsedUpdate records the current activity time for a provider,
// replacing that ID's pending timestamp. Calls after stopping are ignored, and
// the supplied clock is read while lifecycleMu is held.
//
// The stopped check and queue replacement share the lock used by StopContext
// and batch extraction. This orders admission before shutdown or before the
// batch is removed; activity arriving during persistence enters the next
// pending batch. Replacements follow arrival order even if an injected clock
// moves backward.
func (s *DeferredService) ScheduleLastUsedUpdate(id int64) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	// Check admission and store under the same lock as extraction. A flush
	// cannot delete this replacement between its admission check and publication
	// into the pending queue.
	if !s.stopped {
		now := s.options.Now
		if now == nil {
			now = time.Now
		}
		s.lastUsedUpdates.Store(id, now())
	}
}

// StopContext rejects new activity, cancels periodic lock waiters, and attempts
// one final flush using ctx as its remaining shutdown budget. Concurrent
// callers wait for the first stop's result unless their own contexts expire
// first.
//
// The flush lock keeps the final batch behind an active periodic write, whose
// independent database timeout continues to govern that write. Queued
// timestamps remain available if shutdown cannot acquire the lock or the final
// write fails. The stop owner's outcome is retained, including a timeout;
// another StopContext call does not retry that unfinished flush.
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
	// Publish stopped admission before canceling waiters. A periodic callback
	// that wins the flush lock after cancellation then sees stopped and skips
	// extraction, leaving the final batch to this stop owner.
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
	// Store the outcome before closing the completion signal. Waiting stop
	// callers can then read that same result after observing completion,
	// including a failure that left pending activity unflushed.
	s.stopErr = err
	close(done)
	s.lifecycleMu.Unlock()
	return err
}

// flushLastUsed runs the periodic callback and discards its returned flush
// error. The flush operation records database failures through Observe and
// restores their batch entries for a later attempt.
func (s *DeferredService) flushLastUsed() { _ = s.flushLastUsedErr() }

// flushLastUsedErr executes a periodic batch using the service's cancellable
// lock-wait context. Shutdown cancels that wait, while a write already holding
// the flush lock keeps its independent database timeout.
func (s *DeferredService) flushLastUsedErr() error { return s.flush(s.waitCtx, false) }

// flush serializes one activity batch and persists it through the repository.
// final permits a stopped service to drain its remaining queue and makes the
// database context inherit the supplied shutdown context.
//
// Batch extraction holds lifecycleMu so enqueueing cannot interleave with the
// Range/Delete sequence. The mutex is released before persistence, letting new
// activity queue while the write runs. On failure each old timestamp is
// restored with LoadOrStore, retaining a newer value already queued for that
// provider. An empty batch succeeds without a repository call.
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
	// Extract and delete while enqueueing is excluded by lifecycleMu. New
	// request activity can then accumulate in a fresh batch during persistence.
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
	// A periodic write already admitted by flushMu completes on its own bounded
	// context after shutdown cancels lock waiters. The final write inherits
	// the application's remaining budget as well as the ten-second ceiling.
	parent := context.Background()
	if final {
		parent = ctx
	}
	writeCtx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if err := s.providerRepo.BatchUpdateLastUsed(writeCtx, updates); err != nil {
		s.options.Observe("[DeferredService] BatchUpdateLastUsed failed (%d providers): %v", len(updates), err)
		// An activity update may arrive while this failed batch is outside the
		// queue. LoadOrStore keeps that newer value and restores only providers
		// whose pending entry is still absent.
		for id, ts := range updates {
			s.lastUsedUpdates.LoadOrStore(id, ts)
		}
		return err
	}
	s.options.Observe("[DeferredService] BatchUpdateLastUsed flushed %d providers", len(updates))
	return nil
}
