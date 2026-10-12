package egress

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// expiryRepository counts sweeps to detect duplicate starts and starts after stopping.
type expiryRepository struct {
	ProxyRepository               // unused repository method delegation
	calls           chan struct{} // observed sweep notification channel
	count           atomic.Int32  // atomic observed sweep count
}

// SweepExpiredProxies increments the fixture count and sends a notification
// before returning no changes. Tests size calls for their expected sweeps and
// keep it available until worker shutdown completes.
func (r *expiryRepository) SweepExpiredProxies(context.Context, time.Time) (int64, error) {
	// Publish the count before notification so the waiting test can observe a
	// completed fixture invocation once the worker signals its first sweep.
	r.count.Add(1)
	r.calls <- struct{}{}
	return 0, nil
}

// TestProxyExpiryRepeatedStart waits for the initial sweep, invokes Start
// again, and stops the worker. It checks that the recorded sweep count remains
// one across the bounded observation interval.
func TestProxyExpiryRepeatedStart(t *testing.T) {
	repo := &expiryRepository{calls: make(chan struct{}, 4)}
	worker := NewProxyExpiryService(repo, time.Hour)
	worker.Start()
	<-repo.calls
	worker.Start()
	select {
	case <-repo.calls:
	case <-time.After(100 * time.Millisecond):
	}
	worker.Stop()
	if got := repo.count.Load(); got != 1 {
		t.Fatalf("repeated Start ran %d initial sweeps", got)
	}
}

// TestProxyExpiryStopBeforeStart seals an unstarted worker before calling
// Start. It checks that no sweep was recorded during the bounded observation
// interval and that repeated Stop completes.
func TestProxyExpiryStopBeforeStart(t *testing.T) {
	repo := &expiryRepository{calls: make(chan struct{}, 4)}
	worker := NewProxyExpiryService(repo, time.Hour)
	worker.Stop()
	worker.Start()
	select {
	case <-repo.calls:
	case <-time.After(100 * time.Millisecond):
	}
	worker.Stop()
	if got := repo.count.Load(); got != 0 {
		t.Fatalf("Start after Stop ran %d sweeps", got)
	}
}
