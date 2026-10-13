package gemini

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// Gemini image billing observes inline image parts across every candidate in
// one payload. Compatible camelCase and snake_case fields are recognized, and
// an image MIME label plus nonblank data defines the counted image
// observation and excludes empty image-shaped parts.

// CountGeminiInlineImageOutputs counts qualifying inline image parts across
// all candidates of one valid JSON payload. Empty or invalid JSON returns
// zero; it counts parts per payload, while stream accumulation policy belongs
// to callers.
func CountGeminiInlineImageOutputs(payload []byte) int {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return 0
	}
	count := 0
	gjson.GetBytes(payload, "candidates").ForEach(func(_, candidate gjson.Result) bool {
		candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			if GeminiPartIsInlineImage(part) {
				count++
			}
			return true
		})
		return true
	})
	return count
}

// GeminiPartIsInlineImage recognizes an inline-data part with an image MIME
// label and nonblank data text. Existing camelCase fields take precedence over
// snake_case alternatives; this predicate checks presence and MIME, leaving
// base64 decoding to image consumers.
func GeminiPartIsInlineImage(part gjson.Result) bool {
	// Prefer an existing camelCase field before the compatible snake_case
	// spelling, including when the preferred field is empty; that precedence
	// matches both lookups below.
	inline := part.Get("inlineData")
	if !inline.Exists() {
		inline = part.Get("inline_data")
	}
	if !inline.Exists() {
		return false
	}

	mimeType := inline.Get("mimeType")
	if !mimeType.Exists() {
		mimeType = inline.Get("mime_type")
	}
	if !bridge.NativeIsGeminiInlineImageMIMEType(strings.ToLower(strings.TrimSpace(mimeType.String()))) {
		return false
	}

	// A MIME label alone carries no observed image bytes. Require nonblank data
	// text before this part contributes to the caller image count.
	return strings.TrimSpace(inline.Get("data").String()) != ""
}
