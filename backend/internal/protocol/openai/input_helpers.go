package openai

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// input_helpers.go classifies Responses input items and image signals and
// normalizes compatibility seed JSON.
// Responses input cleanup removes call_id from message, reasoning, and image
// output items that do not participate in tool-call pairing. Image detection and
// empty-data checks recognize specific request shapes, while seed normalization
// supplies decoded JSON text to compatibility cache and transcript keys.
//
// These decisions have different input rules: most type predicates trim their
// labels, while the Codex tool predicate compares exact strings. Seed rebuilding
// uses ordinary JSON decoding, so equivalent object layouts can share a seed but
// numeric spelling and precision can change. Invalid seed JSON remains available
// verbatim to callers; the normalization result is not proof of request validity.

// ShouldStripNonPairCallID reports whether this label selects call_id removal by
// Responses input cleanup. Surrounding whitespace is ignored; message, reasoning,
// and image_generation_call match, while pairing call/output labels remain false.
func ShouldStripNonPairCallID(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "message", "reasoning", "image_generation_call":
		return true
	default:
		return false
	}
}

// IsCompactionItemType recognizes the compaction labels used by local replay
// cleanup and forwarding. Surrounding whitespace is ignored, and both compaction
// and compaction_summary match; this classification does not validate their fields.
func IsCompactionItemType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

// JSONValueMayContainImageInput reports image evidence in arrays and nested
// content objects. An input_image type or any existing image_url key qualifies,
// including null or empty image_url values; other object fields are not traversed.
// Callers validate payloads separately because gjson string forms and key presence
// do not establish a usable image or a complete valid request.
func JSONValueMayContainImageInput(value gjson.Result) bool {
	if !value.Exists() {
		return false
	}
	if value.IsArray() {
		found := false
		value.ForEach(func(_, item gjson.Result) bool {
			if JSONValueMayContainImageInput(item) {
				found = true
				return false
			}
			return true
		})
		return found
	}
	if value.IsObject() {
		if strings.TrimSpace(value.Get("type").String()) == "input_image" || value.Get("image_url").Exists() {
			return true
		}
		return JSONValueMayContainImageInput(value.Get("content"))
	}
	return false
}

// IsEmptyBase64DataURI recognizes a literal data: URI whose first semicolon is
// followed by base64, and an empty or whitespace-only payload. Prefix matching is
// case sensitive and the supplied URI is not trimmed; MIME, URL encoding, and
// base64 validity are outside this check.
func IsEmptyBase64DataURI(raw string) bool {
	if !strings.HasPrefix(raw, "data:") {
		return false
	}
	rest := strings.TrimPrefix(raw, "data:")
	semicolonIdx := strings.Index(rest, ";")
	if semicolonIdx < 0 {
		return false
	}
	rest = rest[semicolonIdx+1:]
	if !strings.HasPrefix(rest, "base64,") {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(rest, "base64,")) == ""
}

// NormalizeCompatSeedJSON decodes and rebuilds JSON text for compatibility seeds,
// returning empty text for an absent value. Successful rebuilding removes layout
// whitespace and uses the standard encoder's object-key ordering and escaping, so
// consumers can hash equivalent object layouts consistently.
//
// Ordinary decoding stores numbers as float64; large integer values and numeric
// spellings can change before encoding. Decode or encode failure returns the
// supplied text verbatim, which may be invalid JSON. Callers use this result for
// seed comparison, not lossless forwarding or validation of upstream request data.
func NormalizeCompatSeedJSON(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var tmp any
	// Float64 decoding can round 9007199254740993 to 9007199254740992 before
	// seed hashing. Malformed text still contributes as supplied, keeping
	// distinct invalid values from collapsing to an empty seed.
	if err := json.Unmarshal(v, &tmp); err != nil {
		return string(v)
	}
	out, err := json.Marshal(tmp)
	// If rebuilding fails, use the supplied representation for the seed rather
	// than partial output; normalization exposes no error channel to its key callers.
	if err != nil {
		return string(v)
	}
	return string(out)
}

// IsCodexToolCallItemType recognizes the call and output labels used by Codex
// pairing and continuation logic. Matching is exact and includes the listed
// function, shell, search, custom, and MCP forms; callers trim labels when needed.
func IsCodexToolCallItemType(typ string) bool {
	switch typ {
	case "function_call",
		"tool_call",
		"local_shell_call",
		"tool_search_call",
		"custom_tool_call",
		"mcp_tool_call",
		"function_call_output",
		"mcp_tool_call_output",
		"custom_tool_call_output",
		"tool_search_output":
		return true
	default:
		return false
	}
}
