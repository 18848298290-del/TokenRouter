package textattempt

import (
	"errors"
	"net/http"
	"strconv"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// Fallback resolves an authorized group for a prompt-too-long failure; gateway/text limits fallback to once per request.
func (b *messageAttemptBridge) Fallback(cause error, fallbackUsed bool) bool {
	var tooLong *antigravity.PromptTooLongError
	if !errors.As(cause, &tooLong) {
		return false
	}
	// Committed output or cancellation prevents group replay and any additional JSON error.
	if b.Context().Err() != nil || b.c.Writer.Written() || (b.streamStarted != nil && *b.streamStarted) {
		return false
	}
	source, ok := requeststate.ClientProtocolFromContext(b.Context())
	if !ok {
		source = protocol.ProtocolAnthropicMessages
	}
	d := b.binding()
	key := b.currentAPIKey
	target := b.fallbackGroupID
	if fallbackUsed || key == nil || key.Group == nil || target == nil || *target <= 0 || *target == key.Group.ID || d.resolveFallback == nil || d.planRoute == nil {
		if d.writeMappedClaudeError != nil {
			_ = d.writeMappedClaudeError(b.c, b.provider, tooLong.StatusCode, tooLong.RequestID, tooLong.Body)
		}
		return false
	}
	resolved, subscription, err := d.resolveFallback(b.Context(), key, *target, source)
	if err != nil {
		b.reqLog.Warn("gateway.resolve_fallback_group_failed", zap.Int64("fallback_group_id", *target), zap.Error(err))
		status, code, message, retryAfter := gatewayhttp.BillingErrorDetails(err)
		if retryAfter > 0 {
			b.c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		gatewayhttp.MarkOpsClientBusinessLimited(b.c, gatewayhttp.OpsClientBusinessLimitedReasonLocalPolicyDenied)
		d.handleStreamingAwareError(b.c, status, code, message, false)
		return false
	}
	if resolved == nil || resolved.Group == nil || resolved.GroupID == nil || *resolved.GroupID != *target || resolved.Group.ID != *target || !resolved.Group.IsActive() || !resolved.Group.AllowsClientProtocol(source) {
		gatewayhttp.MarkOpsClientBusinessLimited(b.c, gatewayhttp.OpsClientBusinessLimitedReasonLocalPolicyDenied)
		d.handleStreamingAwareError(b.c, http.StatusForbidden, "permission_error", "fallback group does not allow this request", false)
		return false
	}
	// Retain platform, session, and triggered billing policy while replacing the group with the newly authorized snapshot.
	ctx := requeststate.WithGroup(b.Context(), resolved.Group)
	ctx = apikey.WithRuntimeAPIKey(ctx, resolved)
	ctx = requeststate.WithPrefetchedStickySession(ctx, 0, 0)
	b.hasBoundSession = false
	b.sessionBoundProviderID = 0
	if d.cachedSession != nil && b.sessionKey != "" {
		providerID, cacheErr := d.cachedSession(ctx, resolved.GroupID, b.sessionKey)
		if cacheErr != nil {
			b.reqLog.Warn("gateway.fallback_sticky_lookup_failed", zap.Error(cacheErr))
		} else if providerID > 0 {
			b.hasBoundSession = true
			b.sessionBoundProviderID = providerID
			ctx = requeststate.WithPrefetchedStickySession(ctx, providerID, resolved.Group.ID)
		}
	}
	plan := d.planRoute(ctx, resolved, b.reqModel)
	ctx = requeststate.WithRoutePlan(ctx, plan)
	b.c.Request = b.c.Request.WithContext(ctx)
	b.c.Set("gateway_effective_key", resolved)
	b.c.Set(string(keyhttp.ContextKeyAPIKey), resolved)
	b.apiKey, b.currentAPIKey = resolved, resolved
	b.subscription, b.currentSubscription = subscription, subscription
	b.attemptGroupMapping = plan.Mapping()
	if b.sessionAttempts != nil {
		b.sessionAttempts.Reset()
	}
	return true
}

// Fallback updates the Responses request context and model mapping after successful group fallback.
func (b *genericResponsesAttemptBridge) Fallback(cause error, used bool) bool {
	if !b.messageAttemptBridge.Fallback(cause, used) {
		return false
	}
	b.requestCtx = b.c.Request.Context()
	b.groupMapping = b.attemptGroupMapping
	b.forwardBody = b.body
	if b.groupMapping.Mapped {
		b.forwardBody = b.binding().replaceModel(b.body, b.groupMapping.MappedModel)
	}
	return true
}

func (b *genericChatAttemptBridge) Fallback(cause error, used bool) bool {
	if !b.messageAttemptBridge.Fallback(cause, used) {
		return false
	}
	b.requestCtx = b.c.Request.Context()
	b.groupMapping = b.attemptGroupMapping
	return true
}
