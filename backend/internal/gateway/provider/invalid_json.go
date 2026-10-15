package provider

import (
	"context"
	"net/http"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// invalid_json.go binds invalid-JSON failure classification to provider health
// and request diagnostics.
// A non-JSON upstream response can be an HTTP failure or a nominal success
// with unusable output. The forwarding classifier uses these observations to
// log the failure, apply provider health rules when available, and create a
// failover error carrying the response evidence.

// invalidJSONObservation adapts provider health and diagnostics for invalid
// upstream JSON.
type invalidJSONObservation struct {
	// rateLimit enables persisted health observation when the provider is also
	// present; nil leaves Health using configuration-only error-policy classification.
	rateLimit *provideradapter.UpstreamHealth

	// provider is borrowed from this attempt. Health may transfer credential and
	// Extra changes onto it, so the request owner serializes observation with reads.
	provider *ExecutionProvider
	// headers retains the upstream response map for health observation. Its caller
	// keeps those values unchanged while InvalidJSON invokes the observation adapter.
	headers http.Header
}

// Log writes the invalid-JSON diagnostic through the gateway legacy logging
// component.
func (a invalidJSONObservation) Log(message string) {
	logging.LegacyPrintf("service.gateway", "%s", message)
}

// Health classifies failure policy from provider configuration and optionally
// records full health evidence. A configured observer plus provider enables
// persistence; only the first supplied model is retained, including an
// explicit empty value.
func (a invalidJSONObservation) Health(ctx context.Context, status int, body []byte, models []string) forwardcore.ErrorDecision {
	// InvalidJSON also works without health storage, so begin with provider rules
	// and replace that decision only when persisted observation dependencies exist.
	d := providercore.ErrorDecisionWithoutPersistence(ExecutionErrorPolicy(a.provider), status)
	if a.rateLimit != nil && a.provider != nil {
		if len(models) > 0 {
			d = ApplyExecutionHealth(ctx, a.rateLimit, a.provider, HealthObservationFromContext(ctx, status, a.headers, body, []string{models[0]}))
		} else {
			d = ApplyExecutionHealth(ctx, a.rateLimit, a.provider, HealthObservationFromContext(ctx, status, a.headers, body, nil))
		}
	}
	return forwardcore.ErrorDecision{Generic: d.ShouldReturnGenericError(), RetrySameProvider: d.RetryableOnSameProvider(ExecutionErrorPolicy(a.provider), status)}
}

// Failover wraps response status, body, headers, and same-provider retry
// eligibility for the provider loop. The error retains the supplied byte and
// header storage, so callers keep those values stable while error consumers
// read.
func (a invalidJSONObservation) Failover(status int, headers map[string][]string, body []byte, retry bool) error {
	return &forwardcore.UpstreamFailoverError{StatusCode: status, ResponseBody: body, ResponseHeaders: headers, RetryableOnSameProvider: retry}
}

// NonJSONUpstreamFailure combines a nonnil HTTP response, parse error, body,
// and optional provider for forward.InvalidJSON. The classifier owns status
// interpretation and logging; a missing health observer still allows
// configuration-only retry and generic-error decisions.
func NonJSONUpstreamFailure(
	ctx context.Context,
	healthObserver *provideradapter.UpstreamHealth,
	resp *http.Response,
	provider *ExecutionProvider,
	body []byte,
	parseErr error,
	requestedModel ...string,
) error {
	input := forwardcore.InvalidJSONInput{UpstreamStatus: resp.StatusCode, RequestID: resp.Header.Get("x-request-id"), Headers: resp.Header, Body: body, ParseError: parseErr, RequestedModels: requestedModel}
	if provider != nil {
		input.ProviderID = provider.Record.ID
		input.ProviderName = provider.Record.Name
	}
	return forwardcore.InvalidJSON(ctx, invalidJSONObservation{rateLimit: healthObserver, provider: provider, headers: resp.Header}, input)
}
