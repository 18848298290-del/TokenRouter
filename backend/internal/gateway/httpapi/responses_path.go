package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// responses_path.go extracts and validates operation suffixes from OpenAI
// Responses request paths.
// Responses aliases can append upstream operations such as compact or
// input_tokens. The raw suffix is extracted from the matched HTTP path, then
// checked by the shared upstream validator before route admission or URL
// construction accepts it.

// OpenAIResponsesRequestPathSuffix returns a validated Responses suffix or an
// empty string on failure. Because valid root paths also yield empty, callers
// use IsForwardableOpenAIResponsesRequestPath to decide whether forwarding is
// permitted.
func OpenAIResponsesRequestPathSuffix(c *gin.Context) string {
	suffix, ok := upstream.SanitizedUpstreamPathSuffix(RawOpenAIResponsesRequestPathSuffix(c))
	// Invalid suffixes contribute no upstream path text; forwarding gates use the
	// separate boolean validator to distinguish this rejection from root requests.
	if !ok {
		return ""
	}
	return suffix
}

// IsForwardableOpenAIResponsesRequestPath validates the extracted suffix before
// upstream scheduling or URL construction. Empty suffixes are accepted by the
// shared validator, so root and unrelated paths require route matching to
// establish the Responses entry.
func IsForwardableOpenAIResponsesRequestPath(c *gin.Context) bool {
	_, ok := upstream.SanitizedUpstreamPathSuffix(RawOpenAIResponsesRequestPathSuffix(c))
	return ok
}

// IsOpenAIResponsesInputTokensRequestPath reports whether the validated suffix
// selects input-token preflight.
func IsOpenAIResponsesInputTokensRequestPath(c *gin.Context) bool {
	return OpenAIResponsesRequestPathSuffix(c) == "/input_tokens"
}

// RawOpenAIResponsesRequestPathSuffix extracts text following the last
// /responses marker after trimming trailing slashes. Missing request paths,
// unrelated paths and markers without a slash-prefixed continuation yield
// empty; callers validate any returned suffix before appending it upstream.
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
