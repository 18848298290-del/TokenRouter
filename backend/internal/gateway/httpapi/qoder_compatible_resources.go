package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	gatewaysession "github.com/TokenFlux/TokenRouter/internal/gateway/session"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

func qoderRequestCanceled(ctx context.Context, err error) bool {
	if err != nil && errors.Is(err, context.Canceled) {
		return true
	}
	return ctx != nil && errors.Is(ctx.Err(), context.Canceled)
}

func wrapQoderReleaseOnDone(ctx context.Context, releaseFunc func(), isStream bool) func() {
	mode := scheduler.ReleaseOnCancel
	if isStream {
		mode = scheduler.ReleaseOnCompletion
	}
	return scheduler.WrapRelease(ctx, mode, releaseFunc)
}

func (h *QoderCompatibleRuntime) qoderSessionHash(c *gin.Context, endpoint QoderEndpoint, body []byte, apiKeyID int64) string {
	if seed := qoderExplicitStickySessionSeed(c, body); seed != "" {
		return qoderStickySessionHashFromSeed(seed)
	}
	if h == nil || h.options.Execution == nil {
		return ""
	}
	protocol := capability.PlatformAnthropic
	if endpoint == QoderResponses {
		protocol = "responses"
	}
	parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), protocol)
	if err != nil {
		return ""
	}
	if c != nil {
		parsed.SessionContext = &requeststate.SessionContext{
			ClientIP:  clientip.GetClientIP(c),
			UserAgent: c.GetHeader("User-Agent"),
			APIKeyID:  apiKeyID,
		}
	}
	if generated := gatewaysession.GenerateSessionHash(parsed, slog.Info); generated != "" {
		return qoderStickySessionHashFromSeed("fallback:" + generated)
	}
	return ""
}

// qoderExplicitStickySessionSeed passes request headers and body to the session identity resolver.
func qoderExplicitStickySessionSeed(c *gin.Context, body []byte) string {
	var headers map[string][]string
	if c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	return gatewaysession.QoderExplicitSessionSeed(headers, body)
}

func qoderStickySessionHashFromSeed(seed string) string {
	return gatewaysession.QoderHashFromSeed(seed)
}

func (h *QoderCompatibleRuntime) bindQoderStickySessions(ctx context.Context, groupID *int64, sessionHash string, providerID int64, endpoint QoderEndpoint, result *forwardcore.MessagesResult, reqLog *zap.Logger) {
	if h == nil || h.options.Execution == nil || providerID <= 0 {
		return
	}
	bindCtx, cancel := qoderDetachedTimeoutContext(ctx, 5*time.Second)
	defer cancel()
	bind := func(hash string) {
		if hash == "" {
			return
		}
		if err := h.options.Execution.BindStickySession(bindCtx, groupID, hash, providerID); err != nil && reqLog != nil {
			reqLog.Warn("qoder.bind_sticky_session_failed", zap.Int64("provider_id", providerID), zap.Error(err))
		}
	}
	bind(sessionHash)
	if endpoint == QoderResponses && result != nil {
		bind(qoderStickySessionHashFromSeed(result.RequestID))
	}
}

func qoderDetachedTimeoutContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, timeout)
}

func prepareQoderRequestContext(c *gin.Context, body []byte, endpoint QoderEndpoint) {
	if endpoint == QoderMessages {
		SetClaudeCodeClientContext(c, body, nil)
	}
}

func (h *QoderCompatibleRuntime) shouldRefreshQoderProvider(err error, streamStarted bool) bool {
	if h == nil || !h.options.PlatformAvailable || streamStarted {
		return false
	}
	return h.options.MayRefresh(err)
}

// refreshQoderProvider gives the execution target a thirty-second budget to refresh and persist credentials.
func (h *QoderCompatibleRuntime) refreshQoderProvider(ctx context.Context, target QoderCompatibleTarget) (QoderCompatibleTarget, error) {
	if h == nil || !h.options.PlatformAvailable {
		return nil, errors.New("qoder gateway service is not configured")
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return target.Refresh(refreshCtx)
}

func (h *QoderCompatibleRuntime) acquireQoderProviderSlotWithWait(c *gin.Context, provider *providercore.ProviderSnapshot, waitPlan *scheduler.ProviderWaitPlan, reqStream bool, streamStarted *bool, reqLog *zap.Logger) (func(), error) {
	if provider == nil {
		return nil, errors.New("provider is nil")
	}
	if h == nil || h.concurrencyHelper == nil {
		return nil, nil
	}
	if waitPlan == nil {
		return h.concurrencyHelper.AcquireProviderSlotWithWait(c, provider.ID, provider.Concurrency, reqStream, streamStarted)
	}

	ctx := c.Request.Context()
	providerWaitCounted := false
	waitEntry, err := h.concurrencyHelper.EnterProviderWait(ctx, provider.ID, waitPlan.MaxWaiting)
	canWait := waitEntry.Allowed
	if err != nil {
		if reqLog != nil {
			reqLog.Warn("qoder.provider_wait_counter_increment_failed", zap.Int64("provider_id", provider.ID), zap.Error(err))
		}
	} else if !canWait {
		if reqLog != nil {
			reqLog.Info("qoder.provider_wait_queue_full",
				zap.Int64("provider_id", provider.ID),
				zap.Int("max_waiting", waitPlan.MaxWaiting),
			)
		}
		return nil, &WaitQueueFullError{SlotType: "provider"}
	}
	if err == nil && canWait {
		providerWaitCounted = true
	}
	releaseWait := func() {
		if providerWaitCounted {
			waitEntry.Release()
			providerWaitCounted = false
		}
	}

	providerRelease, err := h.concurrencyHelper.AcquireProviderSlotWithWaitTimeout(c, provider.ID, waitPlan.MaxConcurrency, waitPlan.Timeout, reqStream, streamStarted)
	if err != nil {
		releaseWait()
		return nil, err
	}
	releaseWait()
	return providerRelease, nil
}

func (h *QoderCompatibleRuntime) acquireQoderRetryProviderSlot(c *gin.Context, provider *providercore.ProviderSnapshot, selection QoderCompatibleSelection, reqStream bool, streamStarted *bool) (func(), error) {
	if provider == nil {
		return nil, errors.New("provider is nil")
	}
	if h == nil || h.concurrencyHelper == nil {
		return nil, nil
	}
	maxConcurrency := provider.Concurrency
	if selection != nil && selection.WaitPlan() != nil {
		return h.acquireQoderProviderSlotWithWait(c, provider, selection.WaitPlan(), reqStream, streamStarted, nil)
	}
	return h.concurrencyHelper.AcquireProviderSlotWithWait(c, provider.ID, maxConcurrency, reqStream, streamStarted)
}

func (h *QoderCompatibleRuntime) handleConcurrencyError(c *gin.Context, err error, slotType string, streamStarted bool, endpoint QoderEndpoint) {
	status, errType, _, message := ConcurrencyErrorResponse(err, slotType)
	h.streamingAwareError(c, status, errType, message, streamStarted, endpoint)
}

func (h *QoderCompatibleRuntime) writeQoderFailoverExhaustedError(c *gin.Context, endpoint QoderEndpoint, streamStarted bool, err error) bool {
	if err == nil {
		return false
	}
	if status, errType, message, ok := h.qoderGatewayErrorDetails(c, err); ok {
		SetOpsUpstreamError(c, h.options.Errors.Describe(err).SourceStatus, message, "")
		h.streamingAwareError(c, status, errType, message, streamStarted, endpoint)
		return true
	}
	return false
}

func (h *QoderCompatibleRuntime) streamingAwareError(c *gin.Context, status int, errType, message string, streamStarted bool, endpoint QoderEndpoint) {
	WriteQoderStreamError(c, status, errType, message, streamStarted, endpoint)
}

func (h *QoderCompatibleRuntime) errorResponse(c *gin.Context, status int, errType, message string, endpoint QoderEndpoint) {
	WriteQoderError(c, status, errType, message, endpoint)
}

// qoderGatewayErrorDetails delegates to the shared Qoder error presenter.
func (h *QoderCompatibleRuntime) qoderGatewayErrorDetails(c *gin.Context, err error) (int, string, string, bool) {
	return h.options.Errors.Details(c, err)
}

// submitUsageRecordTask submits usage work according to the pool's configuration and running state.
func (h *QoderCompatibleRuntime) submitUsageRecordTask(c *gin.Context, task completion.UsageRecordTask) {
	NewQoderCompletionSubmission(h.options.Pool).Submit(c, task)
}
