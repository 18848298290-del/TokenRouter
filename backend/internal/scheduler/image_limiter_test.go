package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestImageConcurrencyLimiter_DefaultDisabledAllowsRequests uses a zero-value
// limiter with limiting disabled. It requires admission and a nil release
// handle because that request does not reserve local image capacity.
func TestImageConcurrencyLimiter_DefaultDisabledAllowsRequests(t *testing.T) {
	limiter := &ImageConcurrencyLimiter{}

	release, acquired := limiter.TryAcquire(false, 1)

	require.True(t, acquired)
	require.Nil(t, release)
}

// TestImageConcurrencyLimiter_RejectsWhenLimitReachedAndAllowsAfterRelease
// fills a one-slot limiter and attempts another immediate acquisition. It
// checks rejection at capacity and renewed admission after the first release
// returns that slot.
func TestImageConcurrencyLimiter_RejectsWhenLimitReachedAndAllowsAfterRelease(t *testing.T) {
	limiter := &ImageConcurrencyLimiter{}

	release, acquired := limiter.TryAcquire(true, 1)
	require.True(t, acquired)
	require.NotNil(t, release)

	secondRelease, secondAcquired := limiter.TryAcquire(true, 1)
	require.False(t, secondAcquired)
	require.Nil(t, secondRelease)

	release()
	thirdRelease, thirdAcquired := limiter.TryAcquire(true, 1)
	require.True(t, thirdAcquired)
	require.NotNil(t, thirdRelease)
	thirdRelease()
}

// TestImageConcurrencyLimiter_WaitsUntilSlotReleased launches another
// acquisition while holding local image capacity. It returns the first slot
// and requires the asynchronous request to supply a nonnil release handle
// within the test budget.
func TestImageConcurrencyLimiter_WaitsUntilSlotReleased(t *testing.T) {
	limiter := &ImageConcurrencyLimiter{}
	release, acquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)
	require.True(t, acquired)
	require.NotNil(t, release)

	acquiredCh := make(chan func(), 1)
	// Pass the asynchronous request's release handle through the buffered
	// channel so the main test can observe admission and return its capacity.
	// The held slot makes early acquisition attempts wait.
	go func() {
		waitRelease, waitAcquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)
		require.True(t, waitAcquired)
		acquiredCh <- waitRelease
	}()

	time.Sleep(20 * time.Millisecond)
	release()

	select {
	case waitRelease := <-acquiredCh:
		require.NotNil(t, waitRelease)
		waitRelease()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for image concurrency slot")
	}
}

// TestImageConcurrencyLimiter_WaitTimesOut holds the available image slot and
// requests bounded waiting with a short timeout. The waiter must return denied
// admission and no release handle while the held slot remains occupied.
func TestImageConcurrencyLimiter_WaitTimesOut(t *testing.T) {
	limiter := &ImageConcurrencyLimiter{}
	release, acquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)
	require.True(t, acquired)
	require.NotNil(t, release)
	defer release()

	waitRelease, waitAcquired := limiter.Acquire(context.Background(), true, 1, true, 10*time.Millisecond, 1)

	require.False(t, waitAcquired)
	require.Nil(t, waitRelease)
}

// TestImageConcurrencyLimiter_MaxWaitingRequestsRejectsOverflow holds capacity
// and gives one asynchronous request a brief head start before testing
// another. It requires overflow rejection, then releases capacity and waits
// for the first request's completion signal.
func TestImageConcurrencyLimiter_MaxWaitingRequestsRejectsOverflow(t *testing.T) {
	limiter := &ImageConcurrencyLimiter{}
	release, acquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)
	require.True(t, acquired)
	require.NotNil(t, release)
	defer release()

	waitingStarted := make(chan struct{})
	waitingDone := make(chan struct{})
	// Announce goroutine startup before testing overflow, then wait for its
	// completion signal after returning capacity. The brief delay gives its
	// Acquire call an opportunity to occupy the single waiting position.
	go func() {
		close(waitingStarted)
		waitRelease, waitAcquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)
		if waitAcquired && waitRelease != nil {
			waitRelease()
		}
		close(waitingDone)
	}()
	<-waitingStarted
	time.Sleep(20 * time.Millisecond)

	overflowRelease, overflowAcquired := limiter.Acquire(context.Background(), true, 1, true, time.Second, 1)

	require.False(t, overflowAcquired)
	require.Nil(t, overflowRelease)
	release()
	<-waitingDone
}
