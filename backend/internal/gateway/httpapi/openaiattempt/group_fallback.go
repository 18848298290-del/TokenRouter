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

// group_fallback.go switches eligible failed attempts to an admitted fallback
// group.
// A prompt-too-long attempt can move to the configured invalid-request fallback
// group once before output or cancellation ends recovery. The destination is
// resolved against the effective key and client protocol, then checked for
// active group identity and permission before any request state is replaced.
//
// Changing groups replaces the effective key, subscription, route, and mapped
// body together for the next attempt. Sticky prefetch from the old group is
// cleared before looking up the destination binding, while the surrounding
// request context keeps any forced-platform restriction. Existing session
// attempts are reset after the new state is bound, and Messages updates its
// additional model/mapping fields when the shared switch requests retry.

// GroupFallbackPorts authorizes a fallback group and plans its effective
// request route.
type GroupFallbackPorts struct {
	// Resolve reauthorizes the effective key for the target group and
	// protocol, returning that group's key snapshot and subscription before
	// request state changes.
	Resolve func(context.Context, *apikey.APIKey, int64, protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error)
	// Plan supplies the target key's route for the original requested
	// model. The switch binds that plan before rebuilding the next
	// attempt's model and body.
	Plan func(context.Context, *apikey.APIKey, string) routing.RoutePlan
}

// TryGroupFallback handles one configured group switch after a prompt-too-long
// failure. It requires an active request with unwritten output, a key group,
// valid target, and callable Resolve and Plan operations. Unsupported causes
// and exhausted guards return unhandled; authorization or destination
// validation failures write terminal output.
//
// The one-switch flag is set before authorization, so a failed destination
// consumes this opportunity. A successful switch clears old sticky prefetch,
// binds the new key and subscription with the new route, rebuilds the body from
// canonical input, and resets session attempts before requesting another
// provider attempt.
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
	// Consume the single destination attempt before authorization,
	// preventing repeated resolution of a failed or invalid fallback within
	// this same request.
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
	// Clear old-group sticky prefetch before consulting the destination,
	// keeping the previous group's provider affinity from being reused by
	// the next selection.
	ctx = requeststate.WithPrefetchedStickySession(ctx, 0, 0)
	b.hasBoundSession = false
	if b.binding().sessions.StickyProviderID != nil {
		id := b.binding().sessions.StickyProviderID(ctx, key.GroupID, b.sessionHash)
		b.hasBoundSession = id > 0
		ctx = requeststate.WithPrefetchedStickySession(ctx, id, key.Group.ID)
	}
	// Publish destination key, subscription, context and route as the next
	// attempt state, then rebuild canonical input so model selection and
	// body stay aligned.
	plan := ports.Plan(ctx, key, b.reqModel)
	ctx = requeststate.WithRoutePlan(ctx, plan)
	b.c.Request = b.c.Request.WithContext(ctx)
	b.c.Set("gateway_effective_key", key)
	b.c.Set(string(keyhttp.ContextKeyAPIKey), key)
	b.apiKey, b.subscription, b.selectionCtx = key, subscription, ctx
	b.groupMapping = plan.Mapping()
	b.forwardModel = b.groupMapping.MappedModel
	b.forwardBody = b.binding().replaceModelInBody(b.body, b.forwardModel)
	// Retire old-group session registrations after the new request state is
	// installed; Reset allows subsequent acquisitions to register
	// destination-group attempts independently.
	if b.sessionAttempts != nil {
		b.sessionAttempts.Reset()
	}
	return true, true
}

// TryGroupFallback applies the shared group switch to Messages-specific routing
// state. When the shared switch requests retry, it replaces the Messages
// mapping and provider-layer/current routing models. Other outcomes retain
// those fields and their handled/retry flags.
func (b *openAIMessageAttemptBridge) TryGroupFallback(cause error) (bool, bool) {
	handled, retry := b.responsesAttemptBridge.TryGroupFallback(cause)
	if retry {
		b.groupMappingMsg = b.groupMapping
		b.providerLayerModel = b.forwardModel
		b.currentRoutingModel = b.forwardModel
	}
	return handled, retry
}
