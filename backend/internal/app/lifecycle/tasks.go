package lifecycle

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Tasks tracks asynchronous work; callers own contexts, concurrency limits, and retry policy.
// Active tasks may spawn children while draining; final shutdown seals admission when the task count reaches zero.
type Tasks struct {
	mu       sync.Mutex
	active   map[string]int
	count    int
	idle     chan struct{}
	stopping bool
	closed   bool
}

func NewTasks() *Tasks {
	idle := make(chan struct{})
	close(idle)
	return &Tasks{active: make(map[string]int), idle: idle}
}

// Go starts tracked work and returns false after admission closes. Evaluate task inputs before capturing them
// in fn.
func (t *Tasks) Go(name string, fn func()) bool {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false
	}
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

func (t *Tasks) complete(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active[name]--
	if t.active[name] == 0 {
		delete(t.active, name)
	}
	t.count--
	if t.count == 0 {
		if t.stopping {
			t.closed = true
		}
		close(t.idle)
	}
}

// Wait waits for the current task group to become idle while allowing later consumers to submit work.
func (t *Tasks) Wait(ctx context.Context) error { return t.wait(ctx, false) }

// Stop seals admission when all tasks finish. Call it after the last producer completes.
func (t *Tasks) Stop(ctx context.Context) error { return t.wait(ctx, true) }

func (t *Tasks) wait(ctx context.Context, stop bool) error {
	t.mu.Lock()
	if stop {
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
