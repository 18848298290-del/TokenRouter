package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	forward "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// ChatURL resolves the upstream Chat Completions endpoint for non-Grok providers.
func (s *OpenAIRequests) ChatURL(provider *gatewayprovider.ExecutionProvider) (string, error) {
	baseURL := gatewayprovider.ExecutionProtocolTarget(provider).GetOpenAIBaseURL()
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	validatedURL, err := s.ValidateBaseURL(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return httpclient.BuildOpenAIEndpointURL(validatedURL, "/v1/chat/completions"), nil
}

// ChatFallbackTarget resolves credentials and endpoints for Chat Completions fallback paths.
// Grok uses its OAuth or API key credentials and CLI endpoint.
func (s *OpenAIRequests) ChatFallbackTarget(ctx context.Context, provider *gatewayprovider.ExecutionProvider) (apiKey string, targetURL string, err error) {
	if provider.View().IsGrok() {
		apiKey, _, err = s.Credentials.Resolve(ctx, gatewayprovider.ExecutionRecord(provider))
		if err != nil {
			return "", "", err
		}
		targetURL, err = s.GrokRoutes.Chat(provider, true)
		return apiKey, targetURL, err
	}
	apiKey = strings.TrimSpace(provider.View().GetOpenAIProtocolAPIKey())
	if apiKey == "" {
		return "", "", fmt.Errorf("provider %d missing api_key", provider.Record.ID)
	}
	targetURL, err = s.ChatURL(provider)
	if err != nil {
		return "", "", err
	}
	return apiKey, targetURL, nil
}

func (s *OpenAIRequests) SendChat(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	targetURL string,
	body []byte,
	stream bool,
	bearerToken string,
	userAgent string,
	grokCacheIdentity string,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*http.Response, error) {
	var err error
	body, err = s.applyModelAttributes(ctx, provider, body, "openai_chat_completions")
	if err != nil {
		return nil, err
	}
	return openai.SendChatRequest(ctx, body, openai.CCRequestOptions{
		URL: targetURL, Token: bearerToken, Stream: stream, Headers: c.Request.Header,
		RequestContext:  gatewayprovider.DetachUpstreamContext,
		ObserveEndpoint: func() { SetActualOpenAIUpstreamEndpoint(c, "/v1/chat/completions") },
		AllowHeader:     func(name string) bool { return openaiCCRawAllowedHeaders[name] },
		PrepareTransport: func(upstreamReq *http.Request) {
			if len(tlsRouterMatch) == 0 {
				tlsRouterMatch = []egress.TLSFingerprintRouterMatchResult{s.MatchTLS(c, provider)}
			}
			if provider.Record.Platform == capability.PlatformGrok && userAgent != "" {
				upstreamReq.Header.Set("user-agent", userAgent)
			} else if provider.Record.Platform != capability.PlatformGrok {
				s.ApplyUserAgent(c.Request.Context(), c, provider, upstreamReq, false, tlsRouterMatch[0])
			}

			if provider.Record.Platform == capability.PlatformGrok {
				if provider.View().IsGrokOAuth() {
					grok.ApplyCLIHeaders(upstreamReq.Header)
				}
				grok.ApplyGrokCacheHeaders(upstreamReq.Header, grokCacheIdentity)
			}
		},
		FinalizeHeaders: func(headers http.Header) {
			provideradapter.ApplyProviderHeaderOverrides(gatewayprovider.ExecutionProtocolRecord(provider), headers)
			ApplyOpenCodeSessionHeader(c, provider, targetURL, headers)
		},
		Do: func(req *http.Request) (*http.Response, error) {
			proxyURL := ""
			if provider.Record.Proxy != nil {
				proxyURL = provider.Record.Proxy.URL()
			}
			return s.Transport.DoWithTLS(req, proxyURL, provider.Record.ID, provider.Record.Concurrency, s.TLSProfile(provider, tlsRouterMatch...))
		},
		TransportError: func(err error) error { return s.Failure.Handle(ctx, c, provider, err, false) },
	})
}

// AnthropicURL validates and builds the native Anthropic target URL.
func (s *OpenAIRequests) AnthropicURL(provider *gatewayprovider.ExecutionProvider) (string, error) {
	return forward.NativeAnthropicTargetURL(provider.Record.ID, gatewayprovider.ExecutionProtocolTarget(provider).GetAnthropicProtocolBaseURL(), s.ValidateBaseURL)
}

// BuildAnthropic builds a request from inbound headers and provider policy.
func (s *OpenAIRequests) BuildAnthropic(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte, apiKey, targetURL string) (*http.Request, []byte, error) {
	var err error
	body, err = s.applyModelAttributes(ctx, provider, body, "anthropic_messages")
	if err != nil {
		return nil, nil, err
	}
	var headers http.Header
	if c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	return forward.BuildNativeAnthropicRequest(ctx, body, apiKey, targetURL, forward.NativeAnthropicRequestOptions{
		Headers: headers, GetHeader: anthropic.GetHeaderRaw, OverrideValue: gatewayprovider.BindExecutionHeaderValue(provider),
		Sanitize: anthropic.SanitizeAnthropicBodyForBetaTokens, AllowedHeader: func(key string) bool { return anthropic.AllowedHeaders[key] },
		WireCasing: anthropic.ResolveWireCasing, AddHeader: anthropic.AddHeaderRaw, SetHeader: anthropic.SetHeaderRaw,
		AuthHeader: func(h http.Header, key string) {
			anthropic.SetAPIKeyAuthHeader(h, gatewayprovider.ExecutionProtocolRecord(provider).GetAnthropicAPIKeyAuthScheme() == providercore.AnthropicAPIKeyAuthSchemeAuthorizationBearer, key)
		}, ApplyOverrides: gatewayprovider.BindExecutionHeaders(provider),
	})
}

func (s *OpenAIRequests) RawChatURL(provider *gatewayprovider.ExecutionProvider) (string, error) {
	if provider.Record.Platform == capability.PlatformGrok {
		targetURL, err := s.GrokRoutes.Chat(provider, true)
		if err != nil {
			return "", fmt.Errorf("invalid grok base_url: %w", err)
		}
		return targetURL, nil
	}

	return s.ChatURL(provider)
}

// ImagesURL selects the generation or edit endpoint and applies a configured base URL.
func (s *OpenAIRequests) ImagesURL(provider *gatewayprovider.ExecutionProvider, endpoint string) (string, error) {
	targetURL := openAIImagesGenerationsURL
	if endpoint == upstream.OpenAIImagesEditsEndpoint {
		targetURL = openAIImagesEditsURL
	}
	baseURL := gatewayprovider.ExecutionProtocolTarget(provider).GetOpenAIBaseURL()
	if baseURL != "" {
		validatedURL, err := s.ValidateBaseURL(baseURL)
		if err != nil {
			return "", err
		}
		targetURL = httpclient.BuildOpenAIEndpointURL(validatedURL, endpoint)
	}

	return targetURL, nil
}
