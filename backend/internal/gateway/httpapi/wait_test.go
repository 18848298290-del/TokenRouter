package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	httptestkit "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/testkit"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// TestConcurrencyHelper_TryAcquireUserSlot injects successful user acquisition
// through the hook store. The helper returns ownership and invoking its release
// increments the underlying cleanup count.
func TestConcurrencyHelper_TryAcquireUserSlot(t *testing.T) {
	cache := &httptestkit.ConcurrencyHooks{
		AcquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
	}
	helper := NewConcurrencyHelper(scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf:  logging.LegacyPrintf,
		Event: logging.Event,
	},
	), SSEPingFormatNone, time.Second)

	release, acquired, err := helper.TryAcquireUserSlot(context.Background(), 101, 2)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, release)

	release()
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.ReleaseUserCalled))
}

// TestConcurrencyHelper_TryAcquireProviderSlot_NotAcquired injects busy
// provider capacity. It requires false admission, nil release and no cleanup
// attempt when the fixture never granted a slot.
func TestConcurrencyHelper_TryAcquireProviderSlot_NotAcquired(t *testing.T) {
	cache := &httptestkit.ConcurrencyHooks{
		AcquireProviderSlotFn: func(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error) {
			return false, nil
		},
	}
	helper := NewConcurrencyHelper(scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf:  logging.LegacyPrintf,
		Event: logging.Event,
	},
	), SSEPingFormatNone, time.Second)

	release, acquired, err := helper.TryAcquireProviderSlot(context.Background(), 201, 1)
	require.NoError(t, err)
	require.False(t, acquired)
	require.Nil(t, release)
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.ReleaseProviderCalled))
}

// newHelperTestContext builds a Gin HTTP request and response recorder.
func newHelperTestContext(method, path string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, path, nil)
	return c, rec
}

// TestWaitForSlotWithPingTimeout_ProviderAndUserAcquire supplies rejection
// followed by admission for both slot kinds. Each wait retries acquisition and
// returns a release while nonstreaming requests keep stream state unchanged.
func TestWaitForSlotWithPingTimeout_ProviderAndUserAcquire(t *testing.T) {
	cache := &httptestkit.ConcurrencySequence{
		ProviderSeq: []bool{false, true},
		UserSeq:     []bool{false, true},
	}
	concurrency := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)

	t.Run("provider_slot_acquired_after_retry", func(t *testing.T) {
		c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
		streamStarted := false
		release, err := helper.WaitForSlotWithPingTimeout(c, "provider", 101, 2, time.Second, false, &streamStarted, true)
		require.NoError(t, err)
		require.NotNil(t, release)
		require.False(t, streamStarted)
		release()
		require.GreaterOrEqual(t, cache.ProviderAcquireCalls, 2)
		require.GreaterOrEqual(t, cache.ProviderReleaseCalls, 1)
	})

	t.Run("user_slot_acquired_after_retry", func(t *testing.T) {
		c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
		streamStarted := false
		release, err := helper.WaitForSlotWithPingTimeout(c, "user", 202, 3, time.Second, false, &streamStarted, true)
		require.NoError(t, err)
		require.NotNil(t, release)
		release()
		require.GreaterOrEqual(t, cache.UserAcquireCalls, 2)
		require.GreaterOrEqual(t, cache.UserReleaseCalls, 1)
	})
}

// TestAcquireUserSlotWithWait_ImmediateAcquireSkipsWaitQueue grants configured
// user capacity on the first attempt. It requires one acquisition and release
// while both queue-registration and queue-removal counters remain zero.
func TestAcquireUserSlotWithWait_ImmediateAcquireSkipsWaitQueue(t *testing.T) {
	cache := &httptestkit.ConcurrencySequence{
		UserSeq: []bool{true},
	}
	concurrency := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)
	c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	streamStarted := false
	ctx, stages := timing.WithStages(c.Request.Context(), time.Now(), nil)
	c.Request = c.Request.WithContext(scheduler.WithWaitObservation(ctx, stages.Wait))

	release, err := helper.AcquireUserSlotWithWaitTimeout(c, 202, 3, time.Second, false, &streamStarted)
	require.NoError(t, err)
	require.NotNil(t, release)
	release()

	require.Equal(t, 1, cache.UserAcquireCalls)
	require.Equal(t, 0, cache.WaitIncrementCalls)
	require.Equal(t, 0, cache.WaitDecrementCalls)
	require.Equal(t, 1, cache.UserReleaseCalls)
}

// TestAcquireUserSlotWithWait_TracksAPIKeySlot binds an effective Key before
// user admission. It requires tracking that Key identity and releasing both
// user capacity and Key statistics afterward.
func TestAcquireUserSlotWithWait_TracksAPIKeySlot(t *testing.T) {
	cache := &httptestkit.ConcurrencySequence{
		UserSeq: []bool{true},
	}
	concurrency := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)
	c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	c.Set("gateway_effective_key", &apikey.APIKey{ID: 77})
	streamStarted := false
	ctx, stages := timing.WithStages(c.Request.Context(), time.Now(), nil)
	c.Request = c.Request.WithContext(scheduler.WithWaitObservation(ctx, stages.Wait))

	release, err := helper.AcquireUserSlotWithWaitTimeout(c, 202, 3, time.Second, false, &streamStarted)
	require.NoError(t, err)
	require.NotNil(t, release)
	require.Equal(t, 1, cache.APIKeyTrackCalls)
	require.Equal(t, []int64{77}, cache.APIKeyTrackIDs)

	release()

	require.Equal(t, 1, cache.UserReleaseCalls)
	require.Equal(t, 1, cache.APIKeyReleaseCalls)
}

// TestTryAcquireUserSlotForAPIKey_TracksAPIKeySlot passes a Key ID during
// immediate user acquisition. It protects paired user and Key-statistics
// ownership and cleanup through the returned callback.
func TestTryAcquireUserSlotForAPIKey_TracksAPIKeySlot(t *testing.T) {
	cache := &httptestkit.ConcurrencySequence{
		UserSeq: []bool{true},
	}
	concurrency := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)

	release, acquired, err := helper.TryAcquireUserSlotForAPIKey(context.Background(), 202, 3, 77)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, release)
	require.Equal(t, 1, cache.APIKeyTrackCalls)
	require.Equal(t, []int64{77}, cache.APIKeyTrackIDs)

	release()

	require.Equal(t, 1, cache.UserReleaseCalls)
	require.Equal(t, 1, cache.APIKeyReleaseCalls)
}

// TestAcquireUserSlotWithWait_WaitSuccessDecrementsBeforeReturn grants capacity
// after one rejected attempt and admitted wait entry. Queue occupancy must be
// removed before return, with user cleanup retained until release.
func TestAcquireUserSlotWithWait_WaitSuccessDecrementsBeforeReturn(t *testing.T) {
	cache := &httptestkit.ConcurrencySequence{
		UserSeq:     []bool{false, true},
		WaitAllowed: true,
	}
	concurrency := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)
	c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	streamStarted := false
	ctx, stages := timing.WithStages(c.Request.Context(), time.Now(), nil)
	c.Request = c.Request.WithContext(scheduler.WithWaitObservation(ctx, stages.Wait))

	release, err := helper.AcquireUserSlotWithWaitTimeout(c, 202, 3, time.Second, false, &streamStarted)
	require.NoError(t, err)
	require.NotNil(t, release)

	require.Equal(t, 2, cache.UserAcquireCalls)
	require.Equal(t, 1, cache.WaitIncrementCalls)
	require.GreaterOrEqual(t, stages.Snapshot()["user_slot_wait_ms"], int64(100))
	require.Equal(t, int64(0), stages.Snapshot()["provider_slot_wait_ms"])
	require.Equal(t, 20, cache.WaitMaxWait)
	require.Equal(t, 1, cache.WaitDecrementCalls)

	release()
	require.Equal(t, 1, cache.UserReleaseCalls)
}

// TestAcquireUserSlotWithWait_WaitQueueFull rejects user capacity and queue
// admission. It requires a user queue-full error and no decrement for the wait
// count that was never acquired.
func TestAcquireUserSlotWithWait_WaitQueueFull(t *testing.T) {
	cache := &httptestkit.ConcurrencySequence{
		UserSeq: []bool{false},
	}
	concurrency := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)
	c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	streamStarted := false
	ctx, stages := timing.WithStages(c.Request.Context(), time.Now(), nil)
	c.Request = c.Request.WithContext(scheduler.WithWaitObservation(ctx, stages.Wait))

	release, err := helper.AcquireUserSlotWithWaitTimeout(c, 202, 3, time.Second, false, &streamStarted)
	require.Nil(t, release)
	var waitErr *WaitQueueFullError
	require.ErrorAs(t, err, &waitErr)
	require.Equal(t, "user", waitErr.SlotType)
	require.Equal(t, 1, cache.WaitIncrementCalls)
	require.Equal(t, 0, cache.WaitDecrementCalls)
}

// TestAcquireUserSlotWithWait_TimeoutDecrementsWaitQueue keeps user capacity
// busy through the timeout. It requires timed-out concurrency classification,
// one queue removal and no user release for an unacquired slot.
func TestAcquireUserSlotWithWait_TimeoutDecrementsWaitQueue(t *testing.T) {
	cache := &httptestkit.ConcurrencySequence{
		UserSeq:     []bool{false, false, false},
		WaitAllowed: true,
	}
	concurrency := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)
	c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	streamStarted := false

	release, err := helper.AcquireUserSlotWithWaitTimeout(c, 202, 3, 30*time.Millisecond, false, &streamStarted)
	require.Nil(t, release)
	var cErr *ConcurrencyError
	require.ErrorAs(t, err, &cErr)
	require.True(t, cErr.IsTimeout)
	require.Equal(t, 1, cache.WaitIncrementCalls)
	require.Equal(t, 1, cache.WaitDecrementCalls)
	require.Equal(t, 0, cache.UserReleaseCalls)
}

// TestAcquireUserSlotWithWait_RequestCancelDecrementsWaitQueue cancels from the
// wait-registration hook. It requires the cancellation cause, confirmed queue
// cleanup and no release of user capacity that was never acquired.
func TestAcquireUserSlotWithWait_RequestCancelDecrementsWaitQueue(t *testing.T) {
	cancelled := make(chan struct{})
	var cancel context.CancelFunc
	cache := &httptestkit.ConcurrencySequence{
		UserSeq:     []bool{false, false},
		WaitAllowed: true,
		// Cancel at queue registration so the test observes cleanup of confirmed
		// waiting ownership before any later capacity grant can occur.
		WaitIncrementHook: func() {
			cancel()
			close(cancelled)
		},
	}
	concurrency := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)
	c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	reqCtx, cancelFunc := context.WithCancel(c.Request.Context())
	cancel = cancelFunc
	defer cancel()
	c.Request = c.Request.WithContext(reqCtx)
	streamStarted := false
	ctx, stages := timing.WithStages(c.Request.Context(), time.Now(), nil)
	c.Request = c.Request.WithContext(scheduler.WithWaitObservation(ctx, stages.Wait))

	release, err := helper.AcquireUserSlotWithWaitTimeout(c, 202, 3, time.Second, false, &streamStarted)
	<-cancelled
	require.Nil(t, release)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, cache.WaitIncrementCalls)
	require.Equal(t, 1, cache.WaitDecrementCalls)
	require.Equal(t, 0, cache.UserReleaseCalls)
}

// TestWaitForSlotWithPingTimeout_TimeoutAndStreamPing keeps provider capacity
// busy in ordinary and streaming waits. Both time out, while the streaming case
// writes comment heartbeats and sets its commitment flag.
func TestWaitForSlotWithPingTimeout_TimeoutAndStreamPing(t *testing.T) {
	cache := &httptestkit.ConcurrencySequence{
		ProviderSeq: []bool{false, false, false},
	}
	concurrency := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)

	t.Run("timeout_returns_concurrency_error", func(t *testing.T) {
		helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)
		c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
		streamStarted := false
		release, err := helper.WaitForSlotWithPingTimeout(c, "provider", 101, 2, 130*time.Millisecond, false, &streamStarted, true)
		require.Nil(t, release)
		var cErr *ConcurrencyError
		require.ErrorAs(t, err, &cErr)
		require.True(t, cErr.IsTimeout)
	})

	t.Run("stream_mode_sends_ping_before_timeout", func(t *testing.T) {
		helper := NewConcurrencyHelper(concurrency, SSEPingFormatComment, 10*time.Millisecond)
		c, rec := newHelperTestContext(http.MethodPost, "/v1/messages")
		streamStarted := false
		release, err := helper.WaitForSlotWithPingTimeout(c, "provider", 101, 2, 70*time.Millisecond, true, &streamStarted, true)
		require.Nil(t, release)
		var cErr *ConcurrencyError
		require.ErrorAs(t, err, &cErr)
		require.True(t, cErr.IsTimeout)
		require.True(t, streamStarted)
		require.Contains(t, rec.Body.String(), ":\n\n")
	})
}

// TestWaitForSlotWithPingTimeout_ParentContextCanceled starts with an already
// canceled request. The helper must return context cancellation directly rather
// than classify it as a concurrency timeout.
func TestWaitForSlotWithPingTimeout_ParentContextCanceled(t *testing.T) {
	cache := &httptestkit.ConcurrencySequence{
		ProviderSeq: []bool{false},
	}
	concurrency := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)
	c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	reqCtx, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(reqCtx)
	cancel()

	streamStarted := false
	release, err := helper.WaitForSlotWithPingTimeout(c, "provider", 101, 2, time.Second, false, &streamStarted, true)
	require.Nil(t, release)
	require.ErrorIs(t, err, context.Canceled)
	var cErr *ConcurrencyError
	require.False(t, errors.As(err, &cErr))
}

// TestWaitForSlotWithPingTimeout_AcquireError injects a Redis acquisition
// failure. It requires nil ownership and preserves that error text instead of
// waiting until a synthetic capacity timeout.
func TestWaitForSlotWithPingTimeout_AcquireError(t *testing.T) {
	errCache := &httptestkit.ConcurrencySequenceWithError{
		Err: errors.New("redis unavailable"),
	}
	concurrency := scheduler.NewConcurrencyService(errCache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)
	c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	streamStarted := false
	release, err := helper.WaitForSlotWithPingTimeout(c, "provider", 1, 1, 200*time.Millisecond, false, &streamStarted, true)
	require.Nil(t, release)
	require.Error(t, err)
	require.Contains(t, err.Error(), "redis unavailable")
}

// TestAcquireProviderSlotWithWaitTimeout_ImmediateAttemptBeforeBackoff sets a
// budget shorter than initial scheduler backoff. At least one acquisition
// attempt must occur before the wait returns its timeout error.
func TestAcquireProviderSlotWithWaitTimeout_ImmediateAttemptBeforeBackoff(t *testing.T) {
	cache := &httptestkit.ConcurrencySequence{
		ProviderSeq: []bool{false},
	}
	concurrency := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	helper := NewConcurrencyHelper(concurrency, SSEPingFormatNone, 5*time.Millisecond)
	c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	streamStarted := false

	release, err := helper.AcquireProviderSlotWithWaitTimeout(c, 301, 1, 30*time.Millisecond, false, &streamStarted)
	require.Nil(t, release)
	var cErr *ConcurrencyError
	require.ErrorAs(t, err, &cErr)
	require.True(t, cErr.IsTimeout)
	require.GreaterOrEqual(t, cache.ProviderAcquireCalls, 1)
}
