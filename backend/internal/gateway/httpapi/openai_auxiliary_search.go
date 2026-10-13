package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	mediaprovider "github.com/TokenFlux/TokenRouter/internal/gateway/media/provider"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Standalone Alpha Search and PAT hosted search produce client search output
// through different upstream protocols. Provider mapping supplies the upstream
// model while the requested name remains available for completion. PAT
// credentials lacking account identity can acquire metadata before
// construction, and a failed durable write can leave request-local credentials
// updated.
//
// The native executor owns response resources and conversion. Error callbacks
// may install replay readers for health handling, so cleanup targets the
// original acquired body. Successful searches carry one per-call billing unit;
// handled HTTP errors return no completion result. API-key missing endpoints
// and other classified failures can switch providers before output, while the
// shared error resolver excludes authentication and endpoint-compatibility
// statuses from health side effects.

// chatgptCodexAlphaSearchURL and openAIPlatformAlphaSearchURL are default
// standalone search targets, selected by credential type before API-key base
// validation and incoming query handling.
const (
	// chatgptCodexAlphaSearchURL is the standalone Codex search target.
	chatgptCodexAlphaSearchURL = "https://chatgpt.com/backend-api/codex/alpha/search"
	// openAIPlatformAlphaSearchURL is the public API-key search target.
	openAIPlatformAlphaSearchURL = "https://api.openai.com/v1/alpha/search"
)

// OpenAIAuxiliary binds shared execution services for selected-provider
// auxiliary HTTP exchanges.
type OpenAIAuxiliary struct {
	Requests      *OpenAIRequests                     // shared outgoing request services
	Output        *OpenAIResponseOutput               // shared response and health adapters
	CodexUsage    *provideradapter.CodexUsageObserver // Codex quota-header observation service
	Authorization *providercore.OpenAIAuthorization   // PAT account-metadata discovery service
	// Enter optionally registers application work before the native
	// exchange. Successful admission returns cleanup that execution invokes
	// after original response-body closure.
	Enter func() (func(), error)
}

// ForwardAlphaSearch executes standalone search or the selected PAT's
// hosted-search path. It requires a service, Gin context, provider, and string
// model before preparing credential identity, provider mapping, sanitized
// input, and authentication metadata. Native execution owns the original
// upstream body throughout HTTP handling.
//
// PAT providers use Responses web_search; other supported credentials use Alpha
// Search. Success returns one WebSearchCalls unit for outer completion billing.
// A handled upstream error can return nil result and nil error, so callers use
// result availability to decide whether to record a search charge.
func (s *OpenAIAuxiliary) ForwardAlphaSearch(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*forwardcore.OpenAIResult, error) {
	if s == nil || c == nil || provider == nil {
		return nil, fmt.Errorf("service, context, and provider are required")
	}
	if _, err := PrepareCodexIdentity(ctx, c, s.Requests.Providers, provider); err != nil {
		return nil, err
	}
	modelResult := gjson.GetBytes(body, "model")
	requestedModel := strings.TrimSpace(modelResult.String())
	if modelResult.Type != gjson.String || requestedModel == "" {
		return nil, fmt.Errorf("model is required")
	}

	upstreamModel := gatewayprovider.ExecutionModelPolicy(provider).NormalizeOpenAI(gatewayprovider.ExecutionModelPolicy(provider).Mapped(requestedModel))
	if upstreamModel != "" && upstreamModel != requestedModel {
		body = openaiprotocol.ReplaceModelInBody(body, upstreamModel)
	}
	sanitizedBody, err := openai.SanitizeOpenAIAlphaSearchBody(body)
	if err != nil {
		return nil, fmt.Errorf("sanitize alpha search request body: %w", err)
	}
	body = sanitizedBody

	token, _, err := s.Requests.Credentials.Resolve(ctx, gatewayprovider.ExecutionRecord(provider))
	if err != nil {
		return nil, err
	}

	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}
	if err := s.ensureOpenAIAlphaSearchAuthMetadata(ctx, provider, token, proxyURL); err != nil {
		return nil, err
	}
	SetOpsUpstreamModel(c, upstreamModel)

	// PAT credentials select hosted web_search through the Responses
	// builder, which converts alpha input and maps the successful Responses
	// events back to search JSON.
	if provider.View().IsOpenAIPersonalAccessToken() {
		return s.forwardAlphaSearchViaResponsesWebSearch(ctx, c, provider, body, token, proxyURL, requestedModel, upstreamModel, tlsRouterMatch...)
	}

	req, err := s.buildOpenAIAlphaSearchRequest(ctx, c, provider, body, token, tlsRouterMatch...)
	if err != nil {
		return nil, err
	}

	target := &mediaprovider.AlphaSearchOptions{
		ProviderID: provider.Record.ID, Request: req, ResponsesFallback: false, Model: upstreamModel, Enter: s.Enter,
		Do: func(request *http.Request) (*http.Response, error) {
			return s.Requests.Transport.DoWithTLS(request, proxyURL, provider.Record.ID, provider.Record.Concurrency, s.Requests.TLSProfile(provider, tlsRouterMatch...))
		},
		Latency: func(duration time.Duration) {
			SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, duration.Milliseconds())
		},
		TransportError: func(err error) error { return s.Requests.Failure.Handle(ctx, c, provider, err, true) },
		ReadBody: func(reader io.Reader) ([]byte, error) {
			return ReadUpstreamResponseBody(reader, s.Output.Options.ReadLimit, c, OpenAIResponseTooLarge)
		},
		HTTPError: func(resp *http.Response, respBody []byte) error {
			upstreamMessage := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
			return gatewaymedia.ResolveAlphaFailure(resp.StatusCode, gatewaymedia.AlphaFailurePorts{
				Failover: func() bool {
					return gatewayprovider.ShouldFailoverOpenAIResponse(resp.StatusCode, upstreamMessage, respBody)
				},
				EndpointUnsupported: func() bool { return isOpenAIAlphaSearchEndpointUnsupported(provider, resp.StatusCode) },
				// Health handling can reread captured errors;
				// native execution still closes the originally
				// acquired body after this replay reader
				// replaces resp.Body.
				Prepare: func() { resp.Body = io.NopCloser(bytes.NewReader(respBody)) },
				ApplySideEffects: func() bool {
					return s.Output.ApplyHTTPFailure(ctx, resp, provider, respBody, openAIAlphaSearchSchedulingModel(provider, requestedModel)).StopScheduling
				},
				NewFailover: func(shouldDisable bool) error {
					retryableOnSameProvider := !shouldDisable && provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(resp.StatusCode)
					// OAuth-like 429 and access-state
					// evidence carry typed recovery
					// metadata; ordinary failures return
					// the status/body shape expected by
					// outer search retry handling.
					if provider.View().IsOpenAIOAuthLike() && resp.StatusCode == http.StatusTooManyRequests {
						return (gatewayprovider.OpenAIFailoverPolicy{Health: s.Output.Health}).NewProviderFailure(provider, resp.StatusCode, resp.Header, respBody, upstreamMessage, shouldDisable, retryableOnSameProvider)
					}
					if gatewayprovider.IsOpenAIHTTPUpstreamAccessStateError(resp.StatusCode, upstreamMessage, respBody) {
						return gatewayprovider.NewOpenAIUpstreamFailure(resp.StatusCode, resp.Header, respBody, upstreamMessage, retryableOnSameProvider)
					}
					return &forwardcore.UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: respBody, RetryableOnSameProvider: retryableOnSameProvider}
				},
			})
		},
		UpdateQuota: func(headers http.Header) {
			// Shadow quota state comes from its dedicated usage
			// query, so these response headers update only the
			// selected nonshadow provider's Codex snapshot here.
			if !provider.View().IsShadow() {
				s.CodexUsage.Headers(ctx, provider.Record.ID, headers)
			}
		},
		Headers: func(dst, src http.Header) {
			WriteOpenAIPassthroughResponseHeaders(dst, src, s.Output.Headers)
		},
	}
	result, err := (mediaprovider.AlphaSearch{Options: *target}).Execute(ctx, upstream.AttemptInput{Protocol: protocol.ProtocolAlphaSearch, ResponseModel: requestedModel}, ResponseSink{Writer: c.Writer})
	if err != nil {
		return nil, err
	}
	// A handled upstream error may already have been forwarded. Without a
	// successful search count, return no completion result for the outer
	// per-call billing entry.
	if result.SearchCount == 0 {
		return nil, nil
	}
	output := &forwardcore.OpenAIResult{RequestID: result.RequestID, UpstreamHeaders: result.UpstreamHeaders, Model: requestedModel, UpstreamModel: upstreamModel, Duration: result.Duration, WebSearchCalls: 1}
	return output, nil
}

// forwardAlphaSearchViaResponsesWebSearch executes a PAT search through
// Responses web_search. The supplied alpha input becomes hosted-search input
// sent to Codex. Empty upstream model uses the requested name; successful
// conversion returns one search-call unit and Responses endpoint/header
// metadata for outer completion.
//
// HTTP failures follow the shared search recovery rules. Native execution
// closes its original response body even when error handling installs a replay
// reader. Handled responses with no successful search return nil result,
// leaving completion eligibility to the outer media caller.
func (s *OpenAIAuxiliary) forwardAlphaSearchViaResponsesWebSearch(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	alphaBody []byte,
	token string,
	proxyURL string,
	requestedModel string,
	upstreamModel string,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*forwardcore.OpenAIResult, error) {
	if upstreamModel == "" {
		upstreamModel = requestedModel
	}
	responsesBody, err := openai.BuildOpenAIAlphaSearchResponsesWebSearchBody(alphaBody, upstreamModel)
	if err != nil {
		return nil, err
	}
	req, err := s.buildOpenAIAlphaSearchResponsesWebSearchRequest(ctx, c, provider, alphaBody, responsesBody, token, tlsRouterMatch...)
	if err != nil {
		return nil, err
	}
	SetActualOpenAIUpstreamEndpoint(c, "/v1/responses")

	target := &mediaprovider.AlphaSearchOptions{
		ProviderID: provider.Record.ID, Request: req, ResponsesFallback: true, Model: upstreamModel, Enter: s.Enter,
		Do: func(request *http.Request) (*http.Response, error) {
			return s.Requests.Transport.DoWithTLS(request, proxyURL, provider.Record.ID, provider.Record.Concurrency, s.Requests.TLSProfile(provider, tlsRouterMatch...))
		},
		Latency: func(duration time.Duration) {
			SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, duration.Milliseconds())
		},
		TransportError: func(err error) error { return s.Requests.Failure.Handle(ctx, c, provider, err, true) },
		ReadBody: func(reader io.Reader) ([]byte, error) {
			return ReadUpstreamResponseBody(reader, s.Output.Options.ReadLimit, c, OpenAIResponseTooLarge)
		},
		HTTPError: func(resp *http.Response, respBody []byte) error {
			upstreamMessage := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
			return gatewaymedia.ResolveAlphaFailure(resp.StatusCode, gatewaymedia.AlphaFailurePorts{
				Failover: func() bool {
					return gatewayprovider.ShouldFailoverOpenAIResponse(resp.StatusCode, upstreamMessage, respBody)
				},
				EndpointUnsupported: func() bool { return false },
				// Health handling can reread captured errors;
				// native execution still closes the originally
				// acquired body after this replay reader
				// replaces resp.Body.
				Prepare: func() { resp.Body = io.NopCloser(bytes.NewReader(respBody)) },
				ApplySideEffects: func() bool {
					return s.Output.ApplyHTTPFailure(ctx, resp, provider, respBody, openAIAlphaSearchSchedulingModel(provider, requestedModel)).StopScheduling
				},
				NewFailover: func(shouldDisable bool) error {
					retryableOnSameProvider := !shouldDisable && provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(resp.StatusCode)
					// OAuth-like 429 and access-state
					// evidence carry typed recovery
					// metadata; ordinary failures return
					// the status/body shape expected by
					// outer search retry handling.
					if provider.View().IsOpenAIOAuthLike() && resp.StatusCode == http.StatusTooManyRequests {
						return (gatewayprovider.OpenAIFailoverPolicy{Health: s.Output.Health}).NewProviderFailure(provider, resp.StatusCode, resp.Header, respBody, upstreamMessage, shouldDisable, retryableOnSameProvider)
					}
					if gatewayprovider.IsOpenAIHTTPUpstreamAccessStateError(resp.StatusCode, upstreamMessage, respBody) {
						return gatewayprovider.NewOpenAIUpstreamFailure(resp.StatusCode, resp.Header, respBody, upstreamMessage, retryableOnSameProvider)
					}
					return &forwardcore.UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: respBody, RetryableOnSameProvider: retryableOnSameProvider}
				},
			})
		},
		UpdateQuota: func(headers http.Header) {
			// Shadow quota state comes from its dedicated usage
			// query, so these response headers update only the
			// selected nonshadow provider's Codex snapshot here.
			if !provider.View().IsShadow() {
				s.CodexUsage.Headers(ctx, provider.Record.ID, headers)
			}
		},
		Headers: func(dst, src http.Header) {
			WriteOpenAIPassthroughResponseHeaders(dst, src, s.Output.Headers)
		},
	}
	result, err := (mediaprovider.AlphaSearch{Options: *target}).Execute(ctx, upstream.AttemptInput{Protocol: protocol.ProtocolAlphaSearch, ResponseModel: requestedModel}, ResponseSink{Writer: c.Writer})
	if err != nil {
		return nil, err
	}
	// A handled upstream error may already have been forwarded. Without a
	// successful search count, return no completion result for the outer
	// per-call billing entry.
	if result.SearchCount == 0 {
		return nil, nil
	}
	output := &forwardcore.OpenAIResult{RequestID: result.RequestID, UpstreamHeaders: result.UpstreamHeaders, Model: requestedModel, UpstreamModel: upstreamModel, Duration: result.Duration, WebSearchCalls: 1}
	output.UpstreamEndpoint = "/v1/responses"
	output.ResponseHeaders = result.UpstreamHeaders.Clone()
	return output, nil
}

// openAIAlphaSearchSchedulingModel returns the provider's canonical model for
// search health feedback.
func openAIAlphaSearchSchedulingModel(provider *gatewayprovider.ExecutionProvider, requestedModel string) string {
	return gatewayprovider.ExecutionModelPolicy(provider).CanonicalSchedulingModel(requestedModel)
}

// buildOpenAIAlphaSearchResponsesWebSearchRequest constructs a Codex
// hosted-search POST. It binds current identity and passthrough user-agent
// callbacks, optional inbound query/header readers, and delegates session
// metadata and capability filtering to the selected upstream request builder.
func (s *OpenAIAuxiliary) buildOpenAIAlphaSearchResponsesWebSearchRequest(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, alphaBody []byte, body []byte, token string, tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult) (*http.Request, error) {
	targetURL := ChatgptCodexURL
	options := s.Requests.ResponseOptions(ctx, c, provider, token, targetURL, true, tlsRouterMatch...)
	options.ApplyUserAgent = func(req *http.Request) { s.Requests.ApplyUserAgent(ctx, c, provider, req, true, tlsRouterMatch...) }
	return openai.BuildAlphaSearchResponsesRequest(ctx, alphaBody, body, openai.AlphaSearchRequestOptions{
		ResponsesRequestOptions: options,
		Query: func() url.Values {
			if c == nil || c.Request == nil || c.Request.URL == nil {
				return nil
			}
			return c.Request.URL.Query()
		},
		OAuth:         func() bool { return provider.Record.Type == capability.ProviderTypeOAuth },
		InboundHeader: func(key string) string { return openAIAlphaSearchInboundHeader(c, key) },
		IdentityWithKey: func(headers http.Header, key int64) {
			openai.ApplyCodexProviderIdentityHeaders(headers, provideradapter.CodexIdentityNamespace(CodexIdentityRecord(c, provider.View())), key)
		},
		ResponsesLiteHeader: gatewaymedia.ResponsesLiteHeaderKey,
	})
}

// buildOpenAIAlphaSearchRequest constructs the selected provider's standalone
// search POST. Target validation precedes construction. The upstream builder
// receives inbound query and trimmed header values, OAuth eligibility, and the
// current provider's identity callbacks.
func (s *OpenAIAuxiliary) buildOpenAIAlphaSearchRequest(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	token string,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*http.Request, error) {
	targetURL, err := s.openAIAlphaSearchURL(provider)
	if err != nil {
		return nil, err
	}
	options := s.Requests.ResponseOptions(ctx, c, provider, token, targetURL, true, tlsRouterMatch...)
	options.ApplyUserAgent = func(req *http.Request) { s.Requests.ApplyUserAgent(ctx, c, provider, req, true, tlsRouterMatch...) }
	return openai.BuildAlphaSearchRequest(ctx, body, openai.AlphaSearchRequestOptions{
		ResponsesRequestOptions: options,
		Query: func() url.Values {
			if c == nil || c.Request == nil || c.Request.URL == nil {
				return nil
			}
			return c.Request.URL.Query()
		},
		OAuth:         func() bool { return provider.Record.Type == capability.ProviderTypeOAuth },
		InboundHeader: func(key string) string { return openAIAlphaSearchInboundHeader(c, key) },
		IdentityWithKey: func(headers http.Header, key int64) {
			openai.ApplyCodexProviderIdentityHeaders(headers, provideradapter.CodexIdentityNamespace(CodexIdentityRecord(c, provider.View())), key)
		},
		ResponsesLiteHeader: gatewaymedia.ResponsesLiteHeaderKey,
	})
}

// openAIAlphaSearchInboundHeader returns a trimmed inbound value for search
// construction. A nil context returns empty text, allowing the upstream request
// builder to select its normal metadata defaults for that request.
func openAIAlphaSearchInboundHeader(c *gin.Context, key string) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.GetHeader(key))
}

// ensureOpenAIAlphaSearchAuthMetadata fills missing account identity for
// selected PAT credentials. Absent authorization capability or existing account
// metadata skips lookup. Callers hold exclusive execution-record access; local
// credentials are applied before optional storage, and storage errors can leave
// that local update visible.
func (s *OpenAIAuxiliary) ensureOpenAIAlphaSearchAuthMetadata(ctx context.Context, provider *gatewayprovider.ExecutionProvider, token string, proxyURL string) error {
	if s == nil || provider == nil || !provider.View().IsOpenAIPersonalAccessToken() {
		return nil
	}
	if strings.TrimSpace(provider.View().GetChatGPTAccountID()) != "" {
		return nil
	}
	oauthService := s.Authorization
	if oauthService == nil {
		return nil
	}
	// Request-visible metadata is applied before optional persistence.
	// Shallow credential copies separate top-level maps while retaining
	// shared nested values from existing configuration.
	ports := providercore.OpenAIAlphaMetadataPorts{
		Apply: func(credentials map[string]any) { provider.Record.Credentials = querycache.ShallowMap(credentials) },
	}
	if s.Requests.Providers != nil {
		ports.Persist = func(ctx context.Context, credentials map[string]any) error {
			return gatewayprovider.PersistExecutionCredentials(ctx, s.Requests.Providers, provider, credentials)
		}
	}
	return oauthService.EnsureAlphaSearchMetadata(ctx, gatewayprovider.ExecutionRecord(provider), token, proxyURL, ports)
}

// isOpenAIAlphaSearchEndpointUnsupported reports API-key search endpoint
// compatibility failures. The shared predicate checks absent-endpoint statuses
// for API-key targets; OAuth and missing providers do not select this
// endpoint-recovery rule.
func isOpenAIAlphaSearchEndpointUnsupported(provider *gatewayprovider.ExecutionProvider, statusCode int) bool {
	return gatewaymedia.AlphaEndpointUnsupported(provider != nil && provider.Record.Type == capability.ProviderTypeAPIKey, statusCode)
}

// openAIAlphaSearchURL selects a standalone target for supported credential
// types. OAuth and setup tokens use Codex; API keys use the public default or a
// validated protocol base. Missing providers and unsupported types return
// errors.
func (s *OpenAIAuxiliary) openAIAlphaSearchURL(provider *gatewayprovider.ExecutionProvider) (string, error) {
	if provider == nil {
		return "", fmt.Errorf("provider is required")
	}
	switch provider.Record.Type {
	case capability.ProviderTypeOAuth, capability.ProviderTypeSetupToken:
		return chatgptCodexAlphaSearchURL, nil
	case capability.ProviderTypeAPIKey:
		baseURL := gatewayprovider.ExecutionProtocolTarget(provider).GetOpenAIBaseURL()
		if baseURL == "" {
			return openAIPlatformAlphaSearchURL, nil
		}
		validatedURL, err := s.Requests.ValidateBaseURL(baseURL)
		if err != nil {
			return "", err
		}
		return httpclient.BuildOpenAIEndpointURL(validatedURL, "/v1/alpha/search"), nil
	default:
		return "", fmt.Errorf("unsupported OpenAI provider type: %s", provider.Record.Type)
	}
}
