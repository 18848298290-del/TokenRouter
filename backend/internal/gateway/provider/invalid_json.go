package provider

import (
	"context"
	"net/http"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// invalidJSONObservation applies health policy to JSON failures and builds forwarding errors.
type invalidJSONObservation struct {
	rateLimit *provideradapter.UpstreamHealth

	provider *ExecutionProvider
	headers  http.Header
}

func (a invalidJSONObservation) Log(message string) {
	logging.LegacyPrintf("service.gateway", "%s", message)
}

func (a invalidJSONObservation) Health(ctx context.Context, status int, body []byte, models []string) forwardcore.ErrorDecision {
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

func (a invalidJSONObservation) Failover(status int, headers map[string][]string, body []byte, retry bool) error {
	return &forwardcore.UpstreamFailoverError{StatusCode: status, ResponseBody: body, ResponseHeaders: headers, RetryableOnSameProvider: retry}
}

// NonJSONUpstreamFailure shares health feedback and failover errors between direct Anthropic and compatible output.
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
