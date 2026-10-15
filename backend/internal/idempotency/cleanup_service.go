package idempotency

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// cleanup_service.go runs bounded expiry cleanup and coordinates worker
// startup and shutdown.
// Expired command records are deleted in one bounded batch per cleanup round.
// The worker starts with an immediate round, then uses its configured interval,
// so a restart can begin reducing accumulated expiry before the first timer tick.
// Construction retains dependencies and options until app starts the worker.
//
// Lifecycle state serializes repeated starts and shutdown so one service launches
// one worker. Stop closes the scheduling signal and waits for that worker,
// including any cleanup already in progress. Each batch has an independent
// ten-second context; stopping scheduling does not cancel the current deletion.
// A stopped service remains inactive, and a new service is needed for another
// worker lifetime.

// IdempotencyCleanupService schedules bounded expiry batches and waits for worker shutdown.
type IdempotencyCleanupService struct {
	// observer receives lifecycle notifications under lifecycleMu and cleanup
	// notifications from the worker. Implementations must not call Start or Stop
	// from these callbacks: reacquiring the lock or waiting for the current worker
	// would prevent the callback from returning.
	observer Observer
	// lifecycleMu protects started and stopped, serializing startup with the stop
	// transition so worker registration cannot occur after Stop begins waiting.
	lifecycleMu sync.Mutex            // worker startup and shutdown lock
	started     bool                  // background worker was launched
	stopped     bool                  // terminal scheduling stop state
	repo        IdempotencyRepository // idempotency expiry deletion store
	interval    time.Duration         // periodic cleanup scheduling interval
	batch       int                   // maximum expired records per round

	// startOnce registers the worker in wg once, while stopOnce closes stopCh once.
	// Stop waits on wg after publishing stopped and closing the scheduling signal.
	wg        sync.WaitGroup // active cleanup worker count
	startOnce sync.Once      // one worker registration guard
	stopOnce  sync.Once      // one stop signal guard
	stopCh    chan struct{}  // stop future scheduling signal
}

// CleanupOptions configures the cleanup interval, batch size, and observer.
type CleanupOptions struct {
	Observer Observer // optional cleanup event receiver
	// Interval selects periodic scheduling. Nonpositive values choose sixty seconds,
	// and the worker still performs its first bounded cleanup immediately after startup.
	Interval time.Duration
	// Batch bounds one deletion round. Nonpositive values choose five hundred;
	// a full batch does not trigger another deletion before the next scheduled round.
	Batch int
}

// NewIdempotencyCleanupService creates an inactive expiry worker using the
// supplied repository and observer. Nonpositive interval and batch options choose
// sixty seconds and five hundred records; Start begins its first cleanup round.
func NewIdempotencyCleanupService(repo IdempotencyRepository, opts CleanupOptions) *IdempotencyCleanupService {
	interval := opts.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	batch := opts.Batch
	if batch <= 0 {
		batch = 500
	}
	return &IdempotencyCleanupService{observer: opts.Observer, repo: repo, interval: interval, batch: batch, stopCh: make(chan struct{})}
}

// Start launches one cleanup worker after confirming repository availability.
// Nil services and missing repositories stay inactive, and repeated starts return
// once the service has started or stopped.
//
// The lifecycle lock covers the state transition and wait-group registration,
// preventing Stop from finishing its wait before a concurrently starting worker
// is registered. Observer notification happens synchronously before launch, so an
// observer used here must avoid reentering this service's lifecycle methods.
func (s *IdempotencyCleanupService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.stopped {
		return
	}
	// Publish startup and register the worker while the lifecycle lock is held,
	// so concurrent shutdown sees either an inactive service or a registered cleanup lifetime.
	s.started = true

	s.startOnce.Do(func() {
		notifyObserver(s.observer, "service.idempotency_cleanup", fmt.Sprintf("[IdempotencyCleanup] started interval=%s batch=%d", s.interval, s.batch))
		s.wg.Go(func() { ; s.runLoop() })
	})
}

// Stop prevents further startup and waits for the cleanup worker to exit.
// Repeated stops share the closed scheduling channel and can wait again; nil
// services return immediately.
//
// The worker observes the closed stopCh between batches, after completing any
// chosen deletion under its independent timeout. Notification precedes waiting, so it
// reports the scheduling transition rather than completed deletion. The lifecycle
// lock is released before Wait to let concurrent stop callers reach the same
// worker completion instead of blocking its entire lifetime behind that lock.
func (s *IdempotencyCleanupService) Stop() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	// Mark the terminal state before closing stopCh so a concurrent Start cannot
	// register a new worker after shutdown begins accounting for the existing worker.
	s.stopped = true

	s.stopOnce.Do(func() {
		// Close scheduling once while retaining the active batch's independent context;
		// waiting afterward includes that batch rather than interpreting the signal as completed cleanup.
		close(s.stopCh)
		notifyObserver(s.observer, "service.idempotency_cleanup", "[IdempotencyCleanup] stopped")
	})
	s.lifecycleMu.Unlock()
	s.wg.Wait()
}

// runLoop performs an initial cleanup and then schedules one batch per interval
// until it observes a closed stopCh. It owns the ticker lifetime, while cleanupOnce supplies
// a separate bounded database context for each round.
func (s *IdempotencyCleanupService) runLoop() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Begin reducing accumulated expired records before the first scheduled tick;
	// this initial round is part of the worker lifetime that Stop waits to finish.
	s.cleanupOnce()

	for {
		select {
		case <-ticker.C:
			s.cleanupOnce()
		case <-s.stopCh:
			return
		}
	}
}

// cleanupOnce deletes one batch of expired records using a fresh ten-second
// context and reports its result to the observer. Failures leave later rounds
// available to retry expiry cleanup, and zero deletions produce no success event.
func (s *IdempotencyCleanupService) cleanupOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deleted, err := s.repo.DeleteExpired(ctx, time.Now(), s.batch)
	if err != nil {
		// Report this round's failure and return to scheduling, retaining remaining
		// expired records for a later batch rather than retrying inside the same database window.
		notifyObserver(s.observer, "service.idempotency_cleanup", fmt.Sprintf("[IdempotencyCleanup] cleanup failed err=%v", err))
		return
	}
	if deleted > 0 {
		notifyObserver(s.observer, "service.idempotency_cleanup", fmt.Sprintf("[IdempotencyCleanup] cleaned expired records count=%d", deleted))
	}
}
