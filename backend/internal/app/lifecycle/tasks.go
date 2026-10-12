package lifecycle

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Background work may create child tasks while application shutdown is
// draining. Task admission remains open until the current group reaches zero,
// allowing a parent to register its child before the parent finishes. Ordinary
// Wait observes a captured group's idle signal and leaves admission available
// for later consumers; Stop asks the final idle transition to seal it.
//
// A mutex coordinates the count, diagnostic names, and idle-channel replacement.
// Each accepted callback returns through complete, which closes idle at zero
// and closes admission when stopping was requested. Caller contexts bound
// waiting, while callbacks retain their own contexts, concurrency limits, and
// retry policy. A timed-out stop can be retried after the remaining work returns.

// Tasks counts named asynchronous callbacks and coordinates their final admission seal.
type Tasks struct {
	// mu protects active, count, idle, stopping, and closed. A nonempty group
	// has an open idle signal; completing its last callback closes the signal
	// and seals admission when stopping is set.
	mu       sync.Mutex     // task admission lock
	active   map[string]int // tasks per diagnostic name
	count    int            // accepted callback count
	idle     chan struct{}  // captured group completion
	stopping bool           // final idle seals admission
	closed   bool           // new work rejected
}

// NewTasks constructs an idle tracker with open task admission and an empty
// diagnostic map. Its initial completion channel is closed, allowing Wait or
// Stop to observe an empty group immediately.
func NewTasks() *Tasks {
	idle := make(chan struct{})
	close(idle)
	return &Tasks{active: make(map[string]int), idle: idle}
}

// Go admits a named callback and starts it asynchronously, returning false when
// admission is closed. Evaluate mutable inputs before capturing them in fn,
// and provide the callback's cancellation, limits, and retry behavior yourself.
//
// Registration replaces an idle group's channel and increments its count under
// the same mutex used by shutdown. This prevents a stop from sealing admission
// between accepting the callback and recording it. The callback defers complete,
// so its return decrements the tracked count and may finish final shutdown.
func (t *Tasks) Go(name string, fn func()) bool {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false
	}
	// Open an idle group before publishing callback membership under the same
	// lock. Wait and Stop then observe the accepted work before its goroutine starts.
	if t.count == 0 {
		t.idle = make(chan struct{})
	}
	t.count++
	t.active[name]++
	t.mu.Unlock()
	go func() {
		defer t.complete(name)
		fn()
	}()
	return true
}

// complete removes one named callback after its work returns and publishes an
// idle transition when the count reaches zero. It runs once for each accepted
// Go callback and keeps diagnostic names consistent with the total count.
//
// The admission mutex covers both the count and the closed flag. If Stop has
// set stopping, the final callback seals admission before closing idle, so a
// concurrent producer cannot start a new group after that final completion.
// Ordinary waits leave stopping false and permit subsequent groups.
func (t *Tasks) complete(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active[name]--
	if t.active[name] == 0 {
		delete(t.active, name)
	}
	t.count--
	if t.count == 0 {
		// The last callback owns final sealing while holding the admission lock.
		// Close idle afterward so stop waiters cannot observe an unsealed empty group.
		if t.stopping {
			t.closed = true
		}
		close(t.idle)
	}
}

// Wait observes the idle signal captured for the current group and returns
// after it closes or ctx expires. It leaves admission state unchanged; later
// consumers may submit another group while the tracker remains open.
func (t *Tasks) Wait(ctx context.Context) error { return t.wait(ctx, false) }

// Stop requests final sealing when the captured group becomes idle and waits
// with ctx. Call it after the final producer; accepted callbacks may still add
// children until the count reaches zero. A timeout can be retried after work finishes.
func (t *Tasks) Stop(ctx context.Context) error { return t.wait(ctx, true) }

// wait captures a group's completion signal and optionally requests final
// admission sealing. An empty group is sealed immediately for stop, while
// active callbacks keep admission open until complete observes the final return.
//
// The caller waits on that captured signal rather than a future group's signal.
// Cancellation returns an error with a sorted snapshot of active names and
// counts, but callbacks continue with their own contexts. The stopping flag
// remains set after timeout, allowing the eventual final callback to close
// admission before a later Stop observes completion.
func (t *Tasks) wait(ctx context.Context, stop bool) error {
	t.mu.Lock()
	if stop {
		// Set stopping before releasing the lock. The current final callback will
		// seal admission at zero, or this empty group can be sealed immediately.
		t.stopping = true
		if t.count == 0 {
			t.closed = true
		}
	}
	idle := t.idle
	t.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		// Timeout affects this wait rather than the callbacks. Copy diagnostic
		// counts under the mutex, then sort and report them after releasing shared state.
		t.mu.Lock()
		names := make([]string, 0, len(t.active))
		for name, n := range t.active {
			names = append(names, fmt.Sprintf("%s=%d", name, n))
		}
		t.mu.Unlock()
		sort.Strings(names)
		return fmt.Errorf("background work incomplete [%s]: %w", strings.Join(names, ", "), ctx.Err())
	}
}
