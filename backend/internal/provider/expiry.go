package provider

import (
	"context"
	"sync"
	"time"
)

// ExpiryRepository pauses expired providers that have automatic expiry pausing enabled.
type ExpiryRepository interface {
	AutoPauseExpiredProviders(context.Context, time.Time) (int64, error)
}
type ExpiryOptions struct {
	Interval time.Duration
	Now      func() time.Time
	Observe  func(string, ...any)
}

// ExpiryService scans immediately on Start and periodically afterward; construction starts no work and stopped services cannot restart.
type ExpiryService struct {
	repo             ExpiryRepository
	options          ExpiryOptions
	mu               sync.Mutex
	started, stopped bool
	cancel           context.CancelFunc
	runDone          chan struct{}
	stopDone         chan struct{}
	stopErr          error
}

func NewExpiryService(repo ExpiryRepository, options ExpiryOptions) *ExpiryService {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Observe == nil {
		options.Observe = func(string, ...any) {}
	}
	return &ExpiryService{repo: repo, options: options}
}

func (s *ExpiryService) Start() {
	if s == nil || s.repo == nil || s.options.Interval <= 0 {
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
	s.runDone = make(chan struct{})
	go s.run(ctx)
}

func (s *ExpiryService) run(ctx context.Context) {
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

func (s *ExpiryService) scan(parent context.Context) {
	if parent.Err() != nil {
		return
	}
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
func (s *ExpiryService) Stop() { _ = s.StopContext(context.Background()) }

// StopContext cancels scans and waits for in-flight work; later calls retain the first stop result, including a timeout.
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
