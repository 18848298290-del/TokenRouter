package ws

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openAIWSFakeDialer returns successful in-memory sockets without consulting
// request parameters.
type openAIWSFakeDialer struct{}

// openAIWSFirstDialBlockingCaptureDialer captures headers/sockets while holding
// its first dial for controlled publication tests.
type openAIWSFirstDialBlockingCaptureDialer struct {
	mu        sync.Mutex // captured dial state mutex
	dialCount int        // observed dial attempt count
	// headers stores cloned request header snapshots under mu; callers inspect that
	// slice under the same lock while dials may append.
	headers []http.Header
	// connections stores shared fake pointers after successful creation; inspect
	// the slice under mu and each socket state under its own mutex.
	connections []*openAIWSFakeConn
	// firstStarted closes once when the first counted dial begins waiting, letting
	// the test change provider state before releasing that captured dial.
	firstStarted chan struct{}
	// releaseFirst is the fixture-owned first-dial gate; cleanup closes it once or
	// cancels the dial context before waiting for pool resources.
	releaseFirst chan struct{}
}

// openAIWSAlwaysFailDialer records attempts and returns a fixed transport
// failure.
type openAIWSAlwaysFailDialer struct {
	mu        sync.Mutex // captured dial state mutex
	dialCount int        // observed dial attempt count
}

// openAIWSPingBlockingConn tracks simultaneous probes until cancellation or
// test release.
type openAIWSPingBlockingConn struct {
	// current counts active probes and maxConcurrent remembers their peak across
	// all fixture sockets. Ping updates both atomics and returns current in a
	// defer after either release or cancellation completes this probe.
	current       *atomic.Int32 // shared active probe counter
	maxConcurrent *atomic.Int32 // shared peak overlapping probes
	// release ends held ping probes when closed; this stub's Close does not signal
	// it, so fixture owners release or cancel pending probes.
	release <-chan struct{}
}

// openAIWSIdlePingUnsupportedConn exposes the shared fake socket with
// reader-dependent ping capability.
type openAIWSIdlePingUnsupportedConn struct {
	// openAIWSFakeConn supplies ordinary read/write/close behavior while the outer
	// fixture overrides idle-ping capability for retirement checks.
	openAIWSFakeConn
}

// openAIWSBlockingConn delays reads under context while permitting immediate
// writes and ping.
type openAIWSBlockingConn struct {
	readDelay time.Duration // configured fake read delay
}

// openAIWSWriteBlockingConn waits for write cancellation while other operations
// return immediately.
type openAIWSWriteBlockingConn struct{}

// openAIWSPingFailConn returns fixed messages and fails every health probe.
type openAIWSPingFailConn struct{}

// openAIWSContextProbeConn retains the context supplied by a serialized write.
type openAIWSContextProbeConn struct {
	// lastWriteCtx retains the supplied write context without locking; the test
	// reads it after the connection wrapper has completed its serialized write.
	lastWriteCtx context.Context
}

// openAIWSNilConnDialer reports HTTP success while returning no transport
// object.
type openAIWSNilConnDialer struct{}

// TestPoolShrinkKeepsActiveLeaseAndRejectsUpdatesAfterClose shrinks capacity
// around one busy and one idle socket. The busy lease survives, release
// reclaims it under zero-idle policy, and later option publication fails after
// shutdown.
func TestPoolShrinkKeepsActiveLeaseAndRejectsUpdatesAfterClose(t *testing.T) {
	pool := NewWSConnPool(&WSPoolOptions{MaxConnsPerProvider: 4, MinIdlePerProvider: 0, MaxIdlePerProvider: 4})
	provider := pool.getOrCreateProviderPool(1)
	busy := NewWSConn("busy", 1, nil, nil, nil, "")
	idle := NewWSConn("idle", 1, nil, nil, nil, "")
	require.True(t, busy.tryAcquire())
	provider.conns[busy.id], provider.conns[idle.id] = busy, idle
	require.NoError(t, pool.UpdateOptions(WSPoolOptions{MaxConnsPerProvider: 1, MinIdlePerProvider: 0, MaxIdlePerProvider: 0}))
	state, ok := pool.SnapshotProviderState(1)
	require.True(t, ok)
	require.Equal(t, 1, state.LeasedConnections)
	require.Equal(t, 1, state.Connections)
	(&WSConnLease{pool: pool, ProviderID: 1, Conn: busy}).Release()
	state, _ = pool.SnapshotProviderState(1)
	require.Zero(t, state.Connections)
	pool.Close()
	require.Error(t, pool.UpdateOptions(WSPoolOptions{MaxConnsPerProvider: 2}))
}

// TestPoolConcurrentOptionsPublication runs concurrent option updates and
// hard-cap reads through four joined workers. It exercises completion of those
// operations without asserting a consistent multi-field option tuple.
func TestPoolConcurrentOptionsPublication(t *testing.T) {
	pool := NewWSConnPool(&WSPoolOptions{MaxConnsPerProvider: 4})
	defer pool.Close()
	// Join publication/read workers before deferred pool Close, keeping shutdown
	// outside the concurrent option-update interval exercised by this fixture.
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for i := range 100 {
				_ = pool.UpdateOptions(WSPoolOptions{MaxConnsPerProvider: i + 1})
				_ = pool.nativeOptions().MaxConnsHardCap()
			}
		})
	}
	workers.Wait()
}

// TestPrewarmUpdatesDuringDial blocks the first prewarm dial while changing
// idle/capacity demand and unrelated reservations. Assertions retain busy
// leases, return unused batch slots, and accept or close the late socket
// according to updated need.
func TestPrewarmUpdatesDuringDial(t *testing.T) {
	for _, tc := range []struct {
		name string // prewarm demand scenario label
		// minimum is the idle floor, maximum the idle ceiling, capacity the total
		// cap, busy the retained leased count, otherCreating another dial's reserved
		// slots, and wantIdle the accepted late idle count. Together they change
		// demand while the first captured dial remains held by the test gate.
		minimum, maximum, capacity, busy, otherCreating, wantIdle int
	}{
		{name: "zero_idle", capacity: 4},
		{name: "lower_idle", minimum: 1, maximum: 1, capacity: 4, wantIdle: 1},
		{name: "lower_target", minimum: 1, maximum: 4, capacity: 4, wantIdle: 1},
		{name: "no_idle_target", maximum: 4, capacity: 4},
		{name: "lower_capacity", minimum: 1, maximum: 1, capacity: 1, wantIdle: 1},
		{name: "busy_connections", capacity: 1, busy: 2},
		{name: "busy_with_idle_budget", maximum: 1, capacity: 4, busy: 2, wantIdle: 1},
		{name: "other_dial_reservation", capacity: 4, otherCreating: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := WSPoolOptions{MaxConnsPerProvider: 4, MinIdlePerProvider: 4, MaxIdlePerProvider: 4, DialTimeoutSeconds: 2}
			pool := NewWSConnPool(&initial)
			t.Cleanup(pool.Close)
			dialer := newOpenAIWSFirstDialBlockingCaptureDialer()
			pool.SetClientDialerForTest(dialer)
			// Normal test completion and cleanup share one release latch, so either can
			// unblock the held first dial before pool shutdown waits.
			var releaseOnce sync.Once
			releaseDial := func() { releaseOnce.Do(func() { close(dialer.releaseFirst) }) }
			t.Cleanup(releaseDial)
			req := WSAcquireRequest{Provider: &WSPoolProvider{ID: 9001, Type: "apikey"}, WSURL: "wss://example.test/responses"}
			ap := pool.getOrCreateProviderPool(9001)
			ap.lastAcquire = CloneWSAcquireRequestPtr(&req)
			var busy []*WSConnLease
			for range tc.busy {
				conn := NewWSConn(pool.nextConnID(9001), 9001, &openAIWSFakeConn{}, nil, nil, "")
				require.True(t, conn.tryAcquire())
				ap.conns[conn.id] = conn
				lease := &WSConnLease{pool: pool, ProviderID: 9001, Conn: conn}
				busy = append(busy, lease)
				t.Cleanup(lease.Release)
			}
			pool.ensureTargetIdleAsync(9001)
			select {
			case <-dialer.firstStarted:
			case <-time.After(time.Second):
				t.Fatal("预热拨号未开始")
			}
			ap.mu.Lock()
			ap.creating += tc.otherCreating
			ap.mu.Unlock()
			next := initial
			next.MinIdlePerProvider, next.MaxIdlePerProvider, next.MaxConnsPerProvider = tc.minimum, tc.maximum, tc.capacity
			require.NoError(t, pool.UpdateOptions(next))
			releaseDial()
			pool.prewarmWG.Wait()
			state, ok := pool.SnapshotProviderState(9001)
			require.True(t, ok)
			require.Equal(t, tc.wantIdle+tc.busy, state.Connections)
			require.Equal(t, tc.busy, state.LeasedConnections)
			require.Equal(t, 1, dialer.DialCount(), "需求降低后应停止本批剩余拨号")
			ap.mu.Lock()
			creating, active := ap.creating, ap.prewarmActive
			ap.creating -= tc.otherCreating
			ap.mu.Unlock()
			require.Equal(t, tc.otherCreating, creating, "应归还本批的剩余名额，保留其他请求的名额")
			require.False(t, active)
			dialer.mu.Lock()
			connections := append([]*openAIWSFakeConn(nil), dialer.connections...)
			dialer.mu.Unlock()
			require.Len(t, connections, 1)
			connections[0].mu.Lock()
			closed := connections[0].closed
			connections[0].mu.Unlock()
			require.Equal(t, tc.wantIdle == 0, closed)
			for _, lease := range busy {
				require.NoError(t, lease.WriteJSON(map[string]any{"type": "response.create"}, time.Second))
				lease.Release()
			}
			// A zero idle limit still permits on-demand dialing; release then reclaims the connection.
			if tc.maximum == 0 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				lease, err := pool.Acquire(ctx, req)
				require.NoError(t, err)
				lease.Release()
				state, _ = pool.SnapshotProviderState(9001)
				require.Zero(t, state.Connections)
			}
		})
	}
}

// BenchmarkOpenAIWSPoolAcquire warms a bounded pool before measuring parallel
// acquire/release allocations and throughput. It retries the closed-resource
// sentinel up to three times and fails unexpected acquisition outcomes, with no
// latency-budget assertion.
func BenchmarkOpenAIWSPoolAcquire(b *testing.B) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 8
	cfg.MinIdlePerProvider = 1
	cfg.MaxIdlePerProvider = 4
	cfg.QueueLimitPerConn = 256
	cfg.DialTimeoutSeconds = 1

	pool := newStartedWSConnPoolForTest(cfg)
	pool.SetClientDialerForTest(&openAIWSCountingDialer{})

	provider := &WSPoolProvider{ID: 1001, Type: "apikey"}
	req := WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
	}
	ctx := context.Background()

	lease, err := pool.Acquire(ctx, req)
	if err != nil {
		b.Fatalf("warm acquire failed: %v", err)
	}
	lease.Release()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var (
				got        *WSConnLease
				acquireErr error
			)
			// Bound the measured loop's closed-resource recovery; other errors leave
			// their selected result for benchmark failure reporting instead of consuming another retry.
			for range 3 {
				got, acquireErr = pool.Acquire(ctx, req)
				if acquireErr == nil {
					break
				}
				if !errors.Is(acquireErr, errOpenAIWSConnClosed) {
					break
				}
			}
			if acquireErr != nil {
				b.Fatalf("acquire failed: %v", acquireErr)
			}
			got.Release()
		}
	})
}

// TestWSPoolConstructionDoesNotStartWorkers constructs an unused pool and waits
// on its worker group. Startup stays false and the wait completes before the
// fixture timeout, protecting lazy periodic-worker registration.
func TestWSPoolConstructionDoesNotStartWorkers(t *testing.T) {
	pool := NewWSConnPool(nil)
	t.Cleanup(pool.Close)
	require.False(t, pool.started)
	// Observe worker-group completion through a channel so the fixture can detect
	// accidental constructor startup without starting those workers itself.
	finished := make(chan struct{})
	go func() {
		pool.workerWg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("构造期间启动了后台 worker")
	}
}

// TestWSPoolRepeatedStartAndClose calls startup and shutdown concurrently from
// several callers. The pool records one started/closed lifecycle and rejects
// subsequent acquisition with the closed-resource sentinel.
func TestWSPoolRepeatedStartAndClose(t *testing.T) {
	pool := NewWSConnPool(nil)
	// Join each caller batch before the next lifecycle phase, isolating concurrent
	// Start and concurrent Close from the final rejected-acquisition assertion.
	var callers sync.WaitGroup
	for range 16 {
		callers.Go(pool.Start)
	}
	callers.Wait()
	require.True(t, pool.started)
	for range 16 {
		callers.Go(pool.Close)
	}
	callers.Wait()
	pool.Start()
	require.True(t, pool.closed)
	_, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider: &WSPoolProvider{ID: 1, Type: "oauth", Concurrency: 1},
		WSURL:    "wss://example.invalid/responses",
	})
	require.ErrorIs(t, err, openai.ErrWSConnClosed)
}

// TestWSPoolCloseBeforeStart closes a constructed pool before periodic workers
// are launched. A later Start keeps started false and retains the published
// closed state.
func TestWSPoolCloseBeforeStart(t *testing.T) {
	pool := NewWSConnPool(nil)
	pool.Close()
	pool.Start()
	require.False(t, pool.started)
	require.True(t, pool.closed)
}

// TestWSPoolExpiredConnectionIsRetiredBeforeHandoff ages a busy socket while
// ordinary or forced continuation waits. Active work remains usable, then
// ordinary selection redials and forced selection returns unavailable after
// release.
func TestWSPoolExpiredConnectionIsRetiredBeforeHandoff(t *testing.T) {
	for _, required := range []bool{false, true} {
		name := "ordinary"
		if required {
			name = "required"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pool, req := newWSReuseTestPool(1, 1)
				defer pool.Close()
				first, err := pool.Acquire(context.Background(), req)
				require.NoError(t, err)
				defer first.Release()
				first.Conn.createdAtNano.Store(time.Now().Add(-WSConnMaxAge + time.Millisecond).UnixNano())
				if required {
					req.PreferredConnID = first.ConnID()
					req.ForcePreferredConn = true
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer func() {
					cancel()
					synctest.Wait()
				}()
				result := acquireWSInBackground(pool, ctx, req)
				synctest.Wait()
				_, waiters, _ := pool.ProviderPoolLoad(req.Provider.ID)
				require.Equal(t, 1, waiters)
				// Advance the synthetic clock past expiry while the existing lease remains
				// active, then release it to test the queued handoff decision.
				time.Sleep(2 * time.Millisecond)
				// An active lease can finish its work even after the connection reaches its age limit.
				require.NoError(t, first.WriteJSONContext(ctx, map[string]string{"type": "response.create"}))
				first.Release()
				synctest.Wait()
				got := <-result
				if got.lease != nil {
					defer got.lease.Release()
				}
				if required {
					require.ErrorIs(t, got.err, openai.ErrOpenAIWSPreferredConnUnavailable)
				} else {
					require.NoError(t, got.err)
					require.NotEqual(t, first.ConnID(), got.lease.ConnID())
				}
			})
		})
	}
}

// TestWSPoolChecksAgeBetweenCleanupSweeps crosses the socket lifetime limit
// immediately after release. The next acquisition replaces its ID before
// periodic cleanup could supply that retirement.
func TestWSPoolChecksAgeBetweenCleanupSweeps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, req := newWSReuseTestPool(1, 1)
		defer pool.Close()
		first, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		first.Conn.createdAtNano.Store(time.Now().Add(-WSConnMaxAge + time.Millisecond).UnixNano())
		first.Release()
		// Cross the lifetime threshold before another acquire while staying short of
		// the foreground cleanup interval, exposing its per-selection age check.
		time.Sleep(time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		second, err := pool.Acquire(ctx, req)
		require.NoError(t, err)
		defer second.Release()
		require.NotEqual(t, first.ConnID(), second.ConnID())
	})
}

// TestWSPoolCanceledAcquireDoesNotWaitForClose cancels a waiting acquisition
// while idle socket closure blocks. Cancellation returns its slot promptly,
// background close begins, and pool shutdown waits until that close is
// released.
func TestWSPoolCanceledAcquireDoesNotWaitForClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, req := newWSReuseTestPool(1, 0)
		pool.Start()
		defer pool.Close()
		slow := &delayedCloseConn{entered: make(chan struct{}), release: make(chan struct{})}
		// Normal completion and deferred cleanup can both unblock the delayed transport
		// close; one latch prevents duplicate gate closure during either outcome.
		var release sync.Once
		unblock := func() { release.Do(func() { close(slow.release) }) }
		defer unblock()
		conn := NewWSConn("cancel-close", req.Provider.ID, slow, nil, nil, "")
		conn.compatibility = wsCompatibilityForRequest(req)
		require.True(t, conn.tryAcquire())
		ap := pool.getOrCreateProviderPool(req.Provider.ID)
		ap.conns[conn.id] = conn
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := acquireWSInBackground(pool, ctx, req)
		synctest.Wait()
		ap.mu.Lock()
		conn.release()
		cancel()
		ap.mu.Unlock()
		synctest.Wait()
		select {
		case got := <-result:
			require.ErrorIs(t, got.err, context.Canceled)
		default:
			t.Fatal("取消返回被关闭握手阻塞")
		}
		select {
		case <-slow.entered:
		default:
			t.Fatal("后台清理没有开始关闭空闲连接")
		}
		_, waiters, conns := pool.ProviderPoolLoad(req.Provider.ID)
		require.Zero(t, waiters)
		require.Zero(t, conns)
		// Shutdown joins cleanup; the canceled acquisition can return before cleanup finishes.
		stopped := make(chan struct{})
		go func() {
			pool.Close()
			close(stopped)
		}()
		synctest.Wait()
		select {
		case <-stopped:
			t.Fatal("池在后台清理结束前完成了关闭")
		default:
		}
		unblock()
		synctest.Wait()
		<-stopped
	})
}

// TestWSPoolCanceledDeliveryClosesInBackground gives delivery an already
// canceled context and a selected token. Delivery returns nil lease/canceled
// error while background cleanup starts the blocked idle close.
func TestWSPoolCanceledDeliveryClosesInBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, req := newWSReuseTestPool(1, 0)
		pool.Start()
		defer pool.Close()
		slow := &delayedCloseConn{entered: make(chan struct{}), release: make(chan struct{})}
		// Keep delayed close blocked through delivery assertions, then release it
		// before deferred pool Close waits for the background cleanup worker.
		defer close(slow.release)
		conn := NewWSConn("undelivered", req.Provider.ID, slow, nil, nil, "")
		conn.compatibility = wsCompatibilityForRequest(req)
		require.True(t, conn.tryAcquire())
		ap := pool.getOrCreateProviderPool(req.Provider.ID)
		ap.conns[conn.id] = conn
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result := make(chan wsAcquireTestResult, 1)
		go func() {
			lease, err := pool.deliverLease(ctx, req, 0, conn, true, 0)
			result <- wsAcquireTestResult{lease: lease, err: err}
		}()
		synctest.Wait()
		select {
		case got := <-result:
			require.Nil(t, got.lease)
			require.ErrorIs(t, got.err, context.Canceled)
		default:
			t.Fatal("取消交付时同步等待了关闭握手")
		}
		select {
		case <-slow.entered:
		default:
			t.Fatal("未交付的连接没有进入后台回收")
		}
	})
}

// TestWSPoolShutdownCloseDoesNotBlockWaiterExit blocks an idle close while a
// forced continuation waits. The waiter receives the closed-resource error
// before the fixture permits pool shutdown to finish.
func TestWSPoolShutdownCloseDoesNotBlockWaiterExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, req := newWSReuseTestPool(2, 2)
		defer pool.Close()
		first, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		defer first.Release()
		slow := &delayedCloseConn{entered: make(chan struct{}), release: make(chan struct{})}
		// Hold shutdown close until the waiting request exits, and share one gate latch
		// between ordinary completion and fixture cleanup.
		var release sync.Once
		unblock := func() { release.Do(func() { close(slow.release) }) }
		defer unblock()
		idle := NewWSConn("shutdown-idle", req.Provider.ID, slow, nil, nil, "")
		idle.compatibility = wsCompatibilityForRequest(req)
		ap := pool.getOrCreateProviderPool(req.Provider.ID)
		ap.conns[idle.id] = idle
		req.PreferredConnID = first.ConnID()
		req.ForcePreferredConn = true
		result := acquireWSInBackground(pool, context.Background(), req)
		synctest.Wait()
		stopped := make(chan struct{})
		go func() {
			pool.Close()
			close(stopped)
		}()
		synctest.Wait()
		select {
		case <-slow.entered:
		default:
			t.Fatal("池关闭没有开始回收空闲连接")
		}
		select {
		case got := <-result:
			require.ErrorIs(t, got.err, errOpenAIWSConnClosed)
		default:
			t.Fatal("等待者退出被池关闭握手阻塞")
		}
		unblock()
		synctest.Wait()
		<-stopped
	})
}

// TestWSPoolHandoffPrecedesIdleClose releases two sockets together for an
// ordinary or required waiter under zero-idle retention. The selected lease is
// delivered and usable while another idle handshake closes in the background.
func TestWSPoolHandoffPrecedesIdleClose(t *testing.T) {
	for _, required := range []bool{false, true} {
		name := "ordinary"
		if required {
			name = "required"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pool, req := newWSReuseTestPool(2, 0)
				pool.Start()
				defer pool.Close()
				slow := &delayedCloseConn{entered: make(chan struct{}), release: make(chan struct{})}
				// Keep one idle handshake blocked while delivery is checked; normal and
				// deferred completion share one release action before pool shutdown joins
				// cleanup.
				unblock := sync.OnceFunc(func() { close(slow.release) })
				defer unblock()
				first := NewWSConn("available", req.Provider.ID, &openAIWSFakeConn{}, nil, nil, "")
				second := NewWSConn("slow-idle", req.Provider.ID, slow, nil, nil, "")
				first.compatibility = wsCompatibilityForRequest(req)
				second.compatibility = first.compatibility
				require.True(t, first.tryAcquire())
				require.True(t, second.tryAcquire())
				ap := pool.getOrCreateProviderPool(req.Provider.ID)
				ap.conns[first.id] = first
				ap.conns[second.id] = second
				if required {
					req.PreferredConnID = first.id
					req.ForcePreferredConn = true
				}
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				result := acquireWSInBackground(pool, ctx, req)
				synctest.Wait()
				// Release both leases under the pool lock so the waiter sees both available
				// at its next wakeup.
				ap.mu.Lock()
				first.release()
				second.release()
				first.lastUsedNano.Store(time.Now().Add(-time.Millisecond).UnixNano())
				ap.signalChangedLocked()
				ap.mu.Unlock()
				synctest.Wait()
				var lease *WSConnLease
				select {
				case got := <-result:
					require.NoError(t, got.err)
					lease = got.lease
					defer lease.Release()
					require.Equal(t, first.id, lease.ConnID())
				default:
					t.Fatal("租约交付被另一条空闲连接的关闭握手阻塞")
				}
				require.NoError(t, ctx.Err())
				select {
				case <-slow.entered:
				default:
					t.Fatal("后台任务没有回收多余空闲连接")
				}
				require.NoError(t, lease.WriteJSONContext(ctx, map[string]string{"type": "response.create"}))
				state, _ := pool.SnapshotProviderState(req.Provider.ID)
				require.Equal(t, 1, state.Connections)
				require.Equal(t, 1, state.LeasedConnections)
				unblock()
				synctest.Wait()
				lease.Release()
				state, _ = pool.SnapshotProviderState(req.Provider.ID)
				require.Zero(t, state.Connections)
			})
		})
	}
}

// TestWSPoolUnchangedDestinationReusesConnection adds surrounding whitespace to
// URL/proxy after a first acquisition. Trimming keeps the same socket ID
// eligible and the second lease reports reuse.
func TestWSPoolUnchangedDestinationReusesConnection(t *testing.T) {
	pool, req := newWSReuseTestPool(1, 1)
	defer pool.Close()
	req.ProxyURL = "http://proxy.example:8080"
	first, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	first.Release()
	req.WSURL = " " + req.WSURL + " "
	req.ProxyURL = " " + req.ProxyURL + " "
	second, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	defer second.Release()
	require.Equal(t, first.ConnID(), second.ConnID())
	require.True(t, second.Reused())
}

// TestWSPoolDestinationCompatibility changes destination or proxy under
// ordinary, preferred, and required selection. Ordinary/preferred requests
// create another socket, while required continuation rejects its incompatible
// prior ID.
func TestWSPoolDestinationCompatibility(t *testing.T) {
	for _, destination := range []string{"url", "proxy"} {
		for _, preference := range []string{"ordinary", "preferred", "required"} {
			t.Run(destination+"/"+preference, func(t *testing.T) {
				pool, req := newWSReuseTestPool(1, 1)
				defer pool.Close()
				first, err := pool.Acquire(context.Background(), req)
				require.NoError(t, err)
				first.Release()
				if destination == "url" {
					req.WSURL = "wss://new.example/v1/responses"
				} else {
					req.ProxyURL = "http://proxy.example:8080"
				}
				if preference != "ordinary" {
					req.PreferredConnID = first.ConnID()
				}
				req.ForcePreferredConn = preference == "required"
				second, err := pool.Acquire(context.Background(), req)
				if second != nil {
					defer second.Release()
				}
				if req.ForcePreferredConn {
					require.ErrorIs(t, err, openai.ErrOpenAIWSPreferredConnUnavailable)
					return
				}
				require.NoError(t, err)
				require.NotEqual(t, first.ConnID(), second.ConnID())
				require.False(t, second.Reused())
			})
		}
	}
}

// TestWSPoolDestinationChangeKeepsActiveLease acquires a second destination
// while the first lease remains busy. The new socket ID differs and the earlier
// lease still accepts a context-bound write.
func TestWSPoolDestinationChangeKeepsActiveLease(t *testing.T) {
	pool, req := newWSReuseTestPool(2, 2)
	defer pool.Close()
	first, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	defer first.Release()
	req.WSURL = "wss://new.example/v1/responses"
	second, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	defer second.Release()
	require.NotEqual(t, first.ConnID(), second.ConnID())
	require.NoError(t, first.WriteJSONContext(context.Background(), map[string]string{"type": "response.create"}))
}

// TestWSPoolPrewarmDropsChangedDestination holds old-target prewarming while
// acquiring a new URL/proxy target. The late socket closes, the selected new
// socket stays open, and only that connection remains recorded.
func TestWSPoolPrewarmDropsChangedDestination(t *testing.T) {
	for _, destination := range []string{"url", "proxy"} {
		t.Run(destination, func(t *testing.T) {
			pool, req := newWSReuseTestPool(2, 2)
			defer pool.Close()
			dialer := newOpenAIWSFirstDialBlockingCaptureDialer()
			pool.SetClientDialerForTest(dialer)
			// Gate the old-target first dial across the new acquire, with idempotent
			// release available during cleanup so a failed assertion cannot leave prewarm
			// held.
			var release sync.Once
			unblock := func() { release.Do(func() { close(dialer.releaseFirst) }) }
			defer unblock()
			ap := pool.getOrCreateProviderPool(req.Provider.ID)
			ap.lastAcquire = CloneWSAcquireRequestPtr(&req)
			options := *pool.nativeOptions()
			options.MinIdlePerProvider = 1
			require.NoError(t, pool.UpdateOptions(options))
			<-dialer.firstStarted
			if destination == "url" {
				req.WSURL = "wss://new.example/v1/responses"
			} else {
				req.ProxyURL = "http://proxy.example:8080"
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			lease, err := pool.Acquire(ctx, req)
			require.NoError(t, err)
			defer lease.Release()
			unblock()
			pool.prewarmWG.Wait()
			state, _ := pool.SnapshotProviderState(req.Provider.ID)
			require.Equal(t, 1, state.Connections)
			dialer.mu.Lock()
			connections := append([]*openAIWSFakeConn(nil), dialer.connections...)
			dialer.mu.Unlock()
			require.Len(t, connections, 2)
			for _, conn := range connections {
				conn.mu.Lock()
				closed := conn.closed
				conn.mu.Unlock()
				require.Equal(t, conn != lease.Conn.ws, closed)
			}
		})
	}
}

// TestWSPoolWaiterUsesAnyCompatibleConnection queues behind two busy compatible
// sockets. Releasing the newer socket wakes and supplies the waiter even though
// the other socket has an older last-used timestamp.
func TestWSPoolWaiterUsesAnyCompatibleConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, req := newWSReuseTestPool(2, 2)
		defer pool.Close()
		first, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		defer first.Release()
		second, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		defer second.Release()
		first.Conn.lastUsedNano.Store(time.Now().Add(-time.Second).UnixNano())
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer func() {
			cancel()
			synctest.Wait()
		}()
		result := acquireWSInBackground(pool, ctx, req)
		synctest.Wait()
		second.Release()
		synctest.Wait()
		select {
		case got := <-result:
			require.NoError(t, got.err)
			defer got.lease.Release()
			require.Equal(t, second.ConnID(), got.lease.ConnID())
		default:
			t.Fatal("兼容连接已归还，等待者仍未取得租约")
		}
	})
}

// TestWSPoolIdleLimitExcludesLeasedConnections creates twenty-two leases and
// releases two under a larger idle budget. Twenty busy leases remain counted,
// and both idle sockets stay recorded alongside them.
func TestWSPoolIdleLimitExcludesLeasedConnections(t *testing.T) {
	pool, req := newWSReuseTestPool(128, 12)
	defer pool.Close()
	leases := make([]*WSConnLease, 0, 22)
	for range 22 {
		lease, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		defer lease.Release()
		leases = append(leases, lease)
	}
	leases[20].Release()
	leases[21].Release()
	state, ok := pool.SnapshotProviderState(req.Provider.ID)
	require.True(t, ok)
	require.Equal(t, 20, state.LeasedConnections)
	require.Equal(t, 22, state.Connections)
}

// TestWSPoolShrinkRespectsBusyAndIdleLimits updates total/idle limits around
// one busy socket and ordered idle entries. Assertions retain active work,
// close the oldest reclaimable socket, and reach the final idle limit after
// release.
func TestWSPoolShrinkRespectsBusyAndIdleLimits(t *testing.T) {
	for _, tc := range []struct {
		name string // idle shrinking scenario label
		// capacity is the total limit, idle the retained idle limit, and want
		// the expected map count before releasing busy work. Their shared fixture
		// keeps one busy socket while eligible idle entries are ordered for removal.
		capacity, idle, want int
	}{
		{name: "idle_limit", capacity: 4, idle: 2, want: 3},
		{name: "capacity_limit", capacity: 2, idle: 2, want: 2},
		{name: "zero_idle", capacity: 4, idle: 0, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, req := newWSReuseTestPool(4, 4)
			defer pool.Close()
			leases := make([]*WSConnLease, 0, 4)
			for range 4 {
				lease, err := pool.Acquire(context.Background(), req)
				require.NoError(t, err)
				defer lease.Release()
				leases = append(leases, lease)
			}
			for i := 1; i < len(leases); i++ {
				leases[i].Release()
				leases[i].Conn.lastUsedNano.Store(time.Now().Add(time.Duration(i-4) * time.Second).UnixNano())
			}
			options := *pool.nativeOptions()
			options.MaxConnsPerProvider = tc.capacity
			options.MaxIdlePerProvider = tc.idle
			require.NoError(t, pool.UpdateOptions(options))
			state, _ := pool.SnapshotProviderState(req.Provider.ID)
			require.Equal(t, tc.want, state.Connections)
			require.Equal(t, 1, state.LeasedConnections)
			require.NoError(t, leases[0].WriteJSONContext(context.Background(), map[string]string{"type": "response.create"}))
			// Reclaim the least recently used idle connection first.
			select {
			case <-leases[1].Conn.closedCh:
			default:
				t.Fatal("最久未使用的空闲连接仍在池中")
			}
			leases[0].Release()
			state, _ = pool.SnapshotProviderState(req.Provider.ID)
			require.Equal(t, min(tc.idle, tc.capacity), state.Connections)
		})
	}
}

// TestWSPoolZeroIdleHandsConnectionToWaiter queues a request behind one busy
// socket with zero idle retention. Release hands that same ID to the waiter,
// then the final release removes its record.
func TestWSPoolZeroIdleHandsConnectionToWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, req := newWSReuseTestPool(1, 0)
		defer pool.Close()
		first, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		defer first.Release()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer func() {
			cancel()
			synctest.Wait()
		}()
		result := acquireWSInBackground(pool, ctx, req)
		synctest.Wait()
		first.Release()
		synctest.Wait()
		got := <-result
		require.NoError(t, got.err)
		require.Equal(t, first.ConnID(), got.lease.ConnID())
		got.lease.Release()
		state, _ := pool.SnapshotProviderState(req.Provider.ID)
		require.Zero(t, state.Connections)
	})
}

// TestOpenAIWSConnPool_CleanupStaleAndTrimIdle supplies an over-age socket and
// two idle timestamps. Cleanup removes stale and oldest excess entries while
// retaining the newer idle socket under the configured ceiling.
func TestOpenAIWSConnPool_CleanupStaleAndTrimIdle(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxIdlePerProvider = 1
	pool := newStartedWSConnPoolForTest(cfg)

	providerID := int64(10)
	ap := pool.getOrCreateProviderPool(providerID)

	stale := NewWSConn("stale", providerID, nil, nil, nil, "")
	stale.createdAtNano.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	stale.lastUsedNano.Store(time.Now().Add(-2 * time.Hour).UnixNano())

	idleOld := NewWSConn("idle_old", providerID, nil, nil, nil, "")
	idleOld.lastUsedNano.Store(time.Now().Add(-10 * time.Minute).UnixNano())

	idleNew := NewWSConn("idle_new", providerID, nil, nil, nil, "")
	idleNew.lastUsedNano.Store(time.Now().Add(-1 * time.Minute).UnixNano())

	ap.conns[stale.id] = stale
	ap.conns[idleOld.id] = idleOld
	ap.conns[idleNew.id] = idleNew

	evicted := pool.cleanupProviderLocked(ap, time.Now(), pool.maxConnsHardCap())
	closeOpenAIWSConns(evicted)

	require.Nil(t, ap.conns["stale"], "stale connection should be rotated")
	require.Nil(t, ap.conns["idle_old"], "old idle should be trimmed by max_idle")
	require.NotNil(t, ap.conns["idle_new"], "newer idle should be kept")
}

// TestOpenAIWSConnPool_NextConnIDFormat generates two IDs for one provider in a
// fresh pool. Their prefix, exact first sequences, and distinct values protect
// pool-local monotonically increasing naming.
func TestOpenAIWSConnPool_NextConnIDFormat(t *testing.T) {
	pool := newStartedWSConnPoolForTest(&WSPoolOptions{})
	id1 := pool.nextConnID(42)
	id2 := pool.nextConnID(42)

	require.True(t, strings.HasPrefix(id1, "oa_ws_42_"))
	require.True(t, strings.HasPrefix(id2, "oa_ws_42_"))
	require.NotEqual(t, id1, id2)
	require.Equal(t, "oa_ws_42_1", id1)
	require.Equal(t, "oa_ws_42_2", id2)
}

// TestOpenAIWSConnPool_AcquireCleanupInterval reads the fixed foreground
// cleanup interval. It asserts three seconds and a shorter interval than the
// periodic background sweep ticker.
func TestOpenAIWSConnPool_AcquireCleanupInterval(t *testing.T) {
	require.Equal(t, 3*time.Second, openAIWSAcquireCleanupInterval)
	require.Less(t, openAIWSAcquireCleanupInterval, openAIWSBackgroundSweepTicker)
}

// TestNormalizeOpenAIWSRoutingAffinityPrefersCanonicalAndSortsVariants supplies
// blank/nonblank canonical values and an uppercase variant. The canonical
// nonblank value wins, and deleting that key permits the variant value.
func TestNormalizeOpenAIWSRoutingAffinityPrefersCanonicalAndSortsVariants(t *testing.T) {
	headers := http.Header{
		"X-CODEX-ROUTING-HINT": []string{" variant-uppercase "},
		"X-Codex-Routing-Hint": []string{" ", " canonical "},
	}

	require.Equal(t, "canonical", normalizeOpenAIWSRoutingAffinity(headers))

	delete(headers, "X-Codex-Routing-Hint")
	require.Equal(t, "variant-uppercase", normalizeOpenAIWSRoutingAffinity(headers))
}

// TestSameOpenAIWSPrewarmTargetKeepsRoutingHintSoftAndTLSHard changes routing,
// TLS key, and beta values independently. Target equality survives the hint
// change and fails for each hard transport identity change.
func TestSameOpenAIWSPrewarmTargetKeepsRoutingHintSoftAndTLSHard(t *testing.T) {
	baseHeaders := http.Header{
		"X-Codex-Beta-Features":      {"responses_websockets_v2"},
		openAICodexRoutingHintHeader: {"model=gpt-5.6-codex"},
	}
	base := WSAcquireRequest{
		WSURL:         "wss://example.com/v1/responses",
		ProxyURL:      "http://proxy.example.com",
		Headers:       baseHeaders,
		TLSProfileKey: "tls-profile-a",
	}

	hintChanged := CloneWSAcquireRequest(base)
	hintChanged.Headers.Set(openAICodexRoutingHintHeader, "model=gpt-5.6-codex;tier=priority")
	require.True(t, sameOpenAIWSPrewarmTarget(base, hintChanged), "路由提示变化不应使预热拨号失效")

	tlsChanged := CloneWSAcquireRequest(base)
	tlsChanged.TLSProfileKey = "tls-profile-b"
	require.False(t, sameOpenAIWSPrewarmTarget(base, tlsChanged), "TLS 指纹变化必须使预热拨号失效")

	betaChanged := CloneWSAcquireRequest(base)
	betaChanged.Headers.Set("X-Codex-Beta-Features", "remote_compaction_v2")
	require.False(t, sameOpenAIWSPrewarmTarget(base, betaChanged), "beta feature 变化必须使预热拨号失效")
}

// TestOpenAIWSConnLease_WriteJSONAndGuards writes through a retained fake
// connection with nonpositive timeout. Nil leases and missing connections
// reject contextual writes through the closed-resource sentinel.
func TestOpenAIWSConnLease_WriteJSONAndGuards(t *testing.T) {
	conn := NewWSConn("lease_write", 1, &openAIWSFakeConn{}, nil, nil, "")
	lease := &WSConnLease{Conn: conn}
	require.NoError(t, lease.WriteJSON(map[string]any{"type": "response.create"}, 0))

	var nilLease *WSConnLease
	err := nilLease.WriteJSONWithContextTimeout(context.Background(), map[string]any{"type": "response.create"}, time.Second)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)

	err = (&WSConnLease{}).WriteJSONWithContextTimeout(context.Background(), map[string]any{"type": "response.create"}, time.Second)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
}

// TestOpenAIWSConn_WriteJSONWithTimeout_NilParentContextUsesBackground
// supplies context.Background to the write helper. Its assertions check
// successful output and a nonnil context reaching the probe transport.
func TestOpenAIWSConn_WriteJSONWithTimeout_NilParentContextUsesBackground(t *testing.T) {
	probe := &openAIWSContextProbeConn{}
	conn := NewWSConn("ctx_probe", 1, probe, nil, nil, "")
	require.NoError(t, conn.writeJSONWithTimeout(context.Background(), map[string]any{"type": "response.create"}, 0))
	require.NotNil(t, probe.lastWriteCtx)
}

// TestOpenAIWSConnPool_TargetConnCountAdaptive combines two occupied tokens and
// two forced waiters under half utilization. The target reaches the cap, then
// returns to the idle floor after those observations are cleared.
func TestOpenAIWSConnPool_TargetConnCountAdaptive(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 6
	cfg.MinIdlePerProvider = 1
	cfg.PoolTargetUtilization = 0.5

	pool := newStartedWSConnPoolForTest(cfg)
	ap := pool.getOrCreateProviderPool(88)

	conn1 := NewWSConn("c1", 88, nil, nil, nil, "")
	conn2 := NewWSConn("c2", 88, nil, nil, nil, "")
	require.True(t, conn1.tryAcquire())
	require.True(t, conn2.tryAcquire())
	conn1.waiters.Store(1)
	conn2.waiters.Store(1)

	ap.conns[conn1.id] = conn1
	ap.conns[conn2.id] = conn2

	target := pool.targetConnCountLocked(ap, pool.maxConnsHardCap())
	require.Equal(t, 6, target, "应按 inflight+waiters 与 target_utilization 自适应扩容到上限")

	conn1.release()
	conn2.release()
	conn1.waiters.Store(0)
	conn2.waiters.Store(0)
	target = pool.targetConnCountLocked(ap, pool.maxConnsHardCap())
	require.Equal(t, 1, target, "低负载时应缩回到最小空闲连接")
}

// TestOpenAIWSConnPool_TargetConnCountMinIdleZero uses an idle floor of zero
// with no recorded demand. Target calculation returns zero, protecting the
// configured empty-pool case.
func TestOpenAIWSConnPool_TargetConnCountMinIdleZero(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 4
	cfg.MinIdlePerProvider = 0
	cfg.PoolTargetUtilization = 0.8

	pool := newStartedWSConnPoolForTest(cfg)
	ap := pool.getOrCreateProviderPool(66)

	target := pool.targetConnCountLocked(ap, pool.maxConnsHardCap())
	require.Equal(t, 0, target, "min_idle=0 且无负载时应允许缩容到 0")
}

// TestOpenAIWSConnPool_EnsureTargetIdleAsync installs a retained provider
// request and an idle target of two. The pool eventually records both sockets
// and counts at least two reserved scale-up slots.
func TestOpenAIWSConnPool_EnsureTargetIdleAsync(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 4
	cfg.MinIdlePerProvider = 2
	cfg.MaxIdlePerProvider = cfg.MinIdlePerProvider
	cfg.PoolTargetUtilization = 0.8
	cfg.DialTimeoutSeconds = 1

	pool := newStartedWSConnPoolForTest(cfg)
	pool.SetClientDialerForTest(&openAIWSFakeDialer{})

	providerID := int64(77)
	provider := &WSPoolProvider{ID: providerID, Type: "apikey"}
	ap := pool.getOrCreateProviderPool(providerID)
	ap.mu.Lock()
	ap.lastAcquire = &WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
	}
	ap.mu.Unlock()

	pool.ensureTargetIdleAsync(providerID)

	require.Eventually(t, func() bool {
		ap, ok := pool.getProviderPool(providerID)
		if !ok || ap == nil {
			return false
		}
		ap.mu.Lock()
		defer ap.mu.Unlock()
		return len(ap.conns) >= 2
	}, 2*time.Second, 20*time.Millisecond)

	metrics := pool.SnapshotMetrics()
	require.GreaterOrEqual(t, metrics.ScaleUpTotal, int64(2))
}

// TestOpenAIWSConnPool_EnsureTargetIdleAsyncCooldown finishes a first batch,
// removes one socket, and probes the cooldown window. Dial count stays fixed
// before expiry and rises after a later eligible request.
func TestOpenAIWSConnPool_EnsureTargetIdleAsyncCooldown(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 4
	cfg.MinIdlePerProvider = 2
	cfg.MaxIdlePerProvider = cfg.MinIdlePerProvider
	cfg.PoolTargetUtilization = 0.8
	cfg.DialTimeoutSeconds = 1
	cfg.PrewarmCooldownMS = 500

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)

	providerID := int64(178)
	provider := &WSPoolProvider{ID: providerID, Type: "apikey"}
	ap := pool.getOrCreateProviderPool(providerID)
	ap.mu.Lock()
	ap.lastAcquire = &WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
	}
	ap.mu.Unlock()

	pool.ensureTargetIdleAsync(providerID)
	require.Eventually(t, func() bool {
		ap, ok := pool.getProviderPool(providerID)
		if !ok || ap == nil {
			return false
		}
		ap.mu.Lock()
		defer ap.mu.Unlock()
		return len(ap.conns) >= 2 && !ap.prewarmActive
	}, 2*time.Second, 20*time.Millisecond)
	firstDialCount := dialer.DialCount()
	require.GreaterOrEqual(t, firstDialCount, 2)

	// Remove an idle entry after the first batch settles, producing new
	// demand while retaining its cooldown timestamp for the next admission check.
	ap, ok := pool.getProviderPool(providerID)
	require.True(t, ok)
	require.NotNil(t, ap)
	ap.mu.Lock()
	for id := range ap.conns {
		delete(ap.conns, id)
		break
	}
	ap.mu.Unlock()

	pool.ensureTargetIdleAsync(providerID)
	time.Sleep(120 * time.Millisecond)
	require.Equal(t, firstDialCount, dialer.DialCount(), "cooldown 窗口内不应再次触发预热")

	time.Sleep(450 * time.Millisecond)
	pool.ensureTargetIdleAsync(providerID)
	require.Eventually(t, func() bool {
		return dialer.DialCount() > firstDialCount
	}, 2*time.Second, 20*time.Millisecond)
}

// TestOpenAIWSConnPool_EnsureTargetIdleAsyncFailureSuppress uses an
// always-failing dialer for two one-slot batches. The next admission attempt
// stays suppressed and the observed dial count remains two.
func TestOpenAIWSConnPool_EnsureTargetIdleAsyncFailureSuppress(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MinIdlePerProvider = 1
	cfg.MaxIdlePerProvider = cfg.MinIdlePerProvider
	cfg.PoolTargetUtilization = 0.8
	cfg.DialTimeoutSeconds = 1
	cfg.PrewarmCooldownMS = 0

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := &openAIWSAlwaysFailDialer{}
	pool.SetClientDialerForTest(dialer)

	providerID := int64(279)
	provider := &WSPoolProvider{ID: providerID, Type: "apikey"}
	ap := pool.getOrCreateProviderPool(providerID)
	ap.mu.Lock()
	ap.lastAcquire = &WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
	}
	ap.mu.Unlock()

	pool.ensureTargetIdleAsync(providerID)
	require.Eventually(t, func() bool {
		ap, ok := pool.getProviderPool(providerID)
		if !ok || ap == nil {
			return false
		}
		ap.mu.Lock()
		defer ap.mu.Unlock()
		return !ap.prewarmActive
	}, 2*time.Second, 20*time.Millisecond)

	pool.ensureTargetIdleAsync(providerID)
	require.Eventually(t, func() bool {
		ap, ok := pool.getProviderPool(providerID)
		if !ok || ap == nil {
			return false
		}
		ap.mu.Lock()
		defer ap.mu.Unlock()
		return !ap.prewarmActive
	}, 2*time.Second, 20*time.Millisecond)
	require.Equal(t, 2, dialer.DialCount())

	// Two completed failed batches leave recent suppression evidence; another
	// admission call must not increase the dial count while that failure state remains current.
	pool.ensureTargetIdleAsync(providerID)
	time.Sleep(120 * time.Millisecond)
	require.Equal(t, 2, dialer.DialCount())
}

// TestOpenAIWSConnPool_AcquireQueueWaitMetrics releases a busy socket after an
// injected queue delay. Reuse reports measurable waiting, and lifetime
// queue-entry/time plus pick counters record that acquisition.
func TestOpenAIWSConnPool_AcquireQueueWaitMetrics(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 1
	cfg.MinIdlePerProvider = 0
	cfg.QueueLimitPerConn = 4

	pool := newStartedWSConnPoolForTest(cfg)
	providerID := int64(99)
	provider := &WSPoolProvider{ID: providerID, Type: "apikey"}
	conn := NewWSConn("busy", providerID, &openAIWSFakeConn{}, nil, nil, "")
	conn.compatibility = wsCompatibilityForRequest(WSAcquireRequest{Provider: provider, WSURL: "wss://example.com/v1/responses"})
	require.True(t, conn.tryAcquire()) // Occupy the connection so the next request queues.

	ap := pool.getOrCreateProviderPool(providerID)
	ap.mu.Lock()
	ap.conns[conn.id] = conn
	ap.lastAcquire = &WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
	}
	ap.mu.Unlock()

	// Delay token return to create actual queue residence, then let the acquisition
	// measure that wait independently of ordinary pick and dial work.
	go func() {
		time.Sleep(60 * time.Millisecond)
		(&WSConnLease{pool: pool, ProviderID: provider.ID, Conn: conn}).Release()
	}()

	lease, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
	})
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.True(t, lease.Reused())
	require.GreaterOrEqual(t, lease.QueueWaitDuration(), 50*time.Millisecond)
	lease.Release()

	metrics := pool.SnapshotMetrics()
	require.GreaterOrEqual(t, metrics.AcquireQueueWaitTotal, int64(1))
	require.Greater(t, metrics.AcquireQueueWaitMsTotal, int64(0))
	require.GreaterOrEqual(t, metrics.ConnPickTotal, int64(1))
}

// TestOpenAIWSConnPool_DialSuccessWakesTopologyWaiterAndCanceledWaiterDoesNotLoseLease
// queues during a held dial, then cancels the waiter. Its reservation exits,
// the successful prewarm target stays unchanged, and a later acquire receives
// the released token.
func TestOpenAIWSConnPool_DialSuccessWakesTopologyWaiterAndCanceledWaiterDoesNotLoseLease(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 1
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 1
	cfg.QueueLimitPerConn = 4

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := newOpenAIWSFirstDialBlockingCaptureDialer()
	pool.SetClientDialerForTest(dialer)
	provider := &WSPoolProvider{ID: 991, Type: "oauth"}
	req := WSAcquireRequest{Provider: provider, WSURL: "wss://example.com/v1/responses"}

	// result transfers one background acquisition's lease/error outcome to its
	// controlled handoff fixture without sharing result mutation after delivery.
	type result struct {
		lease *WSConnLease // background acquired connection lease
		err   error        // background acquisition failure value
	}
	// Buffer acquisition outcomes so each producer can finish independently while
	// the fixture inspects waiter state and cancels the second request.
	firstCh := make(chan result, 1)
	go func() {
		lease, err := pool.Acquire(context.Background(), req)
		firstCh <- result{lease: lease, err: err}
	}()
	<-dialer.firstStarted

	waitCtx, cancelWait := context.WithCancel(context.Background())
	secondCh := make(chan result, 1)
	waitReq := req
	waitReq.Headers = http.Header{openAICodexRoutingHintHeader: {"model=gpt-5.6-codex;tier=priority"}}
	go func() {
		lease, err := pool.Acquire(waitCtx, waitReq)
		secondCh <- result{lease: lease, err: err}
	}()

	close(dialer.releaseFirst)
	first := <-firstCh
	require.NoError(t, first.err)
	require.NotNil(t, first.lease)

	// Waits during dialing and active leases both count toward the provider queue.
	require.Eventually(t, func() bool {
		ap, ok := pool.getProviderPool(provider.ID)
		if !ok || ap == nil {
			return false
		}
		ap.mu.Lock()
		defer ap.mu.Unlock()
		_, waiters := providerPoolLoadLocked(ap)
		return waiters == 1
	}, time.Second, 5*time.Millisecond)

	cancelWait()
	second := <-secondCh
	require.ErrorIs(t, second.err, context.Canceled)
	require.Nil(t, second.lease)
	ap, ok := pool.getProviderPool(provider.ID)
	require.True(t, ok)
	ap.mu.Lock()
	require.NotNil(t, ap.lastAcquire)
	require.Empty(t, normalizeOpenAIWSRoutingAffinity(ap.lastAcquire.Headers), "a canceled acquire must not replace the successful prewarm target")
	ap.mu.Unlock()
	first.lease.Release()

	third, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.True(t, third.Reused(), "a canceled waiter must not consume the released semaphore token")
	require.Equal(t, first.lease.ConnID(), third.ConnID())
	third.Release()
}

// TestOpenAIWSConnPool_PrewarmHintChangeDoesNotInvalidateHealthyDial changes
// only routing preference during a held prewarm. One dial remains admitted with
// its captured old affinity, protecting advisory hints from becoming hard
// compatibility.
func TestOpenAIWSConnPool_PrewarmHintChangeDoesNotInvalidateHealthyDial(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 1
	cfg.MinIdlePerProvider = 1
	cfg.MaxIdlePerProvider = 1
	cfg.DialTimeoutSeconds = 2

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := newOpenAIWSFirstDialBlockingCaptureDialer()
	pool.SetClientDialerForTest(dialer)
	provider := &WSPoolProvider{ID: 992, Type: "oauth"}
	oldHeaders := make(http.Header)
	oldHeaders.Set(openAICodexRoutingHintHeader, "model=gpt-5.6-codex")
	newHeaders := make(http.Header)
	newHeaders.Set(openAICodexRoutingHintHeader, "model=gpt-5.6-codex;tier=priority")
	ap := pool.getOrCreateProviderPool(provider.ID)
	ap.mu.Lock()
	ap.lastAcquire = &WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
		Headers:  oldHeaders,
	}
	ap.mu.Unlock()

	// Hold first prewarm after capturing its hint, then replace only the retained
	// request preference to test compatibility admission of that late result.
	pool.ensureTargetIdleAsync(provider.ID)
	<-dialer.firstStarted

	// A routing hint change during prewarming still allows an otherwise compatible
	// connection into the pool.
	ap.mu.Lock()
	ap.lastAcquire = &WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
		Headers:  newHeaders,
	}
	ap.mu.Unlock()
	close(dialer.releaseFirst)

	require.Eventually(t, func() bool {
		ap.mu.Lock()
		defer ap.mu.Unlock()
		if ap.prewarmActive || len(ap.conns) != 1 {
			return false
		}
		for _, conn := range ap.conns {
			return conn != nil && conn.routingAffinity == "model=gpt-5.6-codex"
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, 1, dialer.DialCount(), "routing-hint-only changes must not turn advisory metadata into hard reconnects")
}

// TestOpenAIWSConnPool_ClearProviderWakesIncompatibleTopologyWaiter blocks a
// new beta target behind a busy incompatible socket. Clearing wakes the request
// to create its own compatible socket and increases the dial count once.
func TestOpenAIWSConnPool_ClearProviderWakesIncompatibleTopologyWaiter(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 1
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 1

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)
	provider := &WSPoolProvider{ID: 993, Type: "oauth"}
	baseReq := WSAcquireRequest{Provider: provider, WSURL: "wss://example.com/v1/responses"}
	betaAReq := baseReq
	betaAReq.Headers = http.Header{"X-Codex-Beta-Features": {"feature_a"}}
	betaBReq := baseReq
	betaBReq.Headers = http.Header{"X-Codex-Beta-Features": {"feature_b"}}

	busy, err := pool.Acquire(context.Background(), betaAReq)
	require.NoError(t, err)
	require.Equal(t, 1, dialer.DialCount())

	// result transfers one background acquisition's lease/error outcome to its
	// controlled handoff fixture without sharing result mutation after delivery.
	type result struct {
		lease *WSConnLease // background acquired connection lease
		err   error        // background acquisition failure value
	}
	// Run incompatible-target selection separately so provider clearing can wake
	// it; a buffered outcome prevents result delivery from holding that acquisition
	// open.
	resultCh := make(chan result, 1)
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	go func() {
		lease, acquireErr := pool.Acquire(waitCtx, betaBReq)
		resultCh <- result{lease: lease, err: acquireErr}
	}()

	require.Never(t, func() bool { return dialer.DialCount() > 1 }, 50*time.Millisecond, 5*time.Millisecond)
	pool.ClearProvider(provider.ID)

	resultB := <-resultCh
	require.NoError(t, resultB.err)
	require.NotNil(t, resultB.lease)
	require.False(t, resultB.lease.Reused())
	require.Equal(t, 2, dialer.DialCount(), "ClearProvider must wake the waiter to redial immediately")
	resultB.lease.Release()
	busy.Release()
}

// TestOpenAIWSConnPool_ClearProviderDoesNotReviveInFlightDialGeneration resets
// provider state while its first dial is held. The old result is discarded, a
// second-generation socket is returned, and its successful request restores the
// target.
func TestOpenAIWSConnPool_ClearProviderDoesNotReviveInFlightDialGeneration(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 1
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 1

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := newOpenAIWSFirstDialBlockingCaptureDialer()
	pool.SetClientDialerForTest(dialer)
	provider := &WSPoolProvider{ID: 994, Type: "oauth"}
	req := WSAcquireRequest{Provider: provider, WSURL: "wss://example.com/v1/responses"}

	// result transfers one background acquisition's lease/error outcome to its
	// controlled handoff fixture without sharing result mutation after delivery.
	type result struct {
		lease *WSConnLease // background acquired connection lease
		err   error        // background acquisition failure value
	}
	// Capture first acquisition while its dial is held, clear its generation, and
	// release the gate before inspecting the replacement dial result.
	resultCh := make(chan result, 1)
	go func() {
		lease, err := pool.Acquire(context.Background(), req)
		resultCh <- result{lease: lease, err: err}
	}()
	<-dialer.firstStarted

	pool.ClearProvider(provider.ID)
	close(dialer.releaseFirst)
	got := <-resultCh
	require.NoError(t, got.err)
	require.NotNil(t, got.lease)
	require.Equal(t, 2, dialer.DialCount(), "the pre-clear dial must be discarded and retried in the new generation")
	require.True(t, strings.HasSuffix(got.lease.ConnID(), "_2"))

	ap, ok := pool.getProviderPool(provider.ID)
	require.True(t, ok)
	ap.mu.Lock()
	require.Equal(t, uint64(1), ap.generation)
	require.Len(t, ap.conns, 1)
	require.NotNil(t, ap.lastAcquire, "only the post-clear successful acquire may restore the prewarm target")
	ap.mu.Unlock()
	got.lease.Release()
}

// TestOpenAIWSConnPool_ForceNewConnSkipsReuse releases a usable socket before a
// forced-new request. The fake dialer records a second dial, protecting the
// request mode from ordinary idle reuse.
func TestOpenAIWSConnPool_ForceNewConnSkipsReuse(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 2

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)

	provider := &WSPoolProvider{ID: 123, Type: "apikey"}

	lease1, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)
	lease1.Release()

	lease2, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider:     provider,
		WSURL:        "wss://example.com/v1/responses",
		ForceNewConn: true,
	})
	require.NoError(t, err)
	require.NotNil(t, lease2)
	lease2.Release()

	require.Equal(t, 2, dialer.DialCount(), "ForceNewConn=true 时应跳过空闲连接复用并新建连接")
}

// TestOpenAIWSConnPool_AcquirePassesTLSProfile supplies an OAuth TLS profile
// object during acquisition. The transport dialer receives that same pointer,
// documenting shared profile forwarding rather than a profile clone.
func TestOpenAIWSConnPool_AcquirePassesTLSProfile(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MinIdlePerProvider = 0

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)

	profile := &tlsfingerprint.Profile{Name: "openai-oauth-tls"}
	lease, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider:   &WSPoolProvider{ID: 223, Type: "oauth"},
		WSURL:      "wss://example.com/v1/responses",
		TLSProfile: profile,
	})
	require.NoError(t, err)
	require.NotNil(t, lease)
	lease.Release()

	require.Same(t, profile, dialer.LastTLSProfile(), "OpenAI WS 建连应透传 TLS profile")
}

// TestOpenAIWSConnPool_ReusesPreferredConnWithStableRandomTLSProfileKey changes
// randomized profile objects while keeping one supplied key. Preferred
// acquisition retains its socket ID/reuse flag and performs only one dial.
func TestOpenAIWSConnPool_ReusesPreferredConnWithStableRandomTLSProfileKey(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 2

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)

	provider := &WSPoolProvider{ID: 224, Type: "oauth"}
	lease1, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider:      provider,
		WSURL:         "wss://example.com/v1/responses",
		TLSProfile:    &tlsfingerprint.Profile{Name: "random-profile-a"},
		TLSProfileKey: "tls-random",
	})
	require.NoError(t, err)
	connID := lease1.ConnID()
	lease1.Release()

	lease2, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider:        provider,
		WSURL:           "wss://example.com/v1/responses",
		PreferredConnID: connID,
		TLSProfile:      &tlsfingerprint.Profile{Name: "random-profile-b"},
		TLSProfileKey:   "tls-random",
	})
	require.NoError(t, err)
	require.Equal(t, connID, lease2.ConnID())
	require.True(t, lease2.Reused(), "随机模板应按稳定配置键复用 continuation 连接")
	lease2.Release()
	require.Equal(t, 1, dialer.DialCount())
}

// TestOpenAIWSConnPool_AcquireReusesOnlyMatchingBetaFeatures compares absent,
// reordered, and required beta handshakes. Equivalent feature sets reuse their
// socket, while forced continuation rejects the incompatible plain socket.
func TestOpenAIWSConnPool_AcquireReusesOnlyMatchingBetaFeatures(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 2

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)

	provider := &WSPoolProvider{ID: 128, Type: "apikey"}
	baseReq := WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
	}

	plainLease, err := pool.Acquire(context.Background(), baseReq)
	require.NoError(t, err)
	plainConnID := plainLease.ConnID()
	plainLease.Release()

	betaReq := baseReq
	betaReq.Headers = http.Header{"X-Codex-Beta-Features": {" remote_compaction_v2 ", " responses_websockets_v2 "}}
	betaLease, err := pool.Acquire(context.Background(), betaReq)
	require.NoError(t, err)
	require.False(t, betaLease.Reused())
	require.NotEqual(t, plainConnID, betaLease.ConnID())
	betaConnID := betaLease.ConnID()
	betaLease.Release()

	reorderedReq := baseReq
	reorderedReq.Headers = http.Header{"X-Codex-Beta-Features": {"responses_websockets_v2,remote_compaction_v2"}}
	reorderedLease, err := pool.Acquire(context.Background(), reorderedReq)
	require.NoError(t, err)
	require.True(t, reorderedLease.Reused())
	require.Equal(t, betaConnID, reorderedLease.ConnID())
	reorderedLease.Release()

	_, err = pool.Acquire(context.Background(), WSAcquireRequest{
		Provider:           provider,
		WSURL:              baseReq.WSURL,
		Headers:            betaReq.Headers,
		PreferredConnID:    plainConnID,
		ForcePreferredConn: true,
	})
	require.ErrorIs(t, err, openai.ErrOpenAIWSPreferredConnUnavailable)

	plainLease, err = pool.Acquire(context.Background(), baseReq)
	require.NoError(t, err)
	require.True(t, plainLease.Reused())
	require.Equal(t, plainConnID, plainLease.ConnID())
	plainLease.Release()

	require.Equal(t, 2, dialer.DialCount())
}

// TestOpenAIWSConnPool_AcquireReplacesIdleConnWithDifferentBetaFeatures changes
// beta identity in a one-slot pool after release. The old idle socket is
// replaced and the new lease has another ID with a second dial.
func TestOpenAIWSConnPool_AcquireReplacesIdleConnWithDifferentBetaFeatures(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 1
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 1

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)

	provider := &WSPoolProvider{ID: 129, Type: "apikey"}
	plainLease, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
	})
	require.NoError(t, err)
	plainConnID := plainLease.ConnID()
	plainLease.Release()

	betaLease, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
		Headers:  http.Header{"X-Codex-Beta-Features": {"remote_compaction_v2"}},
	})
	require.NoError(t, err)
	require.False(t, betaLease.Reused())
	require.NotEqual(t, plainConnID, betaLease.ConnID())
	betaLease.Release()

	require.Equal(t, 2, dialer.DialCount())
}

// TestOpenAIWSConnPool_AcquireReplacesIdleConnWithMatchingBetaButDifferentTLS
// keeps beta constant while changing TLS profile identity in a full idle pool.
// Selection returns a fresh different socket and records a second dial.
func TestOpenAIWSConnPool_AcquireReplacesIdleConnWithMatchingBetaButDifferentTLS(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 1
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 1

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)
	provider := &WSPoolProvider{ID: 132, Type: "oauth"}
	headers := http.Header{"X-Codex-Beta-Features": {"remote_compaction_v2"}}

	firstLease, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider:   provider,
		WSURL:      "wss://example.com/v1/responses",
		Headers:    headers,
		TLSProfile: &tlsfingerprint.Profile{Name: "tls-profile-a"},
	})
	require.NoError(t, err)
	firstConnID := firstLease.ConnID()
	firstLease.Release()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	secondLease, err := pool.Acquire(ctx, WSAcquireRequest{
		Provider:   provider,
		WSURL:      "wss://example.com/v1/responses",
		Headers:    headers,
		TLSProfile: &tlsfingerprint.Profile{Name: "tls-profile-b"},
	})
	require.NoError(t, err)
	require.False(t, secondLease.Reused())
	require.NotEqual(t, firstConnID, secondLease.ConnID())
	secondLease.Release()
	require.Equal(t, 2, dialer.DialCount())
}

// TestOpenAIWSConnPool_AcquireWaitsForBusyIncompatibleConnection holds an
// incompatible one-slot socket while another request waits. That request
// remains pending before release, then creates a distinct compatible socket.
func TestOpenAIWSConnPool_AcquireWaitsForBusyIncompatibleConnection(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 1
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 1

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)
	provider := &WSPoolProvider{ID: 130, Type: "apikey"}
	baseReq := WSAcquireRequest{Provider: provider, WSURL: "wss://example.com/v1/responses"}

	plainLease, err := pool.Acquire(context.Background(), baseReq)
	require.NoError(t, err)
	plainConnID := plainLease.ConnID()

	// acquireResult transfers a background incompatible-target acquisition outcome
	// to the waiting fixture after its selected socket becomes available.
	type acquireResult struct {
		lease *WSConnLease // background acquired connection lease
		err   error        // background acquisition failure value
	}
	resultCh := make(chan acquireResult, 1)
	// Track background completion atomically while the incompatible lease remains
	// occupied, allowing the fixture to distinguish waiting from early result
	// delivery.
	var done atomic.Bool
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		betaReq := baseReq
		betaReq.Headers = http.Header{"X-Codex-Beta-Features": {"remote_compaction_v2"}}
		lease, acquireErr := pool.Acquire(ctx, betaReq)
		resultCh <- acquireResult{lease: lease, err: acquireErr}
		done.Store(true)
	}()

	require.Never(t, done.Load, 50*time.Millisecond, 5*time.Millisecond)
	plainLease.Release()

	result := <-resultCh
	require.NoError(t, result.err)
	require.NotNil(t, result.lease)
	require.False(t, result.lease.Reused())
	require.NotEqual(t, plainConnID, result.lease.ConnID())
	result.lease.Release()
	require.Equal(t, 2, dialer.DialCount())
}

// TestOpenAIWSConnPool_AcquireReplacesIncompatibleIdleWhenMatchingBusy fills a
// two-slot pool with idle plain and busy beta sockets. Another beta request
// replaces the incompatible idle entry with a third dial.
func TestOpenAIWSConnPool_AcquireReplacesIncompatibleIdleWhenMatchingBusy(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 2

	pool := newStartedWSConnPoolForTest(cfg)
	dialer := &openAIWSCountingDialer{}
	pool.SetClientDialerForTest(dialer)
	provider := &WSPoolProvider{ID: 131, Type: "apikey"}
	baseReq := WSAcquireRequest{Provider: provider, WSURL: "wss://example.com/v1/responses"}

	plainLease, err := pool.Acquire(context.Background(), baseReq)
	require.NoError(t, err)
	plainConnID := plainLease.ConnID()
	plainLease.Release()

	betaReq := baseReq
	betaReq.Headers = http.Header{"X-Codex-Beta-Features": {"remote_compaction_v2"}}
	busyBetaLease, err := pool.Acquire(context.Background(), betaReq)
	require.NoError(t, err)

	secondBetaLease, err := pool.Acquire(context.Background(), betaReq)
	require.NoError(t, err)
	require.False(t, secondBetaLease.Reused())
	require.NotEqual(t, plainConnID, secondBetaLease.ConnID())
	require.NotEqual(t, busyBetaLease.ConnID(), secondBetaLease.ConnID())

	secondBetaLease.Release()
	busyBetaLease.Release()
	require.Equal(t, 3, dialer.DialCount())
}

// TestOpenAIWSConnPool_AcquireForcePreferredConnUnavailable supplies blank and
// missing required IDs despite another recorded socket. Both requests return
// preferred-unavailable, protecting forced continuation from drifting to that
// other connection.
func TestOpenAIWSConnPool_AcquireForcePreferredConnUnavailable(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 2

	pool := newStartedWSConnPoolForTest(cfg)
	provider := &WSPoolProvider{ID: 124, Type: "apikey"}
	ap := pool.getOrCreateProviderPool(provider.ID)
	otherConn := NewWSConn("other_conn", provider.ID, &openAIWSFakeConn{}, nil, nil, "")
	ap.mu.Lock()
	ap.conns[otherConn.id] = otherConn
	ap.mu.Unlock()

	_, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider:           provider,
		WSURL:              "wss://example.com/v1/responses",
		ForcePreferredConn: true,
	})
	require.ErrorIs(t, err, openai.ErrOpenAIWSPreferredConnUnavailable)

	_, err = pool.Acquire(context.Background(), WSAcquireRequest{
		Provider:           provider,
		WSURL:              "wss://example.com/v1/responses",
		PreferredConnID:    "missing_conn",
		ForcePreferredConn: true,
	})
	require.ErrorIs(t, err, openai.ErrOpenAIWSPreferredConnUnavailable)
}

// TestOpenAIWSConnPool_AcquireForcePreferredConnQueuesOnPreferredOnly delays
// release of a required busy socket while another compatible socket is idle.
// The request waits, receives its required ID, and leaves the other token
// available.
func TestOpenAIWSConnPool_AcquireForcePreferredConnQueuesOnPreferredOnly(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 2
	cfg.QueueLimitPerConn = 4

	pool := newStartedWSConnPoolForTest(cfg)
	provider := &WSPoolProvider{ID: 125, Type: "apikey"}
	ap := pool.getOrCreateProviderPool(provider.ID)
	preferredConn := NewWSConn("preferred_conn", provider.ID, &openAIWSFakeConn{}, nil, nil, "")
	preferredConn.compatibility = wsCompatibilityForRequest(WSAcquireRequest{Provider: provider, WSURL: "wss://example.com/v1/responses"})
	otherConn := NewWSConn("other_conn_idle", provider.ID, &openAIWSFakeConn{}, nil, nil, "")
	otherConn.compatibility = preferredConn.compatibility
	require.True(t, preferredConn.tryAcquire(), "先占用 preferred 连接，触发排队获取")
	ap.mu.Lock()
	ap.conns[preferredConn.id] = preferredConn
	ap.conns[otherConn.id] = otherConn
	ap.lastCleanupAt = time.Now()
	ap.mu.Unlock()

	// Delay required-token return while the other compatible token stays idle,
	// isolating strict continuation waiting from ordinary route selection.
	go func() {
		time.Sleep(60 * time.Millisecond)
		(&WSConnLease{pool: pool, ProviderID: provider.ID, Conn: preferredConn}).Release()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, err := pool.Acquire(ctx, WSAcquireRequest{
		Provider:           provider,
		WSURL:              "wss://example.com/v1/responses",
		PreferredConnID:    preferredConn.id,
		ForcePreferredConn: true,
	})
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.Equal(t, preferredConn.id, lease.ConnID(), "严格模式应只等待并复用 preferred 连接，不可漂移")
	require.GreaterOrEqual(t, lease.QueueWaitDuration(), 40*time.Millisecond)
	lease.Release()
	require.True(t, otherConn.tryAcquire(), "other 连接不应被严格模式抢占")
	otherConn.release()
}

// TestOpenAIWSConnPool_AcquireForcePreferredConnDirectAndQueueFull checks idle
// direct selection and a saturated required queue. Required identity is
// selected when free, and queue-full fails despite another recorded socket.
func TestOpenAIWSConnPool_AcquireForcePreferredConnDirectAndQueueFull(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MinIdlePerProvider = 0
	cfg.MaxIdlePerProvider = 2
	cfg.QueueLimitPerConn = 1

	pool := newStartedWSConnPoolForTest(cfg)
	provider := &WSPoolProvider{ID: 127, Type: "apikey"}
	ap := pool.getOrCreateProviderPool(provider.ID)
	preferredConn := NewWSConn("preferred_conn_direct", provider.ID, &openAIWSFakeConn{}, nil, nil, "")
	preferredConn.compatibility = wsCompatibilityForRequest(WSAcquireRequest{Provider: provider, WSURL: "wss://example.com/v1/responses"})
	otherConn := NewWSConn("other_conn_direct", provider.ID, &openAIWSFakeConn{}, nil, nil, "")
	otherConn.compatibility = preferredConn.compatibility
	ap.mu.Lock()
	ap.conns[preferredConn.id] = preferredConn
	ap.conns[otherConn.id] = otherConn
	ap.lastCleanupAt = time.Now()
	ap.mu.Unlock()

	lease, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider:           provider,
		WSURL:              "wss://example.com/v1/responses",
		PreferredConnID:    preferredConn.id,
		ForcePreferredConn: true,
	})
	require.NoError(t, err)
	require.Equal(t, preferredConn.id, lease.ConnID(), "preferred 空闲时应直接命中")
	lease.Release()

	require.True(t, preferredConn.tryAcquire())
	preferredConn.waiters.Store(1)
	_, err = pool.Acquire(context.Background(), WSAcquireRequest{
		Provider:           provider,
		WSURL:              "wss://example.com/v1/responses",
		PreferredConnID:    preferredConn.id,
		ForcePreferredConn: true,
	})
	require.ErrorIs(t, err, openai.ErrOpenAIWSConnQueueFull, "严格模式下队列满应直接失败，不得漂移")
	preferredConn.waiters.Store(0)
	preferredConn.release()
}

// TestOpenAIWSConnPool_CleanupSkipsPinnedConn pins one idle socket under
// zero-idle cleanup. That socket survives while its peer closes, then becomes
// reclaimable after its pin is released.
func TestOpenAIWSConnPool_CleanupSkipsPinnedConn(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MaxIdlePerProvider = 0

	pool := newStartedWSConnPoolForTest(cfg)
	providerID := int64(126)
	ap := pool.getOrCreateProviderPool(providerID)
	pinnedConn := NewWSConn("pinned_conn", providerID, &openAIWSFakeConn{}, nil, nil, "")
	idleConn := NewWSConn("idle_conn", providerID, &openAIWSFakeConn{}, nil, nil, "")
	ap.mu.Lock()
	ap.conns[pinnedConn.id] = pinnedConn
	ap.conns[idleConn.id] = idleConn
	ap.mu.Unlock()

	require.True(t, pool.PinConn(providerID, pinnedConn.id))
	evicted := pool.cleanupProviderLocked(ap, time.Now(), pool.maxConnsHardCap())
	closeOpenAIWSConns(evicted)

	ap.mu.Lock()
	_, pinnedExists := ap.conns[pinnedConn.id]
	_, idleExists := ap.conns[idleConn.id]
	ap.mu.Unlock()
	require.True(t, pinnedExists, "被 active ingress 绑定的连接不应被 cleanup 回收")
	require.False(t, idleExists, "非绑定的空闲连接应被回收")

	pool.UnpinConn(providerID, pinnedConn.id)
	evicted = pool.cleanupProviderLocked(ap, time.Now(), pool.maxConnsHardCap())
	closeOpenAIWSConns(evicted)
	ap.mu.Lock()
	_, pinnedExists = ap.conns[pinnedConn.id]
	ap.mu.Unlock()
	require.False(t, pinnedExists, "解绑后连接应可被正常回收")
}

// TestOpenAIWSConnPool_PinUnpinConnBranches tests invalid selections and two
// pins on one recorded socket. Matching unpins decrement/remove the entry, and
// extra or invalid unpins are accepted.
func TestOpenAIWSConnPool_PinUnpinConnBranches(t *testing.T) {
	var nilPool *WSConnPool
	require.False(t, nilPool.PinConn(1, "x"))
	nilPool.UnpinConn(1, "x")

	cfg := &WSPoolOptions{}
	pool := newStartedWSConnPoolForTest(cfg)
	providerID := int64(128)
	ap := &openAIWSProviderPool{
		conns: map[string]*WSConn{},
	}
	pool.providers.Store(providerID, ap)

	require.False(t, pool.PinConn(0, "x"))
	require.False(t, pool.PinConn(999, "x"))
	require.False(t, pool.PinConn(providerID, ""))
	require.False(t, pool.PinConn(providerID, "missing"))

	conn := NewWSConn("pin_refcount", providerID, &openAIWSFakeConn{}, nil, nil, "")
	ap.mu.Lock()
	ap.conns[conn.id] = conn
	ap.mu.Unlock()
	require.True(t, pool.PinConn(providerID, conn.id))
	require.True(t, pool.PinConn(providerID, conn.id))

	ap.mu.Lock()
	require.Equal(t, 2, ap.pinnedConns[conn.id])
	ap.mu.Unlock()

	pool.UnpinConn(providerID, conn.id)
	ap.mu.Lock()
	require.Equal(t, 1, ap.pinnedConns[conn.id])
	ap.mu.Unlock()

	pool.UnpinConn(providerID, conn.id)
	ap.mu.Lock()
	_, exists := ap.pinnedConns[conn.id]
	ap.mu.Unlock()
	require.False(t, exists)

	pool.UnpinConn(providerID, conn.id)
	pool.UnpinConn(providerID, "")
	pool.UnpinConn(0, conn.id)
	pool.UnpinConn(999, conn.id)
}

// TestOpenAIWSConnPool_EffectiveMaxConnsByProvider combines hard cap, provider
// type factors, and concurrency values. Assertions protect bounded dynamic
// capacity, minimum one, and hard-cap selection for nil/unlimited providers.
func TestOpenAIWSConnPool_EffectiveMaxConnsByProvider(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 8
	cfg.DynamicMaxConnsByProviderConcurrencyEnabled = true
	cfg.OAuthMaxConnsFactor = 1.0
	cfg.APIKeyMaxConnsFactor = 0.6

	pool := newStartedWSConnPoolForTest(cfg)

	oauthHigh := &WSPoolProvider{Type: "oauth", Concurrency: 10}
	require.Equal(t, 8, pool.effectiveMaxConnsByProvider(oauthHigh), "应受全局硬上限约束")

	oauthLow := &WSPoolProvider{Type: "oauth", Concurrency: 3}
	require.Equal(t, 3, pool.effectiveMaxConnsByProvider(oauthLow))

	apiKeyHigh := &WSPoolProvider{Type: "apikey", Concurrency: 10}
	require.Equal(t, 6, pool.effectiveMaxConnsByProvider(apiKeyHigh), "API Key 应按系数缩放")

	apiKeyLow := &WSPoolProvider{Type: "apikey", Concurrency: 1}
	require.Equal(t, 1, pool.effectiveMaxConnsByProvider(apiKeyLow), "最小值应保持为 1")

	unlimited := &WSPoolProvider{Type: "oauth", Concurrency: 0}
	require.Equal(t, 8, pool.effectiveMaxConnsByProvider(unlimited), "无限并发应回退到全局硬上限")

	require.Equal(t, 8, pool.effectiveMaxConnsByProvider(nil), "缺少提供商上下文应回退到全局硬上限")
}

// TestOpenAIWSConnPool_EffectiveMaxConnsDisabledFallbackHardCap disables
// concurrency-based sizing for a low-concurrency provider. Its effective
// connection capacity remains the configured hard cap instead of a scaled
// value.
func TestOpenAIWSConnPool_EffectiveMaxConnsDisabledFallbackHardCap(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 8
	cfg.DynamicMaxConnsByProviderConcurrencyEnabled = false
	cfg.OAuthMaxConnsFactor = 1.0
	cfg.APIKeyMaxConnsFactor = 1.0

	pool := newStartedWSConnPoolForTest(cfg)
	provider := &WSPoolProvider{Type: "oauth", Concurrency: 2}
	require.Equal(t, 8, pool.effectiveMaxConnsByProvider(provider), "关闭动态模式后应保持旧行为")
}

// TestOpenAIWSConnPool_EffectiveMaxConnsByProvider_DynamicLimitRespectsFactorAndHardCap
// supplies distinct provider factors and nonpositive concurrency. It asserts
// the scaled OAuth result and hard-cap choice for unlimited API-key admission.
func TestOpenAIWSConnPool_EffectiveMaxConnsByProvider_DynamicLimitRespectsFactorAndHardCap(t *testing.T) {
	cfg := &WSPoolOptions{}

	cfg.MaxConnsPerProvider = 8
	cfg.DynamicMaxConnsByProviderConcurrencyEnabled = true
	cfg.OAuthMaxConnsFactor = 0.3
	cfg.APIKeyMaxConnsFactor = 0.6

	pool := newStartedWSConnPoolForTest(cfg)

	high := &WSPoolProvider{Type: "oauth", Concurrency: 20}
	require.Equal(t, 6, pool.effectiveMaxConnsByProvider(high))

	nonPositive := &WSPoolProvider{Type: "apikey", Concurrency: 0}
	require.Equal(t, 8, pool.effectiveMaxConnsByProvider(nonPositive))
}

// TestOpenAIWSConnLease_ReadMessageWithContextTimeout_PerRead uses delayed fake
// reads with shorter/longer budgets and canceled parent. Outcomes distinguish
// timeout, successful completion payload, and parent cancellation for separate
// calls.
func TestOpenAIWSConnLease_ReadMessageWithContextTimeout_PerRead(t *testing.T) {
	conn := NewWSConn("timeout", 1, &openAIWSBlockingConn{readDelay: 80 * time.Millisecond}, nil, nil, "")
	lease := &WSConnLease{Conn: conn}

	_, err := lease.ReadMessageWithContextTimeout(context.Background(), 20*time.Millisecond)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	payload, err := lease.ReadMessageWithContextTimeout(context.Background(), 150*time.Millisecond)
	require.NoError(t, err)
	require.Contains(t, string(payload), "response.completed")

	parentCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = lease.ReadMessageWithContextTimeout(parentCtx, 150*time.Millisecond)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

// TestOpenAIWSConnLease_WriteJSONWithContextTimeout_RespectsParentContext
// cancels a blocked write before its large timeout. The write returns
// context.Canceled within the fixture margin, checking transport parent-context
// propagation.
func TestOpenAIWSConnLease_WriteJSONWithContextTimeout_RespectsParentContext(t *testing.T) {
	conn := NewWSConn("write_timeout_ctx", 1, &openAIWSWriteBlockingConn{}, nil, nil, "")
	lease := &WSConnLease{Conn: conn}

	parentCtx, cancel := context.WithCancel(context.Background())
	// Cancel the parent during the deliberately blocked transport write, checking
	// its propagation before the larger per-operation timeout can expire.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := lease.WriteJSONWithContextTimeout(parentCtx, map[string]any{"type": "response.create"}, 2*time.Minute)
	elapsed := time.Since(start)

	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, elapsed, 200*time.Millisecond)
}

// TestOpenAIWSConnLease_PingWithTimeout probes a valid fake socket and a nil
// lease. The first succeeds while the second returns the shared closed-resource
// sentinel.
func TestOpenAIWSConnLease_PingWithTimeout(t *testing.T) {
	conn := NewWSConn("ping_ok", 1, &openAIWSFakeConn{}, nil, nil, "")
	lease := &WSConnLease{Conn: conn}
	require.NoError(t, lease.PingWithTimeout(50*time.Millisecond))

	var nilLease *WSConnLease
	err := nilLease.PingWithTimeout(50 * time.Millisecond)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
}

// TestOpenAIWSConn_ReadAndWriteCanProceedConcurrently starts a delayed read
// then invokes ping. Ping completes within its margin while the read finishes
// successfully, exercising separate read/write locks through that probe.
func TestOpenAIWSConn_ReadAndWriteCanProceedConcurrently(t *testing.T) {
	conn := NewWSConn("full_duplex", 1, &openAIWSBlockingConn{readDelay: 120 * time.Millisecond}, nil, nil, "")

	// Retain the delayed read outcome independently while timing ping, so read
	// completion can be joined after the direction-lock assertion.
	readDone := make(chan error, 1)
	go func() {
		_, err := conn.readMessageWithContextTimeout(context.Background(), 200*time.Millisecond)
		readDone <- err
	}()

	// Start the delayed read before timing ping, so the fixture measures
	// write-direction independence while that read is expected to hold readMu.
	time.Sleep(20 * time.Millisecond)

	start := time.Now()
	err := conn.pingWithTimeout(50 * time.Millisecond)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Less(t, elapsed, 80*time.Millisecond, "写路径不应被读锁长期阻塞")
	require.NoError(t, <-readDone)
}

// TestOpenAIWSConnPool_BackgroundPingSweep_EvictsDeadIdleConn records an
// upstream socket whose ping fails. A direct background sweep removes its
// provider-map entry, checking failure-driven idle eviction.
func TestOpenAIWSConnPool_BackgroundPingSweep_EvictsDeadIdleConn(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	pool := newStartedWSConnPoolForTest(cfg)

	providerID := int64(301)
	ap := pool.getOrCreateProviderPool(providerID)
	conn := NewWSConn("dead_idle", providerID, &openAIWSPingFailConn{}, nil, nil, "")
	ap.mu.Lock()
	ap.conns[conn.id] = conn
	ap.mu.Unlock()

	pool.runBackgroundPingSweep()

	ap.mu.Lock()
	_, exists := ap.conns[conn.id]
	ap.mu.Unlock()
	require.False(t, exists, "后台 ping 失败的空闲连接应被回收")
}

// TestOpenAIWSConnPool_BackgroundCleanupSweep_WithoutAcquire supplies an
// over-age idle socket and runs cleanup directly. The socket leaves provider
// state even when no new acquisition triggers foreground retirement.
func TestOpenAIWSConnPool_BackgroundCleanupSweep_WithoutAcquire(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MaxIdlePerProvider = 2
	pool := newStartedWSConnPoolForTest(cfg)

	providerID := int64(302)
	ap := pool.getOrCreateProviderPool(providerID)
	stale := NewWSConn("stale_bg", providerID, &openAIWSFakeConn{}, nil, nil, "")
	stale.createdAtNano.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	stale.lastUsedNano.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	ap.mu.Lock()
	ap.conns[stale.id] = stale
	ap.mu.Unlock()

	pool.runBackgroundCleanupSweep(time.Now())

	ap.mu.Lock()
	_, exists := ap.conns[stale.id]
	ap.mu.Unlock()
	require.False(t, exists, "后台清理应在无新 acquire 时也回收过期连接")
}

// TestOpenAIWSConnPool_RecyclesUnsupportedIdlePingConnection supplies a
// reader-dependent idle socket past its recycle threshold. Background cleanup
// removes its record and publishes its closed channel.
func TestOpenAIWSConnPool_RecyclesUnsupportedIdlePingConnection(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.MaxIdlePerProvider = 2
	pool := NewWSConnPool(cfg)

	providerID := int64(303)
	ap := &openAIWSProviderPool{conns: make(map[string]*WSConn)}
	conn := NewWSConn("stale_unsupported_idle_ping", providerID, &openAIWSIdlePingUnsupportedConn{}, nil, nil, "")
	conn.lastUsedNano.Store(time.Now().Add(-openAIWSConnIdleRecycleAfter - time.Second).UnixNano())
	ap.conns[conn.id] = conn
	pool.providers.Store(providerID, ap)

	pool.runBackgroundCleanupSweep(time.Now())

	ap.mu.Lock()
	_, exists := ap.conns[conn.id]
	ap.mu.Unlock()
	require.False(t, exists, "不支持无 reader idle ping 的陈旧连接应被主动回收")
	select {
	case <-conn.closedCh:
	default:
		t.Fatal("被回收的陈旧连接应已关闭")
	}
}

// TestOpenAIWSConnPool_BackgroundWorkerGuardBranches invokes
// nil/missing-channel worker paths and closes active worker stop channels.
// Guarded calls avoid panic and both started loops exit before their fixture
// deadlines.
func TestOpenAIWSConnPool_BackgroundWorkerGuardBranches(t *testing.T) {
	var nilPool *WSConnPool
	require.NotPanics(t, func() {
		nilPool.startBackgroundWorkers()
		nilPool.runBackgroundPingWorker()
		nilPool.runBackgroundPingSweep()
		_ = nilPool.snapshotIdleConnsForPing()
		nilPool.runBackgroundCleanupWorker()
		nilPool.runBackgroundCleanupSweep(time.Now())
	})

	poolNoStop := &WSConnPool{}
	require.NotPanics(t, func() {
		poolNoStop.startBackgroundWorkers()
	})

	poolStopPing := &WSConnPool{workerStopCh: make(chan struct{})}
	// Observe each worker exit after stop closure through its completion channel,
	// keeping the test deadline separate from normal ticker activity.
	pingDone := make(chan struct{})
	go func() {
		poolStopPing.runBackgroundPingWorker()
		close(pingDone)
	}()
	close(poolStopPing.workerStopCh)
	select {
	case <-pingDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runBackgroundPingWorker 未在 stop 信号后退出")
	}

	poolStopCleanup := &WSConnPool{workerStopCh: make(chan struct{})}
	cleanupDone := make(chan struct{})
	go func() {
		poolStopCleanup.runBackgroundCleanupWorker()
		close(cleanupDone)
	}()
	close(poolStopCleanup.workerStopCh)
	select {
	case <-cleanupDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runBackgroundCleanupWorker 未在 stop 信号后退出")
	}
}

// TestOpenAIWSConnPool_SnapshotIdleConnsForPing_SkipsInvalidEntries mixes
// malformed provider values and nil, occupied, waited, and idle sockets. Only
// the usable idle connection appears in the returned candidate list.
func TestOpenAIWSConnPool_SnapshotIdleConnsForPing_SkipsInvalidEntries(t *testing.T) {
	pool := &WSConnPool{}
	pool.providers.Store("invalid-key", &openAIWSProviderPool{})
	pool.providers.Store(int64(123), "invalid-value")

	providerID := int64(123)
	ap := &openAIWSProviderPool{
		conns: make(map[string]*WSConn),
	}
	ap.conns["nil_conn"] = nil

	leased := NewWSConn("leased", providerID, &openAIWSFakeConn{}, nil, nil, "")
	require.True(t, leased.tryAcquire())
	ap.conns[leased.id] = leased

	waiting := NewWSConn("waiting", providerID, &openAIWSFakeConn{}, nil, nil, "")
	waiting.waiters.Store(1)
	ap.conns[waiting.id] = waiting

	idle := NewWSConn("idle", providerID, &openAIWSFakeConn{}, nil, nil, "")
	ap.conns[idle.id] = idle

	pool.providers.Store(providerID, ap)
	candidates := pool.snapshotIdleConnsForPing()
	require.Len(t, candidates, 1)
	require.Equal(t, idle.id, candidates[0].conn.id)
}

// TestOpenAIWSConnPool_RunBackgroundCleanupSweep_SkipsInvalidAndUsesProviderCap
// combines invalid provider storage, nil socket, and stale connection under
// dynamic limits. Cleanup avoids panic, removes invalid/stale entries, and
// records the supplied cleanup time.
func TestOpenAIWSConnPool_RunBackgroundCleanupSweep_SkipsInvalidAndUsesProviderCap(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 4
	cfg.DynamicMaxConnsByProviderConcurrencyEnabled = true

	pool := NewWSConnPool(cfg)
	pool.providers.Store("bad-key", "bad-value")

	providerID := int64(2026)
	ap := &openAIWSProviderPool{
		conns: make(map[string]*WSConn),
	}
	ap.conns["nil_conn"] = nil
	stale := NewWSConn("stale_bg_cleanup", providerID, &openAIWSFakeConn{}, nil, nil, "")
	stale.createdAtNano.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	stale.lastUsedNano.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	ap.conns[stale.id] = stale
	ap.lastAcquire = &WSAcquireRequest{
		Provider: &WSPoolProvider{
			ID:          providerID,
			Type:        "apikey",
			Concurrency: 1,
		},
	}
	pool.providers.Store(providerID, ap)

	now := time.Now()
	require.NotPanics(t, func() {
		pool.runBackgroundCleanupSweep(now)
	})

	ap.mu.Lock()
	_, nilConnExists := ap.conns["nil_conn"]
	_, exists := ap.conns[stale.id]
	lastCleanupAt := ap.lastCleanupAt
	ap.mu.Unlock()

	require.False(t, nilConnExists, "后台清理应移除无效 nil 连接条目")
	require.False(t, exists, "后台清理应清理过期连接")
	require.Equal(t, now, lastCleanupAt)
}

// TestOpenAIWSConnPool_QueueLimitPerConn_DefaultAndConfigured reads queue
// limits from nil and zero options, then publishes a configured value.
// Assertions protect default capacity and visibility of the replacement
// setting.
func TestOpenAIWSConnPool_QueueLimitPerConn_DefaultAndConfigured(t *testing.T) {
	var nilPool *WSConnPool
	require.Equal(t, 256, nilPool.queueLimitPerConn())

	pool := NewWSConnPool(&WSPoolOptions{})
	require.Equal(t, 256, pool.queueLimitPerConn())

	options := *pool.nativeOptions()
	options.QueueLimitPerConn = 9
	require.NoError(t, pool.UpdateOptions(options))
	require.Equal(t, 9, pool.queueLimitPerConn())
}

// TestOpenAIWSConnPool_Close stops a started pool and inspects its worker stop
// channel. Repeated shutdown and a nil receiver are accepted, checking the
// public close guard.
func TestOpenAIWSConnPool_Close(t *testing.T) {
	cfg := &WSPoolOptions{}
	pool := newStartedWSConnPoolForTest(cfg)

	pool.Close()

	select {
	case <-pool.workerStopCh:
	default:
		t.Fatal("Close 后 workerStopCh 应已关闭")
	}

	// Repeated shutdown is safe.
	pool.Close()

	// A nil pool also accepts Close.
	var nilPool *WSConnPool
	nilPool.Close()
}

// TestOpenAIWSDialError_ErrorAndUnwrap constructs status-bearing, status-free,
// and nil dial errors. Error text and underlying errors remain inspectable,
// while nil receiver formatting/unwrap returns empty outcomes.
func TestOpenAIWSDialError_ErrorAndUnwrap(t *testing.T) {
	baseErr := errors.New("boom")
	dialErr := &openai.WSDialError{StatusCode: 502, Err: baseErr}
	require.Contains(t, dialErr.Error(), "status=502")
	require.ErrorIs(t, dialErr.Unwrap(), baseErr)

	noStatus := &openai.WSDialError{Err: baseErr}
	require.Contains(t, noStatus.Error(), "boom")

	var nilDialErr *openai.WSDialError
	require.Equal(t, "", nilDialErr.Error())
	require.NoError(t, nilDialErr.Unwrap())
}

// TestOpenAIWSConnLease_ReadWriteHelpersAndConnStats exercises context/budget
// wrappers, retained handshake text, activity clocks, and missing connections.
// Successful fake operations update/read metadata and guarded nil wrappers
// return the closed sentinel.
func TestOpenAIWSConnLease_ReadWriteHelpersAndConnStats(t *testing.T) {
	conn := NewWSConn("helper_conn", 1, &openAIWSFakeConn{}, http.Header{
		"X-Test": []string{" value "},
	}, nil, "")
	lease := &WSConnLease{Conn: conn}

	require.NoError(t, lease.WriteJSONContext(context.Background(), map[string]any{"type": "response.create"}))
	payload, err := lease.ReadMessage(100 * time.Millisecond)
	require.NoError(t, err)
	require.Contains(t, string(payload), "response.completed")

	payload, err = lease.ReadMessageContext(context.Background())
	require.NoError(t, err)
	require.Contains(t, string(payload), "response.completed")

	payload, err = conn.readMessageWithTimeout(100 * time.Millisecond)
	require.NoError(t, err)
	require.Contains(t, string(payload), "response.completed")

	require.Equal(t, "value", conn.handshakeHeader(" X-Test "))
	require.NotZero(t, conn.createdAt())
	require.NotZero(t, conn.lastUsedAt())
	require.GreaterOrEqual(t, conn.age(time.Now()), time.Duration(0))
	require.GreaterOrEqual(t, conn.idleDuration(time.Now()), time.Duration(0))
	require.False(t, conn.isLeased())

	_, err = conn.readMessage(context.Background())
	require.NoError(t, err)

	var nilConn *WSConn
	require.ErrorIs(t, nilConn.writeJSONWithTimeout(context.Background(), map[string]any{}, time.Second), errOpenAIWSConnClosed)
	_, err = nilConn.readMessageWithTimeout(10 * time.Millisecond)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	_, err = nilConn.readMessageWithContextTimeout(context.Background(), 10*time.Millisecond)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
}

// TestOpenAIWSConnPool_PickOldestIdleAndProviderPoolLoad records differently
// aged idle sockets and one occupied/waited socket. Selection chooses the
// oldest eligible idle entry and public/private load counts agree for the
// fixture.
func TestOpenAIWSConnPool_PickOldestIdleAndProviderPoolLoad(t *testing.T) {
	pool := &WSConnPool{}
	providerID := int64(404)
	ap := &openAIWSProviderPool{conns: map[string]*WSConn{}}

	idleOld := NewWSConn("idle_old", providerID, &openAIWSFakeConn{}, nil, nil, "")
	idleOld.lastUsedNano.Store(time.Now().Add(-10 * time.Minute).UnixNano())
	idleNew := NewWSConn("idle_new", providerID, &openAIWSFakeConn{}, nil, nil, "")
	idleNew.lastUsedNano.Store(time.Now().Add(-1 * time.Minute).UnixNano())
	leased := NewWSConn("leased", providerID, &openAIWSFakeConn{}, nil, nil, "")
	require.True(t, leased.tryAcquire())
	leased.waiters.Store(2)

	ap.conns[idleOld.id] = idleOld
	ap.conns[idleNew.id] = idleNew
	ap.conns[leased.id] = leased

	oldest := pool.pickOldestIdleConnLocked(ap)
	require.NotNil(t, oldest)
	require.Equal(t, idleOld.id, oldest.id)

	inflight, waiters := providerPoolLoadLocked(ap)
	require.Equal(t, 1, inflight)
	require.Equal(t, 2, waiters)

	pool.providers.Store(providerID, ap)
	loadInflight, loadWaiters, conns := pool.ProviderPoolLoad(providerID)
	require.Equal(t, 1, loadInflight)
	require.Equal(t, 2, loadWaiters)
	require.Equal(t, 3, conns)

	zeroInflight, zeroWaiters, zeroConns := pool.ProviderPoolLoad(0)
	require.Equal(t, 0, zeroInflight)
	require.Equal(t, 0, zeroWaiters)
	require.Equal(t, 0, zeroConns)
}

// TestOpenAIWSConnPool_Close_WaitsWorkerGroupAndNilStopChannel installs a held
// worker on a pool lacking a stop channel. It observes no Close result for
// thirty milliseconds while the worker is held, then releases it and waits
// for completion within the fixture deadline.
func TestOpenAIWSConnPool_Close_WaitsWorkerGroupAndNilStopChannel(t *testing.T) {
	pool := &WSConnPool{}
	// Register held work without a stop channel, then close the pool in another
	// goroutine so the fixture can inspect shutdown join ordering.
	release := make(chan struct{})
	pool.workerWg.Go(func() {
		<-release
	})

	closed := make(chan struct{})
	go func() {
		pool.Close()
		close(closed)
	}()

	select {
	case <-closed:
		t.Fatal("Close 不应在 WaitGroup 未完成时提前返回")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close 未等待 workerWg 完成")
	}
}

// TestOpenAIWSConnPool_Close_ClosesOnlyIdleConnections shuts down recorded idle
// and occupied sockets alongside invalid storage. The idle closed signal
// arrives while the occupied socket remains open at the checked point.
func TestOpenAIWSConnPool_Close_ClosesOnlyIdleConnections(t *testing.T) {
	pool := &WSConnPool{
		workerStopCh: make(chan struct{}),
	}

	providerID := int64(606)
	ap := &openAIWSProviderPool{
		conns: map[string]*WSConn{},
	}
	idle := NewWSConn("idle_conn", providerID, &openAIWSFakeConn{}, nil, nil, "")
	leased := NewWSConn("leased_conn", providerID, &openAIWSFakeConn{}, nil, nil, "")
	require.True(t, leased.tryAcquire())

	ap.conns[idle.id] = idle
	ap.conns[leased.id] = leased
	pool.providers.Store(providerID, ap)
	pool.providers.Store("invalid-key", "invalid-value")

	pool.Close()

	select {
	case <-idle.closedCh:
	default:
		t.Fatal("空闲连接应在 Close 时被关闭")
	}

	select {
	case <-leased.closedCh:
		t.Fatal("已租赁连接不应在 Close 时被关闭")
	default:
	}

	leased.release()
	pool.Close()
}

// TestOpenAIWSConnPool_RunBackgroundPingSweep_ConcurrencyLimit holds
// twenty-five fake ping candidates on one release gate. At least ten probes
// overlap, all finish after release, and recorded maximum never exceeds ten.
func TestOpenAIWSConnPool_RunBackgroundPingSweep_ConcurrencyLimit(t *testing.T) {
	cfg := &WSPoolOptions{}
	pool := newStartedWSConnPoolForTest(cfg)
	providerID := int64(505)
	ap := pool.getOrCreateProviderPool(providerID)

	var current atomic.Int32
	var maxConcurrent atomic.Int32
	// Share one release gate across all probe transports so overlap reaches the
	// sweep limit before any probe can complete normally.
	release := make(chan struct{})
	for range 25 {
		conn := NewWSConn(pool.nextConnID(providerID), providerID, &openAIWSPingBlockingConn{
			current:       &current,
			maxConcurrent: &maxConcurrent,
			release:       release,
		}, nil, nil, "")
		ap.mu.Lock()
		ap.conns[conn.id] = conn
		ap.mu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		pool.runBackgroundPingSweep()
		close(done)
	}()

	require.Eventually(t, func() bool {
		return maxConcurrent.Load() >= 10
	}, time.Second, 10*time.Millisecond)

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runBackgroundPingSweep 未在释放后完成")
	}

	require.LessOrEqual(t, maxConcurrent.Load(), int32(10))
}

// TestOpenAIWSConnLease_BasicGetterBranches inspects nil and populated leases
// with stored timings and handshake data. Zero defaults, exact observations,
// protocol-prewarm marking, and tolerated release are checked independently of
// acquisition.
func TestOpenAIWSConnLease_BasicGetterBranches(t *testing.T) {
	var nilLease *WSConnLease
	require.Equal(t, "", nilLease.ConnID())
	require.Equal(t, time.Duration(0), nilLease.QueueWaitDuration())
	require.Equal(t, time.Duration(0), nilLease.ConnPickDuration())
	require.False(t, nilLease.Reused())
	require.Equal(t, "", nilLease.HandshakeHeader("x-test"))
	require.False(t, nilLease.IsPrewarmed())
	nilLease.MarkPrewarmed()
	nilLease.Release()

	conn := NewWSConn("getter_conn", 1, &openAIWSFakeConn{}, http.Header{"X-Test": []string{"ok"}}, nil, "")
	lease := &WSConnLease{
		Conn:      conn,
		queueWait: 3 * time.Millisecond,
		connPick:  4 * time.Millisecond,
		reused:    true,
	}
	require.Equal(t, "getter_conn", lease.ConnID())
	require.Equal(t, 3*time.Millisecond, lease.QueueWaitDuration())
	require.Equal(t, 4*time.Millisecond, lease.ConnPickDuration())
	require.True(t, lease.Reused())
	require.Equal(t, "ok", lease.HandshakeHeader("x-test"))
	require.False(t, lease.IsPrewarmed())
	lease.MarkPrewarmed()
	require.True(t, lease.IsPrewarmed())
	lease.Release()
}

// TestOpenAIWSConnPool_UtilityBranches exercises independent metrics, policy
// defaults, failure suppression/reset, typed lookup, and health eligibility.
// Assertions describe those helper outcomes; the fixture does not require one
// consistent concurrent state snapshot.
func TestOpenAIWSConnPool_UtilityBranches(t *testing.T) {
	var nilPool *WSConnPool
	require.Equal(t, WSPoolMetricsSnapshot{}, nilPool.SnapshotMetrics())
	require.Equal(t, openai.WSTransportMetricsSnapshot{}, nilPool.SnapshotTransportMetrics())

	pool := NewWSConnPool(&WSPoolOptions{})
	pool.metrics.acquireTotal.Store(7)
	pool.metrics.acquireReuseTotal.Store(3)
	metrics := pool.SnapshotMetrics()
	require.Equal(t, int64(7), metrics.AcquireTotal)
	require.Equal(t, int64(3), metrics.AcquireReuseTotal)

	// A dialer without transport metrics returns an empty snapshot.
	pool.clientDialer = &openAIWSFakeDialer{}
	require.Equal(t, openai.WSTransportMetricsSnapshot{}, pool.SnapshotTransportMetrics())
	pool.SetClientDialerForTest(nil)
	require.NotNil(t, pool.clientDialer)

	require.Equal(t, 8, nilPool.maxConnsHardCap())
	require.False(t, nilPool.nativeOptions().DynamicMaxConnsEnabled())
	require.Equal(t, 1.0, nilPool.nativeOptions().MaxConnsFactorByProvider(nil))
	require.Equal(t, 0, nilPool.minIdlePerProvider())
	require.Equal(t, 4, nilPool.maxIdlePerProvider())
	require.Equal(t, 256, nilPool.queueLimitPerConn())
	require.Equal(t, 0.7, nilPool.targetUtilization())
	require.Equal(t, time.Duration(0), nilPool.prewarmCooldown())
	require.Equal(t, 10*time.Second, nilPool.dialTimeout())

	// Missing or expired failure times clear suppression; recent repeated failures retain it.
	now := time.Now()
	apNilFail := &openAIWSProviderPool{prewarmFails: 1}
	require.False(t, pool.shouldSuppressPrewarmLocked(apNilFail, now))
	apZeroTime := &openAIWSProviderPool{prewarmFails: 2}
	require.False(t, pool.shouldSuppressPrewarmLocked(apZeroTime, now))
	require.Equal(t, 0, apZeroTime.prewarmFails)
	apOldFail := &openAIWSProviderPool{prewarmFails: 2, prewarmFailAt: now.Add(-openAIWSPrewarmFailureWindow - time.Second)}
	require.False(t, pool.shouldSuppressPrewarmLocked(apOldFail, now))
	apRecentFail := &openAIWSProviderPool{prewarmFails: openAIWSPrewarmFailureSuppress, prewarmFailAt: now}
	require.True(t, pool.shouldSuppressPrewarmLocked(apRecentFail, now))

	// Negative pick durations count as zero; a nil pool ignores them.
	nilPool.recordConnPickDuration(10 * time.Millisecond)
	pool.recordConnPickDuration(-10 * time.Millisecond)
	require.Equal(t, int64(1), pool.metrics.connPickTotal.Load())

	// Invalid provider IDs and unexpected stored values are handled defensively.
	require.Nil(t, nilPool.getOrCreateProviderPool(1))
	require.Nil(t, pool.getOrCreateProviderPool(0))
	pool.providers.Store(int64(7), "invalid")
	ap := pool.getOrCreateProviderPool(7)
	require.NotNil(t, ap)
	_, ok := pool.getProviderPool(0)
	require.False(t, ok)
	_, ok = pool.getProviderPool(12345)
	require.False(t, ok)
	pool.providers.Store(int64(8), "bad-type")
	_, ok = pool.getProviderPool(8)
	require.False(t, ok)

	// Idle health checks require a connection that supports pings without a reader.
	require.False(t, pool.shouldHealthCheckConn(nil))
	conn := NewWSConn("health", 1, &openAIWSFakeConn{}, nil, nil, "")
	conn.lastUsedNano.Store(time.Now().Add(-openAIWSConnHealthCheckIdle - time.Second).UnixNano())
	require.True(t, pool.shouldHealthCheckConn(conn))
	unsafeConn := NewWSConn("unsafe_health", 1, &openAIWSIdlePingUnsupportedConn{}, nil, nil, "")
	unsafeConn.lastUsedNano.Store(time.Now().Add(-openAIWSConnHealthCheckIdle - time.Second).UnixNano())
	require.False(t, pool.shouldHealthCheckConn(unsafeConn))
}

// TestOpenAIWSConn_LeaseAndTimeHelpers_NilAndClosedBranches checks nil
// timestamp/token helpers and one constructed socket. Its token occupancy
// changes on release, and marked closure rejects another acquisition.
func TestOpenAIWSConn_LeaseAndTimeHelpers_NilAndClosedBranches(t *testing.T) {
	var nilConn *WSConn
	nilConn.touch()
	require.Equal(t, time.Time{}, nilConn.createdAt())
	require.Equal(t, time.Time{}, nilConn.lastUsedAt())
	require.Equal(t, time.Duration(0), nilConn.idleDuration(time.Now()))
	require.Equal(t, time.Duration(0), nilConn.age(time.Now()))
	require.False(t, nilConn.isLeased())
	require.False(t, nilConn.isPrewarmed())
	nilConn.markPrewarmed()

	conn := NewWSConn("lease_state", 1, &openAIWSFakeConn{}, nil, nil, "")
	require.True(t, conn.tryAcquire())
	require.True(t, conn.isLeased())
	conn.release()
	require.False(t, conn.isLeased())
	conn.close()
	require.False(t, conn.tryAcquire())
}

// TestOpenAIWSConnLease_ReadWriteNilConnBranches supplies a lease without
// transport association to each read/write wrapper. Every checked operation
// returns the shared closed-resource sentinel before transport work.
func TestOpenAIWSConnLease_ReadWriteNilConnBranches(t *testing.T) {
	lease := &WSConnLease{}
	require.ErrorIs(t, lease.WriteJSON(map[string]any{"k": "v"}, time.Second), errOpenAIWSConnClosed)
	require.ErrorIs(t, lease.WriteJSONContext(context.Background(), map[string]any{"k": "v"}), errOpenAIWSConnClosed)
	_, err := lease.ReadMessage(10 * time.Millisecond)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	_, err = lease.ReadMessageContext(context.Background())
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	_, err = lease.ReadMessageWithContextTimeout(context.Background(), 10*time.Millisecond)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
}

// TestOpenAIWSConnLease_ReleasedLeaseGuards releases one fake lease twice after
// a successful ping. Subsequent read/write/ping entry points all reject that
// released lease through the same closed-resource error.
func TestOpenAIWSConnLease_ReleasedLeaseGuards(t *testing.T) {
	conn := NewWSConn("released_guard", 1, &openAIWSFakeConn{}, nil, nil, "")
	lease := &WSConnLease{Conn: conn}

	require.NoError(t, lease.PingWithTimeout(50*time.Millisecond))

	lease.Release()
	lease.Release() // idempotent

	require.ErrorIs(t, lease.WriteJSON(map[string]any{"k": "v"}, time.Second), errOpenAIWSConnClosed)
	require.ErrorIs(t, lease.WriteJSONContext(context.Background(), map[string]any{"k": "v"}), errOpenAIWSConnClosed)
	require.ErrorIs(t, lease.WriteJSONWithContextTimeout(context.Background(), map[string]any{"k": "v"}, time.Second), errOpenAIWSConnClosed)

	_, err := lease.ReadMessage(10 * time.Millisecond)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	_, err = lease.ReadMessageContext(context.Background())
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	_, err = lease.ReadMessageWithContextTimeout(context.Background(), 10*time.Millisecond)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)

	require.ErrorIs(t, lease.PingWithTimeout(50*time.Millisecond), errOpenAIWSConnClosed)
}

// TestOpenAIWSConnLease_MarkBrokenAfterRelease_NoEviction releases a recorded
// lease before marking it broken. The provider entry remains, protecting the
// sequential released-lease guard from evicting reusable pool capacity.
func TestOpenAIWSConnLease_MarkBrokenAfterRelease_NoEviction(t *testing.T) {
	conn := NewWSConn("released_markbroken", 7, &openAIWSFakeConn{}, nil, nil, "")
	ap := &openAIWSProviderPool{
		conns: map[string]*WSConn{
			conn.id: conn,
		},
	}
	pool := &WSConnPool{}
	pool.providers.Store(int64(7), ap)

	lease := &WSConnLease{
		pool:       pool,
		ProviderID: 7,
		Conn:       conn,
	}

	lease.Release()
	lease.MarkBroken()

	ap.mu.Lock()
	_, exists := ap.conns[conn.id]
	ap.mu.Unlock()
	require.True(t, exists, "released lease should not evict active pool connection")
}

// TestOpenAIWSConn_AdditionalGuardBranches covers nil/closed/missing
// transports, nonpositive budgets, zero clocks, header cloning, and closing
// lists. Assertions protect selected helper defaults and error outcomes, with
// supplied context cases described literally.
func TestOpenAIWSConn_AdditionalGuardBranches(t *testing.T) {
	var nilConn *WSConn
	require.False(t, nilConn.tryAcquire())
	nilConn.release()
	nilConn.close()
	require.Equal(t, "", nilConn.handshakeHeader("x-test"))

	connClosed := NewWSConn("closed_guard", 1, &openAIWSFakeConn{}, nil, nil, "")
	connClosed.close()
	require.ErrorIs(
		t,
		connClosed.writeJSONWithTimeout(context.Background(), map[string]any{"k": "v"}, time.Second),
		errOpenAIWSConnClosed,
	)
	_, err := connClosed.readMessageWithContextTimeout(context.Background(), time.Second)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	require.ErrorIs(t, connClosed.pingWithTimeout(time.Second), errOpenAIWSConnClosed)

	connNoWS := NewWSConn("no_ws", 1, nil, nil, nil, "")
	require.ErrorIs(t, connNoWS.writeJSON(map[string]any{"k": "v"}, context.Background()), errOpenAIWSConnClosed)
	_, err = connNoWS.readMessage(context.Background())
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	require.ErrorIs(t, connNoWS.pingWithTimeout(time.Second), errOpenAIWSConnClosed)
	require.Equal(t, "", connNoWS.handshakeHeader("x-test"))

	connOK := NewWSConn("ok", 1, &openAIWSFakeConn{}, nil, nil, "")
	require.NoError(t, connOK.writeJSON(map[string]any{"k": "v"}, nil))
	_, err = connOK.readMessageWithContextTimeout(context.Background(), 0)
	require.NoError(t, err)
	require.NoError(t, connOK.pingWithTimeout(0))

	connZero := NewWSConn("zero_ts", 1, &openAIWSFakeConn{}, nil, nil, "")
	connZero.createdAtNano.Store(0)
	connZero.lastUsedNano.Store(0)
	require.True(t, connZero.createdAt().IsZero())
	require.True(t, connZero.lastUsedAt().IsZero())
	require.Equal(t, time.Duration(0), connZero.idleDuration(time.Now()))
	require.Equal(t, time.Duration(0), connZero.age(time.Now()))

	require.Nil(t, CloneWSAcquireRequestPtr(nil))
	copied := cloneHeader(http.Header{
		"X-Empty": []string{},
		"X-Test":  []string{"v1"},
	})
	require.Contains(t, copied, "X-Empty")
	require.Nil(t, copied["X-Empty"])
	require.Equal(t, "v1", copied.Get("X-Test"))

	closeOpenAIWSConns([]*WSConn{nil, connOK})
}

// TestOpenAIWSConnLease_MarkBrokenEvictsConn marks a retained active
// association broken. Its provider entry disappears and the connection token
// cannot be acquired again after disposal.
func TestOpenAIWSConnLease_MarkBrokenEvictsConn(t *testing.T) {
	pool := newStartedWSConnPoolForTest(&WSPoolOptions{})
	providerID := int64(5001)
	conn := NewWSConn("broken_me", providerID, &openAIWSFakeConn{}, nil, nil, "")
	ap := pool.getOrCreateProviderPool(providerID)
	ap.mu.Lock()
	ap.conns[conn.id] = conn
	ap.mu.Unlock()

	lease := &WSConnLease{
		pool:       pool,
		ProviderID: providerID,
		Conn:       conn,
	}
	lease.MarkBroken()

	ap.mu.Lock()
	_, exists := ap.conns[conn.id]
	ap.mu.Unlock()
	require.False(t, exists)
	require.False(t, conn.tryAcquire(), "被标记为 broken 的连接应被关闭")
}

// TestOpenAIWSConnPool_TargetConnCountAndPrewarmBranches supplies invalid
// capacity, bounded idle floor, waiters, absent providers, and failed prewarm.
// Assertions protect target limits and failure evidence after a reserved fake
// dial fails.
func TestOpenAIWSConnPool_TargetConnCountAndPrewarmBranches(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 1
	pool := newStartedWSConnPoolForTest(cfg)

	require.Equal(t, 0, pool.targetConnCountLocked(nil, 1))
	ap := &openAIWSProviderPool{conns: map[string]*WSConn{}}
	require.Equal(t, 0, pool.targetConnCountLocked(ap, 0))

	cfg.MinIdlePerProvider = 3
	require.NoError(t, pool.UpdateOptions(*cfg))
	require.Equal(t, 1, pool.targetConnCountLocked(ap, 1), "minIdle 应被 maxConns 截断")

	// Waiters require a target of at least one more than the current connection count.
	cfg.MinIdlePerProvider = 0
	cfg.PoolTargetUtilization = 0.9
	require.NoError(t, pool.UpdateOptions(*cfg))
	busy := NewWSConn("busy_target", 2, &openAIWSFakeConn{}, nil, nil, "")
	require.True(t, busy.tryAcquire())
	busy.waiters.Store(1)
	ap.conns[busy.id] = busy
	target := pool.targetConnCountLocked(ap, 4)
	require.GreaterOrEqual(t, target, len(ap.conns)+1)

	// Skip prewarm dialing when the provider pool is missing.
	req := WSAcquireRequest{
		Provider: &WSPoolProvider{ID: 999, Type: "apikey"},
		WSURL:    "wss://example.com/v1/responses",
	}
	pool.prewarmConns(999, req, 1)

	// Failed prewarm dials increment the failure count.
	providerID := int64(1000)
	cfg.MinIdlePerProvider = 1
	cfg.MaxIdlePerProvider = 1
	failPool := newStartedWSConnPoolForTest(cfg)
	failPool.SetClientDialerForTest(&openAIWSAlwaysFailDialer{})
	apFail := failPool.getOrCreateProviderPool(providerID)
	apFail.mu.Lock()
	apFail.creating = 1
	req.Provider.ID = providerID
	apFail.lastAcquire = CloneWSAcquireRequestPtr(&req)
	apFail.mu.Unlock()
	failPool.prewarmConns(providerID, req, 1)
	apFail.mu.Lock()
	require.GreaterOrEqual(t, apFail.prewarmFails, 1)
	apFail.mu.Unlock()
}

// TestOpenAIWSConnPool_Acquire_ErrorBranches tests invalid pool/URL, nil
// recorded entries, and saturated waiters. Invalid requests fail, nil entries
// yield fresh capacity, and full queue state returns the queue sentinel.
func TestOpenAIWSConnPool_Acquire_ErrorBranches(t *testing.T) {
	var nilPool *WSConnPool
	_, err := nilPool.Acquire(context.Background(), WSAcquireRequest{})
	require.Error(t, err)

	pool := newStartedWSConnPoolForTest(&WSPoolOptions{})
	_, err = pool.Acquire(context.Background(), WSAcquireRequest{
		Provider: &WSPoolProvider{ID: 1},
		WSURL:    "   ",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ws url is empty")

	// Cleaning invalid connection entries frees capacity for a new dial.
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 1
	cfg.QueueLimitPerConn = 1
	fullPool := newStartedWSConnPoolForTest(cfg)
	fullPool.SetClientDialerForTest(&openAIWSFakeDialer{})
	provider := &WSPoolProvider{ID: 2001, Type: "apikey"}
	ap := fullPool.getOrCreateProviderPool(provider.ID)
	ap.mu.Lock()
	ap.conns["nil"] = nil
	ap.lastCleanupAt = time.Now()
	ap.mu.Unlock()
	lease, err := fullPool.Acquire(context.Background(), WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
	})
	require.NoError(t, err)
	require.False(t, lease.Reused())
	lease.Release()

	// Reject acquisition when the waiter limit is reached.
	provider2 := &WSPoolProvider{ID: 2002, Type: "apikey"}
	ap2 := fullPool.getOrCreateProviderPool(provider2.ID)
	conn := NewWSConn("queue_full", provider2.ID, &openAIWSFakeConn{}, nil, nil, "")
	require.True(t, conn.tryAcquire())
	conn.waiters.Store(1)
	ap2.mu.Lock()
	ap2.conns[conn.id] = conn
	ap2.lastCleanupAt = time.Now()
	ap2.mu.Unlock()
	_, err = fullPool.Acquire(context.Background(), WSAcquireRequest{
		Provider: provider2,
		WSURL:    "wss://example.com/v1/responses",
	})
	require.ErrorIs(t, err, openai.ErrOpenAIWSConnQueueFull)
}

// Dial returns a new shared fake connection with zero status and no
// headers/error. It ignores context, URL, proxy, profile, and request headers;
// use it for pool mechanics, not transport validation or cancellation
// assertions.
func (d *openAIWSFakeDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
) (openai.WSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	_ = profile
	return &openAIWSFakeConn{}, 0, nil, nil
}

// newOpenAIWSFirstDialBlockingCaptureDialer creates fresh first-start and
// release gates for a capturing dialer. Callers eventually release the first
// gate or cancel its context, and inspect captured state under the dialer
// mutex.
func newOpenAIWSFirstDialBlockingCaptureDialer() *openAIWSFirstDialBlockingCaptureDialer {
	return &openAIWSFirstDialBlockingCaptureDialer{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
}

// SupportsIdlePingWithoutReader reports false for the reader-dependent
// idle-ping fixture.
func (c *openAIWSIdlePingUnsupportedConn) SupportsIdlePingWithoutReader() bool {
	return false
}

// WriteJSON returns successful fixture output without retaining payload or
// modeling cancellation.
func (c *openAIWSPingBlockingConn) WriteJSON(context.Context, any) error {
	return nil
}

// ReadMessage returns the fixed completed-response bytes used by the ping
// concurrency fixture.
func (c *openAIWSPingBlockingConn) ReadMessage(context.Context) ([]byte, error) {
	return []byte(`{"type":"response.completed","response":{"id":"resp_blocking_ping"}}`), nil
}

// Ping increments shared overlap counters, updates the observed maximum, and
// waits for context or release when counters exist. Missing counters return
// success immediately. Callers share the same atomics across all candidates and
// eventually release the gate.
//
// The maximum uses compare-and-swap while active count is returned in a defer,
// so cancellation and ordinary release both stop counting this probe. Close is
// a no-op and cannot release the wait; pool tests therefore own the gate or
// context lifetime. This exposes actual overlapping probe calls rather than
// total scheduled candidates.
func (c *openAIWSPingBlockingConn) Ping(ctx context.Context) error {
	if c.current == nil || c.maxConcurrent == nil {
		return nil
	}

	// Count active entry before waiting and return it on either outcome; retain the
	// peak so overlap remains observable after the shared release.
	now := c.current.Add(1)
	for {
		prev := c.maxConcurrent.Load()
		if now <= prev || c.maxConcurrent.CompareAndSwap(prev, now) {
			break
		}
	}
	defer c.current.Add(-1)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.release:
		return nil
	}
}

// Close returns nil for the ping fixture and does not release its gate.
// Existing Ping calls still depend on caller context or release-channel
// completion; use the fixture shutdown gate before waiting for them.
func (c *openAIWSPingBlockingConn) Close() error {
	return nil
}

// Dial counts attempts and clones supplied headers under its mutex, holding
// only the first call on the fixture gate. The first-start channel closes
// before waiting; cancellation returns its error without recording a new
// connection.
//
// Other dial calls can complete while that first request is held. Successfully
// created fake pointers are appended under the mutex after the gate releases.
// Captured headers remain stable copies, while connection objects are shared
// references whose own state needs their mutex. Callers release/cancel the held
// first request before fixture cleanup waits for pool resources.
func (d *openAIWSFirstDialBlockingCaptureDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
) (openai.WSClientConn, int, http.Header, error) {
	_ = wsURL
	_ = proxyURL
	_ = profile
	// Count and clone headers before first-start publication, letting concurrent
	// dials proceed while the fixture holds the first request on its gate.
	d.mu.Lock()
	d.dialCount++
	dialNumber := d.dialCount
	d.headers = append(d.headers, cloneHeader(headers))
	d.mu.Unlock()
	if dialNumber == 1 {
		close(d.firstStarted)
		select {
		case <-ctx.Done():
			return nil, 0, nil, ctx.Err()
		case <-d.releaseFirst:
		}
	}
	conn := &openAIWSFakeConn{}
	d.mu.Lock()
	d.connections = append(d.connections, conn)
	d.mu.Unlock()
	return conn, 0, nil, nil
}

// DialCount reads the captured attempt count under the fixture mutex. It can
// count a canceled first call before any socket is appended, so consumers
// distinguish attempts from recorded connection objects.
func (d *openAIWSFirstDialBlockingCaptureDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

// Dial increments its synchronized attempt count and returns nil transport,
// HTTP 503, and the fixed dial failure. Request parameters/context are ignored;
// this fixture models suppression evidence rather than transport cancellation
// behavior.
func (d *openAIWSAlwaysFailDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
) (openai.WSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	_ = profile
	// Record the failed attempt under the same lock used by DialCount before
	// returning its fixed error for later suppression assertions.
	d.mu.Lock()
	d.dialCount++
	d.mu.Unlock()
	return nil, 503, nil, errors.New("dial failed")
}

// DialCount returns the synchronized number of always-failing dial attempts.
// The counter advances before each fixed error is returned, allowing admission
// tests to inspect completed or currently observed attempt evidence.
func (d *openAIWSAlwaysFailDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

// WriteJSON succeeds immediately for the delayed-read fixture while ignoring
// context and value.
func (c *openAIWSBlockingConn) WriteJSON(ctx context.Context, value any) error {
	_ = ctx
	_ = value
	return nil
}

// ReadMessage waits for its configured delay or caller cancellation, then
// returns the fixed completed response. Nonpositive delay selects ten
// milliseconds. Close does not release this wait, so tests retain context/timer
// ownership until reading finishes.
func (c *openAIWSBlockingConn) ReadMessage(ctx context.Context) ([]byte, error) {
	delay := c.readDelay
	if delay <= 0 {
		delay = 10 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return []byte(`{"type":"response.completed","response":{"id":"resp_blocking"}}`), nil
	}
}

// Ping succeeds immediately for the delayed-read fixture, independently of its
// pending read.
func (c *openAIWSBlockingConn) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

// Close returns nil for the delayed-read fixture and leaves pending reads to
// their timer/context. It provides no transport-unblocking effect, so callers
// cannot infer real socket close behavior from this stub.
func (c *openAIWSBlockingConn) Close() error {
	return nil
}

// WriteJSON waits exclusively for the supplied context to finish and returns
// its error. Pass a nonnil cancelable context; Close is a no-op, so fixture
// owners cancel or set a deadline before waiting for write completion.
func (c *openAIWSWriteBlockingConn) WriteJSON(ctx context.Context, _ any) error {
	<-ctx.Done()
	return ctx.Err()
}

// ReadMessage returns fixed completed-response bytes independently of the
// blocked-write fixture state.
func (c *openAIWSWriteBlockingConn) ReadMessage(context.Context) ([]byte, error) {
	return []byte(`{"type":"response.completed","response":{"id":"resp_write_block"}}`), nil
}

// Ping reports immediate success for the write-cancellation fixture without
// consulting context.
func (c *openAIWSWriteBlockingConn) Ping(context.Context) error {
	return nil
}

// Close returns nil for the write-cancellation fixture and does not unblock
// WriteJSON. The caller owns the write context that eventually releases its
// wait; this stub models cancellation rather than socket disposal.
func (c *openAIWSWriteBlockingConn) Close() error {
	return nil
}

// WriteJSON reports successful fixture output while leaving the failing-ping
// state unchanged.
func (c *openAIWSPingFailConn) WriteJSON(context.Context, any) error {
	return nil
}

// ReadMessage returns the fixed response used independently of failed
// health-probe assertions.
func (c *openAIWSPingFailConn) ReadMessage(context.Context) ([]byte, error) {
	return []byte(`{"type":"response.completed","response":{"id":"resp_ping_fail"}}`), nil
}

// Ping returns the fixed probe error for every failing-health fixture
// invocation.
func (c *openAIWSPingFailConn) Ping(context.Context) error {
	return errors.New("ping failed")
}

// Close returns nil after failing health probes without modeling a transport
// resource.
func (c *openAIWSPingFailConn) Close() error {
	return nil
}

// WriteJSON stores its supplied context and reports successful fixture output.
// Callers serialize probe access through the connection wrapper before
// inspecting lastWriteCtx, because the fixture itself has no mutex.
func (c *openAIWSContextProbeConn) WriteJSON(ctx context.Context, _ any) error {
	c.lastWriteCtx = ctx
	return nil
}

// ReadMessage returns fixed completed-response bytes for the write-context
// capture fixture.
func (c *openAIWSContextProbeConn) ReadMessage(context.Context) ([]byte, error) {
	return []byte(`{"type":"response.completed","response":{"id":"resp_ctx_probe"}}`), nil
}

// Ping returns successful health status for the context-capture fixture without
// inspecting context.
func (c *openAIWSContextProbeConn) Ping(context.Context) error {
	return nil
}

// Close returns nil for the context probe without clearing captured write
// context.
func (c *openAIWSContextProbeConn) Close() error {
	return nil
}

// Dial returns nil transport with HTTP 200 and nil error, ignoring request
// inputs. It models the inconsistent dialer outcome that acquisition must
// reject rather than a successful usable WebSocket.
func (d *openAIWSNilConnDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
) (openai.WSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	_ = profile
	return nil, 200, nil, nil
}

// TestOpenAIWSConnPool_DialConnNilConnection installs a dialer returning nil
// transport with HTTP 200 and no error. Acquisition turns that inconsistent
// result into an error containing the nil-connection diagnostic.
func TestOpenAIWSConnPool_DialConnNilConnection(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 2
	cfg.DialTimeoutSeconds = 1

	pool := newStartedWSConnPoolForTest(cfg)
	pool.SetClientDialerForTest(&openAIWSNilConnDialer{})
	provider := &WSPoolProvider{ID: 91, Type: "apikey"}

	_, err := pool.Acquire(context.Background(), WSAcquireRequest{
		Provider: provider,
		WSURL:    "wss://example.com/v1/responses",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil connection")
}

// TestOpenAIWSConnPool_SnapshotTransportMetrics requests proxy clients for
// repeated and distinct URLs. The pool exposes one hit, two misses, and a
// one-third reuse ratio within the specified float tolerance.
func TestOpenAIWSConnPool_SnapshotTransportMetrics(t *testing.T) {
	cfg := &WSPoolOptions{}
	pool := newStartedWSConnPoolForTest(cfg)

	dialer, ok := pool.clientDialer.(*openai.CoderWSClientDialer)
	require.True(t, ok)

	_, err := dialer.ProxyHTTPClient("http://127.0.0.1:28080", nil)
	require.NoError(t, err)
	_, err = dialer.ProxyHTTPClient("http://127.0.0.1:28080", nil)
	require.NoError(t, err)
	_, err = dialer.ProxyHTTPClient("http://127.0.0.1:28081", nil)
	require.NoError(t, err)

	snapshot := pool.SnapshotTransportMetrics()
	require.Equal(t, int64(1), snapshot.ProxyClientCacheHits)
	require.Equal(t, int64(2), snapshot.ProxyClientCacheMisses)
	// Float tolerance applies to the derived reuse ratio; integer hit/miss
	// counters above establish the three-request fixture independently of rounding in that ratio.
	require.InDelta(t, 1.0/3.0, snapshot.TransportReuseRatio, 0.0001)
}

// newStartedWSConnPoolForTest constructs a pool and starts its periodic workers
// before returning it. It installs no automatic test cleanup; fixture owners
// call Close when they need worker/resource completion before their test ends.
func newStartedWSConnPoolForTest(options *WSPoolOptions) *WSConnPool {
	p := NewWSConnPool(options)
	p.Start()
	return p
}

// TestOpenAIWSConnPoolHeadersFactoryRunsAtDialAndStalePrewarmIsDiscarded
// generates authorization inside a direct prewarm dial. Clearing advances
// generation and removes target/sockets, then an old-generation batch cannot
// reenter provider state.
func TestOpenAIWSConnPoolHeadersFactoryRunsAtDialAndStalePrewarmIsDiscarded(t *testing.T) {
	cfg := &WSPoolOptions{}
	cfg.MaxConnsPerProvider = 1
	cfg.MinIdlePerProvider = 1
	cfg.MaxIdlePerProvider = 1
	pool := newStartedWSConnPoolForTest(cfg)
	defer pool.Close()
	pool.SetClientDialerForTest(&openAIWSFakeDialer{})

	providerID := int64(22)
	ap := pool.getOrCreateProviderPool(providerID)
	factoryCalls := 0
	latestHeader := ""
	req := WSAcquireRequest{
		Provider: &WSPoolProvider{ID: providerID, Type: "oauth"},
		WSURL:    "wss://example.com/v1/responses",
		HeadersFactory: func(_ context.Context, headers http.Header) (http.Header, error) {
			factoryCalls++
			// Generate one character of fake authorization identity for the first
			// asserted dial, keeping this fixture focused on factory invocation rather
			// than real signing.
			latestHeader = "AgentAssertion dial-" + string(rune('0'+factoryCalls))
			if headers == nil {
				headers = make(http.Header)
			}
			headers.Set("Authorization", latestHeader)
			return headers, nil
		},
	}
	ap.mu.Lock()
	ap.lastAcquire = &req
	generation := ap.generation
	ap.mu.Unlock()

	pool.prewarmConns(providerID, req, 1, generation)
	require.Equal(t, 1, factoryCalls, "prewarm must generate authorization inside the actual dial")
	require.Equal(t, "AgentAssertion dial-1", latestHeader)

	pool.ClearProvider(providerID)
	ap.mu.Lock()
	require.Empty(t, ap.conns, "credential recovery must remove pooled connections")
	require.Nil(t, ap.lastAcquire, "credential recovery must discard delayed acquire state")
	require.Equal(t, generation+1, ap.generation)
	ap.mu.Unlock()

	// Reuse the captured old generation after clearing its target, checking
	// that delayed prewarm work cannot publish sockets into the replacement provider state.
	pool.prewarmConns(providerID, req, 1, generation)
	ap.mu.Lock()
	require.Empty(t, ap.conns)
	ap.mu.Unlock()
}
