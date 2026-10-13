package messageforward

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// An upstream error body is inspected by health policy and HTTP presentation
// during the same attempt. The adapter retains captured bytes and can rebuild
// the response reader so later consumers see the diagnostic evidence after the
// network reader has been consumed.
//
// Retry exhaustion first applies explicit provider policy, including custom
// error code and pool decisions. Default persisted handling is then limited to
// OAuth status 403 on that path; ordinary failover health uses the first
// requested model. These decisions are passed to gateway/forward, where error
// rules and response commitment determine the final client presentation.

// errorExchange binds upstream failure evidence to provider health and HTTP
// presentation.
type errorExchange struct {
	// runtime, output, state, and target are borrowed during synchronous
	// error handling. response supplies the current headers and body;
	// ReadBody saves captured bytes in body for subsequent health
	// inspection.
	runtime *Runtime      // shared Messages forwarding runtime
	output  HTTPBoundary  // current HTTP error presenter
	state   *AttemptState // current attempt diagnostic state
	// selected execution provider state
	target   *gatewayadapter.ExecutionProvider
	response *http.Response // current upstream failure response
	body     []byte         // captured error response bytes
}

// handleError selects ordinary or exhausted-retry Messages error handling for
// the retained response. It supplies provider identity, response status,
// request ID, and requested models; caller ownership includes closing any
// rebuilt body.
func (r *Runtime) handleError(ctx context.Context, output HTTPBoundary, state *AttemptState, target *gatewayadapter.ExecutionProvider, response *http.Response, retry bool, models ...string) (*forward.Result, error) {
	ports := &errorExchange{runtime: r, output: output, state: state, target: target, response: response}
	input := forward.ErrorInput{
		ProviderID: target.Record.ID, ProviderName: target.Record.Name, ProviderType: target.Record.Type,
		Platform: target.Record.Platform, RequestID: response.Header.Get("x-request-id"), Status: response.StatusCode,
		RequestedModels: models, LogBody: r.options.LogErrorBody, LogBodyMaxBytes: r.options.LogErrorBodyMaxBytes,
	}
	if retry {
		return forward.AnthropicRetryError(ctx, ports, input)
	}
	return forward.AnthropicError(ctx, ports, input)
}

// ScheduleActivity delegates eligible provider activity scheduling before final
// error handling.
func (e *errorExchange) ScheduleActivity() { e.runtime.scheduleActivity(e.target) }

// ReadBody stores bounded diagnostic bytes and returns the source read error.
// The response remains caller-owned, and later health observation uses the
// saved bytes even when reading reports an error.
func (e *errorExchange) ReadBody() ([]byte, error) {
	body, err := e.runtime.readErrorBody(e.response)
	e.body = body
	return body, err
}

// ResetBody closes the current reader and replaces it with captured
// diagnostics. Callers provide usable response state, and subsequent policy or
// presentation reads consume the new in-memory body.
func (e *errorExchange) ResetBody(body []byte) {
	// Replace the consumed reader with the captured bytes so later health
	// and presentation steps inspect the same bounded evidence after
	// source-body closure.
	_ = e.response.Body.Close()
	e.response.Body = io.NopCloser(bytes.NewReader(body))
}

// Health applies the target provider policy and optional default health
// observation. Persisted default observation receives the first requested
// model, while the result supplies the presentation flow generic and retry
// choices.
func (e *errorExchange) Health(ctx context.Context, status int, models []string) forward.ErrorDecision {
	decision := provider.ErrorDecisionWithoutPersistence(gatewayadapter.ExecutionErrorPolicy(e.target), status)
	if e.runtime.dependencies.Health != nil {
		if len(models) > 0 {
			// Scope this default observation to the first requested
			// model.
			models = models[:1]
		} else {
			models = nil
		}
		decision = gatewayadapter.ApplyExecutionHealth(ctx, e.runtime.dependencies.Health, e.target, gatewayadapter.HealthObservationFromContext(ctx, status, e.response.Header, e.body, models))
	}
	return e.decision(decision, status)
}

// RetryHealth delegates exhausted-retry health handling using the current
// response status and requested models.
func (e *errorExchange) RetryHealth(ctx context.Context, models []string) forward.ErrorDecision {
	return e.decision(e.runtime.retryHealth(ctx, e.response, e.target, models...), e.response.StatusCode)
}

// decision translates a provider health result into generic, failover, and
// retry choices.
func (e *errorExchange) decision(decision provider.UpstreamErrorDecision, status int) forward.ErrorDecision {
	policy := gatewayadapter.ExecutionErrorPolicy(e.target)
	return forward.ErrorDecision{Generic: decision.ShouldReturnGenericError(), Failover: decision.ShouldFailover(policy, status, false), RetrySameProvider: decision.RetryableOnSameProvider(policy, status)}
}

// Failover constructs the failure signal for the outer provider loop. It
// retains the supplied diagnostic bytes, and retry carries same-provider replay
// permission selected by the error handling flow.
func (*errorExchange) Failover(status int, body []byte, retry bool) error {
	return &forward.UpstreamFailoverError{StatusCode: status, ResponseBody: body, RetryableOnSameProvider: retry}
}

// Commit records semantic response commitment through the current HTTP
// presentation adapter.
func (e *errorExchange) Commit() { e.output.Commit() }

// Message delegates the selected Anthropic error envelope to current HTTP
// presentation.
func (e *errorExchange) Message(status int, kind, message string) {
	e.output.MessageError(status, kind, message)
}

// Raw delegates the selected JSON error bytes to current HTTP presentation.
func (e *errorExchange) Raw(status int, body []byte) { e.output.RawError(status, body) }

// MatchRule delegates presentation rule matching for platform, status, and
// error bytes.
func (e *errorExchange) MatchRule(platform string, status int, body []byte) *errorpolicy.ErrorPassthroughRule {
	return e.output.MatchRule(platform, status, body)
}

// SkipMonitoring sets the current request flag suppressing passthrough error
// observation.
func (e *errorExchange) SkipMonitoring() { e.output.SkipMonitoring() }

// ScopeDiagnostic logs saved mimic evidence for recognized Claude Code
// authorization errors. Logging requires a present HTTP context and nonblank
// MimicDebug retained by this attempt preparation.
func (e *errorExchange) ScopeDiagnostic(message string, status int, requestID string) {
	if claudeScopeError(message) && e.output.Present() && strings.TrimSpace(e.state.MimicDebug) != "" {
		logging.LegacyPrintf("service.gateway", "[ClaudeMimicDebugOnError] status=%d request_id=%s %s", status, requestID, e.state.MimicDebug)
	}
}

// SetError records upstream failure status and details for current request
// diagnostics.
func (e *errorExchange) SetError(status int, message, detail string) {
	e.output.SetError(status, message, detail)
}

// Observe appends the failure fields used by this error adapter to HTTP
// diagnostics.
func (e *errorExchange) Observe(notice forward.Notice) {
	e.output.Observe(forward.Notice{
		Platform: notice.Platform, ProviderID: notice.ProviderID,
		UpstreamStatusCode: notice.UpstreamStatusCode, UpstreamRequestID: notice.UpstreamRequestID,
		Kind: notice.Kind, Message: notice.Message, Detail: notice.Detail,
	})
}

// Log writes the supplied error diagnostic under the gateway logging component.
func (*errorExchange) Log(message string) { logging.LegacyPrintf("service.gateway", "%s", message) }

// Truncate delegates a UTF-8 diagnostic prefix using the supplied byte limit.
func (*errorExchange) Truncate(value string, limit int) string {
	return logredact.TruncateUTF8(value, limit)
}

// TruncateBytes delegates bounded one-line diagnostics for the supplied
// response body bytes.
func (*errorExchange) TruncateBytes(body []byte, limit int) string {
	return logredact.TruncateLine(body, limit)
}

// Sanitize removes sensitive upstream query values from supplied error
// diagnostic text.
func (*errorExchange) Sanitize(value string) string { return logredact.SanitizeUpstreamQueries(value) }

// retryHealth selects health handling after retries have consumed their
// provider budget. It captures available error bytes and applies explicit
// policy before deciding whether any default observation should persist.
//
// Custom matches and temporary rules stop scheduling; custom skips and pool
// bypass return their selected policy directly. With no explicit decision,
// OAuth status 403 uses full health observation, while other retry-exhausted
// statuses remain governed by the returned policy and outer attempt handling.
func (r *Runtime) retryHealth(ctx context.Context, response *http.Response, target *gatewayadapter.ExecutionProvider, models ...string) provider.UpstreamErrorDecision {
	// Keep the diagnostic prefix available after read failures because
	// policy evaluation still uses that evidence to classify the last
	// upstream response.
	body, _ := r.readErrorBody(response)
	status := response.StatusCode
	if r.dependencies.Health == nil {
		return provider.ErrorDecisionWithoutPersistence(gatewayadapter.ExecutionErrorPolicy(target), status)
	}
	policy := r.dependencies.Health.ApplyExplicitErrorPolicy(ctx, gatewayadapter.ExecutionRecord(target), gatewayadapter.HealthObservationFromContext(ctx, status, nil, body, models))
	decision := provider.UpstreamErrorDecision{Policy: policy}
	switch policy {
	case provider.ErrorPolicyCustomMatched, provider.ErrorPolicyTempUnscheduled:
		decision.StopScheduling = true
		return decision
	case provider.ErrorPolicyCustomSkipped, provider.ErrorPolicyPoolBypassed:
		return decision
	}
	if target.View().IsOAuth() && status == 403 {
		decision = gatewayadapter.ApplyExecutionHealth(ctx, r.dependencies.Health, target, gatewayadapter.HealthObservationFromContext(ctx, status, response.Header, body, models))
		logging.LegacyPrintf("service.gateway", "Provider %d: applied upstream error policy after %d retries for status %d", target.Record.ID, maxRetryAttempts, status)
	} else {
		logging.LegacyPrintf("service.gateway", "Provider %d: upstream error %d after %d retries (not marking provider)", target.Record.ID, status, maxRetryAttempts)
	}
	return decision
}

// failoverHealth applies default health observation to the first requested
// model after capturing error bytes. Missing Health returns the provider policy
// decision; callers own the response reader after inspection.
func (r *Runtime) failoverHealth(ctx context.Context, response *http.Response, target *gatewayadapter.ExecutionProvider, models ...string) provider.UpstreamErrorDecision {
	// Keep the diagnostic prefix available after read failures because
	// policy evaluation still uses that evidence to classify the last
	// upstream response.
	body, _ := r.readErrorBody(response)
	if r.dependencies.Health == nil {
		return provider.ErrorDecisionWithoutPersistence(gatewayadapter.ExecutionErrorPolicy(target), response.StatusCode)
	}
	if len(models) > 0 {
		// Scope this default observation to the first requested model.
		models = models[:1]
	} else {
		models = nil
	}
	return gatewayadapter.ApplyExecutionHealth(ctx, r.dependencies.Health, target, gatewayadapter.HealthObservationFromContext(ctx, response.StatusCode, response.Header, body, models))
}

// claudeScopeError recognizes both Claude Code authorization phrases in
// normalized error text.
func claudeScopeError(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return message != "" && strings.Contains(message, "only authorized for use with claude code") &&
		strings.Contains(message, "cannot be used for other api requests")
}
