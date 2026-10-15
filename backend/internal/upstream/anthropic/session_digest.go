package anthropic

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// session_digest.go builds conversation digest chains and shortened Anthropic
// session cache keys.
// Conversation digests encode system content followed by each message's role
// and content hash. Per-message entries keep earlier content at the same chain
// positions when a later turn is appended, allowing callers to compare prefixes
// from unchanged conversation history.
//
// Canonical JSON decoding removes object key order and spacing before hashing,
// while decoding failures use the supplied bytes. This representation uses
// encoding/json's ordinary numeric decoding and hashes content rather than
// every message field. Session keys then shorten both a caller-supplied prefix
// hash and UUID; equal retained prefixes can therefore produce equal keys. The
// helpers describe this representation independently of the caller's cache
// lookup and identity decisions.

// Digest session constants select the textual cache-key namespace and the
// five-minute lifetime returned to callers that store conversation bindings.
const (
	// AnthropicSessionTTLSeconds is the five-minute session cache TTL.
	AnthropicSessionTTLSeconds = 300

	// AnthropicDigestSessionKeyPrefix identifies digest-derived session keys.
	AnthropicDigestSessionKeyPrefix = "anthropic:digest:"
)

// AnthropicSessionTTL returns the configured digest-cache lifetime as a
// duration.
func AnthropicSessionTTL() time.Duration {
	// time.Duration counts nanoseconds, so the stored seconds need this scale.
	return AnthropicSessionTTLSeconds * time.Second
}

// BuildAnthropicDigestChain returns ordered system and message content hashes
// with role prefixes. Nonempty nonliteral-null system bytes come first,
// assistant messages use a, and other roles use u; empty inputs can produce an
// empty chain.
//
// Each content value is canonicalized before hashing, so earlier entries retain
// their positions when unchanged turns gain later messages. Only role and
// content determine a message entry; other fields are outside that hash.
// Callers supply messages as a JSON array because this helper traverses gjson
// results without checking the root shape. They decide how the textual chain
// participates in cache lookup.
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

// CanonicalAnthropicDigestJSON decodes and re-encodes JSON to normalize spacing
// and object key order. Empty input or a decode/encode failure returns the
// supplied bytes; ordinary encoding/json decoding represents numbers as
// float64.
func CanonicalAnthropicDigestJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	var value any
	// Unusable JSON retains its supplied bytes as the hash input, so a decode
	// failure still contributes deterministic data to its conversation entry.
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	canonical, err := json.Marshal(value)
	// If re-encoding fails, keep the supplied representation for hashing rather
	// than replacing the content with an empty or partially constructed value.
	if err != nil {
		return raw
	}
	return canonical
}

// RolePrefix maps assistant to a and every other string, including unknown or
// empty roles, to u. Digest callers validate role input separately if
// additional categories need distinct prefixes.
func RolePrefix(role string) string {
	switch role {
	case "assistant":
		return "a"
	default:
		return "u"
	}
}

// GenerateAnthropicDigestSessionKey joins the digest namespace with up to eight
// bytes each from prefixHash and uuid. Shorter values remain intact; callers
// account for collisions when distinct inputs share the retained prefixes.
func GenerateAnthropicDigestSessionKey(prefixHash, uuid string) string {
	prefix := prefixHash
	// Equal hash prefixes share this component even when their later bytes differ.
	if len(prefixHash) >= 8 {
		prefix = prefixHash[:8]
	}
	uuidPart := uuid
	// UUID suffix bytes are discarded here, so distinct caller identities need
	// different retained prefixes to distinguish this component of a cache key.
	if len(uuid) >= 8 {
		uuidPart = uuid[:8]
	}
	return AnthropicDigestSessionKeyPrefix + prefix + ":" + uuidPart
}
