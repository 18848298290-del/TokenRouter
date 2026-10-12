package search

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// WorkGroup tracks operations across Manager generations so shutdown can wait for them.
// Construct it with NewWorkGroup.
type WorkGroup struct {
	mu      sync.Mutex
	active  int
	stopped bool
	done    chan struct{}
}

func NewWorkGroup() *WorkGroup { return &WorkGroup{done: make(chan struct{})} }

// Begin registers work unless stopping has started. Call the returned function when
// the operation and its cleanup finish; repeated calls to that function are safe.
func (g *WorkGroup) Begin() (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return nil, errors.New("websearch: runtime stopped")
	}
	g.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.active--
			if g.stopped && g.active == 0 {
				close(g.done)
			}
		})
	}, nil
}

// Stop rejects new work and waits for registered operations until ctx expires.
// A timeout leaves the group stopped; callers may wait again with another context.
func (g *WorkGroup) Stop(ctx context.Context) error {
	g.mu.Lock()
	if !g.stopped {
		g.stopped = true
		if g.active == 0 {
			close(g.done)
		}
	}
	done := g.done
	g.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		n := g.active
		g.mu.Unlock()
		return fmt.Errorf("websearch: %d unfinished operations: %w", n, ctx.Err())
	}
}
