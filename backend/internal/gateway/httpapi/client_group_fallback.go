package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// ClientGroupFallbackResolver returns a group after authorization, protocol, and funding checks.
type ClientGroupFallbackResolver = func(context.Context, *apikey.APIKey, protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error)

type clientGroupFallbackBackend interface {
	ResolveClientGroup(context.Context, *apikey.APIKey, protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error)
}

// resolveClientGroupForRequest runs after client detection and before group policies and route planning.
// After validation, it publishes the final group and its funding and permission snapshots.
func resolveClientGroupForRequest(c *gin.Context, backend any, key *apikey.APIKey, source protocol.ProtocolID) (*apikey.APIKey, error) {
	if key == nil || key.Group == nil || !key.Group.ClaudeCodeOnly || (source == protocol.ProtocolAnthropicMessages && requeststate.IsClaudeCodeClient(c.Request.Context())) {
		return key, nil
	}
	resolver, ok := backend.(clientGroupFallbackBackend)
	if !ok {
		return nil, routing.ErrClaudeCodeOnly
	}
	resolved, subscription, err := resolver.ResolveClientGroup(c.Request.Context(), key, source)
	if err != nil {
		return nil, err
	}
	if resolved == nil || resolved.Group == nil || resolved.GroupID == nil || *resolved.GroupID != resolved.Group.ID || !resolved.Group.IsActive() || resolved.Group.ClaudeCodeOnly || !resolved.Group.AllowsClientProtocol(source) {
		return nil, routing.ErrClaudeCodeOnly
	}
	ctx := requeststate.WithGroup(c.Request.Context(), resolved.Group)
	ctx = requeststate.WithClientProtocol(ctx, source)
	ctx = apikey.WithRuntimeAPIKey(ctx, resolved)
	ctx = requeststate.WithPrefetchedStickySession(ctx, 0, 0)
	c.Request = c.Request.WithContext(ctx)
	c.Set("gateway_effective_key", resolved)
	c.Set(string(keyhttp.ContextKeyAPIKey), resolved)
	c.Set("subscription", subscription)
	funds := &billing.APIKeyBillingContext{Mode: apikey.APIKeyEffectiveBillingMode(resolved), Source: admission.FundingSourceBalance, Available: true, Subscription: subscription}
	if subscription != nil {
		funds.Source = admission.FundingSourceSubscription
	}
	c.Set("api_key_billing", funds)
	return resolved, nil
}

// writeClientGroupFallbackError writes an initial group resolution failure in the entry protocol.
func writeClientGroupFallbackError(c *gin.Context, err error, write func(*gin.Context, int, string, string)) {
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
	status, code, message, retryAfter := BillingErrorDetails(err)
	if err == routing.ErrClaudeCodeOnly {
		status, code, message = http.StatusForbidden, "permission_error", err.Error()
	}
	if retryAfter > 0 {
		c.Header("Retry-After", strconv.Itoa(retryAfter))
	}
	write(c, status, code, message)
}

func (p messagesHTTPBackend) ResolveClientGroup(ctx context.Context, key *apikey.APIKey, source protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
	if p.bindings.ClientGroupFallback == nil {
		return nil, nil, routing.ErrClaudeCodeOnly
	}
	return p.bindings.ClientGroupFallback(ctx, key, source)
}

func (p openAITextHTTPBackend) ResolveClientGroup(ctx context.Context, key *apikey.APIKey, source protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
	if p.bindings.ClientGroupFallback == nil {
		return nil, nil, routing.ErrClaudeCodeOnly
	}
	return p.bindings.ClientGroupFallback(ctx, key, source)
}
