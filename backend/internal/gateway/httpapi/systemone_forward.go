package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/systemone"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/jev"
)

// SystemOne decisions resolve provider mapping and billing-model pricing
// before a Jev exchange begins. An answer can be served with valid or unknown
// usage independently of client delivery success, so completion consumes the
// execution observations rather than treating every write failure as an unused
// provider.
//
// The adapter retains the requested model beside group and provider mappings,
// restoring aliases or composite prefixes for client output while observing
// the vendor response version separately. HTTP 422 is delivered as a terminal
// input error. Other failures pass through provider health and retry
// classification before final response rules; rewritten JSON drops
// Content-Encoding so its headers describe the actual delivered
// representation.

// SystemOneExecutor combines pricing preflight, provider transport, and one Jev attempt.
type SystemOneExecutor struct {
	// Transport and Pricing.Resolver are checked before Forward prepares a
	// provider request. Missing either dependency stops locally with a configured 503.
	Transport httpclient.UpstreamTransport // shared provider HTTP transport
	URLPolicy egress.OperatorURLPolicy     // operator target URL validation
	// HeaderFilter selects permitted upstream response fields. Nil uses the shared
	// default filter, including its allowed names and forced removal rules.
	HeaderFilter *egress.CompiledHeaderFilter // configured response header filter
	// ReadLimit bounds raw Jev response bytes; nonpositive values use its 16 MiB
	// default. The native executor reads one extra byte to detect an oversized body.
	ReadLimit int64 // response body bytes
	// Health can be nil; failure classification then uses provider policy without
	// persistence. A configured observer applies health before client error presentation.
	Health *provideradapter.UpstreamHealth
	// Pricing and its Resolver are required by Forward. Missing pricing dependencies
	// produce a local 503 before provider parsing or any HTTP exchange begins.
	Pricing *admission.ModelPricing
	// Enter optionally tracks the native attempt during application shutdown;
	// a successful entry supplies cleanup released by the Jev executor.
	Enter func() (func(), error)
}

// Forward validates final-model pricing and executes one selected Jev
// SystemOne request. Transport and a pricing resolver are required, and the
// provider must use the Jev platform. The body model is mapped for transport
// while route-plan requested identity and any composite prefix determine
// client restoration.
//
// Served answers return their observations even with a client delivery error.
// Before service, HTTP 422 writes a terminal parameter error; other HTTP
// failures apply provider health and policy before producing retry guidance or
// final output. Same-provider retry retains the larger of the shared delay and
// Retry-After reset interval, and active transport failures can become a 502
// failover signal.
func (s *SystemOneExecutor) Forward(ctx context.Context, c *gin.Context, target *gatewayprovider.ExecutionProvider, body []byte) (upstream.AttemptResult, error) {
	var result upstream.AttemptResult
	if s == nil || s.Pricing == nil || s.Pricing.Resolver == nil || s.Transport == nil {
		WriteSystemOneError(c, http.StatusServiceUnavailable, "api_error", "", "", "SystemOne execution is not configured")
		return result, errors.New("SystemOne execution is not configured")
	}
	if target == nil || target.Record.Platform != provider.PlatformJev {
		return result, errors.New("jev provider is required")
	}
	selected, err := gatewayprovider.ProviderForProtocolAttempt(ctx, target)
	if err != nil {
		return result, err
	}
	request, err := systemone.ParseRequest(body)
	if err != nil {
		return result, err
	}
	mapped := gatewayprovider.ExecutionModelPolicy(selected).Mapped(request.Model)
	mapping := routing.GroupMappingResult{MappedModel: request.Model}
	requested := request.Model
	if plan, exists := requeststate.RoutePlanFromContext(ctx); exists {
		mapping = plan.Mapping()
		requested = plan.Models().RequestedModel
	}
	key, _ := EffectiveAPIKey(c)
	var groupID *int64
	if key != nil {
		groupID = key.GroupID
	}
	// Price the configured billing identity after mapping, keeping requested and
	// upstream names separate so a priced alias cannot authorize an unpriced billing target.
	billingModel := completion.OpenAIUsageBillingModel(&completion.Result{Model: request.Model, UpstreamModel: mapped}, mapping.ToUsageFields(requested, mapped))
	if err = s.Pricing.Check(ctx, groupID, billingModel); err != nil {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
		WriteSystemOneError(c, 400, "invalid_request_error", "model_pricing_unavailable", "", admission.ModelPricingUnavailableMessage)
		return result, err
	}
	base, err := s.URLPolicy.Validate(selected.View().GetJevBaseURL())
	if err != nil {
		return result, err
	}
	body, err = systemone.ReplaceModel(body, mapped)
	if err != nil {
		return result, err
	}
	SetOpsUpstreamModel(c, mapped)
	SetActualUpstreamEndpoint(c, EndpointSystemOne)
	proxy := ""
	if selected.Record.Proxy != nil {
		proxy = selected.Record.Proxy.URL()
	}
	t := &jev.Target{
		ProviderID: selected.Record.ID, URL: jev.EndpointURL(base, "systemone"), Model: mapped,
		Token: selected.View().GetCredential("api_key"), ReadLimit: s.ReadLimit, Enter: s.Enter,
		ApplyHeaders: gatewayprovider.BindExecutionHeaders(selected),
		Do: func(req *http.Request) (*http.Response, error) {
			return s.Transport.Do(req, proxy, selected.Record.ID, selected.Record.Concurrency)
		},
		WriteHeaders: func(out, in http.Header) { egressprovider.WriteFilteredHeaders(out, in, s.HeaderFilter) },
	}
	// Restore configured client aliases and composite prefixes while the native
	// result retains the observed vendor version for completion and request diagnostics.
	responseModel := ""
	if requested != mapped {
		responseModel = requested
	}
	if clientModel, _, composite := GetCompositeModelFromContext(c); composite {
		responseModel = clientModel
	}
	result, err = (jev.Executor{}).Execute(ctx, upstream.AttemptInput{Protocol: protocol.ProtocolSystemOne, Body: body, ResponseModel: responseModel, Target: t}, ResponseSink{Writer: c.Writer})
	// Ops stores whole milliseconds, discarding submillisecond attempt timing.
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, result.Duration.Milliseconds())
	if result.Served {
		return result, err
	}
	if failure, ok := errors.AsType[*jev.HTTPError](err); ok {
		AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{Platform: provider.PlatformJev, ProviderID: selected.Record.ID, ProviderName: selected.Record.Name, UpstreamStatusCode: failure.Status, UpstreamRequestID: failure.Header.Get("x-request-id"), Kind: "upstream_error", Message: failure.Error()})
		// A 422 describes this request's input validation. Present it directly
		// before provider health classification so the request is not replayed elsewhere.
		if failure.Status == http.StatusUnprocessableEntity {
			s.writeUpstreamError(c, failure)
			return result, err
		}
		decision := provider.ErrorDecisionWithoutPersistence(selected.View(), failure.Status)
		if s.Health != nil {
			// Health writes retain request values under a separate five-second budget,
			// so client cancellation does not abandon the observed provider-state update.
			stateCtx, cancel := provideradapter.ProviderStateContext(ctx)
			decision = s.Health.ApplyUpstreamError(stateCtx, selected.View(), provideradapter.HealthObservation{
				Status: failure.Status, Headers: failure.Header, Body: failure.Body,
				Model: mapped, ModelProvided: true, EffectiveModel: mapped,
			})
			cancel()
		}
		retry := failure.Status == 401 || failure.Status == 403 || failure.Status == 429 || failure.Status >= 500
		if decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(selected), failure.Status, retry) && !decision.ShouldReturnGenericError() {
			retryFailure := &forward.UpstreamFailoverError{StatusCode: failure.Status, ResponseBody: failure.Body, ResponseHeaders: failure.Header.Clone(), RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(selected), failure.Status)}
			// Preserve a longer upstream reset interval for same-provider retry, so
			// the outer loop does not resend before that provider's requested wait has elapsed.
			if retryFailure.RetryableOnSameProvider {
				if reset := jev.RetryAfterResetTime(failure.Header, time.Now()); reset != nil {
					retryFailure.SameProviderRetryDelay = max(failover.SameProviderRetryDelay, time.Until(*reset))
				}
			}
			return result, retryFailure
		}
		if decision.ShouldReturnGenericError() {
			WriteSystemOneError(c, http.StatusInternalServerError, "upstream_error", "", "", "Upstream request failed")
			return result, err
		}
		s.writeUpstreamError(c, failure)
		return result, err
	}
	// A transport failure with an active request has supplied no served answer;
	// expose failover eligibility while cancellation keeps its ordinary error path.
	if result.FailureClass == "transport" && ctx.Err() == nil {
		return result, &forward.UpstreamFailoverError{StatusCode: http.StatusBadGateway}
	}
	return result, err
}

// writeUpstreamError delivers buffered Jev errors after filtered header
// copying. An already-written response is left alone; a matching administrator
// rule writes uncompressed SystemOne JSON, while unmatched errors retain their
// supplied body and Content-Type.
func (s *SystemOneExecutor) writeUpstreamError(c *gin.Context, failure *jev.HTTPError) {
	if c.Writer.Written() {
		return
	}
	egressprovider.WriteFilteredHeaders(c.Writer.Header(), failure.Header, s.HeaderFilter)
	if status, kind, message, matched := ApplyErrorPassthroughRule(c, provider.PlatformJev, failure.Status, failure.Body, failure.Status, "upstream_error", "Upstream request failed"); matched {
		// The replacement is uncompressed JSON. Remove the upstream encoding so
		// clients decode these new bytes using their JSON representation instead of
		// attempting the vendor response's compression.
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.Writer.Header().Del("Content-Encoding")
		WriteSystemOneError(c, status, kind, "", "", message)
		return
	}
	contentType := failure.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(failure.Status, contentType, failure.Body)
}

// WriteSystemOneFailoverExhausted presents a nonnil exhausted Jev failure
// while the response is unwritten. It retains Retry-After, applies
// status/message defaults then administrator response rules, and records the
// original upstream failure for Ops.
func WriteSystemOneFailoverExhausted(c *gin.Context, failure *forward.UpstreamFailoverError) {
	if c.Writer.Written() {
		return
	}
	CopyFailoverRetryAfter(c, http.Header(failure.ResponseHeaders))
	status, kind, message := http.StatusBadGateway, "upstream_error", "Upstream request failed"
	switch failure.StatusCode {
	case http.StatusUnauthorized:
		message = "Upstream authentication failed, please contact administrator"
	case http.StatusForbidden:
		message = "Upstream access forbidden, please contact administrator"
	case http.StatusTooManyRequests:
		status, kind, message = http.StatusTooManyRequests, "rate_limit_error", "Upstream rate limit exceeded, please retry later"
	case 529:
		status, message = http.StatusServiceUnavailable, "Upstream service overloaded, please retry later"
	case 500, 502, 503, 504:
		message = "Upstream service temporarily unavailable"
	}
	status, kind, message, _ = ApplyErrorPassthroughRule(c, provider.PlatformJev, failure.StatusCode, failure.ResponseBody, status, kind, message)
	SetOpsUpstreamError(c, failure.StatusCode, upstream.ExtractErrorMessage(failure.ResponseBody), "")
	WriteSystemOneError(c, status, kind, "", "", message)
}
