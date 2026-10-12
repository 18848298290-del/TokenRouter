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

const (
	chatgptCodexAlphaSearchURL   = "https://chatgpt.com/backend-api/codex/alpha/search"
	openAIPlatformAlphaSearchURL = "https://api.openai.com/v1/alpha/search"
)

// OpenAIAuxiliary binds outbound search, embedding, and token-counting operations.
type OpenAIAuxiliary struct {
	Requests      *OpenAIRequests
	Output        *OpenAIResponseOutput
	CodexUsage    *provideradapter.CodexUsageObserver
	Authorization *providercore.OpenAIAuthorization
	Enter         func() (func(), error)
}

// ForwardAlphaSearch forwards Codex standalone web searches or uses hosted search for PAT providers.
//
// Successful searches return one WebSearchCalls unit for per-call billing.
// Passed-through upstream errors return a nil result and incur no search charge.
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

	// Codex PATs (at-...) use ChatGPT Codex /responses; standalone /alpha/search rejects them with
	// no_matching_rule.
	// Route PAT searches through the Responses hosted web_search tool.
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
				Prepare:             func() { resp.Body = io.NopCloser(bytes.NewReader(respBody)) },
				ApplySideEffects: func() bool {
					return s.Output.ApplyHTTPFailure(ctx, resp, provider, respBody, openAIAlphaSearchSchedulingModel(provider, requestedModel)).StopScheduling
				},
				NewFailover: func(shouldDisable bool) error {
					retryableOnSameProvider := !shouldDisable && provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(resp.StatusCode)
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
	if result.SearchCount == 0 {
		return nil, nil
	}
	output := &forwardcore.OpenAIResult{RequestID: result.RequestID, UpstreamHeaders: result.UpstreamHeaders, Model: requestedModel, UpstreamModel: upstreamModel, Duration: result.Duration, WebSearchCalls: 1}
	return output, nil
}

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
				Prepare:             func() { resp.Body = io.NopCloser(bytes.NewReader(respBody)) },
				ApplySideEffects: func() bool {
					return s.Output.ApplyHTTPFailure(ctx, resp, provider, respBody, openAIAlphaSearchSchedulingModel(provider, requestedModel)).StopScheduling
				},
				NewFailover: func(shouldDisable bool) error {
					retryableOnSameProvider := !shouldDisable && provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(resp.StatusCode)
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
	if result.SearchCount == 0 {
		return nil, nil
	}
	output := &forwardcore.OpenAIResult{RequestID: result.RequestID, UpstreamHeaders: result.UpstreamHeaders, Model: requestedModel, UpstreamModel: upstreamModel, Duration: result.Duration, WebSearchCalls: 1}
	output.UpstreamEndpoint = "/v1/responses"
	output.ResponseHeaders = result.UpstreamHeaders.Clone()
	return output, nil
}

func openAIAlphaSearchSchedulingModel(provider *gatewayprovider.ExecutionProvider, requestedModel string) string {
	return gatewayprovider.ExecutionModelPolicy(provider).CanonicalSchedulingModel(requestedModel)
}

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

func openAIAlphaSearchInboundHeader(c *gin.Context, key string) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.GetHeader(key))
}

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

func isOpenAIAlphaSearchEndpointUnsupported(provider *gatewayprovider.ExecutionProvider, statusCode int) bool {
	return gatewaymedia.AlphaEndpointUnsupported(provider != nil && provider.Record.Type == capability.ProviderTypeAPIKey, statusCode)
}

// openAIAlphaSearchURL selects the ChatGPT Codex or API-key search endpoint by provider type.
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
