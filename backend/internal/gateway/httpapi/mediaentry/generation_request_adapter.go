package mediaentry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/openaiattempt"
	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	routingerrors "github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	upstreamgrok "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type generationRequestAdapter struct {
	grok                                    bool
	h                                       *Runtime
	c                                       *gin.Context
	apiKey                                  *apikey.APIKey
	subject                                 authctx.AuthSubject
	subscription                            *billing.UserSubscription
	reqLog                                  *zap.Logger
	streamStarted                           *bool
	parsed                                  *gatewaymedia.ImageRequest
	body                                    []byte
	requestModel, routingModel, sessionHash string
	groupMapping                            routingerrors.GroupMappingResult
	endpoint                                upstreamgrok.GrokMediaEndpoint
	requestID, contentType, videoCreated    string
	boundProviderID                         int64
	selection                               *gatewaycapture.SelectionResult
	decision                                scheduler.PlatformDecision
	oauth429                                failover.OAuth429State
	writerBefore                            int
}

func (p *generationRequestAdapter) SelectGeneration(ctx context.Context, excluded map[int64]struct{}) (gatewaymedia.GenerationSelection, bool, error) {
	var err error
	if p.grok {
		p.selection, p.decision, err = p.h.bindings.Common.Selection.SelectProviderWithSchedulerForCapability(ctx, p.apiKey.GroupID, "", p.sessionHash, p.routingModel, excluded, egress.OpenAIUpstreamTransportHTTPSSE, grokMediaRequiredCapability(p.endpoint), false, false, capability.PlatformGrok)
	} else {
		p.selection, p.decision, err = p.h.bindings.Platform.SelectImages(ctx, p.apiKey.GroupID, p.sessionHash, p.requestModel, excluded, p.parsed.RequiredCapability)
	}
	if p.selection == nil || p.selection.Provider == nil {
		return gatewaymedia.GenerationSelection{}, false, err
	}
	return gatewaymedia.GenerationSelection{Provider: gatewaycapture.ExecutionSnapshot(p.selection.Provider), RetryLimit: p.selection.Provider.View().GetPoolModeRetryCount()}, true, err
}

func (p *generationRequestAdapter) ActivateGeneration(_ gatewaymedia.GenerationSelection) {
	provider := p.selection.Provider
	if !p.grok && p.usesGrok() {
		p.endpoint = upstreamgrok.GrokMediaEndpointImagesGenerations
		if p.parsed != nil && p.parsed.IsEdits() {
			p.endpoint = upstreamgrok.GrokMediaEndpointImagesEdits
		}
	}
	if !p.usesGrok() {
		p.logSchedule()
	}
	p.sessionHash = openaiattempt.EnsureOpenAIPoolModeSessionHash(p.sessionHash, provider)
	if !p.usesGrok() {
		p.reqLog.Debug("openai.images.provider_selected", zap.Int64("provider_id", provider.Record.ID), zap.String("provider_name", provider.Record.Name))
	}
	gatewayhttp.SetOpsSelectedProvider(p.c, provider.Record.ID, provider.Record.Platform)
}

func (p *generationRequestAdapter) GenerationEligible(ctx context.Context, _ gatewaymedia.GenerationSelection) (bool, string, error) {
	return p.h.ensureGrokMediaProviderEligibility(ctx, p.selection.Provider)
}

func (p *generationRequestAdapter) AcquireGeneration(_ context.Context, _ gatewaymedia.GenerationSelection) (func(), bool) {
	stream := false
	if p.parsed != nil {
		stream = p.parsed.Stream
	}
	return p.h.bindings.Common.Support.AcquireResponsesProviderSlot(p.c, p.apiKey.GroupID, p.sessionHash, p.selection, stream, p.streamStarted, p.reqLog)
}

func (p *generationRequestAdapter) StartGenerationKeepalive() func() {
	return gatewayhttp.StartOpenAIImagesJSONKeepalive(p.c, p.h.bindings.Options.ImageKeepalive)
}

func (p *generationRequestAdapter) ForwardGeneration(ctx context.Context, _ gatewaymedia.GenerationSelection, body []byte) gatewaymedia.GenerationOutcome {
	provider := p.selection.Provider
	var result *forwardcore.OpenAIResult
	var err error
	p.writerBefore = p.c.Writer.Size()
	if p.usesGrok() {
		result, err = p.h.bindings.Platform.GrokMedia(ctx, p.c, provider, p.endpoint, p.requestID, body, p.contentType)
	} else {
		p.writerBefore = gatewayhttp.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(p.c)
		match := p.h.bindings.Common.Forward.MatchOpenAITLSFingerprintRouterForRequest(p.c, provider)
		err = p.h.bindings.Common.Forward.EnforceOpenAIClientPolicyForRequest(ctx, p.c, provider, body, match)
		if err == nil {
			result, err = p.h.bindings.Platform.Images(ctx, p.c, provider, body, p.parsed, p.routingModel, match)
		}
	}
	after := p.c.Writer.Size()
	if !p.usesGrok() {
		after = gatewayhttp.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(p.c)
	}
	outcome := gatewaymedia.GenerationOutcome{Result: generationResultView(result), Err: err, OutputChanged: after != p.writerBefore}
	if failure, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		outcome.Failure = failure.RetryFailure()
		outcome.ReportFailure = failure.ShouldReportProviderScheduleFailure()
	}
	if imageErr, ok := errors.AsType[*openai.OpenAIImagesUpstreamError](err); ok {
		outcome.ImageError = true
		outcome.ImageErrorRetryable = openai.IsOpenAIImagesRetryableUpstreamError(imageErr)
		outcome.ImageErrorStatus = imageErr.StatusCode
		outcome.ImageErrorType = imageErr.ErrorType
		outcome.ImageErrorCode = imageErr.Code
	}
	return outcome
}

func (p *generationRequestAdapter) ReportGeneration(_ context.Context, _ gatewaymedia.GenerationSelection, value *gatewaymedia.GenerationResult, success bool, err error) {
	provider := p.selection.Provider
	result := legacyGenerationResult(value)
	if p.usesGrok() {
		p.h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(provider, grokMediaScheduleModel(provider, p.routingModel, result), success, nil)
		return
	}
	if success {
		p.h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(provider, openaiattempt.OpenAIProviderScheduleModel(p.c, provider, p.requestModel, false, result), true, nil)
		return
	}
	p.h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(provider, openaiattempt.OpenAIProviderScheduleModel(p.c, provider, p.requestModel, false, result), false, nil, err)
}

func (p *generationRequestAdapter) SwitchGeneration(gatewaymedia.GenerationSelection) {
	p.h.bindings.Common.Selection.RecordOpenAIProviderSwitchForSelection(p.selection)
}

func (p *generationRequestAdapter) StopGeneration429(_ gatewaymedia.GenerationSelection, status, count int) bool {
	return p.h.bindings.Platform.Stop429(p.selection.Provider, status, count, &p.oauth429)
}

func (p *generationRequestAdapter) GenerationClientGone() bool {
	return gatewayhttp.FailoverClientGone(p.c)
}

func (p *generationRequestAdapter) logSchedule() {
	name := "openai.images"
	if p.usesGrok() {
		name = "grok_media"
	}
	d := p.decision
	p.reqLog.Debug(name+".provider_schedule_decision", zap.String("layer", d.Layer), zap.Bool("sticky_session_hit", d.StickySessionHit), zap.Int("candidate_count", d.CandidateCount), zap.Int("top_k", d.TopK), zap.Int64("latency_ms", d.LatencyMs), zap.Float64("load_skew", d.LoadSkew))
}

func (p *generationRequestAdapter) ObserveGeneration(e gatewaymedia.GenerationEvent) {
	name := "openai.images"
	if p.usesGrok() {
		name = "grok_media"
	}
	status := 0
	if e.Outcome.Failure != nil {
		status = e.Outcome.Failure.StatusCode
	}
	switch e.Kind {
	case "selecting":
		p.reqLog.Debug(name+".provider_selecting", zap.Int("excluded_provider_count", e.Excluded))
	case "select_failed":
		p.reqLog.Warn(name+".provider_select_failed", zap.Error(e.Outcome.Err), zap.Int("excluded_provider_count", e.Excluded))
	case "select_canceled":
		p.reqLog.Info(name+".provider_select_aborted_client_disconnected", zap.Error(e.Outcome.Err))
	case "forward_canceled":
		p.reqLog.Info(name+".failover_aborted_client_disconnected", zap.Int64("provider_id", e.Selection.Provider.ID), zap.Int("upstream_status", status))
	case "schedule":
		p.logSchedule()
	case "ineligible":
		p.reqLog.Warn(name+".provider_eligibility_rejected", zap.Int64("provider_id", e.Selection.Provider.ID), zap.String("reason", e.Reason), zap.Bool("probe_failed", e.ProbeFailed))
	case "routing":
		gatewayhttp.SetOpsLatencyMs(p.c, gatewayhttp.OpsRoutingLatencyMsKey, e.Elapsed.Milliseconds())
	case "response":
		elapsed := e.Elapsed.Milliseconds()
		upstream, _ := openaiattempt.GetContextInt64(p.c, gatewayhttp.OpsUpstreamLatencyMsKey)
		if upstream > 0 && elapsed > upstream {
			elapsed -= upstream
		}
		gatewayhttp.SetOpsLatencyMs(p.c, gatewayhttp.OpsResponseLatencyMsKey, elapsed)
		if !p.usesGrok() && e.Outcome.Result != nil && e.Outcome.Result.FirstTokenMs != nil {
			gatewayhttp.SetOpsLatencyMs(p.c, gatewayhttp.OpsTimeToFirstTokenMsKey, int64(*e.Outcome.Result.FirstTokenMs))
		}
	case "partial":
		p.reqLog.Warn(name+".forward_partial_error_with_image_result", zap.Int64("provider_id", e.Selection.Provider.ID), zap.Int("image_count", e.Outcome.Result.ImageCount), zap.Error(e.Outcome.Err))
	case "image_error":
		event := "upstream_user_error"
		if e.Outcome.ImageErrorRetryable {
			event = "upstream_server_error_after_flush"
		}
		p.reqLog.Warn(name+"."+event, zap.Int64("provider_id", e.Selection.Provider.ID), zap.Int("status_code", e.Outcome.ImageErrorStatus), zap.String("error_type", e.Outcome.ImageErrorType), zap.String("error_code", e.Outcome.ImageErrorCode), zap.Error(e.Outcome.Err))
	case "after_output":
		p.reqLog.Warn(name+".upstream_failover_skipped_after_flush", zap.Int64("provider_id", e.Selection.Provider.ID), zap.Int("upstream_status", status))
	case "retry":
		p.reqLog.Warn(name+".pool_mode_same_provider_retry", zap.Int64("provider_id", e.Selection.Provider.ID), zap.Int("upstream_status", status), zap.Int("retry_limit", e.RetryLimit), zap.Int("retry_count", e.RetryCount), zap.Duration("retry_delay", e.Delay))
	case "switch":
		p.reqLog.Warn(name+".upstream_failover_switching", zap.Int64("provider_id", e.Selection.Provider.ID), zap.Int("upstream_status", status), zap.Int("switch_count", e.Switches), zap.Int("max_switches", e.MaxSwitches))
	case "completed":
		p.reqLog.Debug(name+".request_completed", zap.Int64("provider_id", e.Selection.Provider.ID), zap.Int("switch_count", e.Switches))
	}
}

func (p *generationRequestAdapter) EndGeneration(f gatewaymedia.GenerationFailure) {
	id := int64(0)
	if p.selection != nil && p.selection.Provider != nil {
		id = p.selection.Provider.Record.ID
	}
	gatewayhttp.WriteGenerationFailure(f, gatewayhttp.MediaFailureContext{Grok: p.usesGrok(), Generation: p.endpoint.IsGenerationRequest(), StreamStarted: *p.streamStarted, BoundProviderID: p.boundProviderID, ProviderID: id, WriterBefore: p.writerBefore, Context: p.c, Log: p.reqLog}, p)
}

func (p *generationRequestAdapter) CompleteGeneration(ctx context.Context, _ gatewaymedia.GenerationSelection, value *gatewaymedia.GenerationResult) {
	if p.usesGrok() {
		p.completeGrok(ctx, value)
	} else {
		p.completeImages(value)
	}
}

func (p *generationRequestAdapter) completeImages(value *gatewaymedia.GenerationResult) {
	h, c, apiKey, provider, requestModel, parsed, body, subscription, subject, groupMapping := p.h, p.c, p.apiKey, p.selection.Provider, p.requestModel, p.parsed, p.body, p.subscription, p.subject, p.groupMapping
	result := legacyGenerationResult(value)
	if result != nil {
		// Spark shadow codex_* usage is updated through QueryUsage (/wham/usage bengalfox).
		if provider.Record.Type == capability.ProviderTypeOAuth && !provider.View().IsShadow() {
			h.bindings.Common.Selection.UpdateCodexUsageSnapshotFromHeaders(c.Request.Context(), provider.Record.ID, result.ResponseHeaders)
		}
		h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(provider, openaiattempt.OpenAIProviderScheduleModel(c, provider, requestModel, false, result), true, result.FirstTokenMs)
	} else {
		h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(provider, openaiattempt.OpenAIProviderScheduleModel(c, provider, requestModel, false, result), true, nil)
	}

	userAgent := c.GetHeader("User-Agent")
	clientIP := clientip.GetClientIP(c)
	requestPayloadHash := billing.HashUsageRequestPayload(body)
	if parsed.Multipart {
		requestPayloadHash = billing.HashUsageRequestPayload([]byte(parsed.StickySessionSeed()))
	}
	inboundEndpoint := gatewayhttp.GetInboundEndpoint(c)
	upstreamEndpoint := gatewayhttp.GetUpstreamEndpoint(c, provider.Record.Platform)

	clientSessionID := gatewayhttp.ExtractClientSessionID(c)

	upstreamModel := ""
	if result != nil {
		upstreamModel = result.UpstreamModel
	}
	completionInput := gatewaycapture.CaptureOpenAI(c.Request.Context(), &gatewaycapture.OpenAICapture{
		Result:             result,
		APIKey:             apiKey,
		User:               apiKey.User,
		Provider:           gatewaycapture.ExecutionCompletionRecord(provider),
		Subscription:       subscription,
		InboundEndpoint:    inboundEndpoint,
		UpstreamEndpoint:   upstreamEndpoint,
		UserAgent:          userAgent,
		IPAddress:          clientIP,
		RequestPayloadHash: requestPayloadHash,
		RequestBody:        body,
		APIKeyService:      h.bindings.Quota,

		ClientSessionID:    clientSessionID,
		PricingUsageFields: groupMapping.ToUsageFields(requestModel, upstreamModel),
	})
	completionRecorder := h.bindings.Common.Recorder
	completionLog := logging.L().With(
		zap.String("component", "handler.openai_gateway.images"),
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
		zap.String("model", requestModel),
		zap.Int64("provider_id", provider.Record.ID),
	)
	h.bindings.Common.Support.Submission.SubmitMandatory(c, func(ctx context.Context) {
		if err := completionRecorder.Record(ctx, completionInput, true); err != nil {
			completionLog.Error("openai.images.record_usage_failed", zap.Error(err))
		}
	})
}

func (p *generationRequestAdapter) completeGrok(requestCtx context.Context, value *gatewaymedia.GenerationResult) {
	h, c, apiKey, reqLog, provider, requestModel, body, subscription, subject, groupMapping, endpoint, requestID, videoCreateStartedAt := p.h, p.c, p.apiKey, p.reqLog, p.selection.Provider, p.requestModel, p.body, p.subscription, p.subject, p.groupMapping, p.endpoint, p.requestID, p.videoCreated
	result := legacyGenerationResult(value)
	if isGrokVideoCreateEndpoint(endpoint) && strings.TrimSpace(result.ResponseID) != "" {
		// Defer video billing until a completion query; retain the model, duration, and resolution for pricing.
		pending := gatewaymedia.GrokVideoPendingBilling{
			Model:                requestModel,
			BillingModel:         firstNonEmptyString(result.BillingModel, requestModel),
			UpstreamModel:        result.UpstreamModel,
			VideoResolution:      result.VideoResolution,
			VideoDurationSeconds: result.VideoDurationSeconds,
			OriginalModel:        gatewayhttp.ClientRequestedModel(c, requestModel),
			// Measure elapsed wall time from creation acceptance to the first observed completion.
			CreatedAt: videoCreateStartedAt,
		}
		h.bindings.VideoTasks().TrackCreated(requestCtx, apiKey.GroupID, result.ResponseID, subject.UserID, apiKey.ID, provider.Record.ID, pending, grokVideoObserver{log: reqLog})
	}
	if endpoint == upstreamgrok.GrokMediaEndpointVideoStatus || endpoint == upstreamgrok.GrokMediaEndpointVideoContent {
		taskID := strings.TrimSpace(requestID)
		billingKey := gatewaymedia.StableGrokVideoBillingRequestID(taskID)
		telemetry.RecordRelatedRequest(requestCtx, telemetry.RequestRecord{RequestID: telemetry.StableRequestID(fmt.Sprintf("%d/%s", apiKey.ID, billingKey)), State: firstNonEmptyString(result.VideoState, "running"), Aliases: []telemetry.RequestAlias{{Kind: "task", Value: taskID}, {Kind: "billing", Value: billingKey}}})
		if billResult := prepareGrokVideoCompletionBilling(requestCtx, h, reqLog, apiKey, subject, taskID, result); billResult != nil {
			recordGrokMediaUsage(c, h, reqLog, apiKey, subject, subscription, provider, billResult, billResult.Model, groupMapping, body, taskID)
		}
	} else if shouldRecordGrokMediaUsage(endpoint, requestModel, result) {
		recordGrokMediaUsage(c, h, reqLog, apiKey, subject, subscription, provider, result, requestModel, groupMapping, body, requestID)
	}
}

func generationResultView(r *forwardcore.OpenAIResult) *gatewaymedia.GenerationResult {
	if r == nil {
		return nil
	}
	return gatewaymedia.CloneGenerationResult(&gatewaymedia.GenerationResult{RequestID: r.RequestID, ResponseID: r.ResponseID, Model: r.Model, BillingModel: r.BillingModel, UpstreamModel: r.UpstreamModel, Usage: r.Usage, Stream: r.Stream, Duration: r.Duration, FirstTokenMs: r.FirstTokenMs, ImageCount: r.ImageCount, VideoCount: r.VideoCount, VideoState: r.VideoState, VideoDurationSeconds: r.VideoDurationSeconds, ImageSize: r.ImageSize, ImageInputSize: r.ImageInputSize, ImageOutputSize: r.ImageOutputSize, ImageSizeSource: r.ImageSizeSource, VideoResolution: r.VideoResolution, ImageOutputSizes: r.ImageOutputSizes, ImageSizeBreakdown: r.ImageSizeBreakdown, Headers: http.Header(r.UpstreamHeaders).Clone(), ResponseHeaders: http.Header(r.ResponseHeaders).Clone()})
}

func legacyGenerationResult(r *gatewaymedia.GenerationResult) *forwardcore.OpenAIResult {
	if r == nil {
		return nil
	}
	r = gatewaymedia.CloneGenerationResult(r)
	return &forwardcore.OpenAIResult{RequestID: r.RequestID, ResponseID: r.ResponseID, Model: r.Model, BillingModel: r.BillingModel, UpstreamModel: r.UpstreamModel, Usage: r.Usage, Stream: r.Stream, Duration: r.Duration, FirstTokenMs: r.FirstTokenMs, ImageCount: r.ImageCount, VideoCount: r.VideoCount, VideoState: r.VideoState, VideoDurationSeconds: r.VideoDurationSeconds, ImageSize: r.ImageSize, ImageInputSize: r.ImageInputSize, ImageOutputSize: r.ImageOutputSize, ImageSizeSource: r.ImageSizeSource, VideoResolution: r.VideoResolution, ImageOutputSizes: r.ImageOutputSizes, ImageSizeBreakdown: r.ImageSizeBreakdown, UpstreamHeaders: http.Header(r.Headers).Clone(), ResponseHeaders: http.Header(r.ResponseHeaders).Clone()}
}

// MediaClassify uses the shared no-provider classification for the current media route.
func (p *generationRequestAdapter) MediaClassify() gatewayhttp.MediaNoProvider {
	platform := ""
	routing := p.requestModel
	if p.usesGrok() {
		platform = capability.PlatformGrok
		routing = p.routingModel
	}
	result := openaiattempt.ClassifyNoProviderErrorFromGin(p.c, p.h.bindings.Common.Diagnoser, p.apiKey, p.requestModel, routing, platform)
	return gatewayhttp.MediaNoProvider{ModelNotFound: result.ModelNotFound, Status: result.Status, Type: result.ErrType, Message: result.Message}
}

func (p *generationRequestAdapter) MediaNoAvailable(err error) bool {
	return errors.Is(err, scheduler.ErrNoAvailableProviders)
}

func (p *generationRequestAdapter) MediaCapacity(err error, conditional bool) {
	if conditional {
		gatewayhttp.MarkOpsRoutingCapacityLimitedIfNoAvailable(p.c, err)
	} else {
		gatewayhttp.MarkOpsRoutingCapacityLimited(p.c)
	}
}

func (p *generationRequestAdapter) MediaError(status int, typ, message string, stream bool) {
	if stream {
		gatewayhttp.DefaultOpenAIErrorOutput().StreamError(p.c, status, typ, message, *p.streamStarted)
	} else {
		gatewayhttp.DefaultOpenAIErrorOutput().WriteError(p.c, status, typ, message)
	}
}

func (p *generationRequestAdapter) MediaFailover(err error, stream bool) {
	if value, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		p.h.bindings.Common.Support.HandleFailoverExhausted(p.c, value, stream)
	}
}

func (p *generationRequestAdapter) MediaSimpleExhausted() {
	p.h.bindings.Common.Support.HandleFailoverExhaustedSimple(p.c, 502, *p.streamStarted)
}

func (p *generationRequestAdapter) mediaStatus() int {
	status, _ := openaiattempt.GetContextInt64(p.c, gatewayhttp.OpsUpstreamStatusCodeKey)
	return int(status)
}

func (p *generationRequestAdapter) MediaForwardCyber(err error) bool {
	return p.h.bindings.Common.Support.RecordOpenAIForwardErrorCyberWarning(p.c, p.reqLog, p.apiKey, p.selection.Provider, p.requestModel, p.mediaStatus(), err)
}

func (p *generationRequestAdapter) MediaCyber(err error) {
	p.h.bindings.Common.Support.RecordOpenAICyberWarning(p.c, p.reqLog, p.apiKey, p.selection.Provider, p.requestModel, p.mediaStatus(), nil, err.Error())
}

func (p *generationRequestAdapter) MediaReportUnexpected(result *gatewaymedia.GenerationResult, err error) {
	p.ReportGeneration(p.c.Request.Context(), gatewaymedia.GenerationSelection{}, result, false, err)
}

func (p *generationRequestAdapter) MediaCommunicated(err error) bool {
	if p.usesGrok() {
		return gatewayhttp.IsResponseCommitted(p.c)
	}
	return gatewayhttp.OpenAIForwardErrorAlreadyCommunicated(p.c, p.writerBefore, err)
}

func (p *generationRequestAdapter) MediaEnsureFallback(err error) bool {
	return gatewayhttp.DefaultOpenAIErrorOutput().EnsureResponse(p.c, *p.streamStarted, err)
}

func (p *generationRequestAdapter) MediaWarnFailure(wrote bool) bool {
	return gatewayhttp.ShouldLogOpenAIForwardFailureAsWarn(p.c, wrote)
}

// usesGrok checks the current selection after each switch so a previous Grok attempt
// does not restrict later candidates.
func (p *generationRequestAdapter) usesGrok() bool {
	if p.selection == nil || p.selection.Provider == nil {
		return p.grok
	}
	return p.selection.Provider.Record.Platform == capability.PlatformGrok
}
