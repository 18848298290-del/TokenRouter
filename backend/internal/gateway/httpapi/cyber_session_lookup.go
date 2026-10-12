package httpapi

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// A cyber session may be identified by a client header or prompt-cache key,
// while transcript matching depends on source scope and runtime settings.
// HTTP adapters pass explicit identity as a callback so the session core can
// skip its parsing when blocking is disabled or storage is absent.

// CyberSessionExplicitBlockKey hashes explicit request identity within one API key.
// Missing context or identity produces an empty key through the session helper.
// ExplicitOpenAISessionID supplies header precedence and nested WS body handling;
// the session hash helper owns the encoded block-key representation.
func CyberSessionExplicitBlockKey(id int64, c *gin.Context, body []byte) string {
	return session.HashCyberSessionBlockKey(id, ExplicitOpenAISessionID(c, body))
}

// FindCyberSessionForRequest delegates scoped lookup with lazy explicit-session resolution.
// The core owns runtime checks, storage failures, and bounded transcript behavior.
// Nil or disabled cores return an empty result before invoking explicit identity,
// and callers treat a nonempty key as the block decision.
func FindCyberSessionForRequest(ctx context.Context, core *session.CyberBlocks, id int64, c *gin.Context, body []byte, ip, agent string) string {
	return core.Find(ctx, session.CyberLookup{APIKeyID: id, Body: body, ClientIP: ip, UserAgent: agent, ExplicitKey: func() string { return CyberSessionExplicitBlockKey(id, c, body) }})
}

// FindBlockedCyberSession reads client IP and User-Agent for session-block lookup.
// A nil core returns empty immediately, and nil HTTP context supplies empty
// source values. The core decides whether those values permit scoped transcript
// lookup or an explicit-session match.
func FindBlockedCyberSession(ctx context.Context, core *session.CyberBlocks, id int64, c *gin.Context, body []byte) string {
	if core == nil {
		return ""
	}
	ip, agent := "", ""
	if c != nil {
		ip = strings.TrimSpace(clientip.GetClientIP(c))
		agent = c.GetHeader("User-Agent")
	}
	return FindCyberSessionForRequest(ctx, core, id, c, body, ip, agent)
}
