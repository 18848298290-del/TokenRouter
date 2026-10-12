package provider

import (
	"context"
	"fmt"
	"sync"
)

type operationActivity struct {
	mu       sync.Mutex
	stopped  bool
	next     uint64
	active   map[uint64]context.CancelFunc
	idle     chan struct{}
	stopDone chan struct{}
	stopErr  error
}

// begin tracks an operation through completion and links cancellation to its parent and shutdown.
// The caller must invoke the returned finish function exactly once.
func (state *operationActivity) begin(parent context.Context, stoppedError error) (context.Context, func(), error) {
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	state.mu.Lock()
	if state.stopped {
		state.mu.Unlock()
		return nil, nil, stoppedError
	}
	if len(state.active) == 0 {
		state.idle = make(chan struct{})
	}
	if state.active == nil {
		state.active = make(map[uint64]context.CancelFunc)
	}
	state.next++
	id := state.next
	ctx, cancel := context.WithCancel(parent)
	state.active[id] = cancel
	state.mu.Unlock()
	return ctx, func() {
		cancel()
		state.mu.Lock()
		delete(state.active, id)
		if len(state.active) == 0 {
			close(state.idle)
		}
		state.mu.Unlock()
	}, nil
}

// stop rejects new operations, cancels active work, and retains the first shutdown result, including timeout.
func (state *operationActivity) stop(ctx context.Context, label string) error {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	if state.stopped {
		done := state.stopDone
		state.mu.Unlock()
		select {
		case <-done:
			return state.stopErr
		default:
		}
		select {
		case <-done:
			return state.stopErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	state.stopped = true
	state.stopDone = make(chan struct{})
	done := state.stopDone
	idle := state.idle
	cancels := make([]context.CancelFunc, 0, len(state.active))
	for _, cancel := range state.active {
		cancels = append(cancels, cancel)
	}
	state.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	var err error
	if idle != nil {
		select {
		case <-idle:
		default:
			select {
			case <-idle:
			case <-ctx.Done():
				err = fmt.Errorf("%s work remains unfinished: %w", label, ctx.Err())
			}
		}
	}
	state.mu.Lock()
	state.stopErr = err
	close(done)
	state.mu.Unlock()
	return err
}
