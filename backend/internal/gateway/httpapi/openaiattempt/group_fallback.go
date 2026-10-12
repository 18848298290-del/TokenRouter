package openaiattempt

import (
	"context"
	"errors"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// GroupFallbackPorts reauthorizes, resolves funding, and validates sessions before switching groups.
type GroupFallbackPorts struct {
	Resolve func(context.Context, *apikey.APIKey, int64, protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error)
	Plan    func(context.Context, *apikey.APIKey, string) routing.RoutePlan
}

// TryGroupFallback permits one group switch for PromptTooLongError before output or cancellation.
func (b *responsesAttemptBridge) TryGroupFallback(cause error) (handled, retry bool) {
	var tooLong *antigravity.PromptTooLongError
	if !errors.As(cause, &tooLong) || b.groupFallbackUsed || b.apiKey == nil || b.apiKey.Group == nil || !b.CanAttempt() || b.c.Writer.Written() || (b.streamStarted != nil && *b.streamStarted) {
		return false, false
	}
	target := b.apiKey.Group.FallbackGroupIDOnInvalidRequest
	ports := b.binding().fallback
	if target == nil || *target <= 0 || *target == b.apiKey.Group.ID || ports.Resolve == nil || ports.Plan == nil {
		return false, false
	}
	b.groupFallbackUsed = true
	source, _ := requeststate.ClientProtocolFromContext(b.Context())
	key, subscription, err := ports.Resolve(b.Context(), b.apiKey, *target, source)
	if err != nil {
		status, kind, message, _ := gatewayhttp.BillingErrorDetails(err)
		gatewayhttp.MarkOpsClientBusinessLimited(b.c, gatewayhttp.OpsClientBusinessLimitedReasonLocalPolicyDenied)
		if source == protocol.ProtocolAnthropicMessages {
			b.binding().anthropicStreamingAwareError(b.c, status, kind, message, *b.streamStarted)
		} else {
			b.binding().handleStreamingAwareError(b.c, status, kind, message, *b.streamStarted)
		}
		return true, false
	}
	if key == nil || key.Group == nil || key.GroupID == nil || *key.GroupID != *target || key.Group.ID != *target || !key.Group.IsActive() || !key.Group.AllowsClientProtocol(source) {
		b.binding().handleStreamingAwareError(b.c, http.StatusForbidden, "permission_error", "fallback group does not allow this protocol", *b.streamStarted)
		return true, false
	}
	ctx := requeststate.WithGroup(b.Context(), key.Group)
	ctx = apikey.WithRuntimeAPIKey(ctx, key)
	// Look up the target group sticky provider after switching groups.
	ctx = requeststate.WithPrefetchedStickySession(ctx, 0, 0)
	b.hasBoundSession = false
	if b.binding().sessions.StickyProviderID != nil {
		id := b.binding().sessions.StickyProviderID(ctx, key.GroupID, b.sessionHash)
		b.hasBoundSession = id > 0
		ctx = requeststate.WithPrefetchedStickySession(ctx, id, key.Group.ID)
	}
	plan := ports.Plan(ctx, key, b.reqModel)
	ctx = requeststate.WithRoutePlan(ctx, plan)
	b.c.Request = b.c.Request.WithContext(ctx)
	b.c.Set("gateway_effective_key", key)
	b.c.Set(string(keyhttp.ContextKeyAPIKey), key)
	b.apiKey, b.subscription, b.selectionCtx = key, subscription, ctx
	b.groupMapping = plan.Mapping()
	b.forwardModel = b.groupMapping.MappedModel
	b.forwardBody = b.binding().replaceModelInBody(b.body, b.forwardModel)
	if b.sessionAttempts != nil {
		b.sessionAttempts.Reset()
	}
	return true, true
}

func (b *openAIMessageAttemptBridge) TryGroupFallback(cause error) (bool, bool) {
	handled, retry := b.responsesAttemptBridge.TryGroupFallback(cause)
	if retry {
		b.groupMappingMsg = b.groupMapping
		b.providerLayerModel = b.forwardModel
		b.currentRoutingModel = b.forwardModel
	}
	return handled, retry
}
