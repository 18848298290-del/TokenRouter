package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// selected_provider_slot.go acquires or adopts selected-provider capacity and
// binds its release lifetime to the HTTP request.
// A selected provider may already hold capacity or carry a plan for waiting.
// HTTP adoption keeps that resource attached to the request, with cancellation
// cleanup normally enabled and a completion mode available for callers
// collecting tail usage after client disconnect. An acquired selection uses the
// scheduler's existing sticky binding.
//
// Waiting first retries immediate acquisition, then asks the queue counter for
// admission before using the scheduler wait loop. Counter failures allow the
// wait to proceed; confirmed admitted counts are released when waiting ends.
// Acquisition observations and later sticky binding happen after that cleanup,
// while a sticky write failure does not discard a usable provider slot.

// SelectedProviderSlot carries the selected provider's slot ownership and wait plan.
type SelectedProviderSlot struct {
	// CompleteBeforeRelease selects caller completion cleanup so tail usage can
	// be collected after client cancellation; callers then invoke the returned release.
	CompleteBeforeRelease bool
	ProviderID            int64 // selected provider identity
	Acquired              bool  // selection already owns capacity
	// ReleaseFunc belongs to an acquired selection and is adopted with
	// lease-managed cleanup. Waiting acquisition replaces it with the newly
	// acquired provider callback.
	ReleaseFunc func()
	// WaitPlan supplies capacity, queue and timeout limits when Acquired is false.
	// A missing plan in that state produces an immediate no-provider error.
	WaitPlan *scheduler.ProviderWaitPlan
}

// SlotStickyBinder publishes provider affinity after HTTP waiting acquires
// capacity.
type SlotStickyBinder interface {
	// BindStickySession records provider affinity for the supplied group and
	// session. Acquisition callers treat errors as diagnostics and keep the usable
	// slot; implementations return the storage result for that observation.
	BindStickySession(context.Context, *int64, string, int64) error
}

// ProviderSlotHooks records acquired capacity or an unavailable selection
// during admission.
type ProviderSlotHooks struct {
	// Acquired and CapacityLimited are required observation callbacks for their
	// respective admission paths. They run synchronously with the request before
	// the helper returns ownership or writes its capacity error.
	Acquired        func(*gin.Context) // required acquired-capacity observation callback
	CapacityLimited func(*gin.Context) // required no-capacity observation callback
}

// AcquireSelectedProviderSlot adopts an acquired provider slot or waits using
// the selected plan and returns a lease-managed release. Missing selections or
// wait plans write a capacity error. Callers provide hooks, error output and a
// request context; cancellation normally releases ownership, while
// CompleteBeforeRelease retains it until caller cleanup.
//
// A waiting selection gets an immediate acquisition attempt before queue
// admission. Queue-counter failure is logged and acquisition continues; denied
// admission ends with 429. Successful waiting removes its owned wait count
// before reporting acquisition and binding stickiness. Sticky-binding failure
// remains diagnostic, and an already acquired selection keeps the scheduler's
// existing binding. Nonnil returned releases are idempotent across cancellation
// and explicit cleanup; an adopted nil source callback remains nil.
func AcquireSelectedProviderSlot(c *gin.Context, groupID *int64, sessionHash string, selection *SelectedProviderSlot, reqStream bool, streamStarted *bool, reqLog *zap.Logger, writeError func(int, string, string, string), concurrency *ConcurrencyHelper, sticky SlotStickyBinder, hooks ProviderSlotHooks) (func(), bool) {
	if selection == nil {
		hooks.CapacityLimited(c)
		writeError(http.StatusServiceUnavailable, "api_error", "", "No available providers")
		return nil, false
	}

	ctx := c.Request.Context()
	mode := scheduler.ReleaseOnCancel
	// Tail-usage callers keep provider ownership until explicit completion
	// cleanup, allowing upstream accounting work to outlive cancellation of the
	// client request.
	if selection.CompleteBeforeRelease {
		mode = scheduler.ReleaseOnCompletion
	}
	providerID := selection.ProviderID
	if selection.Acquired {
		hooks.Acquired(c)
		return scheduler.WrapRelease(ctx, mode, selection.ReleaseFunc), true
	}
	if selection.WaitPlan == nil {
		hooks.CapacityLimited(c)
		writeError(http.StatusServiceUnavailable, "api_error", "", "No available providers")
		return nil, false
	}

	fastReleaseFunc, fastAcquired, err := concurrency.TryAcquireProviderSlot(
		ctx,
		providerID,
		selection.WaitPlan.MaxConcurrency,
	)
	if err != nil {
		reqLog.Warn("openai.provider_slot_quick_acquire_failed", zap.Int64("provider_id", providerID), zap.Error(err))
		status, errType, code, message := ConcurrencyErrorResponse(err, "provider")
		writeError(status, errType, code, message)
		return nil, false
	}
	// Bind stickiness after obtaining usable capacity; a failed affinity write is
	// logged while this request still retains its acquired slot.
	if fastAcquired {
		hooks.Acquired(c)
		if err := sticky.BindStickySession(ctx, groupID, sessionHash, providerID); err != nil {
			reqLog.Warn("openai.bind_sticky_session_failed", zap.Int64("provider_id", providerID), zap.Error(err))
		}
		return scheduler.WrapRelease(ctx, mode, fastReleaseFunc), true
	}

	waitEntry, waitErr := concurrency.EnterProviderWait(ctx, providerID, selection.WaitPlan.MaxWaiting)
	canWait := waitEntry.Allowed
	// Failure entering the wait registration leaves no owned waiting entry.
	// Log it and continue to slot acquisition, which retains its own request budget.
	if waitErr != nil {
		reqLog.Warn("openai.provider_wait_counter_increment_failed", zap.Int64("provider_id", providerID), zap.Error(waitErr))
	} else if !canWait {
		reqLog.Info("openai.provider_wait_queue_full",
			zap.Int64("provider_id", providerID),
			zap.Int("max_waiting", selection.WaitPlan.MaxWaiting),
		)
		writeError(http.StatusTooManyRequests, "rate_limit_error", GatewayQueueFullCode, "Too many pending requests, please retry later")
		return nil, false
	}

	providerWaitCounted := waitErr == nil && canWait
	// Invoke cleanup for allowed registrations and clear the local flag before
	// deferred exit repeats it. WaitResult itself skips unconfirmed counter decrements.
	releaseWait := func() {
		if providerWaitCounted {
			waitEntry.Release()
			providerWaitCounted = false
		}
	}
	defer releaseWait()

	providerReleaseFunc, err := concurrency.AcquireProviderSlotWithWaitTimeout(
		c,
		providerID,
		selection.WaitPlan.MaxConcurrency,
		selection.WaitPlan.Timeout,
		reqStream,
		streamStarted,
	)
	if err != nil {
		reqLog.Warn("openai.provider_slot_acquire_failed", zap.Int64("provider_id", providerID), zap.Error(err))
		status, errType, code, message := ConcurrencyErrorResponse(err, "provider")
		writeError(status, errType, code, message)
		return nil, false
	}

	// Remove confirmed queue occupancy before acquired hooks and sticky writes,
	// so subsequent observers no longer count this admitted request as waiting.
	releaseWait()
	hooks.Acquired(c)
	// A failed sticky write after waiting remains diagnostic; the acquired
	// provider slot remains usable and returns with its normal request cleanup.
	if err := sticky.BindStickySession(ctx, groupID, sessionHash, providerID); err != nil {
		reqLog.Warn("openai.bind_sticky_session_failed", zap.Int64("provider_id", providerID), zap.Error(err))
	}
	return scheduler.WrapRelease(ctx, mode, providerReleaseFunc), true
}
