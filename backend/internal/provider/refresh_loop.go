package provider

import (
	"context"
	"errors"
	"sync"
	"time"
)

// RefreshLoop runs periodic refresh cycles; the supplied cycle selects candidates and exchanges credentials.
// The first successful StartContext launches the loop. A stopped loop cannot restart.
type RefreshLoop struct {
	mu               sync.Mutex
	started, stopped bool
	cycle            func(context.Context)
	activity         operationActivity
}

func NewRefreshLoop(cycle func(context.Context)) *RefreshLoop { return &RefreshLoop{cycle: cycle} }
func (l *RefreshLoop) StartContext(parent context.Context, interval time.Duration) (bool, error) {
	if l == nil || l.cycle == nil {
		return false, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started || l.stopped {
		return false, nil
	}
	if interval <= 0 {
		return false, errors.New("token refresh interval must be positive")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, done, err := l.activity.begin(parent, ErrRefreshStopped)
	if err != nil {
		return false, err
	}
	l.started = true
	go func() {
		defer done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		if ctx.Err() == nil {
			l.cycle(ctx)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				l.cycle(ctx)
			}
		}
	}()
	return true, nil
}

// StopContext cancels the loop and waits for the active cycle; its first result remains fixed, including
// timeout errors.
func (l *RefreshLoop) StopContext(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	l.stopped = true
	l.mu.Unlock()
	return l.activity.stop(ctx, "token refresh")
}
