package apikey

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// lifecycleLookup holds a storage read until its test releases the barrier.
type lifecycleLookup struct {
	APIKeyRepository // unused methods require setup
	// entered announces the held lookup or claim before release lets it
	// finish. Each fixture closes entered once; the test owns closing release
	// so shutdown can wait on work that ignores its canceled context.
	entered, release chan struct{} // operation entry signal; test-owned release barrier
}

// lifecycleSubscription observes subscriber startup and cancellation through
// separate test barriers.
type lifecycleSubscription struct {
	APIKeyCache // unused cache methods require setup
	// entered announces subscription startup; cancelled announces completion
	// after ctx ends. The test reads both channels while the one subscriber
	// owns their close operations in its startup and exit sequence.
	entered, cancelled chan struct{} // subscription entry signal; subscription exit observation
}

// lifecycleOutbox holds a counted outbox claim despite worker context
// cancellation.
type lifecycleOutbox struct {
	AuthCacheInvalidationOutboxRepository // unused outbox methods require setup
	// entered announces the held lookup or claim before release lets it
	// finish. Each fixture closes entered once; the test owns closing release
	// so shutdown can wait on work that ignores its canceled context.
	entered, release chan struct{} // operation entry signal; test-owned release barrier
	claims           atomic.Int64  // counted worker claim attempts
}

// GetByKeyForAuth signals entry, waits for its barrier, then reports a missing
// Key. The fixture ignores caller cancellation, so the lifecycle test can hold
// an admitted operation beyond StopContext's wait deadline.
func (r *lifecycleLookup) GetByKeyForAuth(context.Context, string) (*APIKey, error) {
	// Signal after the caller has registered its operation, then hold it
	// despite cancellation until the test finishes inspecting shutdown resource lifetime.
	close(r.entered)
	<-r.release
	return nil, ErrAPIKeyNotFound
}

// SubscribeAuthCacheInvalidation signals startup, waits for cancellation, then
// signals subscriber exit. It invokes no invalidation callback; callers use
// each fixture for one subscription so its close operations have one owner.
func (c *lifecycleSubscription) SubscribeAuthCacheInvalidation(ctx context.Context, _ func(string)) error {
	// Retain this subscription until cancellation so the test can observe
	// whether shutdown keeps it alive while a separately held lookup drains.
	close(c.entered)
	<-ctx.Done()
	close(c.cancelled)
	return ctx.Err()
}

// TestAuthenticationStopWaitsBeforeClosingCaches holds one admitted lookup
// beyond a short shutdown wait. It checks new-work rejection, retained cache
// and subscriber lifetime before drain, repeated shutdown, final closure, and
// restart rejection.
func TestAuthenticationStopWaitsBeforeClosingCaches(t *testing.T) {
	repo := &lifecycleLookup{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(repo.release) }) }
	t.Cleanup(unblock)
	cache := &lifecycleSubscription{entered: make(chan struct{}), cancelled: make(chan struct{})}
	opts := &Options{}
	opts.APIKeyAuth.L1Size = 1024
	opts.APIKeyAuth.L1TTLSeconds = 60
	service := NewAPIKeyService(repo, nil, nil, nil, nil, cache, opts)
	// Cleanup releases the held lookup before waiting for Stop, including
	// assertion failures, so a test cannot strand the service's drain monitor.
	t.Cleanup(func() { unblock(); service.Stop() })
	require.Nil(t, service.authCacheL1.Load(), "构造不能启动 Ristretto 或订阅")
	select {
	case <-cache.entered:
		t.Fatal("构造启动了订阅")
	default:
	}
	service.Start()
	first := service.authCacheL1.Load()
	require.NotNil(t, first)
	service.Start()
	require.Same(t, first, service.authCacheL1.Load(), "重复启动不能复制缓存状态")
	require.True(t, first.Set("sentinel", true, 1))
	// Ristretto accepts writes asynchronously and can reject them. Flush this
	// sentinel before checking presence so later closure assertions have a known entry.
	first.Wait()
	_, present := first.Get("sentinel")
	require.True(t, present, "先确认哨兵实际被缓存接纳")
	select {
	case <-cache.entered:
	case <-time.After(time.Second):
		t.Fatal("订阅未启动")
	}
	result := make(chan error, 1)
	// Hold a counted authentication operation in the repository so shutdown
	// must leave cache and subscriber resources open until its barrier is released.
	go func() { _, err := service.GetByKey(context.Background(), "sk-lifecycle"); result <- err }()
	select {
	case <-repo.entered:
	case <-time.After(time.Second):
		t.Fatal("认证未进入仓储")
	}
	budget, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, service.StopContext(budget), context.DeadlineExceeded)
	_, err := service.GetByKey(context.Background(), "sk-new")
	require.ErrorIs(t, err, ErrAuthenticationStopped)
	_, ok := first.Get("sentinel")
	require.True(t, ok, "在途认证退出前不能关闭缓存")
	select {
	case <-cache.cancelled:
		t.Fatal("在途认证退出前取消了订阅")
	default:
	}
	// Release the storage barrier only after observing timed-out shutdown
	// behavior; the monitor can then drain this operation and close its resources.
	unblock()
	require.ErrorIs(t, <-result, ErrAPIKeyNotFound)
	finish, cancelFinish := context.WithTimeout(context.Background(), time.Second)
	defer cancelFinish()
	require.NoError(t, service.StopContext(finish))
	require.NoError(t, service.StopContext(finish))
	select {
	case <-cache.cancelled:
	default:
		t.Fatal("停止没有等待订阅退出")
	}
	_, ok = first.Get("sentinel")
	require.False(t, ok)
	service.Start()
	require.Same(t, first, service.authCacheL1.Load(), "停止后的实例不得重启")
	service.InvalidateAuthCacheByKey(context.Background(), "sk-after-stop")
	require.ErrorIs(t, service.TouchLastUsed(context.Background(), 1), ErrAuthenticationStopped)
}

// TestAuthenticationConcurrentStartStop runs several startup and synchronous
// shutdown callers against each fresh service. It checks the final operation
// gate stays stopped after all calls return across repeated interleavings.
func TestAuthenticationConcurrentStartStop(t *testing.T) {
	for range 10 {
		service := NewAPIKeyService(nil, nil, nil, nil, nil, nil, nil)
		var callers sync.WaitGroup
		for range 4 {
			// All callers race startup with shutdown on the same zero-cache service;
			// joining them allows the final gate state to represent completed lifecycle calls.
			callers.Go(func() { ; service.Start(); service.Stop() })
		}
		callers.Wait()
		require.True(t, service.operations.isStopping())
	}
}

// Claim counts and signals one worker read, then waits for the fixture barrier
// before returning ctx.Err. Cancellation does not release the barrier; callers
// hold the claim to exercise bounded shutdown waiting.
func (r *lifecycleOutbox) Claim(ctx context.Context, _ string, _ int, _ time.Duration) ([]AuthCacheInvalidationEvent, error) {
	r.claims.Add(1)
	// Signal after the caller has registered its operation, then hold it
	// despite cancellation until the test finishes inspecting shutdown resource lifetime.
	close(r.entered)
	<-r.release
	return nil, ctx.Err()
}

// TestOutboxStopWaitsForClaim holds a repository claim after the worker context
// is canceled by shutdown. It checks timeout, restart rejection, one claim,
// repeated completion waits and final stopped health after releasing the
// barrier.
func TestOutboxStopWaitsForClaim(t *testing.T) {
	repo := &lifecycleOutbox{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(repo.release) }) }
	t.Cleanup(unblock)
	worker := NewAuthCacheInvalidationWorker(repo, &lifecycleSubscription{})
	// Cleanup releases the held claim before waiting for worker shutdown,
	// including assertion failures, so the deliberately blocked repository can finish.
	t.Cleanup(func() { unblock(); worker.Stop() })
	require.Zero(t, repo.claims.Load())
	worker.Start()
	select {
	case <-repo.entered:
	case <-time.After(time.Second):
		t.Fatal("worker 未认领")
	}
	budget, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.True(t, errors.Is(worker.StopContext(budget), context.DeadlineExceeded))
	worker.Start()
	unblock()
	finish, cancelFinish := context.WithTimeout(context.Background(), time.Second)
	defer cancelFinish()
	require.NoError(t, worker.StopContext(finish))
	require.NoError(t, worker.StopContext(finish))
	require.Equal(t, int64(1), repo.claims.Load())
	require.False(t, worker.running.Load())
}
