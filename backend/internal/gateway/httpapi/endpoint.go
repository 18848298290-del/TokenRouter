package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	providererrors "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const (
	// Register inbound and upstream API paths here for endpoint normalization and derivation.

	EndpointMessages             = "/v1/messages"
	EndpointChatCompletions      = "/v1/chat/completions"
	EndpointSystemOne            = "/v1/systemone"
	EndpointEmbeddings           = "/v1/embeddings"
	EndpointAlphaSearch          = "/v1/alpha/search"
	EndpointResponses            = "/v1/responses"
	EndpointResponsesInputTokens = "/v1/responses/input_tokens"
	EndpointResponsesCompact     = "/v1/responses/compact"
	EndpointImagesGenerations    = "/v1/images/generations"
	EndpointImagesEdits          = "/v1/images/edits"
	EndpointVideosGenerations    = "/v1/videos/generations"
	EndpointVideosEdits          = "/v1/videos/edits"
	EndpointVideosExtensions     = "/v1/videos/extensions"
	EndpointVideos               = "/v1/videos"
	EndpointGeminiModels         = "/v1beta/models"

	// EndpointAntigravityGenerateContent is Antigravity's native streaming generation endpoint.
	EndpointAntigravityGenerateContent = "/v1internal:streamGenerateContent"

	// gin.Context keys used by the middleware and helpers below.
	ctxKeyInboundEndpoint        = "_gateway_inbound_endpoint"
	ctxKeyActualUpstreamEndpoint = "_gateway_actual_upstream_endpoint"
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
func isResponsesCompactAliasPath(path string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return false
	}
	return isBareOrSubpathOf(trimmed, "/responses/compact") || isBareOrSubpathOf(trimmed, "/backend-api/codex/responses/compact")
}

// isResponsesRootAliasPath matches /responses and /backend-api/codex/responses, including all subpaths.
// Matching starts at the path root, so unrelated prefixes such as /foo/responses are rejected.
func isResponsesRootAliasPath(path string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return false
	}
	return isBareOrSubpathOf(trimmed, "/responses") || isBareOrSubpathOf(trimmed, "/backend-api/codex/responses")
}

// isBareOrSubpathOf reports whether path equals root or lies below root.
// Root-anchored matching excludes identical names nested under unrelated prefixes.
func isBareOrSubpathOf(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

// DeriveUpstreamEndpoint derives an upstream endpoint from the provider platform and canonical inbound endpoint.
//
// OpenAI and Grok default to Responses and retain subpaths such as /compact.
// Grok Chat execution can override this estimate with its actual endpoint. Media,
// embeddings, and search keep their endpoints. Anthropic uses Messages; Gemini
// uses /v1beta/models. Antigravity chooses between them from the inbound endpoint.
func DeriveUpstreamEndpoint(inbound, rawRequestPath, platform string) string {
	inbound = strings.TrimSpace(inbound)

	switch platform {
	case capability.PlatformOpenAI, capability.PlatformGrok:
		if inbound == EndpointSystemOne || inbound == EndpointEmbeddings || inbound == EndpointAlphaSearch || inbound == EndpointResponsesInputTokens || inbound == EndpointImagesGenerations || inbound == EndpointImagesEdits || inbound == EndpointVideosGenerations || inbound == EndpointVideosEdits || inbound == EndpointVideosExtensions || inbound == EndpointVideos {
			return inbound
		}
		// Other OpenAI and Grok requests use Responses.
		// Retain subresource suffixes such as /compact or /compact/detail.
		if suffix := responsesSubpathSuffix(rawRequestPath); suffix != "" {
			return EndpointResponses + suffix
		}
		// When the raw path has no suffix, retain a canonical Compact inbound endpoint
		// so it is not misreported as the Responses root.
		if inbound == EndpointResponsesCompact {
			return EndpointResponsesCompact
		}
		return EndpointResponses

	case capability.PlatformAnthropic:
		return EndpointMessages

	case capability.PlatformGemini:
		return EndpointGeminiModels

	case capability.PlatformAntigravity:
		// Antigravity supports both Claude and Gemini protocols.
		if inbound == EndpointGeminiModels {
			return EndpointGeminiModels
		}
		return EndpointMessages
	}

	// Use the inbound endpoint for unknown platforms.
	return inbound
}

// responsesSubpathSuffix extracts the part after "/responses" in a raw
// request path, e.g. "/openai/v1/responses/compact" → "/compact".
// Returns "" when there is no meaningful suffix.
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
// Apply this middleware to all gateway route groups.
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
			// Expose the endpoint during authentication so configured group fallback can run before the handler.
			ctx := apikey.WithInboundEndpoint(c.Request.Context(), normalized)
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	}
}

// GetInboundEndpoint returns the canonical endpoint saved by middleware.
// If absent, it normalizes the request URL before trying FullPath, preserving compact identity on wildcard routes.
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
func SetActualUpstreamEndpoint(c *gin.Context, endpoint string) {
	if c != nil {
		c.Set(ctxKeyActualUpstreamEndpoint, strings.TrimSpace(endpoint))
	}
}
