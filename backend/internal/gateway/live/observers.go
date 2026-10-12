package live

import (
	"context"
	"sync"
)

// ObserverState references the observer registry; all four fields belong to the same manager.
type ObserverState struct {
	Mutex   *sync.Mutex
	Stopped *bool
	Cancels *map[string]context.CancelFunc
	Wait    *sync.WaitGroup
}

// Begin registers an observer and increments the wait count under one lock; it rejects calls after Stop.
func (s ObserverState) Begin(owner string) (context.Context, func(), bool) {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()
	if *s.Stopped {
		return nil, nil, false
	}
	if *s.Cancels == nil {
		*s.Cancels = make(map[string]context.CancelFunc)
	}
	ctx, cancel := context.WithCancel(context.Background())
	(*s.Cancels)[owner] = cancel
	s.Wait.Add(1)
	return ctx, func() {
		cancel()
		s.Mutex.Lock()
		delete(*s.Cancels, owner)
		s.Mutex.Unlock()
		s.Wait.Done()
	}, true
}

// Stop cancels local observers and waits within ctx's budget. Remote sessions retain their own lifetime.
func (s ObserverState) Stop(ctx context.Context) error {
	s.Mutex.Lock()
	*s.Stopped = true
	for _, cancel := range *s.Cancels {
		cancel()
	}
	s.Mutex.Unlock()
	done := make(chan struct{})
	go func() { s.Wait.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
