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

// grok_cache_identity.go collects Grok cache identity inputs and resolves
// client-tool cache opt-in precedence.
// Grok prompt-cache routing depends on the caller's session signal, API Key
// and final upstream model. The HTTP adapter collects the available headers
// and supplies content-seed callbacks to platform identity resolution, which
// returns an opaque isolated value or disables caching when required identity
// is absent.
//
// Free OAuth function-tool requests also combine provider policy, a local
// request header and a Claude Desktop fingerprint. Header presence determines
// whether the current or legacy name is read, and recognized request values
// override provider settings. Fingerprint activation applies only when the
// provider policy is absent and the request has not opted out; platform
// routing still checks the provider's Free eligibility and available tool
// intent.

// These local request headers select Grok client-tool cache routing; the current
// TokenRouter name takes precedence over the retained legacy integration name.
const (
	grokClientToolCacheOptInHeader       = "X-TokenRouter-Grok-Client-Tool-Cache" // current opt-in control
	legacyGrokClientToolCacheOptInHeader = "X-Sub2API-Grok-Client-Tool-Cache"     // legacy opt-in control
)

// ExtractClaudeCodeSessionID reads a trimmed Claude session header before
// payload metadata. Nil HTTP context permits payload-only extraction, and an
// absent identity returns an empty string for the caller's other seed choices.
func ExtractClaudeCodeSessionID(c *gin.Context, body []byte) string {
	if c != nil {
		if seed := strings.TrimSpace(c.GetHeader(ClaudeCodeSessionHeader)); seed != "" {
			return seed
		}
	}
	// Payload metadata supplies a session seed for clients that omit identity
	// headers; recover it here before callers choose their other content-based seeds.
	return grok.ExtractClaudeCodeSessionIDFromPayload(body)
}

// ResolveGrokCacheIdentity delegates isolated prompt-cache identity
// construction using HTTP signals and the supplied upstream model. A missing
// positive API Key, compact request, empty model or absent content/session
// seed returns an empty identity.
func ResolveGrokCacheIdentity(c *gin.Context, body []byte, explicitKey, upstreamModel string) string {
	return grok.ResolveCacheIdentity(grokCacheInput(c, explicitKey, upstreamModel), body)
}

// ApplyGrokFreeRequestToolCacheRoute combines provider cache policy with
// recognized local request overrides for Free OAuth tools. Callers retain tool
// intent before filtering and provide a request when the Gin context is
// nonnil. The current header's presence suppresses the legacy header,
// including when its value is empty.
//
// Recognized opt-in can override provider opt-out; opt-out prevents automatic
// Claude Desktop fingerprint activation. The delegated platform rule checks
// Free eligibility, cache identity and tool intent. A tool_choice=none intent
// selects a separate branch that can append native search tools even when
// opt-in flags are false. That branch retains the current body's choice;
// callers retain none there to keep tools disabled.
func ApplyGrokFreeRequestToolCacheRoute(c *gin.Context, body, intentSourceBody []byte, provider *gatewayprovider.ExecutionProvider, cacheIdentity string) ([]byte, error) {
	allowPureClientTools, providerPolicyExplicit := gatewayprovider.GrokClientToolCacheProviderPolicy(provider)
	requestOptOut := false
	if c != nil {
		// Header presence, including an empty current value, prevents consulting the
		// legacy name so older integration settings cannot override this request's control.
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
	// Automatic desktop routing fills an absent provider decision only after
	// request opt-out is ruled out, preserving the precedence of configured controls.
	if !allowPureClientTools && !providerPolicyExplicit && !requestOptOut && isGrokClaudeDesktopResponsesCacheRequest(c) {
		allowPureClientTools = true
	}
	// Forward the selected policy flags beside retained tool intent. The platform
	// still handles tool_choice=none separately by appending disabled native search tools.
	return gatewayprovider.ApplyGrokFreeToolCacheRoute(body, intentSourceBody, provider, cacheIdentity, allowPureClientTools, allowPureClientTools)
}

// isGrokClaudeDesktopResponsesCacheRequest reports whether path and client
// headers match the Claude Desktop Responses fingerprint. Compact paths,
// missing request state or any absent/mismatched signal return false; all
// qualifying signals are required.
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

// grokCacheInput assembles HTTP identities and the platform's content-seed
// callbacks for cache resolution. A nil HTTP context omits header signals; the
// supplied explicit key and final model remain available to identity
// precedence.
func grokCacheInput(c *gin.Context, explicitKey, model string) grok.CacheIdentityInput {
	input := grok.CacheIdentityInput{APIKeyID: APIKeyIDFromContext(c), Compact: IsOpenAIResponsesCompactPath(c), Model: model, ExplicitKey: explicitKey, StablePrefixSeed: gatewaysession.OpenAIStablePrefixSeed, AnchoredSeed: gatewaysession.OpenAIAnchoredContentSeed, PreviousResponseSeed: gatewaysession.GrokPreviousResponseSeed}
	if c != nil {
		input.ClaudeSession = c.GetHeader(ClaudeCodeSessionHeader)
		input.HeaderSession = ExplicitOpenAIHeaderSessionID(c)
		input.ConversationID = c.GetHeader(GrokConversationIDHeader)
	}
	return input
}
