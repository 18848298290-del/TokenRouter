package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// CyberSessionTranscriptBlockKeys returns the full-request key and an optional key preceding the latest user turn.
// The preceding-context key requires model-generated history and tolerates edits to the latest user input.
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

func CyberSessionTranscriptLookupKeys(apiKeyID int64, body []byte) []string {
	return deriveOpenAICyberTranscriptBlockKeys(apiKeyID, body).lookupKeys
}

// CyberSessionScopeKey identifies sources with prior blocks. An active scope triggers transcript lookup;
// content keys determine whether the request is blocked.
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

func HashCyberSessionBlockKey(apiKeyID int64, raw string) string {
	if raw == "" {
		return ""
	}
	isolated := upstream.IsolateSessionID(apiKeyID, raw)
	sum := sha256.Sum256([]byte(isolated))
	return hex.EncodeToString(sum[:])
}
