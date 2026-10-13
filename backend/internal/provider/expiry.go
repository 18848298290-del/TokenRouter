package provider

import (
	"context"
	"sync"
	"time"
)

// Providers configured for automatic expiry pausing qualify for suspension once
// their stored expiration is reached. The worker scans immediately after a
// usable Start and then at the configured interval; construction and disabled
// intervals perform no repository work. Each scan has a separate five-second
// cancellation budget, while the repository determines persistence and
// scheduling-event publication.
//
// Start and StopContext share one lifecycle mutex. Start publishes one worker
// and its completion channel before releasing that mutex, and a stop
// permanently rejects later starts. Cancellation requests the current scan to
// end; completion is acknowledged only after the worker returns. The first stop
// result is retained, including a timeout that leaves repository work
// unfinished.

// ExpiryRepository pauses automatically managed providers whose stored
// expiration is at or before the observation time.
type ExpiryRepository interface {
	// AutoPauseExpiredProviders applies expiration pausing at the supplied time
	// and returns the affected provider count. Repository errors are returned to
	// the worker for observation; publication belongs to the repository.
	AutoPauseExpiredProviders(context.Context, time.Time) (int64, error)
}

// ExpiryOptions supplies the periodic interval, observation clock and scan
// diagnostic callback.
type ExpiryOptions struct {
	// Interval selects the period after the immediate scan; a nonpositive value
	// keeps Start idle.
	Interval time.Duration
	// Now supplies expiration comparison time for each scan; NewExpiryService
	// supplies time.Now when this callback is absent.
	Now func() time.Time
	// Observe receives scan failures and nonzero affected counts synchronously;
	// NewExpiryService replaces an absent callback with a no-op.
	Observe func(string, ...any)
}

// ExpiryService owns one cancellable expiration worker and its permanent
// shutdown result.
type ExpiryService struct {
	// repo is borrowed until the worker actually returns. Keep it usable after a
	// timed-out StopContext while a scan is still unfinished.
	repo    ExpiryRepository
	options ExpiryOptions // normalized worker configuration
	// mu protects started, stopped, cancel, runDone, stopDone and stopErr. The
	// worker closes runDone on exit; the first stopping caller publishes stopErr
	// before closing stopDone for other callers.
	mu               sync.Mutex         // lifecycle state synchronization
	started, stopped bool               // started: worker admitted; stopped: restart rejected
	cancel           context.CancelFunc // current worker cancellation
	runDone          chan struct{}      // actual worker completion
	stopDone         chan struct{}      // stop result publication
	stopErr          error              // first stop result
}

// NewExpiryService retains the repository and normalizes the clock and
// observation callbacks. It starts no work; a usable repository and positive
// Interval are needed for Start to admit a worker.
func NewExpiryService(repo ExpiryRepository, options ExpiryOptions) *ExpiryService {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Observe == nil {
		options.Observe = func(string, ...any) {}
	}
	return &ExpiryService{repo: repo, options: options}
}

// Start admits the first expiration worker when the service has a repository
// and positive interval. Nil services and repeated starts are ignored. A
// service stopped before or after startup remains permanently stopped.
//
// Worker registration and stop registration use the same mutex. The
// cancellation function and completion channel are published together before
// the goroutine starts, so a concurrent stop captures the admitted worker. The
// goroutine performs its immediate scan before waiting for interval ticks.
func (s *ExpiryService) Start() {
	if s == nil || s.repo == nil || s.options.Interval <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}
	// Publish cancellation and completion under the lifecycle mutex, so a
	// concurrent stop captures the same admitted worker before waiting for its
	// repository use to finish.
	s.started = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.runDone = make(chan struct{})
	go s.run(ctx)
}

// run performs the immediate scan and receives interval ticks until its context
// is cancelled. Start supplies the completion channel; run closes it after
// every scan has returned and stops its ticker on exit.
func (s *ExpiryService) run(ctx context.Context) {
	// Completion follows the last repository return. StopContext uses this signal
	// to distinguish finished dependency use from a scan that has merely received
	// cancellation.
	defer close(s.runDone)
	ticker := time.NewTicker(s.options.Interval)
	defer ticker.Stop()
	s.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scan(ctx)
		}
	}
}

// scan asks the repository to pause expired providers at the configured clock
// time. A cancelled parent skips the call, each query receives five seconds,
// and failures or positive counts are observed synchronously.
func (s *ExpiryService) scan(parent context.Context) {
	if parent.Err() != nil {
		return
	}
	// Give each maintenance query five seconds independently of the tick interval,
	// while retaining worker cancellation so shutdown can request the in-flight
	// scan to end.
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	updated, err := s.repo.AutoPauseExpiredProviders(ctx, s.options.Now())
	if err != nil {
		s.options.Observe("[ProviderExpiry] Auto pause expired providers failed: %v", err)
		return
	}
	if updated > 0 {
		s.options.Observe("[ProviderExpiry] Auto paused %d expired providers", updated)
	}
}

// Stop cancels the worker and waits using a background context, discarding the
// retained shutdown error. Use StopContext when the caller needs a finite
// waiting budget or the first stop result.
func (s *ExpiryService) Stop() { _ = s.StopContext(context.Background()) }

// StopContext permanently rejects future starts, cancels the worker and waits
// within the caller context. Nil services succeed, and stopping before Start
// has no worker to await.
//
// The first caller owns stop completion and records either worker completion or
// its waiting error. Later callers await that publication with their own
// contexts and then receive the retained result. An already completed worker
// wins the initial completion check even when the waiting context is cancelled.
// A timeout records unfinished work, so borrowed dependencies remain usable
// until the worker actually exits.
func (s *ExpiryService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.stopped {
		done := s.stopDone
		s.mu.Unlock()
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
	// Publish stopDone under the mutex so later callers can await this result.
	// Closing it after stopErr is stored makes completed-result reads visible; a
	// waiting caller can still exhaust its own context before publication.
	s.stopped = true
	s.stopDone = make(chan struct{})
	done := s.stopDone
	runDone := s.runDone
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	var err error
	if runDone != nil {
		select {
		case <-runDone:
		default:
			select {
			case <-runDone:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
	}
	s.mu.Lock()
	s.stopErr = err
	close(done)
	s.mu.Unlock()
	return err
}
