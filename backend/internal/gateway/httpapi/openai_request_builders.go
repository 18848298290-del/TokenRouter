package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	forward "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func (s *OpenAIRequests) Target(c *gin.Context, a *gatewayprovider.ExecutionProvider, passthrough bool) forward.RequestTargetOptions {
	oauth := a.Record.Type == capability.ProviderTypeOAuth || a.Record.Type == capability.ProviderTypeSetupToken && (!passthrough || a.View().IsOpenAIOAuthLike())
	return forward.RequestTargetOptions{
		OAuthTarget: oauth, APIKey: a.Record.Type == capability.ProviderTypeAPIKey, DefaultURL: OpenaiPlatformAPIURL, CodexURL: ChatgptCodexURL,
		BaseURL: func() string {
			base := gatewayprovider.ExecutionProtocolTarget(a).GetOpenAIBaseURL()
			if _, unified := a.Record.Credentials[provider.UpstreamProtocolsKey]; gatewayprovider.ExecutionProtocolTarget(a).UsesNativeCNResponses() && (unified || gatewayprovider.ExecutionProtocolTarget(a).IsAdaptiveAPIProtocol()) {
				base = gatewayprovider.ExecutionProtocolTarget(a).GetCNProtocolBaseURL(provider.APIProtocolResponses)
			}
			return base
		}, Validate: s.ValidateBaseURL, FromBase: func(base string) string { return forward.ResponsesEndpoint(a.Record.Platform, base) }, AppendSuffix: func(base string) string {
			return openai.AppendResponsesPathSuffix(base, OpenAIResponsesRequestPathSuffix(c))
		},
	}
}

func (s *OpenAIRequests) ResponseOptions(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, token, targetURL string, isCodexCLI bool, routerMatch ...egress.TLSFingerprintRouterMatchResult) openai.ResponsesRequestOptions {
	return openai.ResponsesRequestOptions{
		URL: targetURL, ForwardHeaders: func() http.Header { return c.Request.Header },
		Authenticate: func(ctx context.Context) (http.Header, error) {
			return s.Identity.Headers(ctx, provider, token)
		},
		ProviderHeaders: func(ctx context.Context, headers http.Header) error {
			return gatewayprovider.CredentialChatGPTHeaders(ctx, s.Providers, headers, provider)
		},
		UsesCodex:      provider.View().UsesOpenAICodexProtocol,
		IsCompact:      func() bool { return IsOpenAIResponsesCompactPath(c) },
		ForceCodexCLI:  func() bool { return s.Options.ForceCLI },
		AllowHeader:    func(name string) bool { return openaiAllowedHeaders[name] },
		GuardTurnState: func(headers http.Header) { s.Turns.Guard(c, provider, headers) },
		MessagesBridge: func(body []byte) bool {
			return IsOpenAICompatMessagesBridgeContext(c) || gatewayprovider.IsOpenAICompatMessagesBridgeBody(body)
		},
		Originator:     func() string { return ResolveOpenAIUpstreamOriginator(c, isCodexCLI, routerMatch...) },
		CompactSession: func() string { return ResolveOpenAICompactSessionID(c) },
		APIKeyID:       func() int64 { return APIKeyIDFromContext(c) },
		IsolateSession: func(keyID int64, raw string) string {
			return openai.IsolateOpenAIUpstreamSessionID(keyID, provideradapter.CodexIdentityNamespace(CodexIdentityRecord(c, provider.View())), raw)
		},
		ApplyUserAgent: func(req *http.Request) { s.ApplyUserAgent(ctx, c, provider, req, false, routerMatch...) },
		ApplyProviderIdentity: func(headers http.Header) {
			openai.ApplyCodexProviderIdentityHeaders(headers, provideradapter.CodexIdentityNamespace(CodexIdentityRecord(c, provider.View())), APIKeyIDFromContext(c))
		},
		ApplyFingerprint: func(headers http.Header) { ApplyStagedCodexFingerprintHeaders(c, provider.View(), headers) },
		OverrideHeaders:  gatewayprovider.BindExecutionHeaders(provider),
		OpenCodeSession:  func(headers http.Header) { ApplyOpenCodeSessionHeader(c, provider, targetURL, headers) },
		BetaFeatures: func(headers http.Header) {
			ApplyOpenAICodexBetaFeatures(c, provider != nil && provider.View().IsOpenAIOAuthLike(), headers)
		},
		RoutingHint: func(headers http.Header, body []byte) { SetOpenAICodexRoutingHintFromBody(headers, provider, body) },
		Diagnostics: func(headers http.Header, body []byte) {
			LogOpenAIRoutingDiagnosticsFromBody(ctx, provider, "http", headers, body, "not_applicable")
		},
	}
}

// Build 将执行目标和请求选项传给请求构造器。
func (s *OpenAIRequests) Build(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte, token string, isStream bool, promptCacheKey string, isCodexCLI bool, routerMatch ...egress.TLSFingerprintRouterMatchResult) (*http.Request, error) {
	var err error
	body, err = s.applyModelAttributes(ctx, provider, body, "openai_responses")
	if err != nil {
		return nil, err
	}
	return forward.BuildResponsesRequest(ctx, body, promptCacheKey, s.Target(c, provider, false), func(path string) { SetActualOpenAIUpstreamEndpoint(c, path) }, func(b []byte) []byte {
		return forward.NormalizeCNResponsesBody(provider != nil && gatewayprovider.ExecutionProtocolTarget(provider).UsesNativeCNResponses(), b)
	}, func(target string) openai.ResponsesRequestOptions {
		return s.ResponseOptions(ctx, c, provider, token, target, isCodexCLI, routerMatch...)
	})
}

func (s *OpenAIRequests) BuildPassthrough(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	token string,
	routerMatch ...egress.TLSFingerprintRouterMatchResult,
) (*http.Request, error) {
	var err error
	body, err = s.applyModelAttributes(ctx, provider, body, "openai_responses")
	if err != nil {
		return nil, err
	}
	return forward.BuildPassthroughRequest(ctx, body, s.Target(c, provider, true), func(b []byte) []byte {
		return forward.NormalizeCNResponsesBody(provider != nil && gatewayprovider.ExecutionProtocolTarget(provider).UsesNativeCNResponses(), b)
	}, func(target string) openai.PassthroughRequestOptions {
		options := s.ResponseOptions(ctx, c, provider, token, target, false, routerMatch...)
		options.ForwardHeaders = func() http.Header {
			if c == nil || c.Request == nil {
				return nil
			}
			return c.Request.Header
		}
		options.ApplyUserAgent = func(req *http.Request) { s.ApplyUserAgent(ctx, c, provider, req, true, routerMatch...) }
		options.Diagnostics = func(headers http.Header, body []byte) {
			LogOpenAIRoutingDiagnosticsFromBody(ctx, provider, "http_passthrough", headers, body, "not_applicable")
		}
		return openai.PassthroughRequestOptions{
			ResponsesRequestOptions: options,
			AllowTimeoutHeaders:     s.AllowTimeoutHeaders,
			AllowPassthroughHeader:  isOpenAIPassthroughAllowedRequestHeader,
			MatchedOriginator: func() string {
				if len(routerMatch) > 0 && routerMatch[0].Matched {
					return strings.TrimSpace(routerMatch[0].UpstreamOriginator)
				}
				return ""
			},
		}
	})
}

func isOpenAIPassthroughAllowedRequestHeader(lowerKey string, allowTimeoutHeaders bool) bool {
	if lowerKey == "" {
		return false
	}
	if isOpenAIPassthroughTimeoutHeader(lowerKey) {
		return allowTimeoutHeaders
	}
	return openaiPassthroughAllowedHeaders[lowerKey]
}

func isOpenAIPassthroughTimeoutHeader(lowerKey string) bool {
	switch lowerKey {
	case "x-stainless-timeout", "x-stainless-read-timeout", "x-stainless-connect-timeout", "x-request-timeout", "request-timeout", "grpc-timeout":
		return true
	default:
		return false
	}
}
