package mediaentry

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/systemone"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// systemone.go adapts SystemOne selection, funding checks, provider feedback,
// and completion to the decision loop.
// SystemOne keeps one selected provider lease while retrying eligible pool
// failures, and each exchange rechecks funding with RPM counting skipped.
// Served answers report valid usage for completion or a missing-usage warning
// before the decision loop releases that provider slot.
//
// The HTTP adapter stores the captured key, subscription and
// requested/group-mapped model across selection, forwarding and completion.
// Schedule feedback prefers an observed upstream model and distinguishes
// served work from local written failures or cancelled unserved attempts.
// Final error output runs only while the request remains active and unwritten,
// allowing delivered answers to remain available even when usage was invalid
// or client delivery failed.

// systemOneRun retains selected provider and funding state for serial decision
// execution.
type systemOneRun struct {
	h     *Runtime                       // shared media runtime
	c     *gin.Context                   // active HTTP request
	input gatewayhttp.AuxiliaryHTTPInput // validated decision request input
	// key retains the middleware Key read by NewSystemOne. Selection reads its
	// group and completion reads its hydrated User; callers keep that request data usable.
	key *apikey.APIKey
	// subscription is the optional funding choice captured from the HTTP context;
	// nil is passed through to completion for the request's balance funding.
	subscription *billing.UserSubscription
	log          *zap.Logger // request diagnostic logger
	stream       *bool       // shared output-start flag
	// selection is replaced by Select and retains the execution provider for
	// Acquire, Forward, Report and Complete. Those operations require successful selection.
	selection *gatewayprovider.SelectionResult
}

// NewSystemOne captures middleware Key, optional subscription and validated
// request input for one decision run. The returned object retains these
// request references; later selection installs the provider used by its
// execution operations.
func (p mediaHTTPAdapter) NewSystemOne(c *gin.Context, input gatewayhttp.AuxiliaryHTTPInput, log *zap.Logger, stream *bool) gatewayhttp.SystemOneHTTPExecution {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subscription, _ := gatewayhttp.SubscriptionFromContext(c)
	return &systemOneRun{h: p.h, c: c, input: input, key: key, subscription: subscription, log: log, stream: stream}
}

// ModerateSystemOne submits extracted decision content using the current key
// and subject. Nil or unblocked moderation returns false; a blocked decision
// writes SystemOne client error output and returns true to stop admission.
func (p mediaHTTPAdapter) ModerateSystemOne(c *gin.Context, log *zap.Logger, model string, body []byte) bool {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subject, _ := authctx.GetAuthSubjectFromContext(c)
	decision := p.h.checkContentModeration(c, log, key, subject, moderation.ContentModerationProtocolOpenAIChat, model, body)
	if decision == nil || !decision.Blocked {
		return false
	}
	gatewayhttp.WriteSystemOneError(c, gatewayhttp.ContentModerationStatus(decision), gatewayhttp.ContentModerationErrorCode(decision), "", "", decision.Message)
	return true
}

// Select captures a Jev execution candidate under the current Key group and
// exclusions. An absent selection/provider yields a zero snapshot with its
// error; a valid candidate updates request Ops identity before returning.
func (p *systemOneRun) Select(ctx context.Context, excluded map[int64]struct{}) (provider.ProviderSnapshot, error) {
	selection, _, err := p.h.bindings.Common.Selection.SelectProviderWithSchedulerForCapability(ctx, p.key.GroupID, "", "", p.input.Model, excluded, egress.OpenAIUpstreamTransportHTTPSSE, "", false, false, provider.PlatformJev)
	p.selection = selection
	if selection == nil || selection.Provider == nil {
		return provider.ProviderSnapshot{}, err
	}
	value := gatewayprovider.ExecutionSnapshot(selection.Provider)
	gatewayhttp.SetOpsSelectedProvider(p.c, value)
	return value, err
}

// Acquire reuses or waits for the captured selection's provider slot through
// shared HTTP support. Its context argument is unused; decision execution
// calls this after Select and releases the returned callback after completion
// capture.
func (p *systemOneRun) Acquire(_ context.Context) (func(), bool) {
	return p.h.bindings.Common.Support.AcquireResponsesProviderSlot(p.c, p.key.GroupID, "", p.selection, false, p.stream, p.log)
}

// Forward rechecks captured request funding with RPM accumulation skipped
// before calling the selected provider. Group mapping rewrites this exchange's
// model field; a typed failover error supplies retry metadata and the
// provider's pool retry budget.
func (p *systemOneRun) Forward(ctx context.Context) systemone.Outcome {
	selected := telemetry.RequestProvider(p.c.Request.Context())
	ctx = telemetry.WithRequestProvider(ctx, selected.ProviderID, selected.Header)
	// Funds can change during a slot wait or earlier pool exchange; recheck
	// each attempt with RPM skipped so retries share the initial admission count.
	if failure := (mediaHTTPAdapter{p.h}).checkFunding(p.c, true); failure != nil {
		gatewayhttp.WriteSystemOneError(p.c, failure.Status, failure.Code, "", "", failure.Message)
		return systemone.Outcome{Err: failure.Err}
	}
	body := p.input.Body
	if p.input.Mapping.Mapped {
		body = p.h.bindings.Common.Forward.ReplaceModelInBody(body, p.input.Mapping.MappedModel)
	}
	result, err := p.h.bindings.Platform.SystemOne(ctx, p.c, p.selection.Provider, body)
	outcome := systemone.Outcome{Result: result, Err: err}
	if failure, retry := errors.AsType[*forward.UpstreamFailoverError](err); retry {
		outcome.Failure = failure.RetryFailure()
		outcome.RetryLimit = p.selection.Provider.View().GetPoolModeRetryCount()
	}
	return outcome
}

// Report submits scheduling feedback for an upstream decision outcome, using
// served state independently of client write success. Cancelled unserved work
// is skipped, as is a written unserved local error with no retryable failure.
// A served answer can still report after client cancellation.
//
// The selected scheduling model prefers Result.UpstreamModel. When absent,
// group-mapped identity precedes the original input model, then provider
// mapping resolves that fallback. This keeps feedback attached to the
// execution model while local admission errors do not become provider
// performance evidence.
func (p *systemOneRun) Report(ctx context.Context, outcome systemone.Outcome) {
	if ctx.Err() != nil && !outcome.Result.Served {
		return
	}
	// Written unserved output with no retryable failure represents a local stop.
	// Skip provider feedback for that case; served answers continue through this guard.
	if p.c.Writer.Written() && !outcome.Result.Served && outcome.Failure == nil {
		return
	}
	model := outcome.Result.UpstreamModel
	if model == "" {
		model = p.input.Mapping.MappedModel
		if model == "" {
			model = p.input.Model
		}
		model = gatewayprovider.ExecutionModelPolicy(p.selection.Provider).Mapped(model)
	}
	p.h.bindings.Common.Selection.ReportOpenAIProviderScheduleResult(p.selection.Provider, model, outcome.Result.Served, nil, outcome.Err)
}

// Complete captures served decision usage, caller funding and model identities
// before queue submission. The loop invokes it for valid observed usage while
// its provider lease is still held, and the queued recorder uses the captured
// values after release.
func (p *systemOneRun) Complete(ctx context.Context, result upstream.AttemptResult) {
	value := &forward.OpenAIResult{
		RequestAttempt:        result.RequestAttempt,
		RequestID:             result.RequestID,
		Model:                 p.input.Mapping.MappedModel,
		UpstreamModel:         result.UpstreamModel,
		UpstreamResponseModel: result.UpstreamResponseModel,
		UpstreamHeaders:       result.UpstreamHeaders,
		Usage: openai.ForwardUsage{
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
		},
		Duration: result.Duration,
	}
	if value.Model == "" {
		value.Model = p.input.Model
	}
	// Capture request and funding values before submitting work, so completion
	// can finish after the selected provider lease and HTTP request are released.
	input := gatewayprovider.CaptureOpenAI(ctx, &gatewayprovider.OpenAICapture{RequestPayloadHash: billing.HashUsageRequestPayload(p.input.Body), Result: value, APIKey: p.key, User: p.key.User, Provider: gatewayprovider.ExecutionCompletionRecord(p.selection.Provider), Subscription: p.subscription, InboundEndpoint: gatewayhttp.EndpointSystemOne, UpstreamEndpoint: gatewayhttp.EndpointSystemOne, UserAgent: p.c.GetHeader("User-Agent"), IPAddress: clientip.GetClientIP(p.c), APIKeyService: p.h.bindings.Quota, PricingUsageFields: p.input.Mapping.ToUsageFields(p.input.Model, result.UpstreamModel)})
	recorder, log := p.h.bindings.Common.Recorder, p.log
	p.h.submitOpenAIUsageRecordTask(p.c, value, func(completionContext context.Context) {
		if err := recorder.Record(completionContext, input, true); err != nil {
			log.Error("systemone.record_usage_failed", zap.Error(err))
		}
	})
}

// MissingUsage records an invalid-or-missing usage warning for a served
// answer. It appends correlated Ops/request-attempt evidence while leaving
// that successful answer available; the decision loop skips ordinary
// completion billing for this outcome.
func (p *systemOneRun) MissingUsage(_ context.Context, result upstream.AttemptResult) {
	value := p.selection.Provider.Record
	p.log.Warn("systemone.usage_invalid", zap.Int64("provider_id", value.ID), zap.String("upstream_request_id", result.RequestID))
	gatewayhttp.AppendOpsUpstreamError(p.c, ops.OpsUpstreamErrorEvent{
		Platform:           provider.PlatformJev,
		ProviderID:         value.ID,
		ProviderName:       value.Name,
		UpstreamStatusCode: http.StatusOK,
		UpstreamRequestID:  result.RequestID,
		Kind:               "usage_invalid",
		Message:            "SystemOne usage is missing or invalid; billing skipped",
	}, result.RequestAttempt)
}

// Switch records a permitted provider replacement for the current decision
// selection.
func (p *systemOneRun) Switch() {
	p.h.bindings.Common.Selection.RecordOpenAIProviderSwitchForSelection(p.selection)
}

// End presents a final error only while the request is active and response
// unwritten. Typed failover uses Jev exhausted-error rules; selection business
// errors have precedence, and missing selection receives a local
// provider-unavailable response.
func (p *systemOneRun) End(err error) {
	if err == nil || p.c.Writer.Written() || p.c.Request.Context().Err() != nil {
		return
	}
	if failure, ok := errors.AsType[*forward.UpstreamFailoverError](err); ok {
		gatewayhttp.WriteSystemOneFailoverExhausted(p.c, failure)
		return
	}
	if p.h.bindings.Common.Support.HandleOpenAISelectionBusinessError(p.c, err, false) {
		return
	}
	status := http.StatusBadGateway
	message := "SystemOne upstream request failed"
	if p.selection == nil || p.selection.Provider == nil {
		status = http.StatusServiceUnavailable
		message = "No available SystemOne providers"
		gatewayhttp.MarkOpsRoutingCapacityLimited(p.c)
	}
	gatewayhttp.WriteSystemOneError(p.c, status, "upstream_error", "", "", message)
}
