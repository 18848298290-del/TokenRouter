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

// genericChatAttemptBridge holds forwarding state for one Chat Completions request.
type genericChatAttemptBridge struct {
	messageAttemptBridge
	requestCtx                          context.Context
	forwardBody                         []byte
	groupPlatform, selectionSessionHash string
	groupMapping                        routing.GroupMappingResult
}

// Select chooses a provider for the current Chat Completions attempt.
func (b *genericChatAttemptBridge) Select(excluded map[int64]struct{}) (textflow.Selection, error) {
	var err error
	b.selection, err = b.binding().selectProvider(b.c.Request.Context(), b.apiKey.GroupID, b.selectionSessionHash, b.reqModel, excluded, "", int64(0))
	if err != nil {
		return textflow.Selection{}, err
	}
	b.provider = b.selection.Provider
	gatewayhttp.SetOpsSelectedProvider(b.c, b.provider.Record.ID, b.provider.Record.Platform)
	return gatewaycapture.CaptureTextSelection(b.provider), nil
}

// FirstSelectionFailure writes the error from the initial provider selection.
func (b *genericChatAttemptBridge) FirstSelectionFailure(err error, _ bool) {
	if handleGroupSelectionBusinessError(b.c, err, *b.streamStarted, func(status int, errType string, message string, responseStarted bool) {
		b.binding().chatCompletionsErrorResponse(b.c, status, errType, message)
	}) {
		return
	}
	cls := classifyNoProviderErrorFromGin(b.c, b.binding().diagnoser, b.apiKey, b.reqModel, b.reqModel, b.groupPlatform)
	cls = gatewayhttp.RefineSelectionError(err, cls)
	if !cls.ModelNotFound {
		gatewayhttp.MarkOpsRoutingCapacityLimitedIfNoAvailable(b.c, err)
	}
	message := cls.Message
	if !cls.ModelNotFound {
		message = "No available providers: " + err.Error()
	}
	b.binding().chatCompletionsErrorResponse(b.c, cls.Status, cls.ErrType, message)
}

// Acquire acquires or adopts the selected provider's concurrency slot.
func (b *genericChatAttemptBridge) Acquire() bool {
	var err error
	// Reuse an acquired slot or follow the selection's wait plan.
	b.providerReleaseFunc = b.selection.ReleaseFunc
	if !b.selection.Acquired {
		if b.selection.WaitPlan == nil {
			gatewayhttp.MarkOpsRoutingCapacityLimited(b.c)
			b.binding().chatCompletionsErrorResponse(b.c, http.StatusServiceUnavailable, "api_error", "No available providers")
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
			b.reqLog.Warn("gateway.cc.provider_slot_acquire_failed", zap.Int64("provider_id", b.provider.Record.ID), zap.Error(err))
			b.binding().handleConcurrencyError(b.c, err, "provider", *b.streamStarted)
			return false
		}
	}
	b.providerReleaseFunc = scheduler.WrapRelease(b.c.Request.Context(), scheduler.ReleaseOnCancel, b.providerReleaseFunc)

	return true
}

// Forward dispatches Chat Completions to the selected provider's platform.
func (b *genericChatAttemptBridge) Forward(_ textflow.AttemptState) textflow.Outcome {
	var err error

	if b.groupPlatform == capability.PlatformGemini && b.provider.Record.Platform != capability.PlatformGemini {
		if b.providerReleaseFunc != nil {
			b.providerReleaseFunc()
		}

		return textflow.Outcome{Skip: true}
	}
	// Capture writer size before forwarding so later errors can detect prior output.
	b.writerSizeBeforeForward = b.c.Writer.Size()
	b.forwardBody = b.body
	if b.groupMapping.Mapped {
		b.forwardBody = b.binding().replaceModel(b.body, b.groupMapping.MappedModel)
	}
	gatewayhttp.SetActualUpstreamEndpoint(b.c, "")
	if b.provider.Record.Platform == capability.PlatformGemini {
		if !b.binding().geminiAvailable {
			b.binding().chatCompletionsErrorResponse(b.c, http.StatusBadGateway, "upstream_error", "Gemini compatibility service is not configured")
			if b.providerReleaseFunc != nil {
				b.providerReleaseFunc()
			}
			return textflow.Outcome{Stop: true}
		}
		b.result, err = b.binding().forwardGeminiChat(b.c.Request.Context(), b.c, b.provider, b.forwardBody)
	} else if shouldUseAntigravityCompat(b.provider) {
		if !b.binding().antigravityAvailable {
			b.binding().chatCompletionsErrorResponse(b.c, http.StatusBadGateway, "upstream_error", "Antigravity compatibility service is not configured")
			if b.providerReleaseFunc != nil {
				b.providerReleaseFunc()
			}
			return textflow.Outcome{Stop: true}
		}
		gatewayhttp.SetActualUpstreamEndpoint(b.c, gatewayhttp.EndpointAntigravityGenerateContent)
		b.result, err = b.binding().forwardAntigravityChat(b.c.Request.Context(), b.c, b.provider, b.forwardBody, b.parsedReq)
	} else {
		b.result, err = b.binding().forwardChat(b.c.Request.Context(), b.c, b.provider, b.forwardBody, b.parsedReq)
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
func (b *genericChatAttemptBridge) OtherFailure(err error) {
	upstreamErrorAlreadyCommunicated := gatewayhttp.ForwardErrorAlreadyCommunicated(b.c, b.writerSizeBeforeForward, err)
	wroteFallback := false
	if !upstreamErrorAlreadyCommunicated {
		wroteFallback = b.binding().ensureForwardErrorResponse(b.c, *b.streamStarted)
	}
	b.reqLog.Error("gateway.cc.forward_failed",
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Bool("fallback_error_response_written", wroteFallback),
		zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
		zap.Error(err),
	)
}

// Complete captures usage and records the completed attempt.
func (b *genericChatAttemptBridge) Complete(_ textflow.AttemptState) {
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
			completionLog.Error("gateway.cc.record_usage_failed",
				zap.Int64("provider_id", completionInput.Provider.ID),
				zap.Error(err),
			)
		}
	})
}

func (b *genericChatAttemptBridge) Context() context.Context { return b.requestCtx }

func (b *genericChatAttemptBridge) Begin() {}

func (b *genericChatAttemptBridge) PrepareAttempt() bool { return true }

func (b *genericChatAttemptBridge) Intercept() bool { return false }

func (b *genericChatAttemptBridge) SingleProviderRetry() {}

func (b *genericChatAttemptBridge) Abandon(int64) {}

func (b *genericChatAttemptBridge) Success() {}

func (b *genericChatAttemptBridge) Exhausted(err *textflow.AttemptFailure, _ string, stream bool) {
	var original *forwardcore.UpstreamFailoverError
	if err != nil && errors.As(err.Cause, &original) {
		b.binding().handleCCFailoverExhausted(b.c, original, stream || *b.streamStarted)
	} else {
		b.binding().chatCompletionsErrorResponse(b.c, http.StatusBadGateway, "server_error", "All available providers exhausted")
	}
}

func (b *genericChatAttemptBridge) PolicyFailure(err error) {
	if original, ok := errors.AsType[*anthropic.BetaBlockedError](err); ok {
		gatewayhttp.MarkOpsClientBusinessLimited(b.c, gatewayhttp.OpsClientBusinessLimitedReasonLocalPolicyDenied)
		b.binding().chatCompletionsErrorResponse(b.c, http.StatusBadRequest, "invalid_request_error", original.Message)
	}
}

// shouldUseAntigravityCompat reports whether this is an Antigravity OAuth provider.
func shouldUseAntigravityCompat(provider *gatewaycapture.ExecutionProvider) bool {
	return provider != nil &&
		provider.Record.Platform == capability.PlatformAntigravity &&
		provider.Record.Type == capability.ProviderTypeOAuth
}
