package session

import (
	"net/textproto"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// QoderExplicitSessionSeed checks each header's first value in order, then session fields in the body.
func QoderExplicitSessionSeed(headers map[string][]string, body []byte) string {
	for _, name := range []string{"session_id", "conversation_id", "x-session-id", "x-conversation-id", "X-Claude-Code-Session-Id"} {
		values := headers[textproto.CanonicalMIMEHeaderKey(name)]
		if len(values) > 0 {
			if value := strings.TrimSpace(values[0]); value != "" {
				return value
			}
		}
	}
	for _, path := range []string{"session_id", "conversation_id", "previous_response_id", "prompt_cache_key"} {
		if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); value != "" {
			return value
		}
	}
	return ""
}

// QoderHashFromSeed prefixes the seed with qoder: before deriving the scheduler hash.
func QoderHashFromSeed(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	hash, _ := scheduler.DeriveSessionHashes("qoder:" + seed)
	return hash
}

// QoderRequestHash prefers an explicit session seed, then derives identity from message content and source.
func QoderRequestHash(headers map[string][]string, body []byte, wire string, identity *requeststate.SessionContext, observe func(string, ...any)) string {
	if seed := QoderExplicitSessionSeed(headers, body); seed != "" {
		return QoderHashFromSeed(seed)
	}
	parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), wire)
	if err != nil {
		return ""
	}
	parsed.SessionContext = identity
	generated := GenerateSessionHash(parsed, observe)
	if generated == "" {
		return ""
	}
	return QoderHashFromSeed("fallback:" + generated)
}
