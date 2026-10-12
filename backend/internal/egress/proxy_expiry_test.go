package egress

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// expiryRepository counts sweeps to detect duplicate starts and starts after stopping.
type expiryRepository struct {
	ProxyRepository
	calls chan struct{}
	count atomic.Int32
}

func (r *expiryRepository) SweepExpiredProxies(context.Context, time.Time) (int64, error) {
	r.count.Add(1)
	r.calls <- struct{}{}
	return 0, nil
}

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
