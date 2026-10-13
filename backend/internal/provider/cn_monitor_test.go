package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// cnLifecycleStore controls when platform reads begin and how they respond to
// cancellation.
type cnLifecycleStore struct {
	CNMonitorStore              // unused monitor store operations
	reads          atomic.Int32 // observed platform read entries
	// started and release coordinate test observations across goroutines.
	// The
	// first read closes started; ignore makes each read wait for the test
	// to
	// close release.
	// started signals first-read entry; release permits blocked reads to
	// finish.
	started, release chan struct{}
	ignore           bool // select cancellation-insensitive storage wait
}

// cnNoQueries leaves query methods unused in monitor startup and shutdown
// fixtures.
type cnNoQueries struct {
	CNMonitorQueries /* unused normalized query operations */
}

// cnLeaseFixture records singleton lease arguments and returns configured
// acquisition outcomes.
type cnLeaseFixture struct {
	ok  bool  // configured lease acquisition success
	err error // configured lease acquisition failure
	// key records the lease name; owner records its acquiring instance.
	key, owner string
	ttl        time.Duration // recorded lease lifetime duration
	released   bool          // release arguments matched acquisition
}

// ListByPlatform records entry and optionally blocks the monitor round. The
// first read closes started; ignore selects a release-channel wait, while the
// cancellation-aware path waits for ctx before returning no providers.
func (s *cnLifecycleStore) ListByPlatform(ctx context.Context, _ string) ([]Record, error) {
	if s.reads.Add(1) == 1 && s.started != nil {
		// Signal entry from the worker so the test begins shutdown only
		// after
		// an actual store read is in progress.
		close(s.started)
	}
	if s.ignore {
		// Ignore cancellation deliberately to reproduce storage that
		// keeps a
		// registered round unfinished until the test permits its
		// return.
		<-s.release
	} else if s.started != nil {
		// Keep the platform query in flight until shutdown cancellation
		// arrives, allowing the test to check that stopping waits for
		// return.
		<-ctx.Done()
	}
	return nil, nil
}

// TestCNMonitorStopPreventsLaterStart constructs disabled and subsequently
// stopped monitors with a read counter. It asserts construction and disabled
// startup create no loop, and stopped startup or manual rounds perform no
// reads.
func TestCNMonitorStopPreventsLaterStart(t *testing.T) {
	store := &cnLifecycleStore{}
	disabled := NewCNUsageMonitor(store, cnNoQueries{}, CNMonitorOptions{})
	require.NoError(t, disabled.StartContext(context.Background()))
	require.Nil(t, disabled.cancel, "默认关闭时不得创建后台上下文")
	monitor := NewCNUsageMonitor(store, cnNoQueries{}, CNMonitorOptions{Enabled: true, Interval: time.Millisecond})
	monitor.Stop()
	require.NoError(t, monitor.StartContext(context.Background()))
	require.Nil(t, monitor.cancel)
	require.Zero(t, store.reads.Load())
	monitor.RunOnce(context.Background())
	require.Zero(t, store.reads.Load())
}

// TestCNMonitorWaitsFirstIntervalAndStopsInflight blocks a platform read until
// monitor cancellation. It checks the first read waits for the configured
// interval, stop finishes the active round, and later starts or manual runs add
// no reads.
func TestCNMonitorWaitsFirstIntervalAndStopsInflight(t *testing.T) {
	store := &cnLifecycleStore{started: make(chan struct{})}
	monitor := NewCNUsageMonitor(store, cnNoQueries{}, CNMonitorOptions{Enabled: true, Interval: 30 * time.Millisecond})
	require.Zero(t, store.reads.Load())
	require.NoError(t, monitor.StartContext(context.Background()))
	require.NoError(t, monitor.StartContext(context.Background()))
	// Sample before the thirty-millisecond interval elapses; an entry
	// signal
	// here would expose an immediate first probe instead of delayed
	// polling.
	select {
	case <-store.started:
		t.Fatal("没有等待完整首周期")
	case <-time.After(5 * time.Millisecond):
	}
	// Wait for an actual store entry before stopping, so cancellation must
	// drain an admitted round rather than merely prevent its first start.
	waitUsageSignal(t, store.started)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, monitor.StopContext(ctx))
	require.NoError(t, monitor.StopContext(ctx))
	reads := store.reads.Load()
	require.NoError(t, monitor.StartContext(context.Background()))
	monitor.RunOnce(context.Background())
	require.Equal(t, reads, store.reads.Load())
}

// TestCNMonitorStopReportsBlockedStorage makes the store wait on a fixture
// channel while ignoring cancellation. It asserts a bounded stop reports
// unfinished work and returns that same error after the channel is released.
func TestCNMonitorStopReportsBlockedStorage(t *testing.T) {
	store := &cnLifecycleStore{started: make(chan struct{}), release: make(chan struct{}), ignore: true}
	monitor := NewCNUsageMonitor(store, cnNoQueries{}, CNMonitorOptions{Enabled: true, Interval: time.Millisecond})
	require.NoError(t, monitor.StartContext(context.Background()))
	// Confirm the cancellation-insensitive read is in flight before
	// spending
	// the short stop budget; the later release cannot erase that timeout.
	waitUsageSignal(t, store.started)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := monitor.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "CN usage monitor")
	// Release the deliberately blocked store after timeout;
	// operationActivity
	// still retains the first incomplete stop result for subsequent
	// callers.
	close(store.release)
	require.Same(t, err, monitor.StopContext(context.Background()))
}

// TryAcquireLeaderLock captures the round key, owner, and lease duration before
// returning configured acquisition values. Calls are serial in these fixtures,
// allowing assertions to inspect the captured fields after acquisition.
func (s *cnLeaseFixture) TryAcquireLeaderLock(_ context.Context, key, owner string, ttl time.Duration) (bool, error) {
	s.key = key
	s.owner = owner
	s.ttl = ttl
	return s.ok, s.err
}

// ReleaseLeaderLock records whether both arguments match the captured lease
// identity.
func (s *cnLeaseFixture) ReleaseLeaderLock(_ context.Context, key, owner string) error {
	s.released = key == s.key && owner == s.owner
	return nil
}

// TestCNMonitorLeasePreservesFallbackPolicy supplies successful, contended,
// failed, and absent leader backends. It checks contention skips advisory
// locking, errors invoke advisory locking, and successful release uses the
// expected owner, key, and lease duration.
func TestCNMonitorLeasePreservesFallbackPolicy(t *testing.T) {
	for _, mode := range []string{"success", "contended", "redis_error", "no_backend"} {
		t.Run(mode, func(t *testing.T) {
			lock := &cnLeaseFixture{ok: mode == "success"}
			if mode == "redis_error" {
				lock.err = errors.New("redis unavailable")
			}
			dbCalls := 0
			o := CNMonitorOptions{InstanceID: "fixture", RoundTimeout: time.Minute, Leader: lock, Advisory: func(context.Context, string) (func(), bool) { dbCalls++; return func() {}, true }}
			if mode == "no_backend" {
				o.Leader = nil
				o.Advisory = nil
			}
			m := NewCNUsageMonitor(nil, nil, o)
			release, ok := m.acquireLease(context.Background())
			if mode == "contended" {
				require.False(t, ok)
				require.Nil(t, release)
				require.Zero(t, dbCalls)
				return
			}
			require.True(t, ok)
			release()
			if mode == "success" {
				require.True(t, lock.released)
				require.Equal(t, "cn:usage:monitor:leader", lock.key)
				require.Equal(t, "fixture", lock.owner)
				require.Equal(t, 90*time.Second, lock.ttl)
				require.Zero(t, dbCalls)
			}
			if mode == "redis_error" {
				require.Equal(t, 1, dbCalls)
			}
		})
	}
}
