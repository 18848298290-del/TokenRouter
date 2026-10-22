package live

import (
	"context"
	"sync"
)

// observers.go registers local Live observers and coordinates their
// cancellation and completion during shutdown.
// Live observers can enter while application shutdown starts, so registration
// and stopping must agree on which tasks belong to the wait count. Begin holds
// the manager lock while installing cancellation and increasing that count,
// and Stop marks admission closed under the same lock before waiting.
//
// The returned cleanup cancels its context, removes its owner, and then
// decrements the wait count. Callers use a unique active owner and invoke
// cleanup exactly once because the closure has no repetition guard. Stop
// cancels local tasks and waits after releasing the lock so cleanup can remove
// owners. A caller timeout ends that wait while admitted tasks continue their
// own cleanup; persistent remote calls keep their independent lease and expiry
// lifecycle.

// ObserverManager owns registration and shutdown for local Live observers.
// Its zero value accepts registrations. Share one pointer between callers and
// do not copy the manager after its first use.
type ObserverManager struct {
	// mu protects stopped and cancels, and admission increments wait under that
	// lock before Stop can wait. Accepted tasks remain counted until cleanup
	// removes their owner from the registry.
	mu      sync.Mutex                    // shared observer admission lock
	stopped bool                          // permanent observer admission stop flag
	cancels map[string]context.CancelFunc // active observer owner cancellation registry
	wait    sync.WaitGroup                // accepted observer task completion count
}

// Begin registers a unique active observer owner and returns its detached
// context, cleanup, and acceptance flag. Callers share a nonnil manager and
// accepted callers invoke cleanup exactly once after work finishes. An empty
// owner is accepted under the same uniqueness requirement as other owners.
//
// Registration increments wait while holding the same mutex Stop uses to close
// admission, so shutdown cannot begin waiting before a newly admitted task is
// counted. Stopped managers return nil results with false. Cleanup cancels the
// task, removes its owner under the lock, then decrements wait so stopping
// includes that registry removal.
func (s *ObserverManager) Begin(owner string) (context.Context, func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, nil, false
	}
	if s.cancels == nil {
		s.cancels = make(map[string]context.CancelFunc)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Install cancellation and count the admitted task before releasing the
	// admission lock, so Stop cannot miss a just-entered observer while beginning
	// its completion wait.
	s.cancels[owner] = cancel
	s.wait.Add(1)
	return ctx, func() {
		// Cancel this task and remove its owner before decrementing wait, so the
		// shutdown completion signal includes registry removal as well as work exit.
		cancel()
		s.mu.Lock()
		delete(s.cancels, owner)
		s.mu.Unlock()
		s.wait.Done()
	}, true
}

// Stop closes local observer admission, cancels registered contexts, and waits
// using the supplied nonnil context. Callers use the same nonnil manager as
// registration; repeated calls remain stopped and can wait again after an
// earlier context ends.
//
// The mutex is released before waiting so each accepted cleanup can remove its
// registry owner and decrement wait. A waiter goroutine exposes completion
// without blocking the context-select path. If the context wins, Stop returns
// its error while those tasks continue cleanup; when cancellation and
// completion are both ready, the select can return either result.
func (s *ObserverManager) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = true
	// Cancel accepted owners while admission is closed. Their cleanup retains
	// each wait count until registry removal, so the later wait includes teardown.
	for _, cancel := range s.cancels {
		cancel()
	}
	// Release the admission lock before waiting for task completion; accepted
	// cleanup needs that lock to remove its owner before the wait count can reach
	// zero.
	s.mu.Unlock()
	done := make(chan struct{})
	// Wait separately from the caller's context so a shutdown timeout can return
	// even when an admitted observer has not yet completed its registered
	// cleanup.
	go func() {
		s.wait.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
