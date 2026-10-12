package gemini

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// CountGeminiInlineImageOutputs counts inline image parts in a Gemini JSON response.
// It accepts both inlineData and inline_data field names used by REST, SDKs,
// and compatible relays.
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

func GeminiPartIsInlineImage(part gjson.Result) bool {
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

	// Require nonempty base64 data so empty image parts do not count toward billing.
	return strings.TrimSpace(inline.Get("data").String()) != ""
}
