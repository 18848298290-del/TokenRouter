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

// ObserverState references the observer registry; all four fields belong to
// the same manager.
type ObserverState struct {
	// Mutex, Stopped, Cancels, and Wait reference one shared initialized manager.
	// Begin and Stop use that mutex to close admission before waiting, and accepted
	// tasks stay counted until their owner cleanup removes the registry entry.
	Mutex   *sync.Mutex                    // shared observer admission lock
	Stopped *bool                          // permanent observer admission stop flag
	Cancels *map[string]context.CancelFunc // active observer owner cancellation registry
	Wait    *sync.WaitGroup                // accepted observer task completion count
}

// Begin registers a unique active observer owner and returns its detached
// context, cleanup, and acceptance flag. All state pointers must identify a
// usable common manager, and accepted callers invoke cleanup exactly once
// after work finishes.
//
// Registration increments Wait while holding the same mutex Stop uses to close
// admission, so shutdown cannot begin waiting before a newly admitted task is
// counted. Stopped managers return nil results with false. Cleanup cancels the
// task, removes its owner under the lock, then decrements Wait so stopping
// includes that registry removal.
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
	// Install cancellation and count the admitted task before releasing the
	// admission lock, so Stop cannot miss a just-entered observer while beginning
	// its completion wait.
	(*s.Cancels)[owner] = cancel
	s.Wait.Add(1)
	return ctx, func() {
		// Cancel this task and remove its owner before decrementing Wait, so the
		// shutdown completion signal includes registry removal as well as work exit.
		cancel()
		s.Mutex.Lock()
		delete(*s.Cancels, owner)
		s.Mutex.Unlock()
		s.Wait.Done()
	}, true
}

// Stop closes local observer admission, cancels registered contexts, and waits
// using the supplied nonnil context. Callers keep all state pointers tied to
// the same initialized manager; repeated calls remain stopped and can wait
// again.
//
// The mutex is released before waiting so each accepted cleanup can remove its
// registry owner and decrement Wait. A waiter goroutine exposes completion
// without blocking the context-select path. If the context wins, Stop returns
// its error while those tasks continue cleanup; when cancellation and
// completion are both ready, the select can return either result.
func (s ObserverState) Stop(ctx context.Context) error {
	s.Mutex.Lock()
	*s.Stopped = true
	// Cancel accepted owners while admission is closed. Their cleanup retains
	// each wait count until registry removal, so the later wait includes teardown.
	for _, cancel := range *s.Cancels {
		cancel()
	}
	// Release the admission lock before waiting for task completion; accepted
	// cleanup needs that lock to remove its owner before the wait count can reach
	// zero.
	s.Mutex.Unlock()
	done := make(chan struct{})
	// Wait separately from the caller's context so a shutdown timeout can return
	// even when an admitted observer has not yet completed its registered
	// cleanup.
	go func() { s.Wait.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
