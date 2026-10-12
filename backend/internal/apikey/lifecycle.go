package apikey

import (
	"context"
	"errors"
	"sync"
)

// ErrAuthenticationStopped reports that authentication is no longer accepting work.
var ErrAuthenticationStopped = errors.New("API Key authentication is stopping")

// authOperationGate serializes admission and shutdown so Wait cannot race with Add on a zero count.
type authOperationGate struct {
	mu       sync.Mutex
	stopping bool
	active   sync.WaitGroup
}

func (g *authOperationGate) enter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopping {
		return false
	}
	g.active.Add(1)
	return true
}

func (g *authOperationGate) leave() { g.active.Done() }

func (g *authOperationGate) stop() { g.mu.Lock(); g.stopping = true; g.mu.Unlock() }

func (g *authOperationGate) isStopping() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.stopping }

// Start initializes caches and subscriptions after dependencies are bound. A stopped service cannot restart.
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

// StopContext rejects new work and drains active operations before closing subscriptions and L1 caches.
func (s *APIKeyService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.runtimeMu.Lock()
	s.runtimeStop.Do(func() {
		s.operations.stop()
		s.stopped = make(chan struct{})
		go func() {
			s.operations.active.Wait()
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

// Stop waits synchronously; production shutdown uses StopContext with a deadline.
func (s *APIKeyService) Stop() { _ = s.StopContext(context.Background()) }
