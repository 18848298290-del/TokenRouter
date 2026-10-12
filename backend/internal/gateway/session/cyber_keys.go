package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// A Cyber block can name an explicit session or a transcript continuation.
// Transcript keys include API Key identity and cumulative visible history, while
// a source scope combines the key, client IP, and normalized User-Agent to limit
// when content-based block lookup is enabled. This lets a caller retain blocking
// across edited latest input while separating unrelated credential and source identities.
//
// The full transcript key is the final retained prefix. A second candidate names
// context before the latest user turn when earlier items look model-generated;
// that heuristic also accepts client-supplied assistant examples. Lookup receives
// bounded cumulative prefixes from the transcript implementation, and CyberBlocks
// uses its separate truncation flag before trusting scoped matching.

// CyberSessionTranscriptBlockKeys returns the full transcript key and, when
// available, the context preceding the latest user turn. Missing usable input
// returns nil. The preceding-context key requires earlier model-labelled history
// so a shared user-only setup does not become a block for another first question.
//
// The full key is the newest cumulative prefix, including when the bounded lookup
// list rotated. A distinct pre-user key is appended for edited latest input;
// classification is heuristic and accepts supplied assistant/model labels rather
// than authenticated generation evidence. CyberBlocks separately gates continuation
// lookup on the request's active source scope.
func CyberSessionTranscriptBlockKeys(apiKeyID int64, body []byte) []string {
	derived := deriveOpenAICyberTranscriptBlockKeys(apiKeyID, body)
	if len(derived.lookupKeys) == 0 {
		return nil
	}
	keys := []string{derived.lookupKeys[len(derived.lookupKeys)-1]}
	if derived.preLatestUserKey != "" && derived.preLatestUserKey != keys[0] {
		keys = append(keys, derived.preLatestUserKey)
	}
	return keys
}

// CyberSessionTranscriptLookupKeys returns cumulative transcript prefixes in
// oldest-to-newest retained order. The list keeps at most 256 newest prefixes;
// CyberBlocks uses the internal truncation flag to reject overflow in an active scope.
func CyberSessionTranscriptLookupKeys(apiKeyID int64, body []byte) []string {
	return deriveOpenAICyberTranscriptBlockKeys(apiKeyID, body).lookupKeys
}

// CyberSessionScopeKey hashes an API Key, trimmed IP, and normalized User-Agent
// into a source scope. Nonpositive API Key IDs return empty. User-Agent
// normalization removes supported version noise, allowing the same client product
// to retain scope across version changes.
//
// A scope enables transcript checking after previous blocks from that source;
// the exact transcript keys decide ordinary matches. The IP remains supplied
// text after trimming, so callers provide their resolved address consistently.
// The scope's labelled seed and hash do not authenticate the caller or prove
// that any one transcript is blocked.
func CyberSessionScopeKey(apiKeyID int64, clientIP, userAgent string) string {
	if apiKeyID <= 0 {
		return ""
	}
	raw := "cyber-scope:v1|api_key=" + strconv.FormatInt(apiKeyID, 10) +
		"|ip=" + strings.TrimSpace(clientIP) +
		"|ua=" + requeststate.NormalizeSessionUserAgent(userAgent)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// HashCyberSessionBlockKey hashes an API Key-isolated explicit session value.
// Exactly empty input returns empty. IsolateSessionID trims other input before
// hashing; whitespace-only input therefore produces the SHA-256 digest of an
// empty isolated value.
func HashCyberSessionBlockKey(apiKeyID int64, raw string) string {
	if raw == "" {
		return ""
	}
	isolated := upstream.IsolateSessionID(apiKeyID, raw)
	sum := sha256.Sum256([]byte(isolated))
	return hex.EncodeToString(sum[:])
}
