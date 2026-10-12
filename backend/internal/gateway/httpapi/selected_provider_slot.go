package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// SelectedProviderSlot carries the selected provider's slot ownership and wait plan.
type SelectedProviderSlot struct {
	// CompleteBeforeRelease lets the caller release after collecting tail usage,
	// even when the client disconnects.
	CompleteBeforeRelease bool
	ProviderID            int64
	Acquired              bool
	ReleaseFunc           func()
	WaitPlan              *scheduler.ProviderWaitPlan
}
type SlotStickyBinder interface {
	BindStickySession(context.Context, *int64, string, int64) error
}
type ProviderSlotHooks struct {
	Acquired        func(*gin.Context)
	CapacityLimited func(*gin.Context)
}

// AcquireSelectedProviderSlot acquires or adopts a slot and returns the request's release function.
func AcquireSelectedProviderSlot(c *gin.Context, groupID *int64, sessionHash string, selection *SelectedProviderSlot, reqStream bool, streamStarted *bool, reqLog *zap.Logger, writeError func(int, string, string, string), concurrency *ConcurrencyHelper, sticky SlotStickyBinder, hooks ProviderSlotHooks) (func(), bool) {
	if selection == nil {
		hooks.CapacityLimited(c)
		writeError(http.StatusServiceUnavailable, "api_error", "", "No available providers")
		return nil, false
	}

	ctx := c.Request.Context()
	mode := scheduler.ReleaseOnCancel
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
	if fastAcquired {
		hooks.Acquired(c)
		if err := sticky.BindStickySession(ctx, groupID, sessionHash, providerID); err != nil {
			reqLog.Warn("openai.bind_sticky_session_failed", zap.Int64("provider_id", providerID), zap.Error(err))
		}
		return scheduler.WrapRelease(ctx, mode, fastReleaseFunc), true
	}

	waitEntry, waitErr := concurrency.EnterProviderWait(ctx, providerID, selection.WaitPlan.MaxWaiting)
	canWait := waitEntry.Allowed
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

	// Stop counting this request as waiting once its slot is acquired.
	releaseWait()
	hooks.Acquired(c)
	if err := sticky.BindStickySession(ctx, groupID, sessionHash, providerID); err != nil {
		reqLog.Warn("openai.bind_sticky_session_failed", zap.Int64("provider_id", providerID), zap.Error(err))
	}
	return scheduler.WrapRelease(ctx, mode, providerReleaseFunc), true
}
