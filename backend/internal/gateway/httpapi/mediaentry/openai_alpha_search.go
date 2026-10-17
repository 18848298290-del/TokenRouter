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
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// openai_alpha_search.go adapts AlphaSearch provider attempts and captures
// successful search usage for mandatory completion.
// AlphaSearch keeps original request bytes beside group-mapped execution
// bytes so successful completion can fingerprint the admitted payload while
// platform requests use the selected route. A credential-bearing current
// target is retained inside the request adapter; the media loop receives a
// separate provider snapshot and recovery observations.
//
// Writer change determines whether another attempt could append incompatible
// output. Scheduling feedback follows provider release, and successful nonnil
// results contribute the WebSearchCalls unit supplied by ForwardAlphaSearch.
// Completion captures the result, authentication, subscription and client
// metadata before mandatory submission. The worker records that snapshot
// independently of HTTP output while same-provider and provider-switch retry counts remain
// with the media loop.

// alphaRequestAdapter retains request identity and the current provider
// across serial AlphaSearch attempts.
type alphaRequestAdapter struct {
	h *Runtime // shared application media bindings
	// c, apiKey and subscription retain admitted HTTP authentication and funding.
	// Completion captures their recording values synchronously before submission,
	// leaving request-local state with the HTTP owner throughout serial attempts.
	c      *gin.Context   // current HTTP request context
	apiKey *apikey.APIKey // current authenticated Key view
	// subscription is optional resolved funding for this request. It is passed
	// through completion capture rather than looked up after the platform response.
	subscription *billing.UserSubscription
	groupMapping routing.GroupMappingResult // retained group model mapping
	// requestedModel keeps the parsed input name while sessionHash retains the
	// selection identity supplied by HTTP entry; provider mapping happens later.
	// requestedModel: parsed request model. sessionHash: retained selection hash.
	requestedModel, sessionHash string
	// originalBody shares the HTTP input bytes until synchronous completion capture.
	// Callers keep them stable while mapped forwarding bytes are used independently.
	originalBody []byte
	userID       int64       // authenticated concurrency subject identifier
	reqLog       *zap.Logger // request-scoped AlphaSearch diagnostic logger
	// streamStarted shares admission output state with HTTP entry. Failure
	// rendering dereferences it, so the request factory supplies a usable pointer.
	streamStarted *bool
	// selection is replaced at each selection. Later callbacks use this target
	// with execution credentials instead of their informational provider snapshot.
	selection *gatewaycapture.SelectionResult
	oauth429  failover.OAuth429State // request-local OAuth recovery state
}

// recordAlphaSearchUsage captures a nonnil successful search result for
// mandatory recording. ForwardAlphaSearch supplies one WebSearchCalls unit;
// completion prices that observation using the configured group price and
// multiplier. Callers provide current Key, selected provider and resolved
// subscription.
//
// Original request bytes supply the payload fingerprint, while the parsed
// request model and observed upstream model supply separate group mapping
// fields. Session, endpoint and network metadata are read before
// CaptureOpenAI snapshots funding and provider data. The closure retains the
// snapshot, recorder and separate logger, allowing worker recording to
// proceed after the HTTP request ends without reading Gin state.
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
	// Capture completes before enqueueing. Retained snapshot and logger values
	// let the worker record this admitted search without reading live HTTP context.
	h.bindings.Common.Support.Submission.SubmitMandatory(c, func(ctx context.Context) {
		if err := completionRecorder.Record(ctx, completionInput, true); err != nil {
			completionLog.Error("openai_alpha_search.record_usage_failed", zap.Error(err))
		}
	})
}

// SelectAlpha selects and retains an OpenAI AlphaSearch target under the
// current Key group and session hash. Missing selection or provider returns
// false with any error; successful results include that target's configured
// same-provider retry allowance.
func (p *alphaRequestAdapter) SelectAlpha(ctx context.Context, excluded map[int64]struct{}) (gatewaymedia.AlphaSelection, bool, error) {
	selected, _, err := p.h.bindings.Common.Selection.SelectProviderWithSchedulerForCapability(ctx, p.apiKey.GroupID, "", p.sessionHash, p.requestedModel, excluded, egress.OpenAIUpstreamTransportHTTPSSE, providercore.OpenAIEndpointCapabilityAlphaSearch, false, false, capability.PlatformOpenAI)
	p.selection = selected
	if selected == nil || selected.Provider == nil {
		return gatewaymedia.AlphaSelection{}, false, err
	}
	return gatewaymedia.AlphaSelection{Provider: gatewaycapture.ExecutionSnapshot(selected.Provider), RetryLimit: selected.Provider.View().GetPoolModeRetryCount()}, true, err
}

// AcquireAlpha obtains capacity for the retained target after successful
// selection. The snapshot parameter is informational; the media loop releases
// returned cleanup when forwarding ends before scheduling feedback or
// completion.
func (p *alphaRequestAdapter) AcquireAlpha(_ context.Context, _ gatewaymedia.AlphaSelection) (func(), bool) {
	return p.h.bindings.Common.Support.AcquireResponsesProviderSlot(p.c, p.apiKey.GroupID, p.sessionHash, p.selection, false, p.streamStarted, p.reqLog)
}

// ForwardAlpha executes the retained target after applying current client
// policy and TLS routing. Callers select and acquire first, supplying the
// already group-mapped body. The platform result is converted into media
// search fields with independent header maps.
//
// Writer size is sampled before and after policy and platform execution, so
// even policy-produced output closes the observed retry window. Classified
// upstream errors contribute retry failure evidence while retaining their
// direct error. The media loop combines those observations with its own retry
// counters and current client availability.
func (p *alphaRequestAdapter) ForwardAlpha(ctx context.Context, _ gatewaymedia.AlphaSelection, body []byte) gatewaymedia.AlphaOutcome {
	selected := telemetry.RequestProvider(p.c.Request.Context())
	ctx = telemetry.WithRequestProvider(ctx, selected.ProviderID, selected.Header)
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

// ReportAlpha records selected-target scheduling health using model
// observations from the converted search result. Success passes no error;
// failure retains the supplied error, and the media loop invokes feedback
// after releasing provider capacity.
func (p *alphaRequestAdapter) ReportAlpha(_ context.Context, _ gatewaymedia.AlphaSelection, result *gatewaymedia.AlphaResult, success bool, err error) {
	provider := p.selection.Provider
	if success {
		p.h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(provider, openaiattempt.OpenAIProviderScheduleModel(p.c, provider, p.requestedModel, false, legacyAlphaResult(result)), true, nil)
		return
	}
	p.h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(provider, openaiattempt.OpenAIProviderScheduleModel(p.c, provider, p.requestedModel, false, legacyAlphaResult(result)), false, nil, err)
}

// CompleteAlpha submits a nonnil successful search result with retained
// original body and admitted funding. RunAlphaSearch guards result presence
// before this callback; recording synchronously captures metadata before
// mandatory asynchronous submission.
func (p *alphaRequestAdapter) CompleteAlpha(_ context.Context, _ gatewaymedia.AlphaSelection, result *gatewaymedia.AlphaResult) {
	p.h.recordAlphaSearchUsage(p.c, p.apiKey, p.selection.Provider, p.subscription, p.groupMapping, p.requestedModel, p.originalBody, legacyAlphaResult(result), p.userID)
}

// SwitchAlpha records a provider transition through the shared platform
// observation callback.
func (p *alphaRequestAdapter) SwitchAlpha(gatewaymedia.AlphaSelection) {
	p.h.bindings.Platform.ReportSwitch()
}

// StopAlpha429 applies current-target OAuth recovery policy after transition
// count advances. It retains mutable request-local oauth429 state across
// attempts, so the loop invokes this callback serially before selecting
// another provider.
func (p *alphaRequestAdapter) StopAlpha429(_ gatewaymedia.AlphaSelection, status, count int) bool {
	return p.h.bindings.Platform.Stop429(p.selection.Provider, status, count, &p.oauth429)
}

// AlphaClientGone reports shared HTTP failure handling's observed client
// departure state.
func (p *alphaRequestAdapter) AlphaClientGone() bool { return gatewayhttp.FailoverClientGone(p.c) }

// ObserveAlpha records provider identity, phase timings and classified retry
// stages for HTTP diagnostics. Retry, forward-canceled and switch events
// require their failure observations; the media loop supplies those values
// for these event kinds.
func (p *alphaRequestAdapter) ObserveAlpha(e gatewaymedia.AlphaEvent) {
	switch e.Kind {
	case "selected":
		gatewayhttp.SetOpsSelectedProvider(p.c, e.Provider)
	case "routing":
		// Ops phase fields use whole milliseconds for the retained duration observations.
		gatewayhttp.SetOpsLatencyMs(p.c, gatewayhttp.OpsRoutingLatencyMsKey, e.Elapsed.Milliseconds())
	case "response":
		// Keep response duration in the same millisecond unit as other Ops phase fields.
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

// renderFailure presents terminal AlphaSearch selection, forwarding and
// exhaustion outcomes. Nil leaves successful or adapter-handled output
// intact. Initial selection checks shared business-error presentation before
// local model diagnosis, while later selection can present its retained
// classified failure.
//
// Generic forwarding errors are attempted only before writer change, keeping
// an existing response document intact. Exhaustion delegates classified error
// handling with its output-change flag. Capacity marking remains separate
// from model denial, and logging uses the retained selected target. A usable
// shared stream-state pointer and request logger are required by these
// presentation paths.
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
		// A changed writer can already contain search output. Attempt the generic
		// failure before output only so another JSON object cannot append to that response.
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

// alphaResultView copies search identities, selected endpoint, duration and
// call count into media observations. Nil input returns nil; upstream and
// response header maps with their value slices are cloned for independent
// retention.
func alphaResultView(result *forwardcore.OpenAIResult) *gatewaymedia.AlphaResult {
	if result == nil {
		return nil
	}
	return &gatewaymedia.AlphaResult{RequestID: result.RequestID, Model: result.Model, UpstreamModel: result.UpstreamModel, UpstreamEndpoint: result.UpstreamEndpoint, Headers: http.Header(result.UpstreamHeaders).Clone(), ResponseHeaders: http.Header(result.ResponseHeaders).Clone(), Duration: result.Duration, Calls: result.WebSearchCalls}
}

// legacyAlphaResult copies media search observations into forwarding fields
// used by scheduling and completion. Nil input returns nil; both header maps
// are cloned while identities, duration and WebSearchCalls remain copied
// values.
func legacyAlphaResult(result *gatewaymedia.AlphaResult) *forwardcore.OpenAIResult {
	if result == nil {
		return nil
	}
	return &forwardcore.OpenAIResult{RequestID: result.RequestID, Model: result.Model, UpstreamModel: result.UpstreamModel, UpstreamEndpoint: result.UpstreamEndpoint, UpstreamHeaders: http.Header(result.Headers).Clone(), ResponseHeaders: http.Header(result.ResponseHeaders).Clone(), Duration: result.Duration, WebSearchCalls: result.Calls}
}
