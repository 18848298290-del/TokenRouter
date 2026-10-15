package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	forward "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	openaiws "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws"
)

// openai_ws_forwarder_payload.go builds WebSocket endpoints and handshake
// headers and selects store-recovery policy. Raw client session values remain
// available in the returned resolution for diagnostics; the outgoing Codex
// headers use the Key and provider namespace shared with HTTP requests.
//
// Endpoint preparation validates configured API Key base URLs before changing
// their schemes for WebSocket dialing. OAuth-like setup tokens use Codex
// endpoints and store-disabled affinity rules, while ordinary bearer paths
// require a token. Store recovery permission belongs to provider configuration;
// the ingress mode accessor currently selects strict affinity for all callers
// that ask it for a mode.

// validateOpenAIWSBearerToken rejects a missing provider or blank token on
// bearer-authenticated paths. Agent Identity accepts an empty token because
// the dial-time header refresh supplies assertion authentication for that
// provider.
func validateOpenAIWSBearerToken(provider *gatewayprovider.ExecutionProvider, token string) error {
	if provider == nil {
		return errors.New("provider is nil")
	}
	if strings.TrimSpace(token) == "" && !provider.View().IsOpenAIAgentIdentity() {
		return errors.New("token is empty")
	}
	return nil
}

// buildOpenAIResponsesWSURL returns the selected provider's Responses endpoint
// with a WebSocket scheme. OAuth and OAuth-like setup tokens use Codex; API
// Key endpoints follow configured protocol targeting and base-URL validation.
// HTTP and HTTPS become WS and WSS, existing WebSocket schemes remain
// accepted, and other schemes return an error.
func (s *OpenAIWebSocketExecutor) buildOpenAIResponsesWSURL(provider *gatewayprovider.ExecutionProvider) (string, error) {
	if provider == nil {
		return "", errors.New("provider is nil")
	}
	var targetURL string
	switch provider.Record.Type {
	case capability.ProviderTypeOAuth:
		targetURL = gatewayhttp.ChatgptCodexURL
	case capability.ProviderTypeSetupToken:
		if provider.View().IsOpenAIOAuthLike() {
			targetURL = gatewayhttp.ChatgptCodexURL
		} else {
			targetURL = gatewayhttp.OpenaiPlatformAPIURL
		}
	case capability.ProviderTypeAPIKey:
		baseURL := gatewayprovider.ExecutionProtocolTarget(provider).GetOpenAIBaseURL()
		if _, unified := provider.Record.Credentials[providercore.UpstreamProtocolsKey]; gatewayprovider.ExecutionProtocolTarget(provider).UsesNativeCNResponses() && (unified || gatewayprovider.ExecutionProtocolTarget(provider).IsAdaptiveAPIProtocol()) {
			// Unified or adaptive CN Responses configuration chooses its protocol-specific
			// base URL before validation, matching the endpoint used by Responses HTTP.
			baseURL = gatewayprovider.ExecutionProtocolTarget(provider).GetCNProtocolBaseURL(providercore.APIProtocolResponses)
		}
		if baseURL == "" {
			targetURL = gatewayhttp.OpenaiPlatformAPIURL
		} else {
			validatedURL, err := s.Requests.ValidateBaseURL(baseURL)
			if err != nil {
				return "", err
			}
			targetURL = forward.ResponsesEndpoint(provider.Record.Platform, validatedURL)
		}
	default:
		targetURL = gatewayhttp.OpenaiPlatformAPIURL
	}

	parsed, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil {
		return "", fmt.Errorf("invalid target url: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		// Retain endpoint path and query while converting the validated HTTP scheme
		// to its WebSocket counterpart, so dialing targets the same Responses API.
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "wss", "ws":
		// Accept WebSocket schemes supplied by endpoint configuration; rewriting an
		// already selected transport scheme would discard a usable dial target.
	default:
		return "", fmt.Errorf("unsupported scheme for ws: %s", parsed.Scheme)
	}
	return parsed.String(), nil
}

// buildOpenAIWSHeaders builds handshake headers and returns the raw client
// session resolution used to derive them. It binds HTTP-shared Codex identity
// scoping, staged fingerprints, provider credentials, user-agent policy, beta
// features, and final-model routing diagnostics into BuildWSHeaders. Required
// Requests services and the provider must be available for those callbacks.
//
// ResolveSession captures unscoped session and conversation IDs for the
// returned resolution. Outbound identity headers use the authenticated Key and
// provider namespace so HTTP and WS requests from the same client derive
// matching identities. Routing hints are generated from the supplied final
// model and tier after provider overrides and capability negotiation; header-
// build errors stop acquisition before a connection is leased.
func (s *OpenAIWebSocketExecutor) buildOpenAIWSHeaders(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	token string,
	decision egress.OpenAIWSProtocolDecision,
	isCodexCLI bool,
	turnState string,
	turnMetadata string,
	promptCacheKey string,
	routingModel string,
	routingServiceTier string,
	routerMatch ...egress.TLSFingerprintRouterMatchResult,
) (http.Header, gatewayhttp.OpenAIWSSessionHeaderResolution, error) {
	var sessionResolution gatewayhttp.OpenAIWSSessionHeaderResolution
	headers, err := openaiws.BuildWSHeaders(ctx, openaiws.WSHeaderOptions{
		AgentIdentity: provider != nil && provider.View().IsOpenAIAgentIdentity(), Token: token,
		TurnState: turnState, TurnMetadata: turnMetadata,

		ResolveSession: func() (string, string) {
			sessionResolution = gatewayhttp.ResolveOpenAIWSSessionHeaders(c, promptCacheKey)
			return sessionResolution.SessionID, sessionResolution.ConversationID
		},
		InboundHeaders: func() http.Header {
			if c != nil && c.Request != nil {
				return c.Request.Header
			}
			return nil
		},
		UserAgent: func() string {
			if c != nil {
				return c.GetHeader("User-Agent")
			}
			return ""
		},
		ApplyWSUserAgent: func(headers http.Header) {
			reqCtx := context.Background()
			if c != nil && c.Request != nil {
				reqCtx = c.Request.Context()
			}
			s.Requests.ApplyUserAgentHeader(reqCtx, c, provider, headers, true, routerMatch...)
		},
		ResponsesRequestOptions: upstreamopenai.ResponsesRequestOptions{
			UsesCodex: func() bool { return provider != nil && provider.View().UsesOpenAICodexProtocol() },
			APIKeyID:  func() int64 { return gatewayhttp.APIKeyIDFromContext(c) },
			IsolateSession: func(keyID int64, value string) string {
				return upstreamopenai.IsolateOpenAIUpstreamSessionID(keyID, provideradapter.CodexIdentityNamespace(gatewayhttp.CodexIdentityRecord(c, provider.View())), value)
			},
			ApplyProviderIdentity: func(headers http.Header) {
				upstreamopenai.ApplyCodexProviderIdentityHeaders(headers, provideradapter.CodexIdentityNamespace(gatewayhttp.CodexIdentityRecord(c, provider.View())), gatewayhttp.APIKeyIDFromContext(c))
			},
			ApplyFingerprint: func(headers http.Header) { gatewayhttp.ApplyStagedCodexFingerprintHeaders(c, provider.View(), headers) },
			ProviderHeaders: func(ctx context.Context, headers http.Header) error {
				return gatewayprovider.CredentialChatGPTHeaders(ctx, s.Requests.Providers, headers, provider)
			},
			Originator:      func() string { return gatewayhttp.ResolveOpenAIUpstreamOriginator(c, isCodexCLI, routerMatch...) },
			OverrideHeaders: gatewayprovider.BindExecutionHeaders(provider),
			BetaFeatures: func(headers http.Header) {
				gatewayhttp.ApplyOpenAICodexBetaFeatures(c, provider != nil && provider.View().IsOpenAIOAuthLike(), headers)
			},
			RoutingHint: func(headers http.Header, _ []byte) {
				gatewayhttp.SetOpenAICodexRoutingHint(headers, provider, routingModel, routingServiceTier)
			},
			Diagnostics: func(headers http.Header, _ []byte) {
				gatewayhttp.LogOpenAIRoutingDiagnostics(ctx, provider, string(decision.Transport), routingModel, routingServiceTier, strings.TrimSpace(headers.Get(gatewayhttp.OpenAICodexRoutingHintHeader)) != "", "soft_routing_hint")
			},
		},
	})
	return headers, sessionResolution, err
}

// isOpenAIWSStoreRecoveryAllowed reports whether the provider enables store
// recovery. A nil provider returns false, so OAuth-like requests then use
// store-disabled continuation rules even if their payload asks for storage.
func (s *OpenAIWebSocketExecutor) isOpenAIWSStoreRecoveryAllowed(provider *gatewayprovider.ExecutionProvider) bool {
	if provider != nil && provider.View().IsOpenAIWSAllowStoreRecoveryEnabled() {
		return true
	}

	return false
}

// openAIWSStoreDisabledConnMode returns the strict disabled-store connection
// affinity mode.
func (s *OpenAIWebSocketExecutor) openAIWSStoreDisabledConnMode() string { return "strict" }

// isOpenAIWSStoreDisabledInRequestRaw reports whether the request needs
// disabled-store affinity. Codex providers with recovery disabled return true
// before payload inspection; otherwise only a present JSON boolean false
// disables storage. Empty, absent, or nonboolean store values return false.
func (s *OpenAIWebSocketExecutor) isOpenAIWSStoreDisabledInRequestRaw(reqBody []byte, provider *gatewayprovider.ExecutionProvider) bool {
	if provider != nil && provider.View().UsesOpenAICodexProtocol() && !s.isOpenAIWSStoreRecoveryAllowed(provider) {
		return true
	}
	if len(reqBody) == 0 {
		return false
	}
	storeValue := gjson.GetBytes(reqBody, "store")
	if !storeValue.Exists() {
		return false
	}
	if storeValue.Type != gjson.True && storeValue.Type != gjson.False {
		return false
	}
	return !storeValue.Bool()
}
