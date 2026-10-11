package openaiattempt

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

type responsesAttemptBridge struct {
	sessionAttempts                                                          *scheduler.SessionAttempts
	hasBoundSession                                                          bool
	forceCacheBilling                                                        bool
	groupFallbackUsed                                                        bool
	fixed                                                                    *openAIExecutionDependencies
	c                                                                        *gin.Context
	apiKey                                                                   *apikey.APIKey
	subject                                                                  authctx.AuthSubject
	subscription                                                             *billing.UserSubscription
	reqLog                                                                   *zap.Logger
	body, forwardBody, sessionHashBody                                       []byte
	reqModel, forwardModel, sessionHash, previousResponseID, requestPlatform string
	reqStream, nativeCompactionV2, legacyCompact, requireCompact             bool
	streamStarted                                                            *bool
	selectionCtx                                                             context.Context
	groupMapping                                                             routing.GroupMappingResult
	routingStart                                                             time.Time
	requiredCapability                                                       providercore.OpenAIEndpointCapability
	selection                                                                *gatewaycapture.SelectionResult
	provider                                                                 *gatewaycapture.ExecutionProvider
	providerReleaseFunc                                                      func()
	result                                                                   *forwardcore.OpenAIResult
	writerSizeBeforeForward                                                  int
	cyberPolicyHandled, wroteFallback                                        bool
	passthroughFailoverState                                                 openAIPassthroughFailoverState
	fields                                                                   []zap.Field
}

// Select 选择支持请求模型的提供商。
func (b *responsesAttemptBridge) Select(excluded map[int64]struct{}) (textflow.ResponseSelection, error) {
	// Select provider supporting the requested model
	b.reqLog.Debug("openai.provider_selecting", zap.Int("excluded_provider_count", len(excluded)))
	var scheduleDecision scheduler.PlatformDecision
	var err error
	b.selection, scheduleDecision, err = b.binding().selectProviderWithSchedulerForCapability(
		b.selectionCtx,
		b.apiKey.GroupID,
		b.previousResponseID,
		b.sessionHash,
		b.reqModel,
		excluded, egress.OpenAIUpstreamTransportAny, b.requiredCapability,
		b.requireCompact,
		false,
		b.requestPlatform,
	)
	if err != nil {
		return textflow.ResponseSelection{}, err
	}
	if b.selection == nil || b.selection.Provider == nil {
		cls := ClassifyOpenAICompatibleNoProviderErrorFromGin(b.c, b.binding().diagnoser, b.apiKey, b.reqModel, b.reqModel)
		if !cls.ModelNotFound {
			gatewayhttp.MarkOpsRoutingCapacityLimited(b.c)
		}
		b.binding().handleStreamingAwareError(b.c, cls.Status, cls.ErrType, cls.Message, *b.streamStarted)
		return textflow.ResponseSelection{}, nil
	}
	if b.previousResponseID != "" && b.selection != nil && b.selection.Provider != nil {
		b.reqLog.Debug("openai.provider_selected_with_previous_response_id", zap.Int64("provider_id", b.selection.Provider.Record.ID))
	}
	b.reqLog.Debug("openai.provider_schedule_decision",
		zap.String("layer", scheduleDecision.Layer),
		zap.Bool("sticky_previous_hit", scheduleDecision.StickyPreviousHit),
		zap.Bool("sticky_session_hit", scheduleDecision.StickySessionHit),
		zap.Int("candidate_count", scheduleDecision.CandidateCount),
		zap.Int("top_k", scheduleDecision.TopK),
		zap.Int64("latency_ms", scheduleDecision.LatencyMs),
		zap.Float64("load_skew", scheduleDecision.LoadSkew),
	)
	b.provider = b.selection.Provider
	if b.previousResponseID != "" && !b.provider.View().IsOpenAIApiKey() {
		// The public Responses HTTP API supports previous_response_id on API-key
		// providers. OAuth/SetupToken upstreams do not, so keep searching instead
		// of silently deleting continuation state from a mixed provider pool.

		if b.selection.ReleaseFunc != nil {
			b.selection.ReleaseFunc()
			b.selection.ReleaseFunc = nil
		}
		skipped := &forwardcore.UpstreamFailoverError{
			StatusCode:       http.StatusBadRequest,
			Stage:            forwardcore.GatewayFailureStageInference,
			Scope:            forwardcore.GatewayFailureScopeRequest,
			Reason:           forwardcore.OpenAIHTTPContinuationUnsupportedReason,
			ClientStatusCode: http.StatusBadRequest,
			ClientMessage:    "previous_response_id requires an OpenAI API-key provider for HTTP requests",
		}
		b.reqLog.Debug("openai.provider_skipped_http_continuation_unsupported",
			zap.Int64("provider_id", b.provider.Record.ID),
			zap.String("provider_type", b.provider.Record.Type),
		)
		picked := b.selectedView()
		picked.Skip = &textflow.AttemptFailure{Cause: skipped, Policy: skipped.RetryFailure()}
		return picked, nil
	}
	b.sessionHash = EnsureOpenAIPoolModeSessionHash(b.sessionHash, b.provider)
	b.reqLog.Debug("openai.provider_selected", zap.Int64("provider_id", b.provider.Record.ID), zap.String("provider_name", b.provider.Record.Name))
	gatewayhttp.SetOpsSelectedProvider(b.c, b.provider.Record.ID, b.provider.Record.Platform)

	return b.selectedView(), nil
}

// SelectionFailure 将选择失败写为客户端错误。
func (b *responsesAttemptBridge) SelectionFailure(err error, excludedCount int, last *textflow.AttemptFailure) {
	var lastFailoverErr *forwardcore.UpstreamFailoverError
	if last != nil {
		errors.As(last.Cause, &lastFailoverErr)
	}

	if gatewayhttp.FailoverClientGone(b.c) {
		b.reqLog.Info("openai.provider_select_aborted_client_disconnected", zap.Error(err))
		return
	}
	b.reqLog.Warn("openai.provider_select_failed",
		zap.Error(gatewayhttp.OpenAICompatibleSelectionErrorForLog(err, b.requestPlatform)),
		zap.Int("excluded_provider_count", excludedCount),
	)
	if excludedCount == 0 {
		if b.legacyCompact && errors.Is(err, scheduler.ErrNoAvailableCompactProviders) {
			gatewayhttp.MarkOpsRoutingCapacityLimitedIfNoAvailable(b.c, err)
			b.binding().handleStreamingAwareError(b.c, http.StatusServiceUnavailable, "compact_not_supported", "No available providers support /responses/compact", *b.streamStarted)
			return
		}
		cls := ClassifyNoProviderErrorFromGin(b.c, b.binding().diagnoser, b.apiKey, b.reqModel, b.reqModel, b.requestPlatform)
		cls = classifySelectionFailureError(err, cls)
		if !cls.ModelNotFound {
			gatewayhttp.MarkOpsRoutingCapacityLimitedIfNoAvailable(b.c, err)
		}
		b.binding().handleStreamingAwareError(b.c, cls.Status, cls.ErrType, cls.Message, *b.streamStarted)
		return
	}
	if lastFailoverErr != nil {
		b.binding().handleFailoverExhausted(b.c, lastFailoverErr, *b.streamStarted)
	} else {
		b.binding().handleFailoverExhaustedSimple(b.c, 502, *b.streamStarted)
	}
}

// Acquire 取得当前提供商的并发槽位。
func (b *responsesAttemptBridge) Acquire() bool {
	if b.sessionAttempts != nil && b.binding().sessions.Track != nil {
		b.binding().sessions.Track(b.sessionAttempts, b.provider, b.sessionHash)
	}
	var acquired bool
	b.providerReleaseFunc, acquired = b.binding().acquireResponsesProviderSlot(b.c, b.apiKey.GroupID, b.sessionHash, b.selection, b.reqStream, b.streamStarted, b.reqLog)
	return acquired
}

// Forward 向当前提供商转发请求并捕获响应结果。
func (b *responsesAttemptBridge) Forward() textflow.ResponseOutcome {
	var err error
	// Forward request
	gatewayhttp.SetOpsLatencyMs(b.c, gatewayhttp.OpsRoutingLatencyMsKey, time.Since(b.routingStart).Milliseconds())
	forwardStart := time.Now()
	// 快照扣除心跳注释字节，上游失败时仍可按协议输出状态决定换号（#3887）。
	b.writerSizeBeforeForward = gatewayhttp.OpenAICompactKeepaliveAdjustedWrittenSize(b.c)
	// 从透传切换到非透传提供商时，从 canonical 请求体派生本次正文并清除上游私有的加密 reasoning。
	attemptBody := b.binding().deriveOpenAIForwardAttemptBody(b.reqLog, b.forwardBody, b.provider, &b.passthroughFailoverState)
	b.result, err = func() (*forwardcore.OpenAIResult, error) {
		defer func() {
			if b.providerReleaseFunc != nil {
				b.providerReleaseFunc()
			}
		}()
		return b.binding().forward(b.c.Request.Context(), b.c, b.provider, attemptBody)
	}()
	var cyberBlockBodyHTTP []byte
	if gatewayhttp.GetOpsCyberPolicy(b.c) != nil {
		cyberBlockBodyHTTP = b.sessionHashBody
	}
	b.cyberPolicyHandled = b.binding().recordCyberPolicyIfMarked(b.c, b.apiKey, b.provider, b.subscription, b.reqModel, err != nil, cyberBlockBodyHTTP, gatewayhttp.ClientRequestedUsageFields(b.c, b.groupMapping, b.reqModel, ""), billing.HashUsageRequestPayload(b.body), b.nativeCompactionV2)
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
	if retry, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		out.Failure = &textflow.AttemptFailure{Cause: err, Policy: retry.RetryFailure()}
		out.FirstOutputRecovery = retry.SafeToFailoverAfterWrite
	}
	return out
}

// Complete 捕获用量与请求数据并提交完成任务。
func (b *responsesAttemptBridge) Complete() {
	res := b.result

	if res == nil {
		return
	}
	completionInput := b.captureCompletion(gatewaycapture.OpenAICapture{
		Result:             res,
		RequestPayloadHash: billing.HashUsageRequestPayload(b.body),
		PricingUsageFields: b.groupMapping.ToUsageFields(b.reqModel, res.UpstreamModel),
		NativeCompactionV2: b.nativeCompactionV2,
	})
	completionUserID := b.subject.UserID
	completionModel := b.reqModel
	completionRuntime := b.binding().recorder
	b.binding().submitOpenAIUsageRecordTask(b.c, res, func(ctx context.Context) {
		if err := completionRuntime.Record(ctx, completionInput, true); err != nil {
			logging.L().With(
				zap.String("component", "handler.openai_gateway.responses"),
				zap.Int64("user_id", completionUserID),
				zap.Int64("api_key_id", completionInput.APIKey.ID),
				zap.Any("group_id", completionInput.APIKey.GroupID),
				zap.String("model", completionModel),
				zap.Int64("provider_id", completionInput.Provider.ID),
			).Error("openai.record_usage_failed", zap.Error(err))
		}
	})
}

// captureCompletion 在提交前复制请求和转发结果，供后台记录器使用。
func (b *responsesAttemptBridge) captureCompletion(capture gatewaycapture.OpenAICapture) *completion.Input {
	gatewayhttp.StampOpenAIRequestedReasoningEffort(capture.Result, b.c)
	capture.UserAgent = b.c.GetHeader("User-Agent")
	capture.IPAddress = clientip.GetClientIP(b.c)
	capture.InboundEndpoint = gatewayhttp.GetInboundEndpoint(b.c)
	capture.UpstreamEndpoint = ResolveOpenAIUpstreamEndpoint(b.c, b.provider, capture.Result)
	capture.ClientSessionID = gatewayhttp.ExtractClientSessionID(b.c)
	capture.APIKey = b.apiKey
	capture.User = b.apiKey.User
	capture.Provider = gatewaycapture.ExecutionCompletionRecord(b.provider)
	capture.Subscription = b.subscription
	capture.RequestBody = b.body
	capture.APIKeyService = b.binding().apiKeyService
	capture.CyberBlocked = b.cyberPolicyHandled
	return gatewaycapture.CaptureOpenAI(gatewayhttp.CompletionContext(b.c), &capture)
}

// PartialImages 记录已产出部分图片时发生的转发错误。
func (b *responsesAttemptBridge) PartialImages(err error) {
	b.reqLog.Warn("openai.forward_partial_error_with_image_result",
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Int("image_count", b.result.ImageCount),
		zap.Error(err),
	)
}

// RetryReady 检查客户端连接和输出状态，决定是否允许重试。
func (b *responsesAttemptBridge) RetryReady(failure *textflow.AttemptFailure) bool {
	err := failure.Cause
	var failoverErr *forwardcore.UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		return false
	}
	if gatewayhttp.FailoverClientGone(b.c) {
		b.reqLog.Info("openai.failover_aborted_client_disconnected",
			zap.Int64("provider_id", b.provider.Record.ID),
			zap.Int("upstream_status", failoverErr.StatusCode),
		)
		return false
	}
	b.binding().recordOpenAICyberWarning(b.c, b.reqLog, b.apiKey, b.provider, b.reqModel, failoverErr.StatusCode, failoverErr.ResponseBody, err.Error())
	if !OpenAIForwardMayFailover(b.c, b.writerSizeBeforeForward, failoverErr) {
		b.binding().observeOpenAIProviderHealthFailure(b.c.Request.Context(), b.provider, err)
		b.binding().handleFailoverExhausted(b.c, failoverErr, true)
		return false
	}
	// OpenAIForwardMayFailover 已确认可以换号。响应头已提交时，重试耗尽仍返回 SSE 流内错误。
	if b.c.Writer.Written() {
		*b.streamStarted = true
	}
	if failoverErr.ShouldReportProviderScheduleFailure() {
		b.binding().reportOpenAIProviderScheduleResult(b.provider, OpenAIProviderScheduleModel(b.c, b.provider, b.forwardModel, b.requireCompact, nil), false, nil, err)
	}
	return true
}

// RetryWait 记录同一提供商的重试次数和等待时间。
func (b *responsesAttemptBridge) RetryWait(failure *textflow.AttemptFailure, retryLimit, retryCount int, retryDelay time.Duration) {
	var failoverErr *forwardcore.UpstreamFailoverError
	errors.As(failure.Cause, &failoverErr)
	b.reqLog.Warn("openai.pool_mode_same_provider_retry",
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Int("upstream_status", failoverErr.StatusCode),
		zap.Int("retry_limit", retryLimit),
		zap.Int("retry_count", retryCount),
		zap.Duration("retry_delay", retryDelay),
	)
}

// Switching 记录提供商切换次数和代理信息。
func (b *responsesAttemptBridge) Switching(failure *textflow.AttemptFailure, switchCount, maxProviderSwitches int) {
	var failoverErr *forwardcore.UpstreamFailoverError
	errors.As(failure.Cause, &failoverErr)
	failoverSwitchFields := []zap.Field{
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Int("upstream_status", failoverErr.StatusCode),
		zap.Int("switch_count", switchCount),
		zap.Int("max_switches", maxProviderSwitches),
	}
	failoverSwitchFields = AppendOpenAIProviderProxyLogFields(failoverSwitchFields, b.provider)
	b.reqLog.Warn("openai.upstream_failover_switching", failoverSwitchFields...)
}

// OtherFailure 记录转发错误并补充尚未输出的客户端错误。
func (b *responsesAttemptBridge) OtherFailure(err error) {
	statusCode := 0
	if v, ok := GetContextInt64(b.c, gatewayhttp.OpsUpstreamStatusCodeKey); ok {
		statusCode = int(v)
	}
	recordedWarning := b.binding().recordOpenAIForwardErrorCyberWarning(b.c, b.reqLog, b.apiKey, b.provider, b.reqModel, statusCode, err)
	if !recordedWarning {
		b.binding().recordOpenAICyberWarning(b.c, b.reqLog, b.apiKey, b.provider, b.reqModel, statusCode, nil, err.Error())
	}
	b.binding().reportOpenAIProviderScheduleResult(b.provider, OpenAIProviderScheduleModel(b.c, b.provider, b.forwardModel, b.requireCompact, b.result), false, nil, err)
	upstreamErrorAlreadyCommunicated := gatewayhttp.OpenAIForwardErrorAlreadyCommunicated(b.c, b.writerSizeBeforeForward, err)
	b.wroteFallback = false
	// cyber warning 处理可能已写出 response.failed 或 JSON 错误，
	// 补充错误前检查输出状态，使客户端收到一次终止错误。
	if !upstreamErrorAlreadyCommunicated && (!recordedWarning || gatewayhttp.OpenAICompactKeepaliveAdjustedWrittenSize(b.c) == b.writerSizeBeforeForward) {
		b.wroteFallback = b.binding().ensureOpenAIForwardErrorResponse(b.c, *b.streamStarted, err)
	}
	b.fields = []zap.Field{
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Bool("fallback_error_response_written", b.wroteFallback),
		zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
		zap.Error(err),
	}
}

// Failed 按错误分类记录转发失败日志。
func (b *responsesAttemptBridge) Failed() {
	if gatewayhttp.ShouldLogOpenAIForwardFailureAsWarn(b.c, b.wroteFallback) {
		b.reqLog.Warn("openai.forward_failed", b.fields...)
		return
	}
	b.reqLog.Error("openai.forward_failed", b.fields...)
}

// Success 更新用量快照并报告调度结果。
func (b *responsesAttemptBridge) Success() {
	if b.result != nil {
		// 排除 spark 影子:其 codex_* 仅由 QueryUsage(/wham/usage bengalfox)更新。
		if b.provider.View().IsOpenAI() && b.provider.Record.Type == capability.ProviderTypeOAuth && !b.provider.View().IsShadow() {
			b.binding().updateCodexUsageSnapshotFromHeaders(b.c.Request.Context(), b.provider.Record.ID, b.result.ResponseHeaders)
		}
		b.binding().reportOpenAIProviderScheduleResult(b.provider, OpenAIProviderScheduleModel(b.c, b.provider, b.forwardModel, b.requireCompact, b.result), b.result.SucceededForScheduling(), b.result.FirstTokenMs)
	} else {
		b.binding().reportOpenAIProviderScheduleResult(b.provider, OpenAIProviderScheduleModel(b.c, b.provider, b.forwardModel, b.requireCompact, b.result), b.result.SucceededForScheduling(), nil)
	}
}

// Completed 记录请求完成时的提供商和切换次数。
func (b *responsesAttemptBridge) Completed(switchCount int) {
	b.reqLog.Debug("openai.request_completed",
		zap.Int64("provider_id", b.provider.Record.ID),
		zap.Int("switch_count", switchCount),
	)
}

func (b *responsesAttemptBridge) Context() context.Context { return b.c.Request.Context() }
func (b *responsesAttemptBridge) CanAttempt() bool         { return OpenAIRequestAllowsFailoverReplay(b.c) }

func (b *responsesAttemptBridge) selectedView() textflow.ResponseSelection {
	return textflow.ResponseSelection{Selection: gatewaycapture.CaptureTextSelection(b.provider), Available: true, OAuth: failover.OAuth429Provider{OpenAI: b.provider.View().IsOpenAIOAuthLike(), Grok: b.provider.Record.Platform == capability.PlatformGrok && b.provider.Record.Type == capability.ProviderTypeOAuth}}
}

func (b *responsesAttemptBridge) Exhausted(failure *textflow.AttemptFailure) {
	var original *forwardcore.UpstreamFailoverError
	if failure != nil && errors.As(failure.Cause, &original) {
		b.binding().handleFailoverExhausted(b.c, original, *b.streamStarted)
	} else {
		b.binding().handleFailoverExhaustedSimple(b.c, 502, *b.streamStarted)
	}
}

func (b *responsesAttemptBridge) Switched() {
	if b.sessionAttempts != nil && b.provider != nil {
		b.sessionAttempts.Abandon(b.provider.Record.ID)
	}
	b.binding().recordOpenAIProviderSwitchForSelection(b.selection)
}
