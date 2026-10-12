package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// OpenAIResponsesRequestPathSuffix extracts and validates a Responses path suffix.
func OpenAIResponsesRequestPathSuffix(c *gin.Context) string {
	suffix, ok := upstream.SanitizedUpstreamPathSuffix(RawOpenAIResponsesRequestPathSuffix(c))
	if !ok {
		return ""
	}
	return suffix
}

// IsForwardableOpenAIResponsesRequestPath reports whether a Responses subpath can be forwarded.
// Routes use it after authentication and before scheduling to reject malformed subpaths.
func IsForwardableOpenAIResponsesRequestPath(c *gin.Context) bool {
	_, ok := upstream.SanitizedUpstreamPathSuffix(RawOpenAIResponsesRequestPathSuffix(c))
	return ok
}

// IsOpenAIResponsesInputTokensRequestPath reports whether the path targets Responses input-token preflight.
func IsOpenAIResponsesInputTokensRequestPath(c *gin.Context) bool {
	return OpenAIResponsesRequestPathSuffix(c) == "/input_tokens"
}

// RawOpenAIResponsesRequestPathSuffix extracts a suffix; callers must validate it before forwarding.
func RawOpenAIResponsesRequestPathSuffix(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return ""
	}
	normalizedPath := strings.TrimRight(strings.TrimSpace(c.Request.URL.Path), "/")
	if normalizedPath == "" {
		return ""
	}
	_, after, ok := strings.CutLast(normalizedPath, "/responses")
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
