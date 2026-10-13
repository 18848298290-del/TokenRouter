package ws

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestConnectionsShrinkDoesNotBlockPoolAccess blocks an evicted connection
// close during owner option shrink. Another goroutine must retrieve the same
// pool before the handshake is released, and the update then completes
// successfully.
func TestConnectionsShrinkDoesNotBlockPoolAccess(t *testing.T) {
	initial := &WSPoolOptions{MaxConnsPerProvider: 2, MaxIdlePerProvider: 2}
	pool := NewWSConnPool(initial)
	owner := NewOpenAIWSConnections(initial, nil, pool)
	t.Cleanup(owner.Close)
	slow := &delayedCloseConn{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(slow.release) }) }
	t.Cleanup(unblock)
	provider := pool.getOrCreateProviderPool(1)
	provider.conns["old"] = NewWSConn("old", 1, slow, nil, nil, "")
	provider.conns["old"].lastUsedNano.Store(time.Now().Add(-time.Second).UnixNano())
	provider.conns["new"] = NewWSConn("new", 1, nil, nil, nil, "")
	updated := make(chan error, 1)
	// Start shrinking independently so the test can hold the evicted socket
	// in its close handshake while another goroutine checks access to the owner pool.
	go func() {
		updated <- owner.UpdateOptions(WSPoolOptions{MaxConnsPerProvider: 1, MaxIdlePerProvider: 1})
	}()
	select {
	case <-slow.entered:
	case <-time.After(time.Second):
		t.Fatal("关闭握手没有开始")
	}
	available := make(chan *WSConnPool, 1)
	go func() { available <- owner.Pool() }()
	select {
	case actual := <-available:
		require.Same(t, pool, actual)
	case <-time.After(time.Second):
		t.Error("获取连接池被上游关闭握手阻塞")
	}
	unblock()
	require.NoError(t, <-updated)
}

// TestConnectionsConcurrentUpdatesKeepOwnerAndPoolConsistent races four option
// writers and waits for all of them. It compares final owner/pool options,
// checks updates fail after Close, and verifies lookup retains the stopped
// pool.
func TestConnectionsConcurrentUpdatesKeepOwnerAndPoolConsistent(t *testing.T) {
	owner := NewOpenAIWSConnections(&WSPoolOptions{MaxConnsPerProvider: 4}, nil)
	t.Cleanup(owner.Close)
	pool := owner.Pool()
	var workers sync.WaitGroup
	for worker := range 4 {
		workers.Go(func() {
			for i := range 25 {
				if err := owner.UpdateOptions(WSPoolOptions{MaxConnsPerProvider: 1 + worker*25 + i}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	// Join all option publishers before reading the exported owner pointer;
	// these direct test reads require writers to finish to compare consistent final state.
	workers.Wait()
	require.Equal(t, *owner.Options, *pool.nativeOptions())
	owner.Close()
	require.Error(t, owner.UpdateOptions(WSPoolOptions{MaxConnsPerProvider: 8}))
	require.Same(t, pool, owner.Pool())
}
