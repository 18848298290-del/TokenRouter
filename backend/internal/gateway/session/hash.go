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

// GrokPreviousResponseSeed derives a sticky seed from previous_response_id.
// It accepts resp_* response IDs and returns empty for message IDs or unknown formats.
func GrokPreviousResponseSeed(body []byte) string {
	id := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	if id == "" {
		return ""
	}
	if protocolopenai.ClassifyOpenAIPreviousResponseIDKind(id) != protocolopenai.OpenAIPreviousResponseIDKindResponseID {
		return ""
	}
	// Namespace the seed separately from raw response IDs.
	return "grok-prev-resp:" + id
}

// GrokStickyAffinitySeed combines the model and session ID for sticky routing.
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

// GenerateSessionHash derives a sticky session ID from a parsed request.
func GenerateSessionHash(parsed *requeststate.ParsedRequest, observe func(string, ...any)) string {
	if observe == nil {
		observe = func(string, ...any) {}
	}
	if parsed == nil {
		return ""
	}

	// A session ID in metadata.user_id takes precedence over content-derived identity.
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

	// Prefer content marked with ephemeral cache_control over the full transcript.
	cacheableContent := ExtractCacheableContent(parsed)
	if cacheableContent != "" {
		hash := HashContent(cacheableContent)
		observe("sticky.hash_source",
			"source", "cacheable_content",
			"hash", hash,
		)
		return hash
	}

	// Otherwise hash the source, system text, and message text or Responses input anchor.
	var combined strings.Builder
	// Include the request source to isolate identical content from different callers.
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

func ExtractCacheableContent(parsed *requeststate.ParsedRequest) string {
	if parsed == nil {
		return ""
	}

	systemText := ExtractCacheableTextFromSystemRaw(parsed.SystemRaw())
	if messageText := ExtractCacheableTextFromMessagesRaw(parsed.MessagesRaw()); messageText != "" {
		return messageText
	}
	return systemText
}

func ParseRawJSONView(raw []byte) gjson.Result { return wirejson.ParseView(raw) }

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

func HashContent(content string) string {
	h := xxhash.Sum64String(content)
	return strconv.FormatUint(h, 36)
}

// MessagesMetadataSession selects a sticky ID from the Claude session, existing hash, or metadata.user_id.
func MessagesMetadataSession(claudeSessionID, sessionHash, promptCacheKey, reqModel string, body []byte) (string, string) {
	// Anthropic metadata.user_id and X-Claude-Code-Session-Id provide local provider affinity.
	// ForwardAsAnthropic derives upstream GPT/Codex prompt_cache_key and session_id from cache_control or message digests,
	// allowing upstream cache keys to advance with conversation content.
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

// currentSessionHash derives the scheduler's current session hash.
func currentSessionHash(seed string) string {
	current, _ := scheduler.DeriveSessionHashes(seed)
	return current
}
