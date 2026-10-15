package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// ws_session_headers.go resolves WebSocket session and conversation
// identifiers while retaining the source of each selected value.
// Codex WebSocket clients use both hyphenated and legacy session headers, and
// may also send a conversation header or prompt cache key. A nonblank dedicated
// session value determines connection affinity before either fallback is
// considered. The conversation header still supplies its own conversation
// identity when another session signal wins.
//
// The selected source label travels with each ID so ingress diagnostics can
// identify the input that was used. When dedicated session headers are absent,
// conversation identity fills both values; the prompt cache key fills only a
// remaining session gap. Missing values retain none source labels, keeping
// absence distinguishable from a selected header.

// OpenAIWSSessionHeaderResolution holds the selected IDs and their input sources.
// A source of "none" means no nonempty value was found for that ID.
type OpenAIWSSessionHeaderResolution struct {
	// SessionID, ConversationID, SessionSource and ConversationSource pair each
	// selected value with its input label. A dedicated session header can win
	// while the independent conversation header and its source remain available.
	SessionID          string // selected session identity
	ConversationID     string // conversation header identity
	SessionSource      string // selected session source label
	ConversationSource string // conversation source label
}

// ResolveOpenAIWSSessionHeaders returns session and conversation IDs together
// with the source selected for each value. Trimmed session-id precedes
// session_id, then conversation_id and promptCacheKey; a conversation header
// retains its independent ID even when a dedicated session header wins.
//
// Missing signals keep empty IDs and none source labels. Conversation identity
// can also fill session affinity when dedicated session headers are absent, so
// one header may supply both IDs without implying that the two fields are
// interchangeable.
func ResolveOpenAIWSSessionHeaders(c *gin.Context, promptCacheKey string) OpenAIWSSessionHeaderResolution {
	resolution := OpenAIWSSessionHeaderResolution{
		SessionSource:      "none",
		ConversationSource: "none",
	}
	if c != nil && c.Request != nil {
		if sessionID := strings.TrimSpace(c.Request.Header.Get("session-id")); sessionID != "" {
			resolution.SessionID = sessionID
			resolution.SessionSource = "header_session-id"
		} else if sessionID := strings.TrimSpace(c.Request.Header.Get("session_id")); sessionID != "" {
			resolution.SessionID = sessionID
			resolution.SessionSource = "header_session_id"
		}
		if conversationID := strings.TrimSpace(c.Request.Header.Get("conversation_id")); conversationID != "" {
			resolution.ConversationID = conversationID
			resolution.ConversationSource = "header_conversation_id"
			// Use conversation identity as session affinity only when the dedicated
			// session headers were empty; its separate conversation value remains
			// available.
			if resolution.SessionID == "" {
				resolution.SessionID = conversationID
				resolution.SessionSource = "header_conversation_id"
			}
		}
	}

	cacheKey := strings.TrimSpace(promptCacheKey)
	if cacheKey != "" {
		// Use the supplied prompt cache key after all nonblank header signals,
		// retaining the source label needed for connection diagnostics.
		if resolution.SessionID == "" {
			resolution.SessionID = cacheKey
			resolution.SessionSource = "prompt_cache_key"
		}
	}
	return resolution
}
