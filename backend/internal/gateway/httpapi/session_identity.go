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

const (
	OpenCodeSessionAffinityHeader = "X-Session-Affinity"
	OpenCodeSessionIDHeader       = "X-Session-Id"
	OpenCodeNativeSessionHeader   = "X-OpenCode-Session"
	CodeBuddyConversationHeader   = "X-Conversation-ID"

	GrokConversationIDHeader = "X-Grok-Conv-Id"
	ClaudeCodeSessionHeader  = "X-Claude-Code-Session-Id"
)

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

// WithOpenAIGuardianParentAffinity extracts a review parent from headers and body and hashes it for scheduling affinity.
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

// ExplicitOpenAIHeaderSessionID returns a stable session ID from supported client headers.
// Per-turn request and message IDs would disrupt sticky routing and upstream prompt caching.
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

func ExplicitOpenAISessionID(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}

	sessionID := ExplicitOpenAIHeaderSessionID(c)
	if sessionID == "" && len(body) > 0 {
		// WS response.create nests session fields under response; unwrap it before reading the cache key.
		sessionID = strings.TrimSpace(protocolopenai.RequestPayloadView(body).Get("prompt_cache_key").String())
	}
	return sessionID
}

// ExplicitOpenAIRequestSessionID reads client session fields, including Grok-specific signals when applicable.
func ExplicitOpenAIRequestSessionID(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}

	sessionID := ExplicitOpenAIHeaderSessionID(c)
	if sessionID == "" && IsGrokRequestContext(c) {
		sessionID = strings.TrimSpace(c.GetHeader(GrokConversationIDHeader))
	}
	if sessionID == "" && len(body) > 0 {
		sessionID = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	if sessionID == "" && IsGrokRequestContext(c) && len(body) > 0 {
		sessionID = gatewaysession.GrokPreviousResponseSeed(body)
	}
	return sessionID
}

// GenerateExplicitOpenAISessionHash hashes explicit client session fields, returning empty when they are absent.
func GenerateExplicitOpenAISessionHash(c *gin.Context, body []byte) string {
	sessionID := ExplicitOpenAIRequestSessionID(c, body)
	if sessionID == "" {
		return ""
	}

	currentHash, legacyHash := scheduler.DeriveSessionHashes(sessionID)
	AttachOpenAILegacySessionHash(c, legacyHash)
	return currentHash
}

// GenerateOpenAISessionHash creates a sticky-session hash using the explicit session
// signals resolved by ExplicitOpenAIRequestSessionID, then the request's content seed.
// Grok requests additionally qualify the seed through GrokStickyAffinitySeed.
func GenerateOpenAISessionHash(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}

	sessionID := ExplicitOpenAIRequestSessionID(c, body)
	if sessionID == "" && len(body) > 0 {
		sessionID = gatewaysession.OpenAIContentSeed(body)
	}
	if sessionID == "" {
		return ""
	}

	if IsGrokRequestContext(c) {
		sessionID = gatewaysession.GrokStickyAffinitySeed(sessionID, body)
	}

	currentHash, legacyHash := scheduler.DeriveSessionHashes(sessionID)
	AttachOpenAILegacySessionHash(c, legacyHash)
	return currentHash
}

// GenerateOpenAISessionHashWithFallback uses normal session and content signals first.
// When those yield no hash, it hashes a nonempty fallbackSeed.
// WS ingress uses this seed to retain provider affinity across requests.
func GenerateOpenAISessionHashWithFallback(c *gin.Context, body []byte, fallbackSeed string) string {
	sessionHash := GenerateOpenAISessionHash(c, body)
	if sessionHash != "" {
		return sessionHash
	}

	seed := strings.TrimSpace(fallbackSeed)
	if seed == "" {
		return ""
	}

	currentHash, legacyHash := scheduler.DeriveSessionHashes(seed)
	AttachOpenAILegacySessionHash(c, legacyHash)
	return currentHash
}

// ClaudeCodeSessionIDFromHeader sanitizes the Claude Code session header for Messages sticky routing.
// Usage logging reads its session ID through ExtractClientSessionID.
func ClaudeCodeSessionIDFromHeader(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return gatewaysession.SanitizeClientSessionID(c.GetHeader(ClaudeCodeSessionHeader))
}

func ExtractClientSessionID(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return gatewaysession.ExtractClientSessionID(c.GetHeader, clientSessionIDHeaders, IsGrokRequestContext(c))
}

// IsGrokRequestContext checks the selected provider, then a forced platform, then the Grok session header.
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

func AttachOpenAILegacySessionHash(c *gin.Context, legacyHash string) {
	if c == nil || c.Request == nil {
		return
	}
	c.Request = c.Request.WithContext(requeststate.WithOpenAILegacySessionHash(c.Request.Context(), legacyHash))
}
