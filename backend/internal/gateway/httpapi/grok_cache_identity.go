package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaysession "github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

const (
	grokClientToolCacheOptInHeader       = "X-TokenRouter-Grok-Client-Tool-Cache"
	legacyGrokClientToolCacheOptInHeader = "X-Sub2API-Grok-Client-Tool-Cache"
)

// ExtractClaudeCodeSessionID reads Claude Code session identity from its request header
// or Anthropic/OpenAI-compatible payload metadata.
func ExtractClaudeCodeSessionID(c *gin.Context, body []byte) string {
	if c != nil {
		if seed := strings.TrimSpace(c.GetHeader(ClaudeCodeSessionHeader)); seed != "" {
			return seed
		}
	}
	return grok.ExtractClaudeCodeSessionIDFromPayload(body)
}

func ResolveGrokCacheIdentity(c *gin.Context, body []byte, explicitKey, upstreamModel string) string {
	return grok.ResolveCacheIdentity(grokCacheInput(c, explicitKey, upstreamModel), body)
}

// ApplyGrokFreeRequestToolCacheRoute applies provider policy and request-level cache-routing overrides.
// The cache-control header is local; outbound construction forwards only supported OpenAI-Beta values.
func ApplyGrokFreeRequestToolCacheRoute(c *gin.Context, body, intentSourceBody []byte, provider *gatewayprovider.ExecutionProvider, cacheIdentity string) ([]byte, error) {
	allowPureClientTools, providerPolicyExplicit := gatewayprovider.GrokClientToolCacheProviderPolicy(provider)
	requestOptOut := false
	if c != nil {
		// The current header takes precedence; consult the legacy header only when it is absent.
		value := c.GetHeader(grokClientToolCacheOptInHeader)
		if _, present := c.Request.Header[http.CanonicalHeaderKey(grokClientToolCacheOptInHeader)]; !present {
			value = c.GetHeader(legacyGrokClientToolCacheOptInHeader)
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on", "prefer-cache":
			allowPureClientTools = true
		case "0", "false", "no", "off":
			allowPureClientTools = false
			requestOptOut = true
		}
	}
	if !allowPureClientTools && !providerPolicyExplicit && !requestOptOut && isGrokClaudeDesktopResponsesCacheRequest(c) {
		allowPureClientTools = true
	}
	// Functions named web_search or x_search still express client tools. Known Free OAuth providers
	// default to cache routing. Request opt-in can override provider opt-out; request opt-out wins.
	// The legacy Claude fingerprint applies only when no provider policy is configured (#4486).
	return gatewayprovider.ApplyGrokFreeToolCacheRoute(body, intentSourceBody, provider, cacheIdentity, allowPureClientTools, allowPureClientTools)
}

// isGrokClaudeDesktopResponsesCacheRequest identifies Claude Desktop requests converted by CC Switch.
// All fingerprint signals must match to enable mixed platform and client tool routing.
func isGrokClaudeDesktopResponsesCacheRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil || IsOpenAIResponsesCompactPath(c) {
		return false
	}
	path := strings.TrimRight(strings.TrimSpace(c.Request.URL.Path), "/")
	if !strings.HasSuffix(path, "/responses") {
		return false
	}

	if !clientmeta.NewClaudeCodeValidator().ValidateUserAgent(strings.TrimSpace(c.GetHeader("User-Agent"))) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.GetHeader("X-App"))) {
	case "cli", "cli-bg":
	default:
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(c.GetHeader("anthropic-client-platform")), "desktop_app") {
		return false
	}
	return strings.TrimSpace(c.GetHeader("X-Claude-Code-Session-Id")) != ""
}

// grokCacheInput reads HTTP identity fields and supplies seed callbacks for the platform-selected identity strategy.
func grokCacheInput(c *gin.Context, explicitKey, model string) grok.CacheIdentityInput {
	input := grok.CacheIdentityInput{APIKeyID: APIKeyIDFromContext(c), Compact: IsOpenAIResponsesCompactPath(c), Model: model, ExplicitKey: explicitKey, StablePrefixSeed: gatewaysession.OpenAIStablePrefixSeed, AnchoredSeed: gatewaysession.OpenAIAnchoredContentSeed, PreviousResponseSeed: gatewaysession.GrokPreviousResponseSeed}
	if c != nil {
		input.ClaudeSession = c.GetHeader(ClaudeCodeSessionHeader)
		input.HeaderSession = ExplicitOpenAIHeaderSessionID(c)
		input.ConversationID = c.GetHeader(GrokConversationIDHeader)
	}
	return input
}
