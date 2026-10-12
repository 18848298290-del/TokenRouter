package mediaentry

import (
	"context"
	"errors"
	"net/http"

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
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// alphaRequestAdapter handles HTTP output, platform recovery, and observation.
// The media package orders provider attempts.
type alphaRequestAdapter struct {
	h                           *Runtime
	c                           *gin.Context
	apiKey                      *apikey.APIKey
	subscription                *billing.UserSubscription
	groupMapping                routing.GroupMappingResult
	requestedModel, sessionHash string
	originalBody                []byte
	userID                      int64
	reqLog                      *zap.Logger
	streamStarted               *bool
	selection                   *gatewaycapture.SelectionResult
	oauth429                    failover.OAuth429State
}

// recordAlphaSearchUsage bills a successful alpha/search as one WebSearchCalls unit
// using the group price and multiplier.
// The upstream supplies no usage field. Mandatory tasks run synchronously when the pool is full.
func (h *Runtime) recordAlphaSearchUsage(
	c *gin.Context,
	apiKey *apikey.APIKey,
	provider *gatewaycapture.ExecutionProvider,
	subscription *billing.UserSubscription,
	groupMapping routing.GroupMappingResult,
	requestedModel string,
	body []byte,
	result *forwardcore.OpenAIResult,
	userID int64,
) {
	userAgent := c.GetHeader("User-Agent")
	clientIP := clientip.GetClientIP(c)
	sessionID := gatewayhttp.ExtractClientSessionID(c)
	requestPayloadHash := billing.HashUsageRequestPayload(body)
	inboundEndpoint := gatewayhttp.GetInboundEndpoint(c)
	upstreamEndpoint := gatewayhttp.GetUpstreamEndpoint(c, provider.Record.Platform)

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
		APIKeyService:      h.bindings.Quota,

		ClientSessionID:    sessionID,
		PricingUsageFields: groupMapping.ToUsageFields(requestedModel, result.UpstreamModel),
	})
	completionRecorder := h.bindings.Common.Recorder
	completionLog := logging.L().With(
		zap.String("component", "handler.openai_gateway.alpha_search"),
		zap.Int64("user_id", userID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
		zap.String("model", requestedModel),
		zap.Int64("provider_id", provider.Record.ID),
	)
	h.bindings.Common.Support.Submission.SubmitMandatory(c, func(ctx context.Context) {
		if err := completionRecorder.Record(ctx, completionInput, true); err != nil {
			completionLog.Error("openai_alpha_search.record_usage_failed", zap.Error(err))
		}
	})
}

func (p *alphaRequestAdapter) SelectAlpha(ctx context.Context, excluded map[int64]struct{}) (gatewaymedia.AlphaSelection, bool, error) {
	selected, _, err := p.h.bindings.Common.Selection.SelectProviderWithSchedulerForCapability(ctx, p.apiKey.GroupID, "", p.sessionHash, p.requestedModel, excluded, egress.OpenAIUpstreamTransportHTTPSSE, providercore.OpenAIEndpointCapabilityAlphaSearch, false, false, capability.PlatformOpenAI)
	p.selection = selected
	if selected == nil || selected.Provider == nil {
		return gatewaymedia.AlphaSelection{}, false, err
	}
	return gatewaymedia.AlphaSelection{Provider: gatewaycapture.ExecutionSnapshot(selected.Provider), RetryLimit: selected.Provider.View().GetPoolModeRetryCount()}, true, err
}

func (p *alphaRequestAdapter) AcquireAlpha(_ context.Context, _ gatewaymedia.AlphaSelection) (func(), bool) {
	return p.h.bindings.Common.Support.AcquireResponsesProviderSlot(p.c, p.apiKey.GroupID, p.sessionHash, p.selection, false, p.streamStarted, p.reqLog)
}

func (p *alphaRequestAdapter) ForwardAlpha(ctx context.Context, _ gatewaymedia.AlphaSelection, body []byte) gatewaymedia.AlphaOutcome {
	size := p.c.Writer.Size()
	provider := p.selection.Provider
	match := p.h.bindings.Common.Forward.MatchOpenAITLSFingerprintRouterForRequest(p.c, provider)
	var result *forwardcore.OpenAIResult
	err := p.h.bindings.Common.Forward.EnforceOpenAIClientPolicyForRequest(ctx, p.c, provider, body, match)
	if err == nil {
		result, err = p.h.bindings.Platform.AlphaSearch(ctx, p.c, provider, body, match)
	}
	outcome := gatewaymedia.AlphaOutcome{Result: alphaResultView(result), Err: err, OutputChanged: p.c.Writer.Size() != size}
	if failure, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		outcome.Failure = failure.RetryFailure()
	}
	return outcome
}

func (p *alphaRequestAdapter) ReportAlpha(_ context.Context, _ gatewaymedia.AlphaSelection, result *gatewaymedia.AlphaResult, success bool, err error) {
	provider := p.selection.Provider
	if success {
		p.h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(provider, openaiattempt.OpenAIProviderScheduleModel(p.c, provider, p.requestedModel, false, legacyAlphaResult(result)), true, nil)
		return
	}
	p.h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(provider, openaiattempt.OpenAIProviderScheduleModel(p.c, provider, p.requestedModel, false, legacyAlphaResult(result)), false, nil, err)
}

func (p *alphaRequestAdapter) CompleteAlpha(_ context.Context, _ gatewaymedia.AlphaSelection, result *gatewaymedia.AlphaResult) {
	p.h.recordAlphaSearchUsage(p.c, p.apiKey, p.selection.Provider, p.subscription, p.groupMapping, p.requestedModel, p.originalBody, legacyAlphaResult(result), p.userID)
}

func (p *alphaRequestAdapter) SwitchAlpha(gatewaymedia.AlphaSelection) {
	p.h.bindings.Platform.ReportSwitch()
}

func (p *alphaRequestAdapter) StopAlpha429(_ gatewaymedia.AlphaSelection, status, count int) bool {
	return p.h.bindings.Platform.Stop429(p.selection.Provider, status, count, &p.oauth429)
}
func (p *alphaRequestAdapter) AlphaClientGone() bool { return gatewayhttp.FailoverClientGone(p.c) }
func (p *alphaRequestAdapter) ObserveAlpha(e gatewaymedia.AlphaEvent) {
	switch e.Kind {
	case "selected":
		gatewayhttp.SetOpsSelectedProvider(p.c, e.Provider.ID, e.Provider.Platform)
	case "routing":
		gatewayhttp.SetOpsLatencyMs(p.c, gatewayhttp.OpsRoutingLatencyMsKey, e.Elapsed.Milliseconds())
	case "response":
		gatewayhttp.SetOpsLatencyMs(p.c, gatewayhttp.OpsResponseLatencyMsKey, e.Elapsed.Milliseconds())
	case "select_canceled":
		p.reqLog.Info("openai_alpha_search.provider_select_aborted_client_disconnected", zap.Error(e.Outcome.Err))
	case "forward_canceled":
		p.reqLog.Info("openai_alpha_search.failover_aborted_client_disconnected", zap.Int64("provider_id", e.Provider.ID), zap.Int("upstream_status", e.Outcome.Failure.StatusCode))
	case "retry":
		p.reqLog.Warn("openai_alpha_search.same_provider_retry", zap.Int64("provider_id", e.Provider.ID), zap.Int("upstream_status", e.Outcome.Failure.StatusCode), zap.Int("retry_limit", e.RetryLimit), zap.Int("retry_count", e.RetryCount), zap.Duration("retry_delay", e.RetryDelay))
	case "switch":
		p.reqLog.Warn("openai_alpha_search.upstream_failover_switching", zap.Int64("provider_id", e.Provider.ID), zap.Int("upstream_status", e.Outcome.Failure.StatusCode), zap.Int("switch_count", e.Switches), zap.Int("max_switches", e.MaxSwitches))
	}
}

func (p *alphaRequestAdapter) renderFailure(f *gatewaymedia.AlphaFailure) {
	if f == nil {
		return
	}
	switch f.Stage {
	case "selection":
		if f.Excluded == 0 {
			if f.Err != nil && p.h.bindings.Common.Support.HandleOpenAISelectionBusinessError(p.c, f.Err, *p.streamStarted) {
				return
			}
			cls := openaiattempt.ClassifyNoProviderErrorFromGin(p.c, p.h.bindings.Common.Diagnoser, p.apiKey, p.requestedModel, p.requestedModel, capability.PlatformOpenAI)
			if !cls.ModelNotFound {
				gatewayhttp.MarkOpsRoutingCapacityLimitedIfNoAvailable(p.c, f.Err)
			}
			gatewayhttp.DefaultOpenAIErrorOutput().WriteError(p.c, cls.Status, cls.ErrType, cls.Message)
			return
		}
		if last, ok := errors.AsType[*forwardcore.UpstreamFailoverError](f.Outcome.Err); ok {
			p.h.bindings.Common.Support.HandleFailoverExhausted(p.c, last, false)
		} else {
			gatewayhttp.DefaultOpenAIErrorOutput().WriteError(p.c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		}
	case "forward":
		if !f.Outcome.OutputChanged {
			gatewayhttp.DefaultOpenAIErrorOutput().WriteError(p.c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		}
		p.reqLog.Warn("openai_alpha_search.forward_failed", zap.Int64("provider_id", p.selection.Provider.Record.ID), zap.Error(f.Err))
	case "exhausted":
		if last, ok := errors.AsType[*forwardcore.UpstreamFailoverError](f.Outcome.Err); ok {
			p.h.bindings.Common.Support.HandleFailoverExhausted(p.c, last, f.Outcome.OutputChanged)
		}
	}
}

func alphaResultView(result *forwardcore.OpenAIResult) *gatewaymedia.AlphaResult {
	if result == nil {
		return nil
	}
	return &gatewaymedia.AlphaResult{RequestID: result.RequestID, Model: result.Model, UpstreamModel: result.UpstreamModel, UpstreamEndpoint: result.UpstreamEndpoint, Headers: http.Header(result.UpstreamHeaders).Clone(), ResponseHeaders: http.Header(result.ResponseHeaders).Clone(), Duration: result.Duration, Calls: result.WebSearchCalls}
}

func legacyAlphaResult(result *gatewaymedia.AlphaResult) *forwardcore.OpenAIResult {
	if result == nil {
		return nil
	}
	return &forwardcore.OpenAIResult{RequestID: result.RequestID, Model: result.Model, UpstreamModel: result.UpstreamModel, UpstreamEndpoint: result.UpstreamEndpoint, UpstreamHeaders: http.Header(result.Headers).Clone(), ResponseHeaders: http.Header(result.ResponseHeaders).Clone(), Duration: result.Duration, WebSearchCalls: result.Calls}
}
