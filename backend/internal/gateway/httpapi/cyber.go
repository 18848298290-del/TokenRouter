package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

const (
	CyberBlockResponses CyberBlockFormat = iota
	CyberBlockChat
	CyberBlockAnthropic

	CyberPolicyRecordedKey = "ops_cyber_recorded"
)

type CyberBlockFormat int

type CyberBackend interface {
	Available() bool
	Enabled(context.Context) bool
	Find(context.Context, int64, *gin.Context, []byte) string
	Mark(*gin.Context) *moderationflow.Mark
	StopKeepalive(*gin.Context) bool
	MarkStream(*gin.Context, string, string, int)
	FailedSSE(*gin.Context, string, string) bool
	UpstreamEndpoint(*gin.Context, string) string
}
type CyberHandler struct {
	backend   CyberBackend
	moderator ModerationPort
	endpoints ModerationEndpoints
	runtime   moderationflow.Runtime
}

// CyberPolicyCall carries funding data already captured as an independent completion snapshot.
type CyberPolicyCall struct {
	Key            *apikey.APIKey
	Provider       *moderationflow.Provider
	Usage          *completion.Input
	Model          string
	ForwardErrored bool
	BlockKey       string
	Plan           moderationflow.BlockPlan
	HasPlan        bool
}

func NewCyberHandler(b CyberBackend, m ModerationPort, e ModerationEndpoints, r moderationflow.Runtime) *CyberHandler {
	return &CyberHandler{b, m, e, r}
}

func (h *CyberHandler) GroupInScope(c *gin.Context, key *apikey.APIKey) bool {
	if h == nil || h.moderator == nil || c == nil || c.Request == nil || key == nil {
		return false
	}
	scope, err := h.moderator.CyberSessionBlockGroupInScope(c.Request.Context(), key.GroupID)
	if err != nil {
		RequestLogger(c, "handler.openai_gateway.cyber_session_block").Warn("content_moderation.cyber_session_block_scope_check_failed", zap.Error(err))
		return false
	}
	return scope
}

func (h *CyberHandler) RejectSession(c *gin.Context, key *apikey.APIKey, body []byte, model string, format CyberBlockFormat) bool {
	if h == nil || !h.backend.Available() || key == nil || c == nil {
		return false
	}
	if !h.backend.Enabled(c.Request.Context()) || !h.GroupInScope(c, key) {
		return false
	}
	blockKey := h.backend.Find(c.Request.Context(), key.ID, c, body)
	if blockKey == "" {
		return false
	}
	message := moderationflow.SessionBlockedClientMessage
	// After heartbeat commitment, record the stream failure before writing its terminal Responses event.
	if h.backend.StopKeepalive(c) {
		h.backend.MarkStream(c, "permission_error", message, 403)
		if h.backend.FailedSSE(c, "permission_error", message) {
			h.EnqueueBlocked(c, key, model, blockKey)
			return true
		}
	}
	if format == CyberBlockAnthropic {
		c.JSON(http.StatusForbidden, gin.H{"type": "error", "error": gin.H{"type": "permission_error", "message": message}})
	} else {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"type": "permission_error", "code": "session_blocked_by_cyber_policy", "message": message}})
	}
	h.EnqueueBlocked(c, key, model, blockKey)
	return true
}

func (h *CyberHandler) EnqueueBlocked(c *gin.Context, key *apikey.APIKey, model, blockKey string) {
	if h == nil || h.runtime.Ops == nil || c == nil {
		return
	}
	c.Set("ops_dedicated_error_recorded", true)
	h.runtime.Ops.Enqueue(moderationflow.BuildSessionBlockedOpsEntry(h.Meta(c, key, nil, model, "openai", false, blockKey)))
}

func (h *CyberHandler) RecordPolicy(c *gin.Context, in CyberPolicyCall) bool {
	mark := h.backend.Mark(c)
	if mark == nil || c == nil {
		return false
	}
	blockKey := strings.TrimSpace(in.BlockKey)
	if in.HasPlan {
		blockKey = in.Plan.ScopeKey
		h.runtime.MarkBeforeScope(in.Plan)
	}
	if c.GetBool(CyberPolicyRecordedKey) {
		return true
	}
	platform := "openai"
	if in.Provider != nil && strings.TrimSpace(in.Provider.Platform) != "" {
		platform = in.Provider.Platform
	}
	meta := h.Meta(c, in.Key, in.Provider, in.Model, platform, c.GetBool("ops_stream"), blockKey)
	excerpt := CurrentOpenAICyberWarningPromptExcerpt(c)
	warning := BuildOpenAICyberWarningInput(h.endpoints, c, in.Key, in.Provider, in.Model, mark.UpstreamStatus, []byte(mark.Body), mark.Message, excerpt)
	if h.moderator != nil {
		scope, err := h.moderator.CyberWarningInScope(c.Request.Context(), warning)
		if err != nil {
			RequestLogger(c, "handler.openai_gateway.cyber_policy").Warn("content_moderation.cyber_policy_scope_check_failed", zap.Error(err))
			return false
		}
		if !scope {
			return false
		}
	}
	c.Set(CyberPolicyRecordedKey, true)
	RecordOpenAICyberWarningWithSnapshot(h.endpoints, h.moderator, c, RequestLogger(c, "handler.openai_gateway.cyber_policy"), in.Key, in.Provider, in.Model, mark.UpstreamStatus, []byte(mark.Body), mark.Message, excerpt, CurrentOpenAICyberWarningSnapshot(c))
	// Dispatch snapshots funding, metadata, and the model chain so queued work does not capture HTTP state.
	h.runtime.Dispatch(c.Request.Context(), moderationflow.PolicyCompletion{Usage: in.Usage, Meta: meta, Mark: *mark, ForwardErrored: in.ForwardErrored, BlockKey: blockKey})
	return true
}

func (h *CyberHandler) Meta(c *gin.Context, key *apikey.APIKey, provider *moderationflow.Provider, model, platform string, stream bool, blockKey string) moderationflow.OpsMeta {
	meta := moderationflow.OpsMeta{RequestID: telemetry.RequestIDValue(c.Request.Context()), ClientRequestID: telemetry.RequestIDValue(c.Request.Context()), Platform: platform, Model: strings.TrimSpace(model), Stream: stream, InboundEndpoint: h.endpoints.Inbound(c), UpstreamEndpoint: h.backend.UpstreamEndpoint(c, platform), UserAgent: c.GetHeader("User-Agent"), ClientIP: clientip.GetClientIP(c), CreatedAt: time.Now(), SessionBlockKey: blockKey}
	if c.Request != nil && c.Request.URL != nil {
		meta.RequestPath = c.Request.URL.Path
	}
	if key != nil {
		meta.APIKeyID = key.ID
		meta.APIKeyPrefix = key.Key
		if len(meta.APIKeyPrefix) > 8 {
			meta.APIKeyPrefix = meta.APIKeyPrefix[:8]
		}
		meta.UserID = key.UserID
		meta.GroupID = CloneContentModerationID(key.GroupID)
	}
	if provider != nil {
		meta.ProviderID = provider.ID
	}
	return meta
}

// ClearCyberTurnState clears policy markers when a WebSocket turn ends.
func ClearCyberTurnState(c *gin.Context, clearMark func(*gin.Context)) {
	if c == nil {
		return
	}
	clearMark(c)
	c.Set(CyberPolicyRecordedKey, false)
	c.Set(CyberWarningRecordedKey, false)
}
