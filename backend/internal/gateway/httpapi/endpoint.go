package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	providererrors "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// endpoint.go normalizes inbound endpoint labels and resolves the upstream
// endpoint reported for each request.
// Endpoint observations distinguish a client route from the protocol path
// eventually used by a selected provider. Canonical inbound labels collapse
// aliases and resource details, but retain Compact and input-token identities
// that would be lost if every Responses path were labeled as its root.
//
// Upstream inference combines that inbound label with provider platform and the
// raw Responses suffix. Explicit runtime endpoint records take precedence when
// execution chose another protocol, including failures that return no result.
// Middleware uses the actual request URL before Gin's wildcard template and
// publishes the normalized label for authentication and later Ops consumers.

// Canonical endpoint values label inbound protocol families and inferred upstream
// paths, allowing request observations to use the same spelling across aliases.
const (
	// Protocol labels below distinguish generation, auxiliary counts, media operations,
	// and model families while normalization applies each path family's matching rules.

	EndpointMessages             = "/v1/messages"               // Messages inference endpoint
	EndpointChatCompletions      = "/v1/chat/completions"       // Chat inference endpoint
	EndpointSystemOne            = "/v1/systemone"              // SystemOne decision endpoint
	EndpointEmbeddings           = "/v1/embeddings"             // vector embedding endpoint
	EndpointAlphaSearch          = "/v1/alpha/search"           // Alpha search endpoint
	EndpointResponses            = "/v1/responses"              // Responses inference endpoint
	EndpointResponsesInputTokens = "/v1/responses/input_tokens" // Responses token preflight
	EndpointResponsesCompact     = "/v1/responses/compact"      // legacy compaction endpoint
	EndpointImagesGenerations    = "/v1/images/generations"     // image generation endpoint
	EndpointImagesEdits          = "/v1/images/edits"           // image editing endpoint
	EndpointVideosGenerations    = "/v1/videos/generations"     // video generation endpoint
	EndpointVideosEdits          = "/v1/videos/edits"           // video editing endpoint
	EndpointVideosExtensions     = "/v1/videos/extensions"      // video extension endpoint
	EndpointVideos               = "/v1/videos"                 // video resource operations
	EndpointGeminiModels         = "/v1beta/models"             // Gemini model family

	// EndpointAntigravityGenerateContent is Antigravity's native streaming generation endpoint.
	EndpointAntigravityGenerateContent = "/v1internal:streamGenerateContent"

	// Endpoint context keys retain the canonical inbound observation and a chosen
	// upstream attempt path; a nonempty actual path overrides platform-based inference.
	ctxKeyInboundEndpoint        = "_gateway_inbound_endpoint"         // normalized client endpoint
	ctxKeyActualUpstreamEndpoint = "_gateway_actual_upstream_endpoint" // actual attempt endpoint
)

// NormalizeInboundEndpoint maps a raw request path (which may carry
// prefixes like /antigravity, /openai) to its canonical form.
//
//	"/antigravity/v1/messages"   → "/v1/messages"
//	"/v1/chat/completions"       → "/v1/chat/completions"
//	"/openai/v1/responses/foo"   → "/v1/responses"
//	"/v1beta/models/gemini:gen"  → "/v1beta/models"
//
// Responses also has bare and Codex aliases. Their /compact paths and subpaths
// normalize to EndpointResponsesCompact so logs distinguish compact requests
// from the Responses root. Input-token endpoints likewise have their own constant.
// Other Responses subpaths normalize to the root endpoint.
// Examples:
//
//	"/v1/responses/compact"                         → EndpointResponsesCompact
//	"/v1/responses/compact/detail"                  → EndpointResponsesCompact
//	"/openai/v1/responses/compact"                  → EndpointResponsesCompact
//	"/openai/v1/responses/compact/detail"           → EndpointResponsesCompact
//	"/responses/compact"                            → EndpointResponsesCompact
//	"/responses/compact/detail"                     → EndpointResponsesCompact
//	"/backend-api/codex/responses/compact"          → EndpointResponsesCompact
//	"/backend-api/codex/responses/compact/detail"   → EndpointResponsesCompact
//	"/v1/responses"                                 → EndpointResponses
//	"/openai/v1/responses"                          → EndpointResponses
//	"/responses"                                    → EndpointResponses
//	"/backend-api/codex/responses"                  → EndpointResponses
//
// Check compact paths before Responses roots because their prefixes overlap.
// Unknown results return path after surrounding whitespace is trimmed. Versioned
// families use the branch's substring or suffix tests, while bare Responses
// aliases use root-anchored helpers; this labels observations rather than validating a routed request.
func NormalizeInboundEndpoint(path string) string {
	path = strings.TrimSpace(path)
	switch {
	case strings.HasSuffix(path, EndpointSystemOne):
		return EndpointSystemOne
	case strings.Contains(path, EndpointEmbeddings):
		return EndpointEmbeddings
	case strings.Contains(path, EndpointAlphaSearch) || isBareOrSubpathOf(strings.TrimRight(path, "/"), "/alpha/search") || isBareOrSubpathOf(strings.TrimRight(path, "/"), "/backend-api/codex/alpha/search"):
		return EndpointAlphaSearch
	case strings.Contains(path, EndpointChatCompletions):
		return EndpointChatCompletions
	case strings.Contains(path, EndpointMessages):
		return EndpointMessages
	case strings.Contains(path, EndpointImagesGenerations) || strings.Contains(path, "/images/generations"):
		return EndpointImagesGenerations
	case strings.Contains(path, EndpointImagesEdits) || strings.Contains(path, "/images/edits"):
		return EndpointImagesEdits
	case strings.Contains(path, EndpointVideosGenerations) || strings.Contains(path, "/videos/generations"):
		return EndpointVideosGenerations
	case strings.Contains(path, EndpointVideosEdits) || strings.Contains(path, "/videos/edits"):
		return EndpointVideosEdits
	case strings.Contains(path, EndpointVideosExtensions) || strings.Contains(path, "/videos/extensions"):
		return EndpointVideosExtensions
	case strings.Contains(path, EndpointVideos) || strings.Contains(path, "/videos/"):
		return EndpointVideos
	case strings.Contains(path, EndpointResponsesInputTokens) || isResponsesInputTokensAliasPath(path):
		return EndpointResponsesInputTokens
	case strings.Contains(path, EndpointResponsesCompact) || isResponsesCompactAliasPath(path):
		return EndpointResponsesCompact
	case strings.Contains(path, EndpointResponses) || isResponsesRootAliasPath(path):
		return EndpointResponses
	case strings.Contains(path, EndpointGeminiModels):
		return EndpointGeminiModels
	default:
		return path
	}
}

// isResponsesInputTokensAliasPath matches bare and Codex input-token paths or descendants.
// It trims whitespace and trailing slashes before root-anchored comparison.
// Matching precedes Responses-root classification so auxiliary counting keeps its
// own observation label on those aliases.
func isResponsesInputTokensAliasPath(path string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return false
	}
	return isBareOrSubpathOf(trimmed, "/responses/input_tokens") ||
		isBareOrSubpathOf(trimmed, "/backend-api/codex/responses/input_tokens")
}

// isResponsesCompactAliasPath matches bare and Codex compact aliases, including subpaths.
// Call it before isResponsesRootAliasPath, which also matches compact subpaths.
// Surrounding whitespace and trailing slashes are trimmed, while unknown prefixes
// remain outside this alias match even when they contain a Responses suffix.
func isResponsesCompactAliasPath(path string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return false
	}
	return isBareOrSubpathOf(trimmed, "/responses/compact") || isBareOrSubpathOf(trimmed, "/backend-api/codex/responses/compact")
}

// isResponsesRootAliasPath matches /responses and /backend-api/codex/responses, including all subpaths.
// Matching starts at the path root, so unrelated prefixes such as /foo/responses are rejected.
// It trims surrounding whitespace and trailing slashes before comparing the supported roots.
func isResponsesRootAliasPath(path string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return false
	}
	return isBareOrSubpathOf(trimmed, "/responses") || isBareOrSubpathOf(trimmed, "/backend-api/codex/responses")
}

// isBareOrSubpathOf reports whether path equals root or lies below root.
// Root-anchored matching excludes identical names nested under unrelated prefixes.
// Inputs are compared as supplied; callers pass normalized paths and nonempty
// roots when they need the alias matching used by endpoint observation.
func isBareOrSubpathOf(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

// DeriveUpstreamEndpoint derives an upstream endpoint from the provider platform and canonical inbound endpoint.
//
// OpenAI and Grok default to Responses and retain subpaths such as /compact.
// Grok Chat execution can override this estimate with its actual endpoint. Media,
// embeddings, and search keep their endpoints. Anthropic uses Messages; Gemini
// uses /v1beta/models. Antigravity chooses between them from the inbound endpoint.
//
// For Responses families, a raw suffix preserves resource detail that canonical
// inbound labeling intentionally collapsed. When no raw suffix exists, a Compact
// inbound label still retains compaction identity. Unknown platforms return the
// trimmed inbound label. This is an observation estimate; execution can publish
// its actual protocol through the runtime override read by GetUpstreamEndpoint.
func DeriveUpstreamEndpoint(inbound, rawRequestPath, platform string) string {
	inbound = strings.TrimSpace(inbound)

	switch platform {
	case capability.PlatformOpenAI, capability.PlatformGrok:
		if inbound == EndpointSystemOne || inbound == EndpointEmbeddings || inbound == EndpointAlphaSearch || inbound == EndpointResponsesInputTokens || inbound == EndpointImagesGenerations || inbound == EndpointImagesEdits || inbound == EndpointVideosGenerations || inbound == EndpointVideosEdits || inbound == EndpointVideosExtensions || inbound == EndpointVideos {
			return inbound
		}
		// Other OpenAI and Grok requests default to the Responses observation.
		// Retain subresource suffixes such as /compact or /compact/detail so the
		// inferred observation keeps the selected resource after inbound labeling collapsed its detail.
		if suffix := responsesSubpathSuffix(rawRequestPath); suffix != "" {
			return EndpointResponses + suffix
		}
		// When the raw path has no suffix, retain a canonical Compact inbound endpoint
		// so upstream observations retain compaction identity even when no matching raw
		// Responses suffix is available from the caller's request path.
		if inbound == EndpointResponsesCompact {
			return EndpointResponsesCompact
		}
		return EndpointResponses

	case capability.PlatformAnthropic:
		return EndpointMessages

	case capability.PlatformGemini:
		return EndpointGeminiModels

	case capability.PlatformAntigravity:
		// The Gemini inbound family selects Antigravity's model-path estimate.
		// Other families use Messages until execution publishes the actual upstream
		// endpoint, allowing inference before a provider result is available.
		if inbound == EndpointGeminiModels {
			return EndpointGeminiModels
		}
		return EndpointMessages
	}

	// An unknown platform supplies no protocol-specific inference. Keep its inbound
	// label as the observation available until execution records a more specific endpoint.
	return inbound
}

// responsesSubpathSuffix extracts the part after "/responses" in a raw
// request path, e.g. "/openai/v1/responses/compact" → "/compact".
// Returns "" when there is no meaningful suffix. Extraction trims surrounding
// whitespace and trailing slashes and uses the last Responses fragment; callers
// validate forwarding paths elsewhere before using any suffix for an actual request.
func responsesSubpathSuffix(rawPath string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(rawPath), "/")
	_, after, ok := strings.CutLast(trimmed, "/responses")
	if !ok {
		return ""
	}
	suffix := after
	if suffix == "" || suffix == "/" {
		return ""
	}
	if !strings.HasPrefix(suffix, "/") {
		return ""
	}
	return suffix
}

// InboundEndpointMiddleware normalizes the request path and stores the
// canonical inbound endpoint in gin.Context so that every handler in
// the chain can read it via GetInboundEndpoint.
//
// Apply this middleware before authentication on gateway route groups. The
// request URL takes precedence over FullPath so wildcard templates do not erase
// Compact identity; missing URL path uses the matched route template instead.
func InboundEndpointMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := ""
		if c.Request != nil && c.Request.URL != nil {
			path = c.Request.URL.Path
		}
		if path == "" {
			path = c.FullPath()
		}
		normalized := NormalizeInboundEndpoint(path)
		c.Set(ctxKeyInboundEndpoint, normalized)
		if c.Request != nil {
			// Authentication can resolve a configured group fallback before protocol handling.
			// Publish the same normalized label in typed request context for that earlier decision.
			ctx := apikey.WithInboundEndpoint(c.Request.Context(), normalized)
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	}
}

// GetInboundEndpoint returns the canonical endpoint saved by middleware.
// If absent, it normalizes the request URL before trying FullPath, preserving compact identity on wildcard routes.
// Callers supply a nonnil context because stored-value lookup occurs before
// fallback path checks. Empty or mistyped stored values select on-demand normalization.
func GetInboundEndpoint(c *gin.Context) string {
	if v, ok := c.Get(ctxKeyInboundEndpoint); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}

	path := ""
	if c != nil {
		if c.Request != nil && c.Request.URL != nil {
			path = c.Request.URL.Path
		}
		if path == "" {
			path = c.FullPath()
		}
	}
	return NormalizeInboundEndpoint(path)
}

// GetUpstreamEndpoint returns the actual endpoint when recorded, otherwise deriving it
// from the inbound request and selected provider platform.
// Callers provide a nonnil HTTP context for the fallback lookup. OpenAI, Grok,
// and CN providers first consult the OpenAI runtime override, then the generic
// attempt override; other platforms consult the generic override directly.
func GetUpstreamEndpoint(c *gin.Context, platform string) string {
	// Runtime endpoint state takes precedence over inbound-path inference.
	// A force_chat_completions failure may have no ForwardResult, so this state is
	// needed to avoid reporting the inbound Responses path as the upstream endpoint.
	if platform == capability.PlatformOpenAI || platform == capability.PlatformGrok || providererrors.IsCNProvider(platform) {
		if endpoint := GetActualOpenAIUpstreamEndpoint(c); endpoint != "" {
			return endpoint
		}
	}
	if c != nil {
		if value, ok := c.Get(ctxKeyActualUpstreamEndpoint); ok {
			if endpoint, ok := value.(string); ok && endpoint != "" {
				return endpoint
			}
		}
	}
	inbound := GetInboundEndpoint(c)
	rawPath := ""
	if c != nil && c.Request != nil && c.Request.URL != nil {
		rawPath = c.Request.URL.Path
	}
	return DeriveUpstreamEndpoint(inbound, rawPath, platform)
}

// SetActualUpstreamEndpoint records the upstream endpoint used by this attempt.
// A nil context is ignored. Values are trimmed, and an empty value leaves
// future readers using inference rather than a usable explicit override.
func SetActualUpstreamEndpoint(c *gin.Context, endpoint string) {
	if c != nil {
		c.Set(ctxKeyActualUpstreamEndpoint, strings.TrimSpace(endpoint))
	}
}
