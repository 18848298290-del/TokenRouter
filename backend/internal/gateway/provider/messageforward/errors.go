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

// errorExchange supplies the response, health updates, and HTTP output to forward's error handlers.
type errorExchange struct {
	runtime  *Runtime
	output   HTTPBoundary
	state    *AttemptState
	target   *gatewayadapter.ExecutionProvider
	response *http.Response
	body     []byte
}

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

func (e *errorExchange) ScheduleActivity() { e.runtime.scheduleActivity(e.target) }
func (e *errorExchange) ReadBody() ([]byte, error) {
	body, err := e.runtime.readErrorBody(e.response)
	e.body = body
	return body, err
}

func (e *errorExchange) ResetBody(body []byte) {
	_ = e.response.Body.Close()
	e.response.Body = io.NopCloser(bytes.NewReader(body))
}

func (e *errorExchange) Health(ctx context.Context, status int, models []string) forward.ErrorDecision {
	decision := provider.ErrorDecisionWithoutPersistence(gatewayadapter.ExecutionErrorPolicy(e.target), status)
	if e.runtime.dependencies.Health != nil {
		if len(models) > 0 {
			models = models[:1]
		} else {
			models = nil
		}
		decision = gatewayadapter.ApplyExecutionHealth(ctx, e.runtime.dependencies.Health, e.target, gatewayadapter.HealthObservationFromContext(ctx, status, e.response.Header, e.body, models))
	}
	return e.decision(decision, status)
}

func (e *errorExchange) RetryHealth(ctx context.Context, models []string) forward.ErrorDecision {
	return e.decision(e.runtime.retryHealth(ctx, e.response, e.target, models...), e.response.StatusCode)
}

func (e *errorExchange) decision(decision provider.UpstreamErrorDecision, status int) forward.ErrorDecision {
	policy := gatewayadapter.ExecutionErrorPolicy(e.target)
	return forward.ErrorDecision{Generic: decision.ShouldReturnGenericError(), Failover: decision.ShouldFailover(policy, status, false), RetrySameProvider: decision.RetryableOnSameProvider(policy, status)}
}

func (*errorExchange) Failover(status int, body []byte, retry bool) error {
	return &forward.UpstreamFailoverError{StatusCode: status, ResponseBody: body, RetryableOnSameProvider: retry}
}
func (e *errorExchange) Commit() { e.output.Commit() }
func (e *errorExchange) Message(status int, kind, message string) {
	e.output.MessageError(status, kind, message)
}
func (e *errorExchange) Raw(status int, body []byte) { e.output.RawError(status, body) }
func (e *errorExchange) MatchRule(platform string, status int, body []byte) *errorpolicy.ErrorPassthroughRule {
	return e.output.MatchRule(platform, status, body)
}
func (e *errorExchange) SkipMonitoring() { e.output.SkipMonitoring() }
func (e *errorExchange) ScopeDiagnostic(message string, status int, requestID string) {
	if claudeScopeError(message) && e.output.Present() && strings.TrimSpace(e.state.MimicDebug) != "" {
		logging.LegacyPrintf("service.gateway", "[ClaudeMimicDebugOnError] status=%d request_id=%s %s", status, requestID, e.state.MimicDebug)
	}
}

func (e *errorExchange) SetError(status int, message, detail string) {
	e.output.SetError(status, message, detail)
}

func (e *errorExchange) Observe(notice forward.Notice) {
	e.output.Observe(forward.Notice{
		Platform: notice.Platform, ProviderID: notice.ProviderID,
		UpstreamStatusCode: notice.UpstreamStatusCode, UpstreamRequestID: notice.UpstreamRequestID,
		Kind: notice.Kind, Message: notice.Message, Detail: notice.Detail,
	})
}
func (*errorExchange) Log(message string) { logging.LegacyPrintf("service.gateway", "%s", message) }
func (*errorExchange) Truncate(value string, limit int) string {
	return logredact.TruncateUTF8(value, limit)
}

func (*errorExchange) TruncateBytes(body []byte, limit int) string {
	return logredact.TruncateLine(body, limit)
}

func (*errorExchange) Sanitize(value string) string { return logredact.SanitizeUpstreamQueries(value) }

func (r *Runtime) retryHealth(ctx context.Context, response *http.Response, target *gatewayadapter.ExecutionProvider, models ...string) provider.UpstreamErrorDecision {
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

func (r *Runtime) failoverHealth(ctx context.Context, response *http.Response, target *gatewayadapter.ExecutionProvider, models ...string) provider.UpstreamErrorDecision {
	body, _ := r.readErrorBody(response)
	if r.dependencies.Health == nil {
		return provider.ErrorDecisionWithoutPersistence(gatewayadapter.ExecutionErrorPolicy(target), response.StatusCode)
	}
	if len(models) > 0 {
		models = models[:1]
	} else {
		models = nil
	}
	return gatewayadapter.ApplyExecutionHealth(ctx, r.dependencies.Health, target, gatewayadapter.HealthObservationFromContext(ctx, response.StatusCode, response.Header, body, models))
}

// claudeScopeError reports whether both Claude Code authorization phrases occur in the error.
func claudeScopeError(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return message != "" && strings.Contains(message, "only authorized for use with claude code") &&
		strings.Contains(message, "cannot be used for other api requests")
}
