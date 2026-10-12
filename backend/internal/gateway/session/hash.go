package session

import (
	"strconv"
	"strings"

	"github.com/cespare/xxhash/v2"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Parsed requests can identify a session through Anthropic metadata, an ephemeral
// cache marker, or visible text. Those sources have different scope: metadata
// returns its parsed session ID, marked content hashes alone, and the text path
// includes client-source fields when SessionContext is present. A supplied context
// can therefore produce a hash even when there is no message text.
//
// Messages and Gemini fallback hashing includes all extracted message text, so a
// longer transcript can change the hash. Responses uses leading instruction text
// and the first user or input-text anchor when no message text was appended,
// keeping later turns out of that seed. Text is concatenated without delimiters
// between content pieces, and roles or nontext media do not enter this fallback.
// HTTP callers apply their own explicit-session and platform-specific priority rules.

// GrokPreviousResponseSeed labels a recognized previous_response_id for Grok routing.
// Blank input, message IDs, and unknown formats return empty. The classifier
// requires the exact resp_ prefix and its bounded ASCII suffix; the trimmed ID
// is retained in the labelled seed.
func GrokPreviousResponseSeed(body []byte) string {
	id := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	if id == "" {
		return ""
	}
	if protocolopenai.ClassifyOpenAIPreviousResponseIDKind(id) != protocolopenai.OpenAIPreviousResponseIDKindResponseID {
		return ""
	}
	// Label recognized response IDs so the Grok previous-response path can
	// remain distinct from an explicit session whose text is the same raw ID.
	return "grok-prev-resp:" + id
}

// GrokStickyAffinitySeed combines a trimmed session ID with the body's normalized
// model for HTTP affinity. Blank session text returns empty; absent model uses
// the versioned session label alone, and model text is trimmed and lowercased.
func GrokStickyAffinitySeed(sessionID string, body []byte) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	model := ""
	if len(body) > 0 {
		model = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "model").String()))
	}
	if model == "" {
		return "grok-affinity:v1:" + sessionID
	}
	return "grok-affinity:v1:" + model + ":" + sessionID
}

// GenerateSessionHash returns the strongest available sticky identity from a
// parsed request. Nil input returns empty, and a nil observer suppresses its
// synchronous diagnostics. A recognized metadata.user_id session is returned
// before content hashing; invalid metadata lets the next evidence source proceed.
//
// Nonempty ephemeral content is hashed without SessionContext. Otherwise the seed
// contains supplied client IP, normalized User-Agent, API Key ID, system text, and
// message text. Responses input contributes an anchor only when message extraction
// added no text. A present SessionContext still contributes separators and a key
// ID when its other fields are empty, distinguishing it from a nil context.
//
// The fallback concatenates text pieces without encoding roles or nontext items.
// Messages growth can change its hash; Responses anchoring stops at the first
// user or input-text item after leading instructions. This value supplies affinity
// evidence, and callers apply tenant or platform isolation appropriate to their
// routing path. Keep parsed request bytes stable while these views are read.
func GenerateSessionHash(parsed *requeststate.ParsedRequest, observe func(string, ...any)) string {
	if observe == nil {
		observe = func(string, ...any) {}
	}
	if parsed == nil {
		return ""
	}

	// Recognized metadata identifies the declared conversation before content
	// and client-source hashing, allowing message growth to retain that session ID.
	if parsed.MetadataUserID != "" {
		uid := anthropic.ParseMetadataUserID(parsed.MetadataUserID)
		if uid != nil && uid.SessionID != "" {
			observe("sticky.hash_source",
				"source", "metadata_user_id",
				"session_id", uid.SessionID,
				"device_id", uid.DeviceID,
				"is_new_format", uid.IsNewFormat,
			)
			return uid.SessionID
		}
		observe("sticky.hash_metadata_parse_failed",
			"metadata_user_id", parsed.MetadataUserID,
			"parsed_nil", uid == nil,
		)
	}

	// A selected nonempty ephemeral anchor defines cache affinity independently
	// of request source, so shared marked content wins before client-context hashing.
	cacheableContent := ExtractCacheableContent(parsed)
	if cacheableContent != "" {
		hash := HashContent(cacheableContent)
		observe("sticky.hash_source",
			"source", "cacheable_content",
			"hash", hash,
		)
		return hash
	}

	// With no declared session or marked text, combine source and readable
	// content evidence; the fallback still permits a source-only seed when context exists.
	var combined strings.Builder
	// A present context contributes source identity even with empty text fields,
	// so identical unmarked content can differ by client source or API Key.
	if parsed.SessionContext != nil {
		_, _ = combined.WriteString(parsed.SessionContext.ClientIP)
		_, _ = combined.WriteString(":")
		_, _ = combined.WriteString(requeststate.NormalizeSessionUserAgent(parsed.SessionContext.UserAgent))
		_, _ = combined.WriteString(":")
		_, _ = combined.WriteString(strconv.FormatInt(parsed.SessionContext.APIKeyID, 10))
		_, _ = combined.WriteString("|")
	}
	if systemText := ExtractTextFromSystemRaw(parsed.SystemRaw()); systemText != "" {
		_, _ = combined.WriteString(systemText)
	}
	contentStart := combined.Len()
	AppendMessageTextsFromRaw(&combined, parsed.MessagesRaw())
	// Messages text takes precedence; use the Responses anchor only when that
	// extraction added nothing, retaining one selected content representation in the fallback.
	if combined.Len() == contentStart {
		AppendResponsesSessionAnchorFromRaw(&combined, parsed.InputRaw())
	}
	if combined.Len() > 0 {
		hash := HashContent(combined.String())
		observe("sticky.hash_source",
			"source", "message_content_fallback",
			"hash", hash,
			"content_len", combined.Len(),
		)
		return hash
	}

	return ""
}

// ExtractCacheableContent returns nonempty text from the first message containing
// an ephemeral marker, otherwise concatenated marked system text. Nil requests
// return empty. The chosen message contributes all its text blocks, including
// unmarked text alongside the marker.
//
// Message selection stops at the first marked message even when its extracted
// text is empty. In that case system text can win, but later marked messages are
// not tried. This priority supports GenerateSessionHash's cache-content path,
// which hashes this selected content before considering client-source identity.
func ExtractCacheableContent(parsed *requeststate.ParsedRequest) string {
	if parsed == nil {
		return ""
	}

	systemText := ExtractCacheableTextFromSystemRaw(parsed.SystemRaw())
	if messageText := ExtractCacheableTextFromMessagesRaw(parsed.MessagesRaw()); messageText != "" {
		return messageText
	}
	// An empty first marked message supplies no text; use marked system text
	// rather than searching later messages that were outside the selected cache anchor.
	return systemText
}

// ParseRawJSONView returns a gjson view over the supplied raw byte storage.
// Empty input yields an empty view; callers keep bytes unchanged while the view
// or derived strings are used, and validate JSON separately.
func ParseRawJSONView(raw []byte) gjson.Result { return wirejson.ParseView(raw) }

// ExtractTextFromSystemRaw reads a system string or concatenates array text fields.
// Other representations return empty. Array parts need no particular type label;
// text fields use gjson string conversion and are joined without separators.
func ExtractTextFromSystemRaw(raw []byte) string {
	system := ParseRawJSONView(raw)
	switch system.Type {
	case gjson.String:
		return system.String()
	case gjson.JSON:
		if !system.IsArray() {
			return ""
		}
		var builder strings.Builder
		system.ForEach(func(_, part gjson.Result) bool {
			if text := part.Get("text").String(); text != "" {
				_, _ = builder.WriteString(text)
			}
			return true
		})
		return builder.String()
	}
	return ""
}

// ExtractTextFromContentRaw reads string content or joins exact text-typed array
// blocks. Other shapes and other block types return empty or contribute nothing;
// accepted text fields use gjson conversion and no separators are inserted.
func ExtractTextFromContentRaw(content gjson.Result) string {
	switch content.Type {
	case gjson.String:
		return content.String()
	case gjson.JSON:
		if !content.IsArray() {
			return ""
		}
		var builder strings.Builder
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "text" {
				if text := part.Get("text").String(); text != "" {
					_, _ = builder.WriteString(text)
				}
			}
			return true
		})
		return builder.String()
	}
	return ""
}

// AppendMessageTextsFromRaw appends readable message text in supplied order.
// Nil builders, empty bytes, and non-array views are ignored. An existing content
// field takes precedence over Gemini parts, even when it yields no text; roles
// are omitted from this concatenated fallback seed.
func AppendMessageTextsFromRaw(builder *strings.Builder, raw []byte) {
	if builder == nil || len(raw) == 0 {
		return
	}
	messages := ParseRawJSONView(raw)
	if !messages.IsArray() {
		return
	}
	messages.ForEach(func(_, msg gjson.Result) bool {
		if content := msg.Get("content"); content.Exists() {
			_, _ = builder.WriteString(ExtractTextFromContentRaw(content))
			return true
		}
		// Gemini stores content in parts. Use those text fields when content is
		// absent so the shared fallback can read that protocol's parsed message range.
		parts := msg.Get("parts")
		if parts.IsArray() {
			parts.ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text").String(); text != "" {
					_, _ = builder.WriteString(text)
				}
				return true
			})
		}
		return true
	})
}

// AppendResponsesSessionAnchorFromRaw appends instruction text and the first
// Responses user anchor to a caller's builder. Nil builders, empty bytes, and
// unusable views are ignored. String input appends directly, while array input
// accepts string items, exact role labels, and exact input_text item labels.
//
// System and developer content before the stopping anchor contributes text. The
// first string, user, or input_text item stops traversal even when it supplies
// empty text, so later history does not change this selected prefix. Other items
// are skipped. Content pieces are concatenated without delimiters; callers keep
// this affinity representation distinct from full transcript identity.
func AppendResponsesSessionAnchorFromRaw(builder *strings.Builder, raw []byte) {
	if builder == nil || len(raw) == 0 {
		return
	}
	input := ParseRawJSONView(raw)
	if input.Type == gjson.String {
		_, _ = builder.WriteString(input.String())
		return
	}
	if !input.IsArray() {
		return
	}

	// Stop at the initial user-like item after leading instructions so appended
	// response history keeps using the same chosen conversation anchor on later turns.
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Type == gjson.String {
			_, _ = builder.WriteString(item.String())
			return false
		}

		switch item.Get("role").String() {
		case "system", "developer":
			AppendResponsesContentText(builder, item.Get("content"))
		case "user":
			AppendResponsesContentText(builder, item.Get("content"))
			return false
		default:
			if item.Get("type").String() == "input_text" {
				if text := item.Get("text").String(); text != "" {
					_, _ = builder.WriteString(text)
				}
				return false
			}
		}
		return true
	})
}

// AppendResponsesContentText appends string content or text/input_text array
// blocks to the supplied builder. Nil builders and absent content are ignored;
// other block labels contribute nothing, and accepted text is joined without separators.
func AppendResponsesContentText(builder *strings.Builder, content gjson.Result) {
	if builder == nil || !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		_, _ = builder.WriteString(content.String())
		return
	}
	if !content.IsArray() {
		return
	}
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "input_text", "text":
			if text := part.Get("text").String(); text != "" {
				_, _ = builder.WriteString(text)
			}
		}
		return true
	})
}

// ExtractCacheableTextFromSystemRaw joins text from system-array parts carrying
// an exact ephemeral cache-control label. String systems and other representations
// return empty; marked part text uses gjson conversion and is joined without separators.
func ExtractCacheableTextFromSystemRaw(raw []byte) string {
	system := ParseRawJSONView(raw)
	if !system.IsArray() {
		return ""
	}
	var builder strings.Builder
	system.ForEach(func(_, part gjson.Result) bool {
		if part.Get("cache_control.type").String() == "ephemeral" {
			if text := part.Get("text").String(); text != "" {
				_, _ = builder.WriteString(text)
			}
		}
		return true
	})
	return builder.String()
}

// ExtractCacheableTextFromMessagesRaw returns text from the first message whose
// content array contains an exact ephemeral marker. Non-array history or no marked
// message returns empty. All text blocks of the selected message contribute,
// including those without a marker.
//
// The first marker determines the selected message even if it is attached to a
// nontext block. ExtractTextFromContentRaw may then produce empty, and later marked
// messages are not examined. GenerateSessionHash's caller can use marked system
// text after this empty result, so selection and text availability remain separate
// decisions that affect the chosen cache identity.
func ExtractCacheableTextFromMessagesRaw(raw []byte) string {
	messages := ParseRawJSONView(raw)
	if !messages.IsArray() {
		return ""
	}
	var text string
	messages.ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		found := false
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("cache_control.type").String() == "ephemeral" {
				found = true
				return false
			}
			return true
		})
		if found {
			text = ExtractTextFromContentRaw(content)
			return false
		}
		return true
	})
	return text
}

// HashContent returns the unsigned XXHash64 value encoded in Base36 text.
func HashContent(content string) string {
	h := xxhash.Sum64String(content)
	return strconv.FormatUint(h, 36)
}

// MessagesMetadataSession returns a sticky hash and the supplied prompt cache key.
// A nonempty Claude session wins when promptCacheKey is exactly empty; otherwise
// an existing sessionHash wins before metadata.user_id is considered. Inputs are
// used as supplied except the metadata value, which is trimmed.
//
// When no earlier hash wins, model text and raw metadata.user_id form the
// scheduler seed. The metadata need not parse as a Claude session here. This
// helper leaves promptCacheKey unchanged because upstream cache derivation happens
// in the execution adapter; local provider affinity and the upstream prompt key
// can therefore advance according to different evidence.
func MessagesMetadataSession(claudeSessionID, sessionHash, promptCacheKey, reqModel string, body []byte) (string, string) {
	// A Claude session is selected here when no prompt key was already supplied.
	// Upstream cache derivation stays with the execution adapter, keeping local
	// affinity and cache input separate.
	if promptCacheKey == "" {
		if claudeSessionID != "" {
			return currentSessionHash(claudeSessionID), promptCacheKey
		}
	}
	if sessionHash != "" {
		return sessionHash, promptCacheKey
	}
	if userID := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String()); userID != "" {
		seed := reqModel + "-" + userID
		sessionHash = currentSessionHash(seed)
	}
	return sessionHash, promptCacheKey
}

// currentSessionHash returns the scheduler's current hash for the supplied seed.
// The scheduler trims input and returns empty for blank text; this helper discards
// the legacy hash because MessagesMetadataSession consumes the current value.
func currentSessionHash(seed string) string {
	current, _ := scheduler.DeriveSessionHashes(seed)
	return current
}
