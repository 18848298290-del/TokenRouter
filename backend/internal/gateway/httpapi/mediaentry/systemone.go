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
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// systemOneRun holds provider selection and completion inputs for one request.
type systemOneRun struct {
	h            *Runtime
	c            *gin.Context
	input        gatewayhttp.AuxiliaryHTTPInput
	key          *apikey.APIKey
	subscription *billing.UserSubscription
	log          *zap.Logger
	stream       *bool
	selection    *gatewayprovider.SelectionResult
}

// NewSystemOne captures the request identity, model, and group inputs.
func (p mediaHTTPAdapter) NewSystemOne(c *gin.Context, input gatewayhttp.AuxiliaryHTTPInput, log *zap.Logger, stream *bool) gatewayhttp.SystemOneHTTPExecution {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subscription, _ := gatewayhttp.SubscriptionFromContext(c)
	return &systemOneRun{h: p.h, c: c, input: input, key: key, subscription: subscription, log: log, stream: stream}
}

// ModerateSystemOne submits structured decision content for text moderation.
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

// Select selects Jev candidates for the current group and routing context.
func (p *systemOneRun) Select(ctx context.Context, excluded map[int64]struct{}) (provider.ProviderSnapshot, error) {
	selection, _, err := p.h.bindings.Common.Selection.SelectProviderWithSchedulerForCapability(ctx, p.key.GroupID, "", "", p.input.Model, excluded, egress.OpenAIUpstreamTransportHTTPSSE, "", false, false, provider.PlatformJev)
	p.selection = selection
	if selection == nil || selection.Provider == nil {
		return provider.ProviderSnapshot{}, err
	}
	value := gatewayprovider.ExecutionSnapshot(selection.Provider)
	gatewayhttp.SetOpsSelectedProvider(p.c, value.ID, value.Platform)
	return value, err
}

// Acquire reuses a scheduler-acquired slot or waits according to the selection plan.
func (p *systemOneRun) Acquire(_ context.Context) (func(), bool) {
	return p.h.bindings.Common.Support.AcquireResponsesProviderSlot(p.c, p.key.GroupID, "", p.selection, false, p.stream, p.log)
}

// Forward rechecks funding and calls the selected provider.
func (p *systemOneRun) Forward(ctx context.Context) systemone.Outcome {
	// Funding can change while waiting for a provider slot.
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

// Report reports upstream completion and the final model to the scheduler.
func (p *systemOneRun) Report(ctx context.Context, outcome systemone.Outcome) {
	if ctx.Err() != nil && !outcome.Result.Served {
		return
	}
	// A client write failure still counts as upstream completion when an answer was served.
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

// Complete captures funding ownership and usage for the shared completion queue.
func (p *systemOneRun) Complete(ctx context.Context, result upstream.AttemptResult) {
	value := &forward.OpenAIResult{RequestID: result.RequestID, Model: p.input.Mapping.MappedModel, UpstreamModel: result.UpstreamModel, UpstreamResponseModel: result.UpstreamResponseModel, UpstreamHeaders: result.UpstreamHeaders, Usage: openai.ForwardUsage{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens}, Duration: result.Duration}
	if value.Model == "" {
		value.Model = p.input.Model
	}
	input := gatewayprovider.CaptureOpenAI(ctx, &gatewayprovider.OpenAICapture{RequestPayloadHash: billing.HashUsageRequestPayload(p.input.Body), Result: value, APIKey: p.key, User: p.key.User, Provider: gatewayprovider.ExecutionCompletionRecord(p.selection.Provider), Subscription: p.subscription, InboundEndpoint: gatewayhttp.EndpointSystemOne, UpstreamEndpoint: gatewayhttp.EndpointSystemOne, UserAgent: p.c.GetHeader("User-Agent"), IPAddress: clientip.GetClientIP(p.c), APIKeyService: p.h.bindings.Quota, PricingUsageFields: p.input.Mapping.ToUsageFields(p.input.Model, result.UpstreamModel)})
	recorder, log := p.h.bindings.Common.Recorder, p.log
	p.h.submitOpenAIUsageRecordTask(p.c, value, func(completionContext context.Context) {
		if err := recorder.Record(completionContext, input, true); err != nil {
			log.Error("systemone.record_usage_failed", zap.Error(err))
		}
	})
}

// MissingUsage logs a usage warning and links the upstream attempt to the request record.
func (p *systemOneRun) MissingUsage(_ context.Context, result upstream.AttemptResult) {
	value := p.selection.Provider.Record
	p.log.Warn("systemone.usage_invalid", zap.Int64("provider_id", value.ID), zap.String("upstream_request_id", result.RequestID))
	gatewayhttp.AppendOpsUpstreamError(p.c, ops.OpsUpstreamErrorEvent{Platform: provider.PlatformJev, ProviderID: value.ID, ProviderName: value.Name, UpstreamStatusCode: http.StatusOK, UpstreamRequestID: result.RequestID, Kind: "usage_invalid", Message: "SystemOne usage is missing or invalid; billing skipped"})
}

// Switch records a provider switch.
func (p *systemOneRun) Switch() {
	p.h.bindings.Common.Selection.RecordOpenAIProviderSwitchForSelection(p.selection)
}

// End writes the final error if the response is unwritten and the request is active.
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
