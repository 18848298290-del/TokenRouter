package ws

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestWSPoolWaiterLimitsAndCancellation checks queue slots for ordinary, required, and incompatible requests.
func TestWSPoolWaiterLimitsAndCancellation(t *testing.T) {
	for _, mode := range []string{"ordinary", "required", "incompatible"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pool, req := newWSReuseTestPool(1, 1)
				defer pool.Close()
				first, err := pool.Acquire(context.Background(), req)
				require.NoError(t, err)
				defer first.Release()
				switch mode {
				case "required":
					req.PreferredConnID = first.ConnID()
					req.ForcePreferredConn = true
				case "incompatible":
					req.WSURL = "wss://new.example/v1/responses"
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer func() {
					cancel()
					synctest.Wait()
				}()
				one := acquireWSInBackground(pool, ctx, req)
				two := acquireWSInBackground(pool, ctx, req)
				synctest.Wait()
				_, waiters, _ := pool.ProviderPoolLoad(req.Provider.ID)
				require.Equal(t, 2, waiters)
				_, err = pool.Acquire(ctx, req)
				require.ErrorIs(t, err, openai.ErrOpenAIWSConnQueueFull)
				cancel()
				synctest.Wait()
				require.ErrorIs(t, (<-one).err, context.Canceled)
				require.ErrorIs(t, (<-two).err, context.Canceled)
				_, waiters, _ = pool.ProviderPoolLoad(req.Provider.ID)
				require.Zero(t, waiters)
				first.Release()
				next, err := pool.Acquire(context.Background(), req)
				require.NoError(t, err)
				next.Release()
			})
		})
	}
}

// TestWSPoolWakeupsKeepOriginalDeadline checks that option wakeups retain the acquisition deadline and queue count.
func TestWSPoolWakeupsKeepOriginalDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, req := newWSReuseTestPool(1, 1)
		defer pool.Close()
		first, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		defer first.Release()
		started := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer func() {
			cancel()
			synctest.Wait()
		}()
		result := acquireWSInBackground(pool, ctx, req)
		synctest.Wait()
		for range 4 {
			time.Sleep(200 * time.Millisecond)
			require.NoError(t, pool.UpdateOptions(*pool.nativeOptions()))
			synctest.Wait()
		}
		got := <-result
		require.ErrorIs(t, got.err, context.DeadlineExceeded)
		require.Equal(t, time.Second, time.Since(started))
		metrics := pool.SnapshotMetrics()
		require.Equal(t, int64(1), metrics.AcquireQueueWaitTotal)
		require.Equal(t, int64(1000), metrics.AcquireQueueWaitMsTotal)
		_, waiters, _ := pool.ProviderPoolLoad(req.Provider.ID)
		require.Zero(t, waiters)
	})
}

// TestWSPoolRequiredWaiterIgnoresOtherConnection checks that continuation waits for its required connection.
func TestWSPoolRequiredWaiterIgnoresOtherConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, req := newWSReuseTestPool(2, 2)
		defer pool.Close()
		first, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		defer first.Release()
		second, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		defer second.Release()
		req.PreferredConnID = first.ConnID()
		req.ForcePreferredConn = true
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
			if got.lease != nil {
				got.lease.Release()
			}
			t.Fatal("指定连接尚未释放，续接请求提前结束等待")
		default:
		}
		first.Release()
		synctest.Wait()
		got := <-result
		require.NoError(t, got.err)
		require.Equal(t, first.ConnID(), got.lease.ConnID())
		got.lease.Release()
	})
}

// TestWSPoolCanceledDeliveryReturnsCapacity races cancellation with release and checks lease and waiter cleanup.
func TestWSPoolCanceledDeliveryReturnsCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, req := newWSReuseTestPool(1, 1)
		defer pool.Close()
		for range 32 {
			first, err := pool.Acquire(context.Background(), req)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			result := acquireWSInBackground(pool, ctx, req)
			synctest.Wait()
			go cancel()
			go first.Release()
			synctest.Wait()
			got := <-result
			if got.lease != nil {
				require.NoError(t, got.err)
				got.lease.Release()
			} else {
				require.ErrorIs(t, got.err, context.Canceled)
			}
			state, _ := pool.SnapshotProviderState(req.Provider.ID)
			require.Equal(t, 1, state.Connections)
			require.Zero(t, state.LeasedConnections)
			_, waiters, _ := pool.ProviderPoolLoad(req.Provider.ID)
			require.Zero(t, waiters)
		}
	})
}

// TestWSPoolCloseWakesEveryWaiter checks that all waiter kinds exit on shutdown.
func TestWSPoolCloseWakesEveryWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, req := newWSReuseTestPool(2, 2)
		defer pool.Close()
		first, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		defer first.Release()
		second, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		defer second.Release()
		ordinary := acquireWSInBackground(pool, context.Background(), req)
		req.PreferredConnID = first.ConnID()
		req.ForcePreferredConn = true
		required := acquireWSInBackground(pool, context.Background(), req)
		req.PreferredConnID = ""
		req.ForcePreferredConn = false
		req.WSURL = "wss://new.example/v1/responses"
		incompatible := acquireWSInBackground(pool, context.Background(), req)
		synctest.Wait()
		pool.Close()
		synctest.Wait()
		for _, result := range []<-chan wsAcquireTestResult{ordinary, required, incompatible} {
			require.ErrorIs(t, (<-result).err, errOpenAIWSConnClosed)
		}
		_, waiters, _ := pool.ProviderPoolLoad(req.Provider.ID)
		require.Zero(t, waiters)
	})
}
