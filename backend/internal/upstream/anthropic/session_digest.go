package anthropic

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Digest-based session cache constants.
const (
	// AnthropicSessionTTLSeconds is the five-minute session cache TTL.
	AnthropicSessionTTLSeconds = 300

	// AnthropicDigestSessionKeyPrefix identifies digest-derived session keys.
	AnthropicDigestSessionKeyPrefix = "anthropic:digest:"
)

// AnthropicSessionTTL returns the session cache TTL.
func AnthropicSessionTTL() time.Duration {
	return AnthropicSessionTTLSeconds * time.Second
}

// BuildAnthropicDigestChain hashes system and message content in order.
// Entries use s:<hash>, u:<hash>, or a:<hash>, joined with hyphens.
// Appending messages extends the chain while retaining the existing prefix.
func BuildAnthropicDigestChain(systemRaw, messages []byte) string {
	var parts []string

	if len(systemRaw) > 0 && string(systemRaw) != "null" {
		parts = append(parts, "s:"+upstream.ShortHash(CanonicalAnthropicDigestJSON(systemRaw)))
	}

	if len(messages) > 0 {
		gjson.ParseBytes(messages).ForEach(func(_, msg gjson.Result) bool {
			prefix := RolePrefix(msg.Get("role").String())
			content := msg.Get("content")
			parts = append(parts, prefix+":"+upstream.ShortHash(CanonicalAnthropicDigestJSON([]byte(content.Raw))))
			return true
		})
	}

	return strings.Join(parts, "-")
}

// CanonicalAnthropicDigestJSON removes JSON key-order and whitespace differences.
// Invalid JSON is returned unchanged.
func CanonicalAnthropicDigestJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return canonical
}

// RolePrefix maps assistant to "a" and all other roles to "u".
func RolePrefix(role string) string {
	switch role {
	case "assistant":
		return "a"
	default:
		return "u"
	}
}

// GenerateAnthropicDigestSessionKey combines up to eight bytes each from the
// prefix hash and UUID with the digest session-key prefix.
func GenerateAnthropicDigestSessionKey(prefixHash, uuid string) string {
	prefix := prefixHash
	if len(prefixHash) >= 8 {
		prefix = prefixHash[:8]
	}
	uuidPart := uuid
	if len(uuid) >= 8 {
		uuidPart = uuid[:8]
	}
	return AnthropicDigestSessionKeyPrefix + prefix + ":" + uuidPart
}
