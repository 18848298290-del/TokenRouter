package ops

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// ingressRejectRepoStub counts accepted deltas and injects a configured number
// of write failures.
type ingressRejectRepoStub struct {
	// mu guards failCount, calls, and requests against the aggregator worker and
	// test reads. Batch attempts increment calls even when an injected error
	// prevents counting requests.
	mu        sync.Mutex // fixture write-state access mutex
	failCount int        // remaining injected write failures
	calls     int        // batch write attempt count
	requests  int64      // accepted aggregate request total
}

// BatchUpsertIngressRejects records a call and either consumes an injected
// failure or sums the supplied nonnil rows. It ignores context and holds the
// fixture mutex while changing counters.
func (r *ingressRejectRepoStub) BatchUpsertIngressRejects(_ context.Context, items []*OpsIngressRejectAggregate) error {
	// Serialize injected failure consumption and request accounting so periodic
	// worker writes cannot race with fixture counters observed by the test.
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.failCount > 0 {
		r.failCount--
		return errors.New("temporary database failure")
	}
	for _, item := range items {
		if item != nil {
			r.requests += item.RequestCount
		}
	}
	return nil
}

// ListIngressRejects returns an empty successful fixture result for aggregate
// queries.
func (r *ingressRejectRepoStub) ListIngressRejects(context.Context, *OpsIngressRejectFilter) (*OpsIngressRejectList, error) {
	return &OpsIngressRejectList{}, nil
}

// TestOpsIngressRejectAggregatorUsesGlobalBucketCapacity concentrates six
// hundred dimensions in one shard before exhausting the global dimension
// budget. It checks overflow and retained cardinality after a periodic
// snapshot, independent of per-shard distribution.
func TestOpsIngressRejectAggregatorUsesGlobalBucketCapacity(t *testing.T) {
	repo := &ingressRejectRepoStub{}
	a := NewOpsIngressRejectAggregator(repo)
	a.Start()

	// Concentrate six hundred dimensions in one shard, exceeding an even per-shard
	// share while remaining below the global ordinary-dimension limit.
	inserted := 0
	for i := 0; inserted < 600; i++ {
		ip := fmt.Sprintf("target-%d", i)
		key := ingressRejectKey{reason: "invalid_api_key", routeFamily: "messages", protocol: "anthropic", clientIP: ip}
		if ingressRejectHash(key)%ingressRejectShardCount == 0 {
			a.RecordIngressReject(key.reason, key.routeFamily, key.protocol, ip, 0, 0)
			inserted++
		}
	}
	require.Equal(t, int64(600), a.Health().Cardinality)

	for i := range ingressRejectMaxEntries {
		a.RecordIngressReject("invalid_api_key", "messages", "anthropic", fmt.Sprintf("rotating-%d", i), 0, 0)
	}
	health := a.Health()
	require.Equal(t, int64(ingressRejectMaxEntries), health.Cardinality)
	require.Greater(t, health.Overflowed, uint64(0))

	a.snapshotAndEnqueue(false)
	require.Equal(t, int64(ingressRejectMaxEntries), a.Health().Cardinality, "periodic flush must retain the minute budget")
	a.Stop()
}

// TestOpsIngressRejectAggregatorConcurrentCountAndStopFlush records one
// dimension from thirty-two goroutines, then stops after they finish. It checks
// all submitted counts were written and admission is disabled after that
// serialized stop.
func TestOpsIngressRejectAggregatorConcurrentCountAndStopFlush(t *testing.T) {
	repo := &ingressRejectRepoStub{}
	a := NewOpsIngressRejectAggregator(repo)
	a.Start()

	// Use thirty-two concurrent writers as the contention fixture.
	const goroutines = 32
	// Each writer contributes two hundred requests to one dimension.
	const perGoroutine = 200
	// Join all record calls before Stop so the final-flush assertion measures
	// admitted updates rather than a startup or stop-admission race.
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perGoroutine {
				a.RecordIngressReject("invalid_api_key", "responses", "openai", "192.0.2.10", 0, 0)
			}
		})
	}
	wg.Wait()
	a.Stop()

	repo.mu.Lock()
	require.Equal(t, int64(goroutines*perGoroutine), repo.requests)
	repo.mu.Unlock()
	require.False(t, a.Health().Accepting)
	a.RecordIngressReject("invalid_api_key", "responses", "openai", "192.0.2.10", 0, 0)
}

// TestOpsIngressRejectAggregatorRetriesBoundedPendingBatch injects one failed
// write for a manually collected delta. It checks the retained batch, failure
// count, and successful second attempt with exactly one accepted request.
func TestOpsIngressRejectAggregatorRetriesBoundedPendingBatch(t *testing.T) {
	repo := &ingressRejectRepoStub{failCount: 1}
	a := NewOpsIngressRejectAggregator(repo)
	// Enable recording manually to drive snapshots and failed-write retry
	// deterministically, without starting the periodic worker that could consume
	// the injected failure.
	a.accepting.Store(true)
	a.RecordIngressReject("group_deleted", "messages", "anthropic", "192.0.2.20", 1, 2)
	a.snapshotAndEnqueue(false)
	a.flushPending()
	health := a.Health()
	require.Equal(t, 1, health.PendingBatches)
	require.Equal(t, uint64(1), health.FlushFailures)
	require.Equal(t, uint64(0), health.Dropped)

	a.flushPending()
	require.Equal(t, 0, a.Health().PendingBatches)
	a.Stop()
	repo.mu.Lock()
	require.Equal(t, int64(1), repo.requests)
	require.GreaterOrEqual(t, repo.calls, 2)
	repo.mu.Unlock()
}
