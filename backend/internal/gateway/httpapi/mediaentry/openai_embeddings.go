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

// openai_embeddings.go adapts Embeddings provider attempts and captures
// successful results with their model and funding identities.
// Embedding attempts rebuild group-mapped forwarding bytes from the original
// request for each selected provider. The adapter retains the
// credential-bearing target while the media loop sees a provider snapshot and
// an observed output-change flag; that distinction lets recovery stop after
// actual response bytes have been written.
//
// Scheduling feedback and successful completion run after provider capacity
// is released by the media loop. Result conversion clones upstream headers
// and retains separate parsed, billing and upstream model values. Completion
// reads client endpoint and network metadata synchronously, then captures
// funding and provider data before worker submission. A failed selection uses
// local model diagnosis, while forwarding failure appends a generic error
// only before output changes.

// embeddingRequestAdapter retains admitted request facts and current target
// for serial Embeddings attempts.
type embeddingRequestAdapter struct {
	userID int64    // authenticated concurrency subject identifier
	h      *Runtime // shared application media bindings
	// c, apiKey and subscription retain request-owned authentication and funding.
	// Completion reads them before capture; the worker receives their snapshot
	// rather than retaining Gin or reselecting funding after execution.
	c      *gin.Context   // current HTTP request context
	apiKey *apikey.APIKey // current authenticated Key view
	// subscription is optional resolved funding for this admitted request. A nil
	// value is passed through capture for the recorder's configured funding mode.
	subscription *billing.UserSubscription
	reqModel     string                     // parsed request model name
	groupMapping routing.GroupMappingResult // retained group model mapping
	reqLog       *zap.Logger                // request-scoped Embeddings diagnostic logger
	// streamStarted shares user-wait output state with HTTP entry. Failure
	// rendering dereferences it, so the factory supplies a usable pointer.
	streamStarted *bool
	// selection is replaced before each admitted attempt. Later callbacks use
	// its credential-bearing target instead of their provider snapshot argument.
	selection *gatewaycapture.SelectionResult
}

// SelectEmbedding selects and retains a target supporting Embeddings under
// the current Key group. Missing selection or provider returns false with any
// selection error; later acquisition, forwarding and completion use the
// retained target.
func (p *embeddingRequestAdapter) SelectEmbedding(ctx context.Context, excluded map[int64]struct{}) (providercore.ProviderSnapshot, bool, error) {
	selection, _, err := p.h.bindings.Common.Selection.SelectProviderWithSchedulerForCapability(ctx, p.apiKey.GroupID, "", "", p.reqModel, excluded, egress.OpenAIUpstreamTransportHTTPSSE, providercore.OpenAIEndpointCapabilityEmbeddings, false, false)
	p.selection = selection
	if selection == nil || selection.Provider == nil {
		return providercore.ProviderSnapshot{}, false, err
	}
	return gatewaycapture.ExecutionSnapshot(selection.Provider), true, err
}

// AcquireEmbedding obtains capacity for the retained target after selection
// succeeds. The snapshot parameter is informational, and the media loop
// releases returned cleanup as forwarding ends before feedback or completion.
func (p *embeddingRequestAdapter) AcquireEmbedding(_ context.Context, _ providercore.ProviderSnapshot) (func(), bool) {
	return p.h.bindings.Common.Support.AcquireResponsesProviderSlot(p.c, p.apiKey.GroupID, "", p.selection, false, p.streamStarted, p.reqLog)
}

// ForwardEmbedding executes the retained target using group-mapped bytes
// derived from the supplied original request. The input body stays available
// for later attempts. Current writer size is sampled around the platform call
// and returned as output-change evidence.
//
// Result conversion preserves media model and usage observations while
// cloning upstream headers. A classified upstream failure sets the failover
// flag and status for the loop; ordinary errors keep their direct value.
// Callers perform selection and acquisition first and keep the shared request
// body stable through forwarding.
func (p *embeddingRequestAdapter) ForwardEmbedding(ctx context.Context, _ providercore.ProviderSnapshot, body []byte) gatewaymedia.EmbeddingOutcome {
	selected := telemetry.RequestProvider(p.c.Request.Context())
	ctx = telemetry.WithRequestProvider(ctx, selected.ProviderID, selected.Header)
	forwardBody := body
	// Group substitution precedes the platform's provider mapping. Derive it from
	// the loop's source body on each attempt so these two mapping stages stay separate.
	if p.groupMapping.Mapped {
		forwardBody = p.h.bindings.Common.Forward.ReplaceModelInBody(body, p.groupMapping.MappedModel)
	}
	size := p.c.Writer.Size()
	result, err := p.h.bindings.Platform.Embeddings(ctx, p.c, p.selection.Provider, forwardBody, "")
	outcome := gatewaymedia.EmbeddingOutcome{Result: embeddingResultView(result), Err: err, OutputChanged: p.c.Writer.Size() != size}
	if failure, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		outcome.Failover = true
		outcome.StatusCode = failure.StatusCode
	}
	return outcome
}

// ReportEmbedding records scheduling health for the retained current target
// using model observations from its converted result. Success passes no
// error; failure retains the supplied error for the scheduler after provider
// capacity has been released.
func (p *embeddingRequestAdapter) ReportEmbedding(_ context.Context, _ providercore.ProviderSnapshot, result *gatewaymedia.EmbeddingResult, success bool, err error) {
	provider := p.selection.Provider
	if success {
		p.h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(provider, openaiattempt.OpenAIProviderScheduleModel(p.c, provider, p.reqModel, false, legacyEmbeddingResult(result)), true, nil)
		return
	}
	p.h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(provider, openaiattempt.OpenAIProviderScheduleModel(p.c, provider, p.reqModel, false, legacyEmbeddingResult(result)), false, nil, err)
}

// SwitchEmbedding records provider-transition feedback for the retained
// current selection.
func (p *embeddingRequestAdapter) SwitchEmbedding(_ providercore.ProviderSnapshot) {
	p.h.bindings.Common.Selection.RecordOpenAIProviderSwitchForSelection(p.selection)
}

// ClientGone reports shared HTTP failure handling's observed client departure
// state.
func (p *embeddingRequestAdapter) ClientGone() bool { return gatewayhttp.FailoverClientGone(p.c) }

// ObserveEmbedding records selected provider, phase timings and terminal
// Embeddings stages in the current request. The media loop supplies the event
// fields relevant to each kind, and these observations are handled
// synchronously before another callback can replace selection state.
//
// Response processing time subtracts separately observed upstream latency
// when it is positive and below the measured total. Otherwise the full
// measured interval remains, including missing or incompatible subtraction
// values. Routing and response durations are converted to whole milliseconds
// for their separate Ops fields, while failure logs retain stage and provider
// identity.
func (p *embeddingRequestAdapter) ObserveEmbedding(event gatewaymedia.EmbeddingEvent) {
	switch event.Kind {
	case "selected":
		gatewayhttp.SetOpsSelectedProvider(p.c, event.Provider)
	case "routing":
		// Ops latency fields store the loop's elapsed duration in whole milliseconds.
		gatewayhttp.SetOpsLatencyMs(p.c, gatewayhttp.OpsRoutingLatencyMsKey, event.Elapsed.Milliseconds())
	case "response":
		elapsed := event.Elapsed.Milliseconds()
		upstream, _ := openaiattempt.GetContextInt64(p.c, gatewayhttp.OpsUpstreamLatencyMsKey)
		// Keep response handling separate from upstream time when both observations
		// permit subtraction; otherwise retain the measured total as reported by the loop.
		if upstream > 0 && elapsed > upstream {
			elapsed -= upstream
		}
		gatewayhttp.SetOpsLatencyMs(p.c, gatewayhttp.OpsResponseLatencyMsKey, elapsed)
	case "select_canceled":
		p.reqLog.Info("openai_embeddings.provider_select_aborted_client_disconnected", zap.Error(event.Outcome.Err))
	case "select_failed":
		p.reqLog.Warn("openai_embeddings.provider_select_failed", zap.Error(event.Outcome.Err), zap.Int("excluded_provider_count", event.Excluded))
	case "forward_canceled":
		p.reqLog.Info("openai_embeddings.failover_aborted_client_disconnected", zap.Int64("provider_id", event.Provider.ID), zap.Int("upstream_status", event.Outcome.StatusCode))
	case "switch":
		p.reqLog.Warn("openai_embeddings.upstream_failover_switching", zap.Int64("provider_id", event.Provider.ID), zap.Int("upstream_status", event.Outcome.StatusCode), zap.Int("switch_count", event.Switches), zap.Int("max_switches", event.MaxSwitches))
	case "completed":
		p.reqLog.Debug("openai_embeddings.request_completed", zap.Int64("provider_id", event.Provider.ID), zap.Int("switch_count", event.Switches))
	}
}

// renderFailure presents the media loop's terminal Embeddings stage, leaving
// nil outcomes intact. Initial selection checks business error handling
// before model diagnosis; later selection retains the preceding classified
// upstream failure when available.
//
// Capacity marking remains separate from an unavailable-model result.
// Exhausted classified failures pass their output-change state to terminal
// handling. An ordinary forwarding failure appends a generic error only
// before writer output changes, then logs the selected provider and error.
// The factory supplies usable logger and stream-state pointers for those
// failure paths.
func (p *embeddingRequestAdapter) renderFailure(f *gatewaymedia.EmbeddingFailure) {
	if f == nil {
		return
	}
	switch f.Stage {
	case "selection":
		if f.Excluded == 0 {
			if p.h.bindings.Common.Support.HandleOpenAISelectionBusinessError(p.c, f.Err, *p.streamStarted) {
				return
			}
			cls := openaiattempt.ClassifyNoProviderErrorFromGin(p.c, p.h.bindings.Common.Diagnoser, p.apiKey, p.reqModel, p.reqModel, capability.PlatformOpenAI)
			if !cls.ModelNotFound {
				gatewayhttp.MarkOpsRoutingCapacityLimitedIfNoAvailable(p.c, f.Err)
			}
			gatewayhttp.DefaultOpenAIErrorOutput().WriteError(p.c, cls.Status, cls.ErrType, cls.Message)
			return
		}
		if failure, ok := errors.AsType[*forwardcore.UpstreamFailoverError](f.Outcome.Err); ok {
			p.h.bindings.Common.Support.HandleFailoverExhausted(p.c, failure, false)
		} else {
			gatewayhttp.DefaultOpenAIErrorOutput().WriteError(p.c, http.StatusBadGateway, "api_error", "Upstream request failed")
		}
	case "empty_selection":
		cls := openaiattempt.ClassifyNoProviderErrorFromGin(p.c, p.h.bindings.Common.Diagnoser, p.apiKey, p.reqModel, p.reqModel, capability.PlatformOpenAI)
		if !cls.ModelNotFound {
			gatewayhttp.MarkOpsRoutingCapacityLimited(p.c)
		}
		gatewayhttp.DefaultOpenAIErrorOutput().WriteError(p.c, cls.Status, cls.ErrType, cls.Message)
	case "exhausted":
		if failure, ok := errors.AsType[*forwardcore.UpstreamFailoverError](f.Outcome.Err); ok {
			p.h.bindings.Common.Support.HandleFailoverExhausted(p.c, failure, f.Outcome.OutputChanged)
		}
	case "forward":
		// Existing Embeddings bytes already define the response document. A generic
		// error is attempted only before forwarding changed that writer observation.
		if !f.Outcome.OutputChanged {
			gatewayhttp.DefaultOpenAIErrorOutput().WriteError(p.c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		}
		p.reqLog.Warn("openai_embeddings.forward_failed", zap.Int64("provider_id", p.selection.Provider.Record.ID), zap.Error(f.Err))
	}
}

// embeddingResultView copies Embeddings model identities, scalar usage and
// duration into media observations. Nil input returns nil; upstream headers
// and their value slices are cloned so the result can be retained
// independently.
func embeddingResultView(result *forwardcore.OpenAIResult) *gatewaymedia.EmbeddingResult {
	if result == nil {
		return nil
	}
	return &gatewaymedia.EmbeddingResult{RequestID: result.RequestID, Model: result.Model, BillingModel: result.BillingModel, UpstreamModel: result.UpstreamModel, Headers: http.Header(result.UpstreamHeaders).Clone(), Usage: result.Usage, Duration: result.Duration}
}

// legacyEmbeddingResult copies media Embeddings observations into forwarding
// fields used for scheduling and completion. Nil input returns nil; upstream
// headers are cloned while model, usage and timing values remain scalar
// copies.
func legacyEmbeddingResult(result *gatewaymedia.EmbeddingResult) *forwardcore.OpenAIResult {
	if result == nil {
		return nil
	}
	return &forwardcore.OpenAIResult{RequestID: result.RequestID, Model: result.Model, BillingModel: result.BillingModel, UpstreamModel: result.UpstreamModel, UpstreamHeaders: http.Header(result.Headers).Clone(), Usage: result.Usage, Duration: result.Duration}
}

// CompleteEmbedding captures a nonnil Embeddings result after successful
// forwarding and scheduling feedback. It uses the retained Key, selected
// target and subscription, plus parsed request model and group mapping. The
// result is required because pricing fields read its upstream model.
//
// Endpoint, session, user-agent and IP data are read before CaptureOpenAI
// creates the completion snapshot. The queued closure retains that snapshot
// with recorder and logger, so recording after request exit does not read Gin
// or mutable selection state. Embeddings contributes no image count, and
// submission follows the Runtime's ordinary completion policy.
func (p *embeddingRequestAdapter) CompleteEmbedding(_ context.Context, _ providercore.ProviderSnapshot, value *gatewaymedia.EmbeddingResult) {
	c, h, apiKey, provider := p.c, p.h, p.apiKey, p.selection.Provider
	result := legacyEmbeddingResult(value)
	userAgent := c.GetHeader("User-Agent")
	clientIP := clientip.GetClientIP(c)
	inboundEndpoint := gatewayhttp.GetInboundEndpoint(c)
	upstreamEndpoint := gatewayhttp.GetUpstreamEndpoint(c, provider.Record.Platform)

	clientSessionID := gatewayhttp.ExtractClientSessionID(c)
	// Resolve pricing identity before enqueueing so the worker keeps this request
	// model and its observed upstream model beside the captured funding snapshot.
	pricingFields := p.groupMapping.ToUsageFields(p.reqModel, result.UpstreamModel)
	subscription, reqModel, userID := p.subscription, p.reqModel, p.userID
	completionInput := gatewaycapture.CaptureOpenAI(c.Request.Context(), &gatewaycapture.OpenAICapture{Result: result, APIKey: apiKey, User: apiKey.User, Provider: gatewaycapture.ExecutionCompletionRecord(provider), Subscription: subscription, InboundEndpoint: inboundEndpoint, UpstreamEndpoint: upstreamEndpoint, UserAgent: userAgent, IPAddress: clientIP, APIKeyService: h.bindings.Quota, ClientSessionID: clientSessionID, PricingUsageFields: pricingFields})
	completionRecorder := h.bindings.Common.Recorder
	completionLog := logging.L().With(zap.String("component", "handler.openai_gateway.embeddings"), zap.Int64("user_id", userID), zap.Int64("api_key_id", apiKey.ID), zap.Any("group_id", apiKey.GroupID), zap.String("model", reqModel), zap.Int64("provider_id", provider.Record.ID))
	// Capture is complete before submission. The worker closes over recording
	// values and its logger, keeping HTTP context reads on the request goroutine.
	h.submitOpenAIUsageRecordTask(c, result, func(ctx context.Context) {
		if err := completionRecorder.Record(ctx, completionInput, true); err != nil {
			completionLog.Error("openai_embeddings.record_usage_failed", zap.Error(err))
		}
	})
}
