package textattempt

import (
	"context"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// geminiMessageAttemptBridge adapts Gemini-backed Messages attempts to the shared text loop.
type geminiMessageAttemptBridge struct {
	messageAttemptBridge
	forwardModel string
	forwardBody  []byte
	groupMapping routing.GroupMappingResult
}

// Select chooses a provider using the request's model and session hash.
func (b *geminiMessageAttemptBridge) Select(excluded map[int64]struct{}) (textflow.Selection, error) {
	var err error
	b.selection, err = b.binding().selectProvider(b.c.Request.Context(), b.apiKey.GroupID, b.sessionKey, b.reqModel, excluded, "", int64(0)) // Gemini does not apply the Anthropic session limit.
	if err != nil {
		return textflow.Selection{}, err
	}
	b.provider = b.selection.Provider
	gatewayhttp.SetOpsSelectedProvider(b.c, b.provider.Record.ID, b.provider.Record.Platform)
	return gatewaycapture.CaptureTextSelection(b.provider), nil
}

// FirstSelectionFailure reports group policy errors or diagnoses initial provider unavailability.
func (b *geminiMessageAttemptBridge) FirstSelectionFailure(err error, _ bool) {
	if handleGroupSelectionBusinessError(b.c, err, *b.streamStarted, func(status int, errType string, message string, responseStarted bool) {
		b.binding().handleStreamingAwareError(b.c, status, errType, message, responseStarted)
	}) {
		return
	}
	cls := classifyNoProviderErrorFromGin(b.c, b.binding().diagnoser, b.apiKey, b.reqModel, b.reqModel, capability.PlatformGemini)
	if !cls.ModelNotFound {
		gatewayhttp.MarkOpsRoutingCapacityLimitedIfNoAvailable(b.c, err)
	}
	b.reqLog.Warn("gateway.select_provider_no_available",
		zap.String("model", b.reqModel),
		zap.Int64p("group_id", b.apiKey.GroupID),
		zap.String("platform", b.platform),
		zap.Bool("model_not_found", cls.ModelNotFound),
		zap.Error(err),
	)
	message := cls.Message
	if !cls.ModelNotFound {
		message = "No available providers: " + err.Error()
	}
	b.binding().handleStreamingAwareError(b.c, cls.Status, cls.ErrType, message, *b.streamStarted)
}

// Acquire acquires a provider slot and binds the sticky session after waiting.
func (b *geminiMessageAttemptBridge) Acquire() bool {
	// Reuse the selection's slot or wait according to its plan.
	b.providerReleaseFunc = b.selection.ReleaseFunc
	if !b.selection.Acquired {
		if b.selection.WaitPlan == nil {
			gatewayhttp.MarkOpsRoutingCapacityLimited(b.c)
			b.reqLog.Warn("gateway.select_provider_no_slot_no_wait_plan",
				zap.Int64("provider_id", b.provider.Record.ID),
				zap.String("model", b.reqModel),
				zap.String("platform", b.platform),
			)
			b.binding().handleStreamingAwareError(b.c, http.StatusServiceUnavailable, "api_error", "No available providers", *b.streamStarted)
			return false
		}
		providerWaitCounted := false
		waitEntry, err := b.binding().concurrencyHelper.EnterProviderWait(b.c.Request.Context(), b.provider.Record.ID, b.selection.WaitPlan.MaxWaiting)
		canWait := waitEntry.Allowed
		if err != nil {
			b.reqLog.Warn("gateway.provider_wait_counter_increment_failed", zap.Int64("provider_id", b.provider.Record.ID), zap.Error(err))
		} else if !canWait {
			b.reqLog.Info("gateway.provider_wait_queue_full",
				zap.Int64("provider_id", b.provider.Record.ID),
				zap.Int("max_waiting", b.selection.WaitPlan.MaxWaiting),
			)
			b.binding().handleStreamingAwareErrorWithCode(b.c, http.StatusTooManyRequests, "rate_limit_error", gatewayhttp.GatewayQueueFullCode, "Too many pending requests, please retry later", *b.streamStarted)
			return false
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

		b.providerReleaseFunc, err = b.binding().concurrencyHelper.AcquireProviderSlotWithWaitTimeout(
			b.c,
			b.provider.Record.ID,
			b.selection.WaitPlan.MaxConcurrency,
			b.selection.WaitPlan.Timeout,
			b.reqStream,
			b.streamStarted,
		)
		if err != nil {
			b.reqLog.Warn("gateway.provider_slot_acquire_failed", zap.Int64("provider_id", b.provider.Record.ID), zap.Error(err))
			releaseWait()
			b.binding().handleConcurrencyError(b.c, err, "provider", *b.streamStarted)
			return false
		}
		// Stop counting this request as waiting once its slot is acquired.
		releaseWait()
		if err := b.binding().bindSticky(b.c.Request.Context(), b.apiKey.GroupID, b.sessionKey, b.provider.Record.ID); err != nil {
			b.reqLog.Warn("gateway.bind_sticky_session_failed", zap.Int64("provider_id", b.provider.Record.ID), zap.Error(err))
		}
	}
	// Release the provider slot when the request times out or disconnects.
	b.providerReleaseFunc = scheduler.WrapRelease(b.c.Request.Context(), scheduler.ReleaseOnCancel, b.providerReleaseFunc)

	return true
}

// Forward dispatches Gemini-backed Messages to Gemini or Antigravity.
func (b *geminiMessageAttemptBridge) Forward(state textflow.AttemptState) textflow.Outcome {
	var err error
	// Carry the attempt's provider-switch state into forwarding.

	requestCtx := b.c.Request.Context()
	if state.SwitchCount > 0 {
		requestCtx = requeststate.WithProviderSwitchCount(requestCtx, state.SwitchCount)
	}
	// Compare writer size after forwarding to prevent failover once output has been sent.
	b.writerSizeBeforeForward = b.c.Writer.Size()
	if b.provider.Record.Platform == capability.PlatformAntigravity {
		b.result, err = b.binding().forwardAntigravityGemini(
			requestCtx,
			b.c,
			b.provider,
			b.forwardModel,
			"generateContent",
			b.reqStream,
			b.forwardBody,
			b.hasBoundSession,
			forwardcore.WithGeminiSession(derefGroupID(b.apiKey.GroupID), b.sessionKey),
		)
	} else {
		b.result, err = b.binding().forwardGemini(requestCtx, b.c, b.provider, b.forwardBody)
	}
	if b.providerReleaseFunc != nil {
		b.providerReleaseFunc()
	}
	b.binding().reportSchedule(b.selection, b.provider.Record.ID, err == nil, b.result)
	out := textflow.Outcome{Attempt: messageObservedAttempt(b.result, err), Err: err, HasResult: b.result != nil, OutputChanged: b.c.Writer.Size() != b.writerSizeBeforeForward}
	out.Attempt.HTTPCommitted = b.c.Writer.Written()
	out.Attempt.RetryCommitted = out.OutputChanged
	if retry, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		out.Failure = &textflow.AttemptFailure{Cause: retry, Policy: retry.RetryFailure()}
	}
	return out
}

// Success increments the RPM counter for eligible Anthropic OAuth or setup-token providers.
func (b *geminiMessageAttemptBridge) Success() {
	// Count successful forwarding toward the provider's soft RPM limit.
	// Selection and increment are separate, so concurrent requests can temporarily exceed the limit.
	// A counter-write failure is logged after the response has succeeded.
	if b.provider.View().IsAnthropicOAuthOrSetupToken() && gatewaycapture.ExecutionRuntimeConfig(b.provider).GetBaseRPM() > 0 {
		if err := b.binding().incrementRPM(b.c.Request.Context(), b.provider.Record.ID); err != nil {
			b.reqLog.Warn("gateway.rpm_increment_failed", zap.Int64("provider_id", b.provider.Record.ID), zap.Error(err))
		}
	}
}

// Complete captures the Gemini-backed Messages result for usage recording.
func (b *geminiMessageAttemptBridge) Complete(state textflow.AttemptState) {
	// Capture request metadata while the HTTP context is available.
	userAgent := b.c.GetHeader("User-Agent")
	clientIP := clientip.GetClientIP(b.c)
	requestPayloadHash := billing.HashUsageRequestPayload(b.body)
	inboundEndpoint := gatewayhttp.GetInboundEndpoint(b.c)
	upstreamEndpoint := gatewayhttp.GetUpstreamEndpoint(b.c, b.provider.Record.Platform)

	if b.result.ReasoningEffort == nil {
		b.result.ReasoningEffort = protocol.NormalizeClaudeOutputEffort(b.parsedReq.OutputEffort)
	}
	if b.result.ReasoningEffort == nil && b.parsedReq.ThinkingEnabled {
		protocolModel := b.result.UpstreamModel
		if protocolModel == "" {
			protocolModel = b.result.Model
		}
		b.result.ReasoningEffort = gatewaycapture.DefaultEffortForThinkingEnabled(protocolModel)
	}

	// Submit usage recording through the configured worker pool.
	// Capture the cache-billing flag by value so completion does not retain the failover body.
	forceCacheBilling := state.ForceCacheBilling

	clientSessionID := gatewayhttp.ExtractClientSessionID(b.c)
	gatewayhttp.StampForwardRequestedReasoningEffort(b.result, b.c)
	// Capture funding and request data before queueing so the worker uses this snapshot.
	completionInput := gatewaycapture.CaptureMessages(gatewayhttp.CompletionContext(b.c), &gatewaycapture.MessagesCapture{
		Result: b.result,

		APIKey:             b.apiKey,
		User:               b.apiKey.User,
		Provider:           gatewaycapture.ExecutionCompletionRecord(b.provider),
		Subscription:       b.subscription,
		InboundEndpoint:    inboundEndpoint,
		UpstreamEndpoint:   upstreamEndpoint,
		UserAgent:          userAgent,
		IPAddress:          clientIP,
		ClientSessionID:    clientSessionID,
		RequestPayloadHash: requestPayloadHash,
		RequestBody:        append([]byte(nil), b.body...),
		ForceCacheBilling:  forceCacheBilling,
		APIKeyService:      b.binding().apiKeyService,
		PricingUsageFields: b.groupMapping.ToUsageFields(b.reqModel, b.result.UpstreamModel),
	})
	completionUserID := b.subject.UserID
	completionModel := b.reqModel
	completionRuntime := b.binding().recorder
	b.binding().submitUsageRecordTask(b.c, func(ctx context.Context) {
		if err := completionRuntime.Record(ctx, completionInput, false); err != nil {
			logging.L().With(
				zap.String("component", "handler.gateway.messages"),
				zap.Int64("user_id", completionUserID),
				zap.Int64("api_key_id", completionInput.APIKey.ID),
				zap.Any("group_id", completionInput.APIKey.GroupID),
				zap.String("model", completionModel),
				zap.Int64("provider_id", completionInput.Provider.ID),
			).Error("gateway.record_usage_failed", zap.Error(err))
		}
	})
}

func (b *geminiMessageAttemptBridge) Begin() {
	b.currentAPIKey = b.apiKey
	b.currentSubscription = b.subscription
	if b.binding().singleProviderGroup(b.Context(), b.apiKey.GroupID) {
		b.SingleProviderRetry()
	}
}
func (b *geminiMessageAttemptBridge) PrepareAttempt() bool { return true }
func (b *geminiMessageAttemptBridge) Abandon(int64)        {}
func (b *geminiMessageAttemptBridge) Exhausted(err *textflow.AttemptFailure, _ string, stream bool) {
	b.messageAttemptBridge.Exhausted(err, capability.PlatformGemini, stream)
}
