package egress

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// ProxyExpiryService owns the proxy expiration worker's startup and shutdown.
// Construction is idle. Each sweep has a ten-second budget; stopping cancels and waits for it.
type ProxyExpiryService struct {
	proxyRepo ProxyRepository
	interval  time.Duration
	now       func() time.Time
	mu        sync.Mutex
	started   bool
	stopped   bool
	cancel    context.CancelFunc
	done      chan struct{}
}

func NewProxyExpiryService(repo ProxyRepository, interval time.Duration, clocks ...func() time.Time) *ProxyExpiryService {
	now := time.Now
	if len(clocks) > 0 && clocks[0] != nil {
		now = clocks[0]
	}
	return &ProxyExpiryService{proxyRepo: repo, interval: interval, now: now}
}

// Start launches the worker once per instance and runs its first sweep immediately.
// @project-doc docs/operations/upstream_transport_security.md#upstream_proxy_lifecycle
func (s *ProxyExpiryService) Start() {
	if s == nil || s.proxyRepo == nil || s.interval <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.run(ctx, s.done)
}

func (s *ProxyExpiryService) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			s.sweep(ctx)
		}
	}
}

func (s *ProxyExpiryService) sweep(parent context.Context) {
	if parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	changed, err := s.proxyRepo.SweepExpiredProxies(ctx, s.now())
	if err != nil {
		log.Printf("[ProxyExpiry] sweep expired proxies failed: %v", err)
		return
	}
	if changed > 0 {
		log.Printf("[ProxyExpiry] re-routed %d providers off expired proxies", changed)
	}
}

// StopContext cancels the worker and waits; a context timeout leaves completion pending.
func (s *ProxyExpiryService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.stopped = true
	if s.cancel != nil {
		s.cancel()
	}
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("ProxyExpiryService stop: %w", ctx.Err())
	}
}
func (s *ProxyExpiryService) Stop() { _ = s.StopContext(context.Background()) }
