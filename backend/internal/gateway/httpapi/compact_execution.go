package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/compact"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Explicit Compact requests can select a replacement model on the current
// provider after a recognized model or context-window failure. HTTP adapters
// pass compact intent and retry state to compact.Recovery, and bind observations,
// response cleanup, and model diagnostics for an accepted recovery.

// CompactExecutor supplies model configuration and HTTP observations to compact.Recovery.
type CompactExecutor struct {
	Models gatewayprovider.CompactModels // global compact model configuration
	// LogBody controls diagnostic payload inclusion, while LogBodyMaxBytes supplies
	// the byte budget used when inclusion is enabled. Retry observations read both from this executor.
	LogBody bool // include rejected response body
	// LogBodyMaxBytes bounds retry detail. compact.Notice uses 2048 bytes when
	// this value is nonpositive and body logging is enabled.
	LogBodyMaxBytes int // retry payload byte limit
}

// compactRetryEffects binds rejected HTTP attempt details to compact recovery side effects.
type compactRetryEffects struct {
	// options supplies payload logging settings. A nil executor leaves body
	// logging disabled while provider and response metadata can still be observed.
	options  *CompactExecutor
	c        *gin.Context                       // request diagnostic context
	provider *gatewayprovider.ExecutionProvider // rejected execution provider
	// response supplies the rejected status and headers before CloseResponse
	// releases its body. compact.Recovery orders observation before invoking that cleanup.
	response *http.Response
}

// ObserveRetry records the rejected compact exchange as a passthrough retry.
// Recovery invokes it after selecting a replacement but before response closure,
// so observation can read the rejected provider and response metadata.
func (p *compactRetryEffects) ObserveRetry(payload []byte, message string) {
	p.observe(payload, message, true)
}

// observe builds and appends the retry diagnostic for the rejected exchange.
// Missing provider data produces no notice. Response metadata overrides the
// default bad-request status, and executor options decide whether bounded body
// detail accompanies the query-sanitized summary.
func (p *compactRetryEffects) observe(payload []byte, message string, passthrough bool) {
	in := compact.RetryObservation{Status: http.StatusBadRequest, Passthrough: passthrough}
	if p.provider != nil {
		in.ProviderPresent = true
		in.ProviderID = p.provider.Record.ID
		in.ProviderName = p.provider.Record.Name
		in.Platform = p.provider.Record.Platform
	}
	if p.response != nil {
		in.Status = p.response.StatusCode
		in.RequestID = p.response.Header.Get("x-request-id")
	}
	if p.options != nil {
		in.LogBody = p.options.LogBody
		in.LogBodyMaxBytes = p.options.LogBodyMaxBytes
	}
	notice := compact.Notice(in, payload, message, logredact.TruncateUTF8)
	if notice == nil {
		return
	}
	AppendOpsUpstreamError(p.c, ops.OpsUpstreamErrorEvent{
		Platform: notice.Platform, ProviderID: notice.ProviderID, ProviderName: notice.ProviderName,
		UpstreamStatusCode: notice.Status, UpstreamRequestID: notice.RequestID, Passthrough: notice.Passthrough,
		Kind: notice.Kind, Reason: notice.Reason, Message: notice.Message, Detail: notice.Detail, UpstreamResponseBody: notice.Detail,
	})
}

// CloseResponse releases the rejected response body before the replacement exchange.
// A missing response or body is ignored, and close errors are discarded because
// retry eligibility has already been decided from the captured failure.
func (p *compactRetryEffects) CloseResponse() {
	if p.response != nil && p.response.Body != nil {
		// The captured failure remains available after body closure. Cleanup errors
		// cannot improve that response, so recovery continues with its already prepared replacement.
		_ = p.response.Body.Close()
	}
}

// SetModel records the replacement upstream model for the current request's diagnostics.
func (p *compactRetryEffects) SetModel(model string) { SetOpsUpstreamModel(p.c, model) }

// LogRetry reports rejected and replacement models with the captured upstream code.
// Missing provider data uses an empty name. Recovery calls this after model
// diagnostics have been updated, before the caller starts its next exchange.
func (p *compactRetryEffects) LogRetry(from, model, code string) {
	name := ""
	if p.provider != nil {
		name = p.provider.Record.Name
	}
	logging.LegacyPrintf("service.openai_gateway", "[OpenAI passthrough] Retrying explicit compact request once with fallback model (provider: %s, from: %s, to: %s, upstream_code: %s)", name, from, model, code)
}

// IsExplicitOpenAICompactContext reports Compact intent from endpoint or native-compaction markers.
func IsExplicitOpenAICompactContext(c *gin.Context) bool {
	return IsOpenAIResponsesCompactPath(c) || IsOpenAINativeCompactionV2(c)
}

// IsExplicitOpenAICompactRequest reports Compact intent from its endpoint or input trigger.
func IsExplicitOpenAICompactRequest(c *gin.Context, body []byte) bool {
	return IsOpenAIResponsesCompactPath(c) || openaiprotocol.HasCompactionTriggerInInput(body)
}

// NewOpenAICompactFailure captures a recoverable failure for an explicit Compact context.
// Ordinary requests and unrecognized failures return nil. Recovery copies the
// payload and sanitizes the message, so later retry observation retains its captured error data.
func NewOpenAICompactFailure(c *gin.Context, payload []byte, message string) error {
	signal := (gatewayprovider.CompactModels{}).Recovery(nil).NewFailure(IsExplicitOpenAICompactContext(c), payload, message)
	if signal == nil {
		// Keep absent recovery as a nil error interface. Returning the nil Failure
		// pointer through error would report a nonnil error to response readers.
		return nil
	}
	return signal
}

// ResolveModel returns the Compact target configured for this provider and request model.
// Provider compact mappings take precedence over the global fallback, which is
// resolved through ordinary upstream mapping. An empty result disables model recovery.
func (p *CompactExecutor) ResolveModel(target *gatewayprovider.ExecutionProvider, model string) string {
	return p.Models.Recovery(target).ResolveModel(model)
}

// Prepare asks recovery for one replacement body on the selected provider.
// tried reports whether that recovery has been consumed. A false result keeps
// the supplied body; a true result requires the caller to verify its output
// window still permits another exchange and then record the consumed retry.
func (p *CompactExecutor) Prepare(c *gin.Context, target *gatewayprovider.ExecutionProvider, requested string, body []byte, status int, message string, payload []byte, tried bool) ([]byte, string, bool) {
	return p.Models.Recovery(target).Prepare(compact.Request{Explicit: IsExplicitOpenAICompactRequest(c, body), AlreadyRetried: tried, RequestedModel: requested, Body: body}, status, message, payload)
}

// Observe records a Compact retry diagnostic using the supplied HTTP exchange metadata.
// passthrough labels the exchange mode, and executor settings control body detail.
// A missing provider yields no notice; this observation leaves response cleanup to recovery.
func (p *CompactExecutor) Observe(c *gin.Context, target *gatewayprovider.ExecutionProvider, response *http.Response, payload []byte, message string, passthrough bool) {
	effects := compactRetryEffects{options: p, c: c, provider: target, response: response}
	effects.observe(payload, message, passthrough)
}

// ApplySignal turns a captured Compact failure into one prepared recovery attempt.
// An unrelated or ineligible error returns the supplied body and false.
// Successful recovery observes the rejected response, closes its body, and records
// the replacement model before returning; the caller owns output eligibility
// and performs the subsequent exchange with tried set for later decisions.
func (p *CompactExecutor) ApplySignal(c *gin.Context, target *gatewayprovider.ExecutionProvider, requested string, body []byte, err error, tried bool, response *http.Response) ([]byte, string, bool) {
	signal, ok := compact.AsFailure(err)
	if !ok {
		return body, "", false
	}
	return p.Models.Recovery(target).ApplySignal(compact.Request{Explicit: IsExplicitOpenAICompactRequest(c, body), AlreadyRetried: tried, RequestedModel: requested, Body: body}, signal, &compactRetryEffects{options: p, c: c, provider: target, response: response})
}
