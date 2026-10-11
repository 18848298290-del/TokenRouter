package openaiattempt

import (
	"context"
	"errors"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// openAIMessageAttemptBridge 与 Responses 共享固定能力，保留各自错误与计量时机。
type openAIMessageAttemptBridge struct {
	responsesAttemptBridge
	providerLayerModel, currentRoutingModel, promptCacheKey string
	groupMappingMsg                                         routing.GroupMappingResult
	mappedBodyForMessages                                   func(bool, string) []byte
}

// Select 保留 OpenAI Messages 适配差异；提供商重试复用同一核心。
func (b *openAIMessageAttemptBridge) Select(excluded map[int64]struct{}) (textflow.ResponseSelection, error) {
	b.reqLog.Debug("openai_messages.provider_selecting", zap.Int("excluded_provider_count", len(excluded)))
	var scheduleDecision scheduler.PlatformDecision
	var err error
	b.selection, scheduleDecision, err = b.binding().selectProviderWithSchedulerForCapabilityAndRoutingModel(
		b.c.Request.Context(),
		b.apiKey.GroupID,
		"", // no previous_response_id
		b.sessionHash,
		b.reqModel,
		b.providerLayerModel,
		excluded, egress.OpenAIUpstreamTransportAny, provider.OpenAIEndpointCapabilityTextGeneration,
		false,
		false,
		b.requestPlatform,
	)
	if err != nil {
		return textflow.ResponseSelection{}, err
	}
	if b.selection == nil || b.selection.Provider == nil {
		cls := ClassifyOpenAICompatibleNoProviderErrorFromGin(b.c, b.binding().resolvedDiagnoser, b.apiKey, b.providerLayerModel, b.reqModel)
		if !cls.ModelNotFound {
			gatewayhttp.MarkOpsRoutingCapacityLimited(b.c)
		}
		b.binding().anthropicStreamingAwareError(b.c, cls.Status, cls.ErrType, cls.Message, *b.streamStarted)
		return textflow.ResponseSelection{}, nil
	}
	b.provider = b.selection.Provider
	b.sessionHash = EnsureOpenAIPoolModeSessionHash(b.sessionHash, b.provider)
	b.reqLog.Debug("openai_messages.provider_selected", zap.Int64("provider_id", b.provider.Record.ID), zap.String("provider_name", b.provider.Record.Name))
	_ = scheduleDecision
	gatewayhttp.SetOpsSelectedProvider(b.c, b.provider.Record.ID, b.provider.Record.Platform)

	return b.selectedView(), nil
}

// SelectionFailure 保留 OpenAI Messages 适配差异；提供商重试复用同一核心。
func (b *openAIMessageAttemptBridge) SelectionFailure(err error, excludedCount int, last *textflow.AttemptFailure) {
	var lastFailoverErr *forwardcore.UpstreamFailoverError
	if last != nil {
		errors.As(last.Cause, &lastFailoverErr)
	}

	if gatewayhttp.FailoverClientGone(b.c) {
		b.reqLog.Info("openai_messages.provider_select_aborted_client_disconnected", zap.Error(err))
		return
	}
	b.reqLog.Warn("openai_messages.provider_select_failed",
		zap.Error(gatewayhttp.OpenAICompatibleSelectionErrorForLog(err, b.requestPlatform)),
		zap.Int("excluded_provider_count", excludedCount),
	)
	if excludedCount == 0 {
		if err != nil {
			if b.binding().handleOpenAISelectionBusinessError(b.c, err, *b.streamStarted) {
				return
			}
			cls := ClassifyOpenAICompatibleNoProviderErrorFromGin(b.c, b.binding().resolvedDiagnoser, b.apiKey, b.providerLayerModel, b.reqModel)
			if !cls.ModelNotFound {
				gatewayhttp.MarkOpsRoutingCapacityLimitedIfNoAvailable(b.c, err)
			}
			b.binding().anthropicStreamingAwareError(b.c, cls.Status, cls.ErrType, cls.Message, *b.streamStarted)
			return
		}
	} else {
		if lastFailoverErr != nil {
			b.binding().handleAnthropicFailoverExhausted(b.c, lastFailoverErr, *b.streamStarted)
		} else {
			b.binding().anthropicStreamingAwareError(b.c, http.StatusBadGateway, "api_error", "Upstream request failed", *b.streamStarted)
		}
		return
	}
}

// Forward 保留 OpenAI Messages 适配差异；提供商重试复用同一核心。
func (b *openAIMessageAttemptBridge) Forward() textflow.ResponseOutcome {
	gatewayhttp.BindNativeMessageStreamState(b.c, b.streamStarted)
	var err error
	gatewayhttp.SetOpsLatencyMs(b.c, gatewayhttp.OpsRoutingLatencyMsKey, time.Since(b.routingStart).Milliseconds())
	forwardStart := time.Now()

	// 应用分组模型映射到请求体
	attemptBody := b.mappedBodyForMessages(b.groupMappingMsg.Mapped, b.groupMappingMsg.MappedModel)
	b.writerSizeBeforeForward = b.c.Writer.Size()
	b.result, err = func() (*forwardcore.OpenAIResult, error) {
		defer func() {
			if b.providerReleaseFunc != nil {
				b.providerReleaseFunc()
			}
		}()
		tlsRouterMatch := b.binding().matchOpenAITLSFingerprintRouterForRequest(b.c, b.provider)
		if err := b.binding().enforceOpenAIClientPolicyForRequest(b.c.Request.Context(), b.c, b.provider, attemptBody, tlsRouterMatch); err != nil {
			return nil, err
		}
		return b.binding().forwardAsAnthropic(b.c.Request.Context(), b.c, b.provider, attemptBody, b.promptCacheKey, b.providerLayerModel, tlsRouterMatch)
	}()
	if gatewayhttp.NativeMessageIntercepted(b.c) {
		return textflow.ResponseOutcome{Outcome: textflow.Outcome{Stop: true}}
	}
	var cyberBlockBodyMsg []byte
	if gatewayhttp.GetOpsCyberPolicy(b.c) != nil {
		cyberBlockBodyMsg = b.body
	}
	b.cyberPolicyHandled = b.binding().recordCyberPolicyIfMarked(b.c, b.apiKey, b.provider, b.subscription, b.reqModel, err != nil, cyberBlockBodyMsg, gatewayhttp.ClientRequestedUsageFields(b.c, b.groupMappingMsg, b.reqModel, ""), billing.HashUsageRequestPayload(b.body))
	forwardDurationMs := time.Since(forwardStart).Milliseconds()
	upstreamLatencyMs, _ := GetContextInt64(b.c, gatewayhttp.OpsUpstreamLatencyMsKey)
	responseLatencyMs := forwardDurationMs
	if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
		responseLatencyMs = forwardDurationMs - upstreamLatencyMs
	}
	gatewayhttp.SetOpsLatencyMs(b.c, gatewayhttp.OpsResponseLatencyMsKey, responseLatencyMs)
	if err == nil && b.result != nil && b.result.FirstTokenMs != nil {
		gatewayhttp.SetOpsLatencyMs(b.c, gatewayhttp.OpsTimeToFirstTokenMsKey, int64(*b.result.FirstTokenMs))
	}
	out := textflow.ResponseOutcome{Outcome: textflow.Outcome{Attempt: openAIObservedAttempt(b.result, err), Err: err, HasResult: b.result != nil}, Images: b.result != nil && b.result.ImageCount > 0}
	out.NativePartial = err != nil && b.result != nil && b.result.NativeUsage != nil && (b.result.NativeUsage.HasObservedTokens() || b.result.ImageCount > 0)
	out.Attempt.HTTPCommitted = b.c.Writer.Written()
	if err != nil && !out.Images {
		if overLimit, ok := errors.AsType[*routing.ReasoningEffortOverLimitError](err); ok {
			b.reqLog.Info("openai_messages.reasoning_effort_policy_denied", zap.String("reason", overLimit.Error()))
			out.Stop = true
			return out
		}
	}
	if retry, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		out.Failure = &textflow.AttemptFailure{Cause: err, Policy: retry.RetryFailure()}
	}
	return out
}

// Complete 保留 OpenAI Messages 适配差异；提供商重试复用同一核心。
func (b *openAIMessageAttemptBridge) Complete() {
	res := b.result

	if res == nil {
		return
	}
	completionInput := b.captureCompletion(gatewaycapture.OpenAICapture{
		Result:             res,
		RequestPayloadHash: billing.HashUsageRequestPayload(b.body),
		PricingUsageFields: b.groupMappingMsg.ToUsageFields(b.reqModel, res.UpstreamModel),
	})
	completionUserID := b.subject.UserID
	completionModel := b.reqModel
	completionRuntime := b.binding().recorder
	b.binding().submitOpenAIUsageRecordTask(b.c, res, func(ctx context.Context) {
		if err := completionRuntime.Record(ctx, completionInput, true); err != nil {
			logging.L().With(
				zap.String("component", "handler.openai_gateway.messages"),
				zap.Int64("user_id", completionUserID),
				zap.Int64("api_key_id", completionInput.APIKey.ID),
				zap.Any("group_id", completionInput.APIKey.GroupID),
				zap.String("model", completionModel),
				zap.Int64("provider_id", completionInput.Provider.ID),
			).Error("openai_messages.record_usage_failed", zap.Error(err))
		}
	})
}

// PartialImages 保留 OpenAI Messages 适配差异；提供商重试复用同一核心。
func (b *openAIMessageAttemptBridge) PartialImages(err error) {
	b.reqLog.Warn("openai_messages.forward_partial_error_with_image_result",
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Int("image_count", b.result.ImageCount),
		zap.Error(err),
	)
}

// RetryReady 保留 OpenAI Messages 适配差异；提供商重试复用同一核心。
func (b *openAIMessageAttemptBridge) RetryReady(failure *textflow.AttemptFailure) bool {
	err := failure.Cause
	var failoverErr *forwardcore.UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		return false
	}
	if gatewayhttp.FailoverClientGone(b.c) {
		b.reqLog.Info("openai_messages.failover_aborted_client_disconnected",
			zap.Int64("provider_id", b.provider.Record.ID),
			zap.Int("upstream_status", failoverErr.StatusCode),
		)
		return false
	}
	b.binding().recordOpenAICyberWarning(b.c, b.reqLog, b.apiKey, b.provider, b.reqModel, failoverErr.StatusCode, failoverErr.ResponseBody, err.Error())
	if b.c.Writer.Size() != b.writerSizeBeforeForward {
		b.binding().observeOpenAIProviderHealthFailure(b.c.Request.Context(), b.provider, err)
		b.binding().handleAnthropicFailoverExhausted(b.c, failoverErr, true)
		return false
	}
	if failoverErr.ShouldReportProviderScheduleFailure() {
		b.binding().reportOpenAIProviderScheduleResult(b.provider, OpenAIProviderScheduleModel(b.c, b.provider, b.currentRoutingModel, false, nil), false, nil, err)
	}
	return true
}

// RetryWait 保留 OpenAI Messages 适配差异；提供商重试复用同一核心。
func (b *openAIMessageAttemptBridge) RetryWait(failure *textflow.AttemptFailure, retryLimit, retryCount int, retryDelay time.Duration) {
	var failoverErr *forwardcore.UpstreamFailoverError
	errors.As(failure.Cause, &failoverErr)
	b.reqLog.Warn("openai_messages.pool_mode_same_provider_retry",
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Int("upstream_status", failoverErr.StatusCode),
		zap.Int("retry_limit", retryLimit),
		zap.Int("retry_count", retryCount),
		zap.Duration("retry_delay", retryDelay),
	)
}

// Switching 保留 OpenAI Messages 适配差异；提供商重试复用同一核心。
func (b *openAIMessageAttemptBridge) Switching(failure *textflow.AttemptFailure, switchCount, maxProviderSwitches int) {
	var failoverErr *forwardcore.UpstreamFailoverError
	errors.As(failure.Cause, &failoverErr)
	b.reqLog.Warn("openai_messages.upstream_failover_switching",
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Int("upstream_status", failoverErr.StatusCode),
		zap.Int("switch_count", switchCount),
		zap.Int("max_switches", maxProviderSwitches),
	)
}

// OtherFailure 保留 OpenAI Messages 适配差异；提供商重试复用同一核心。
func (b *openAIMessageAttemptBridge) OtherFailure(err error) {
	if b.result != nil && b.result.ClientDisconnect {
		b.reqLog.Info("openai_messages.client_disconnected",
			zap.Int64("provider_id", b.provider.Record.ID),
			zap.Error(err),
		)
		return
	}
	statusCode := 0
	if v, ok := GetContextInt64(b.c, gatewayhttp.OpsUpstreamStatusCodeKey); ok {
		statusCode = int(v)
	}
	recordedWarning := b.binding().recordOpenAIForwardErrorCyberWarning(b.c, b.reqLog, b.apiKey, b.provider, b.reqModel, statusCode, err)
	if !recordedWarning {
		b.binding().recordOpenAICyberWarning(b.c, b.reqLog, b.apiKey, b.provider, b.reqModel, statusCode, nil, err.Error())
	}
	b.binding().reportOpenAIProviderScheduleResult(b.provider, OpenAIProviderScheduleModel(b.c, b.provider, b.currentRoutingModel, false, b.result), false, nil, err)
	upstreamErrorAlreadyCommunicated := gatewayhttp.OpenAIForwardErrorAlreadyCommunicated(b.c, b.writerSizeBeforeForward, err)
	b.wroteFallback = false
	if !upstreamErrorAlreadyCommunicated && (!recordedWarning || b.c.Writer.Size() == b.writerSizeBeforeForward) {
		b.wroteFallback = b.binding().ensureAnthropicErrorResponse(b.c, *b.streamStarted)
	}
	b.reqLog.Warn("openai_messages.forward_failed",
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Bool("fallback_error_response_written", b.wroteFallback),
		zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
		zap.Error(err),
	)
}

// Success 保留 OpenAI Messages 适配差异；提供商重试复用同一核心。
func (b *openAIMessageAttemptBridge) Success() {
	if b.result != nil {
		b.binding().reportOpenAIProviderScheduleResult(b.provider, OpenAIProviderScheduleModel(b.c, b.provider, b.currentRoutingModel, false, b.result), true, b.result.FirstTokenMs)
	} else {
		b.binding().reportOpenAIProviderScheduleResult(b.provider, OpenAIProviderScheduleModel(b.c, b.provider, b.currentRoutingModel, false, b.result), true, nil)
	}
}

// Completed 保留 OpenAI Messages 适配差异；提供商重试复用同一核心。
func (b *openAIMessageAttemptBridge) Completed(switchCount int) {
	b.reqLog.Debug("openai_messages.request_completed",
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Int("switch_count", switchCount),
	)
}

func (b *openAIMessageAttemptBridge) CanAttempt() bool { return !gatewayhttp.FailoverClientGone(b.c) }
func (b *openAIMessageAttemptBridge) Failed()          {}
func (b *openAIMessageAttemptBridge) Exhausted(failure *textflow.AttemptFailure) {
	var original *forwardcore.UpstreamFailoverError
	if failure != nil && errors.As(failure.Cause, &original) {
		b.binding().handleAnthropicFailoverExhausted(b.c, original, *b.streamStarted)
	} else {
		b.binding().anthropicStreamingAwareError(b.c, http.StatusBadGateway, "api_error", "Upstream request failed", *b.streamStarted)
	}
}
