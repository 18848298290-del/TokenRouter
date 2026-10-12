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
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// genericResponsesAttemptBridge holds request-local state for the shared text attempt loop.
type genericResponsesAttemptBridge struct {
	messageAttemptBridge
	requestCtx   context.Context
	forwardBody  []byte
	groupMapping routing.GroupMappingResult
}

// Select chooses a provider for the current Responses attempt.
func (b *genericResponsesAttemptBridge) Select(excluded map[int64]struct{}) (textflow.Selection, error) {
	var err error
	b.selection, err = b.binding().selectProvider(b.requestCtx, b.apiKey.GroupID, b.sessionKey, b.reqModel, excluded, "", int64(0))
	if err != nil {
		return textflow.Selection{}, err
	}
	b.provider = b.selection.Provider
	gatewayhttp.SetOpsSelectedProvider(b.c, b.provider.Record.ID, b.provider.Record.Platform)
	return gatewaycapture.CaptureTextSelection(b.provider), nil
}

// FirstSelectionFailure classifies initial selection failure and writes a Responses error.
func (b *genericResponsesAttemptBridge) FirstSelectionFailure(err error, _ bool) {
	cls := classifyNoProviderErrorFromGin(b.c, b.binding().diagnoser, b.apiKey, b.reqModel, b.reqModel, gatewayhttp.EffectiveAPIKeyPlatform(b.c, b.apiKey))
	cls = gatewayhttp.RefineSelectionError(err, cls)
	if !cls.ModelNotFound {
		gatewayhttp.MarkOpsRoutingCapacityLimitedIfNoAvailable(b.c, err)
	}
	message := cls.Message
	if !cls.ModelNotFound {
		message = "No available providers: " + err.Error()
	}
	b.binding().responsesErrorResponse(b.c, cls.Status, cls.ErrType, message)
}

// Acquire acquires or adopts the selected provider's concurrency slot.
func (b *genericResponsesAttemptBridge) Acquire() bool {
	var err error
	// Reuse an acquired slot or follow the selection's wait plan.
	b.providerReleaseFunc = b.selection.ReleaseFunc
	if !b.selection.Acquired {
		if b.selection.WaitPlan == nil {
			gatewayhttp.MarkOpsRoutingCapacityLimited(b.c)
			b.binding().responsesErrorResponse(b.c, http.StatusServiceUnavailable, "api_error", "No available providers")
			return false
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
			b.reqLog.Warn("gateway.responses.provider_slot_acquire_failed", zap.Int64("provider_id", b.provider.Record.ID), zap.Error(err))
			b.binding().handleConcurrencyError(b.c, err, "provider", *b.streamStarted)
			return false
		}
	}
	b.providerReleaseFunc = scheduler.WrapRelease(b.c.Request.Context(), scheduler.ReleaseOnCancel, b.providerReleaseFunc)

	return true
}

// Forward dispatches a Responses attempt to Gemini or the generic executor.
func (b *genericResponsesAttemptBridge) Forward(_ textflow.AttemptState) textflow.Outcome {
	var err error
	// Capture writer size before forwarding so later errors can detect prior output.
	b.writerSizeBeforeForward = b.c.Writer.Size()
	gatewayhttp.SetActualUpstreamEndpoint(b.c, "")
	if b.provider.Record.Platform == capability.PlatformGemini {
		if !b.binding().geminiAvailable {
			b.binding().responsesErrorResponse(b.c, http.StatusBadGateway, "upstream_error", "Gemini compatibility service is not configured")
			if b.providerReleaseFunc != nil {
				b.providerReleaseFunc()
			}
			return textflow.Outcome{Stop: true}
		}
		gatewayhttp.SetActualUpstreamEndpoint(b.c, gatewayhttp.EndpointGeminiModels)
		b.result, err = b.binding().forwardGeminiResponses(b.requestCtx, b.c, b.provider, b.forwardBody, b.parsedReq)
	} else if shouldUseAntigravityCompat(b.provider) {
		if !b.binding().antigravityAvailable {
			b.binding().responsesErrorResponse(b.c, http.StatusBadGateway, "upstream_error", "Antigravity compatibility service is not configured")
			if b.providerReleaseFunc != nil {
				b.providerReleaseFunc()
			}
			return textflow.Outcome{Stop: true}
		}
		gatewayhttp.SetActualUpstreamEndpoint(b.c, gatewayhttp.EndpointAntigravityGenerateContent)
		b.result, err = b.binding().forwardAntigravityResponses(b.requestCtx, b.c, b.provider, b.forwardBody, b.parsedReq)
	} else {
		b.result, err = b.binding().forwardResponses(b.requestCtx, b.c, b.provider, b.forwardBody, b.parsedReq)
	}

	if b.providerReleaseFunc != nil {
		b.providerReleaseFunc()
	}
	b.binding().reportSchedule(b.selection, b.provider.Record.ID, err == nil, b.result)

	out := textflow.Outcome{Attempt: messageObservedAttempt(b.result, err), Err: err, HasResult: b.result != nil, OutputChanged: b.c.Writer.Size() != b.writerSizeBeforeForward}
	out.Attempt.HTTPCommitted = b.c.Writer.Written()
	out.Attempt.RetryCommitted = out.OutputChanged
	var policy *anthropic.BetaBlockedError
	var retry *forwardcore.UpstreamFailoverError
	switch {
	case errors.As(err, &policy):
		out.Kind = textflow.FailurePolicy
	case errors.As(err, &retry):
		out.Failure = &textflow.AttemptFailure{Cause: err, Policy: retry.RetryFailure()}
	}
	return out
}

// OtherFailure reports forwarding errors that were not communicated to the client.
func (b *genericResponsesAttemptBridge) OtherFailure(err error) {
	upstreamErrorAlreadyCommunicated := gatewayhttp.ForwardErrorAlreadyCommunicated(b.c, b.writerSizeBeforeForward, err)
	wroteFallback := false
	if !upstreamErrorAlreadyCommunicated {
		wroteFallback = b.binding().ensureForwardErrorResponse(b.c, *b.streamStarted)
	}
	b.reqLog.Error("gateway.responses.forward_failed",
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Bool("fallback_error_response_written", wroteFallback),
		zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
		zap.Error(err),
	)
}

// Complete captures usage and records the completed attempt.
func (b *genericResponsesAttemptBridge) Complete(_ textflow.AttemptState) {
	// Capture request metadata before submitting completion work.
	userAgent := b.c.GetHeader("User-Agent")
	clientIP := clientip.GetClientIP(b.c)
	requestPayloadHash := billing.HashUsageRequestPayload(b.body)
	inboundEndpoint := gatewayhttp.GetInboundEndpoint(b.c)
	upstreamEndpoint := gatewayhttp.GetUpstreamEndpoint(b.c, b.provider.Record.Platform)

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
		RequestPayloadHash: requestPayloadHash,
		RequestBody:        b.body,
		APIKeyService:      b.binding().apiKeyService,
		ClientSessionID:    clientSessionID,
		PricingUsageFields: b.groupMapping.ToUsageFields(b.reqModel, b.result.UpstreamModel),
	})
	completionRuntime := b.binding().recorder
	completionLog := b.reqLog
	b.binding().submitUsageRecordTask(b.c, func(ctx context.Context) {
		if err := completionRuntime.Record(ctx, completionInput, false); err != nil {
			completionLog.Error("gateway.responses.record_usage_failed",
				zap.Int64("provider_id", completionInput.Provider.ID),
				zap.Error(err),
			)
		}
	})
}

func (b *genericResponsesAttemptBridge) Context() context.Context { return b.requestCtx }
func (b *genericResponsesAttemptBridge) Begin()                   {}
func (b *genericResponsesAttemptBridge) PrepareAttempt() bool     { return true }
func (b *genericResponsesAttemptBridge) Intercept() bool          { return false }
func (b *genericResponsesAttemptBridge) SingleProviderRetry()     {}
func (b *genericResponsesAttemptBridge) Abandon(int64)            {}
func (b *genericResponsesAttemptBridge) Success()                 {}
func (b *genericResponsesAttemptBridge) Exhausted(err *textflow.AttemptFailure, _ string, stream bool) {
	var original *forwardcore.UpstreamFailoverError
	if err != nil && errors.As(err.Cause, &original) {
		b.binding().handleResponsesFailoverExhausted(b.c, original, stream || *b.streamStarted)
	} else {
		b.binding().responsesErrorResponse(b.c, http.StatusBadGateway, "server_error", "All available providers exhausted")
	}
}

func (b *genericResponsesAttemptBridge) PolicyFailure(err error) {
	if original, ok := errors.AsType[*anthropic.BetaBlockedError](err); ok {
		gatewayhttp.MarkOpsClientBusinessLimited(b.c, gatewayhttp.OpsClientBusinessLimitedReasonLocalPolicyDenied)
		b.binding().responsesErrorResponse(b.c, http.StatusBadRequest, "invalid_request_error", original.Message)
	}
}
