package apikey

import (
	"context"
	"errors"
	"sync"
)

// Authentication work can still use local caches while application shutdown is
// beginning. The operation gate marks admission stopped under the same mutex
// used before adding active work, so the drain monitor can wait after future
// additions are excluded. Startup and shutdown share runtimeMu, and a stopped
// service cannot initialize its resources again.
//
// Shutdown waits for admitted work before canceling the subscription and
// closing either L1 cache. StopContext limits its caller's wait; expiration
// leaves the monitor running until its dependencies finish. Active operations
// retain their own contexts, so a blocked repository or unreleased request
// lease can keep that monitor waiting beyond one caller's shutdown deadline.

// ErrAuthenticationStopped reports rejected work after the service closes its
// authentication admission gate during shutdown.
var ErrAuthenticationStopped = errors.New("API Key authentication is stopping")

// authOperationGate counts admitted authentication work and serializes
// admission with shutdown.
type authOperationGate struct {
	// mu orders the stopping flag check with active-count admission. Shutdown
	// sets stopping before waiting so no later enter can add active work.
	mu       sync.Mutex     // admission state serialization lock
	stopping bool           // permanent admission stop flag
	active   sync.WaitGroup // count of admitted unfinished operations
}

// enter admits one operation unless shutdown has marked the gate stopping. It
// returns whether the caller owns an active count, and each accepted operation
// requires one later leave. Admission and the stopping check share the mutex so
// a shutdown monitor cannot wait on zero while another caller adds work. The
// mutex is released before the accepted caller executes its operation, allowing
// previously admitted work to drain concurrently.
func (g *authOperationGate) enter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopping {
		return false
	}
	// Count admission before releasing mu so shutdown cannot begin its wait
	// between the successful stopping check and registration of this operation.
	g.active.Add(1)
	return true
}

// leave releases one active count previously acquired by an accepted enter
// call. Callers balance each accepted operation once; releasing an unowned
// count violates sync.WaitGroup accounting and can panic.
func (g *authOperationGate) leave() { g.active.Done() }

// stop prevents subsequent enter calls from adding active work and retains that
// state permanently. It uses the admission mutex; callers can begin waiting on
// active work after this method returns.
func (g *authOperationGate) stop() { g.mu.Lock(); g.stopping = true; g.mu.Unlock() }

// isStopping reads whether shutdown has closed admission under the gate mutex.
func (g *authOperationGate) isStopping() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.stopping }

// Start initializes optional local caches and invalidation subscription once
// after dependencies are bound. Nil services do nothing, and a stopped service
// cannot restart.
//
// The runtime mutex orders startup against shutdown, while the operation gate
// supplies the permanent stopping state. Cache construction and subscriber
// startup happen in the once callback; subsequent starts reuse those resources.
// The subscriber takes its own lifecycle lock and runs with a background
// context until its shutdown method cancels it.
func (s *APIKeyService) Start() {
	if s == nil {
		return
	}
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.operations.isStopping() {
		return
	}
	s.runtimeStart.Do(func() { s.initAuthCaches(); s.StartAuthCacheInvalidationSubscriber(context.Background()) })
}

// StopContext rejects new authentication work and waits for admitted operations
// before closing caches. Nil services return successfully; repeated callers
// observe the same completion channel and can each supply a nonnil context with
// its own wait deadline.
//
// The once callback closes admission before the monitor waits, preventing later
// active-count additions. That monitor drains work, then cancels and joins the
// subscriber before closing positive and negative L1 caches. A canceled waiting
// context returns its error while the monitor continues; callers and
// dependencies still need to finish admitted operations.
func (s *APIKeyService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.runtimeMu.Lock()
	s.runtimeStop.Do(func() {
		s.operations.stop()
		s.stopped = make(chan struct{})
		// Close admission before launching this monitor. Existing operation owners
		// finish independently, while timed-out shutdown callers can return and wait again.
		go func() {
			s.operations.active.Wait()
			// The subscriber reads these caches too. Join it after operation drain
			// and before Close so its invalidation callback cannot use closing storage.
			s.StopAuthCacheInvalidationSubscriber()
			if cache := s.authCacheL1.Load(); cache != nil {
				cache.Close()
			}
			if cache := s.authNegativeCacheL1.Load(); cache != nil {
				cache.Close()
			}
			close(s.stopped)
		}()
	})
	done := s.stopped
	s.runtimeMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop closes authentication admission and waits until resource shutdown
// completes. It uses a background context, so callers needing a deadline use
// StopContext; blocked admitted work can keep this call waiting.
func (s *APIKeyService) Stop() { _ = s.StopContext(context.Background()) }
