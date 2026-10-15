package httpapi

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	gatewaysession "github.com/TokenFlux/TokenRouter/internal/gateway/session"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// session_identity.go derives provider-affinity hashes and extracts validated
// client session identifiers from HTTP requests.
// Provider affinity and persisted client session correlation use different
// input rules. Affinity can use explicit headers, a prompt cache key or
// selected request content, while persistence reads validated session headers.
// Selected or forced platform evidence determines whether Grok-specific
// conversation and model qualification participate.
//
// The current scheduling hash and legacy compatibility hash are derived
// together from one final seed. Explicit-only media callers skip content
// fallback and Grok model qualification, while ordinary text hashing can use
// both. Review-parent affinity binds separately to validated lineage, keeping a
// parent conversation signal distinct from per-turn request identifiers and
// unrelated client correlation metadata.

// These header names identify supported conversation signals for routing and
// persisted session correlation; their ordered reader lists select precedence.
const (
	OpenCodeSessionAffinityHeader = "X-Session-Affinity"
	OpenCodeSessionIDHeader       = "X-Session-Id"
	OpenCodeNativeSessionHeader   = "X-OpenCode-Session"
	CodeBuddyConversationHeader   = "X-Conversation-ID"

	GrokConversationIDHeader = "X-Grok-Conv-Id"
	ClaudeCodeSessionHeader  = "X-Claude-Code-Session-Id"
)

// These lists order common affinity headers before the extra Claude persistence
// signal. Grok-specific header fallback is selected separately by request context.
var (
	explicitOpenAIHeaderSessionNames = []string{
		"session-id",
		"session_id",
		"conversation_id",
		OpenCodeSessionAffinityHeader,
		OpenCodeSessionIDHeader,
		OpenCodeNativeSessionHeader,
		CodeBuddyConversationHeader,
	}

	clientSessionIDHeaders = append(append([]string(nil), explicitOpenAIHeaderSessionNames...), ClaudeCodeSessionHeader)
)

// WithOpenAIGuardianParentAffinity binds current and legacy hashes for an
// unambiguous Codex review parent. Nil context, missing Gin context or
// unrelated models retain the supplied context. A live request is required when
// lineage headers are read; validated header/body evidence supplies the parent
// seed.
func WithOpenAIGuardianParentAffinity(ctx context.Context, c *gin.Context, body []byte, model string) context.Context {
	if ctx == nil || c == nil || !clientmeta.IsCodexReviewModel(model) {
		return ctx
	}
	parent := clientmeta.CodexReviewParent(clientmeta.CodexReviewInput{Model: model, Body: body, Subagent: c.GetHeader(clientmeta.OpenAISubagentHeader), ParentThreadID: c.GetHeader(clientmeta.CodexParentThreadIDHeader), TurnMetadata: c.GetHeader(clientmeta.CodexTurnMetadataHeader)})
	if parent == "" {
		return ctx
	}
	current, legacy := scheduler.DeriveSessionHashes(parent)
	if current == "" {
		return ctx
	}
	return requeststate.WithGuardianParentAffinity(ctx, requeststate.GuardianParentAffinity{CurrentSessionHash: current, LegacySessionHash: legacy})
}

// ExplicitOpenAIHeaderSessionID returns the first nonblank trimmed supported
// session header in its configured order. Nil context returns empty; a nonnil
// context needs a request. Per-request correlation headers are excluded so
// changing turn IDs do not change this explicit affinity signal.
func ExplicitOpenAIHeaderSessionID(c *gin.Context) string {
	if c == nil {
		return ""
	}

	for _, header := range explicitOpenAIHeaderSessionNames {
		if sessionID := strings.TrimSpace(c.GetHeader(header)); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

// ExplicitOpenAISessionID prefers supported session headers, then a prompt
// cache key from the OpenAI payload view. Nil context returns empty and other
// contexts need a request; the view supports a nested response.create envelope
// before reading its key.
func ExplicitOpenAISessionID(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}

	sessionID := ExplicitOpenAIHeaderSessionID(c)
	if sessionID == "" && len(body) > 0 {
		// A nested response.create payload keeps its prompt cache key under response;
		// use the shared payload view before applying this header-free fallback.
		sessionID = strings.TrimSpace(protocolopenai.RequestPayloadView(body).Get("prompt_cache_key").String())
	}
	return sessionID
}

// ExplicitOpenAIRequestSessionID resolves supported headers before
// protocol-specific request signals. A nonnil context needs its request; nil
// returns empty. Grok context permits its conversation header after standard
// headers, then the root prompt cache key, then a recognized previous-response
// seed.
func ExplicitOpenAIRequestSessionID(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}

	sessionID := ExplicitOpenAIHeaderSessionID(c)
	// After common session headers, permit the Grok conversation header only when
	// actual or forced platform evidence has not identified another execution
	// platform.
	if sessionID == "" && IsGrokRequestContext(c) {
		sessionID = strings.TrimSpace(c.GetHeader(GrokConversationIDHeader))
	}
	if sessionID == "" && len(body) > 0 {
		sessionID = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	// A recognized previous response can supply Grok affinity after explicit cache
	// input is absent; its labelled seed separates continuation from a literal
	// session header.
	if sessionID == "" && IsGrokRequestContext(c) && len(body) > 0 {
		sessionID = gatewaysession.GrokPreviousResponseSeed(body)
	}
	return sessionID
}

// GenerateExplicitOpenAISessionHash hashes the resolved explicit signal and
// attaches its legacy hash to request context. Missing signals return empty,
// and content is not used as fallback. The explicit seed is not model-qualified
// by Grok affinity handling here.
func GenerateExplicitOpenAISessionHash(c *gin.Context, body []byte) string {
	sessionID := ExplicitOpenAIRequestSessionID(c, body)
	if sessionID == "" {
		return ""
	}

	currentHash, legacyHash := scheduler.DeriveSessionHashes(sessionID)
	AttachOpenAILegacySessionHash(c, legacyHash)
	return currentHash
}

// GenerateOpenAISessionHash derives routing affinity from explicit signals or
// the shared content seed. Nil context or no usable seed returns empty; nonnil
// context requires its request.
//
// Grok context qualifies the chosen seed with normalized model identity before
// hashing, keeping its model-specific affinity distinct. Current and legacy
// hashes come from the same final seed, with the legacy value attached for
// compatibility lookup. Content fallback rules belong to OpenAIContentSeed, so
// only the content that helper selects can affect subsequent turn affinity.
func GenerateOpenAISessionHash(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}

	sessionID := ExplicitOpenAIRequestSessionID(c, body)
	// Use shared content anchoring after explicit input is absent; that helper
	// chooses stable leading content so unrelated later transcript additions can
	// stay outside the seed.
	if sessionID == "" && len(body) > 0 {
		sessionID = gatewaysession.OpenAIContentSeed(body)
	}
	if sessionID == "" {
		return ""
	}

	// Qualify the chosen Grok seed with its request model before deriving both
	// hashes, keeping different model affinity from sharing one plain session
	// seed.
	if IsGrokRequestContext(c) {
		sessionID = gatewaysession.GrokStickyAffinitySeed(sessionID, body)
	}

	currentHash, legacyHash := scheduler.DeriveSessionHashes(sessionID)
	AttachOpenAILegacySessionHash(c, legacyHash)
	return currentHash
}

// GenerateOpenAISessionHashWithFallback uses normal explicit/content resolution
// before a trimmed fallback seed. Blank fallback returns empty when normal
// resolution failed; a nonblank fallback can still produce a hash with nil Gin
// context, though legacy attachment then has no request to update.
func GenerateOpenAISessionHashWithFallback(c *gin.Context, body []byte, fallbackSeed string) string {
	sessionHash := GenerateOpenAISessionHash(c, body)
	if sessionHash != "" {
		return sessionHash
	}

	// Normal request evidence produced no affinity; derive the connection fallback
	// separately so an ingress identity remains available even with an empty
	// request body.
	seed := strings.TrimSpace(fallbackSeed)
	if seed == "" {
		return ""
	}

	currentHash, legacyHash := scheduler.DeriveSessionHashes(seed)
	AttachOpenAILegacySessionHash(c, legacyHash)
	return currentHash
}

// ClaudeCodeSessionIDFromHeader returns the sanitized Claude session signal for
// Messages routing. Missing context or request yields empty; shared validation
// rejects unusable correlation values, while usage logging resolves its own
// header priority through ExtractClientSessionID.
func ClaudeCodeSessionIDFromHeader(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return gatewaysession.SanitizeClientSessionID(c.GetHeader(ClaudeCodeSessionHeader))
}

// ExtractClientSessionID resolves a validated persistence ID from supported
// headers. Nil context or request returns empty; invalid earlier values allow
// later headers, and Grok adds its conversation header after the common list.
// Body cache keys and content seeds are excluded.
func ExtractClientSessionID(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return gatewaysession.ExtractClientSessionID(c.GetHeader, clientSessionIDHeaders, IsGrokRequestContext(c))
}

// IsGrokRequestContext resolves Grok classification from selected platform,
// then a nonblank forced platform, then its conversation header. Nil context
// returns false; a nonnil context needs a request when header fallback is
// reached. Selected labels other than the exact unknown sentinel take
// precedence over conflicting headers.
//
// Platform labels compare to grok as stored. The presence checks trim whitespace
// but do not normalize accepted labels, so producers supply canonical spelling;
// a nonblank padded or differently cased label suppresses header fallback and
// returns false here.
func IsGrokRequestContext(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if platform := selectedOpsPlatform(c); platform != "" && platform != "unknown" {
		return platform == capability.PlatformGrok
	}
	if c.Request != nil {
		if platform, ok := apikey.ForcePlatformFromContext(c.Request.Context()); ok && strings.TrimSpace(platform) != "" {
			return platform == capability.PlatformGrok
		}
	}
	return strings.TrimSpace(c.GetHeader(GrokConversationIDHeader)) != ""
}

// AttachOpenAILegacySessionHash stores the supplied legacy affinity value in
// request context. Missing contexts or requests skip the update, and blank
// values remain valid assignments so callers can replace an earlier
// compatibility hash.
func AttachOpenAILegacySessionHash(c *gin.Context, legacyHash string) {
	if c == nil || c.Request == nil {
		return
	}
	c.Request = c.Request.WithContext(requeststate.WithOpenAILegacySessionHash(c.Request.Context(), legacyHash))
}
