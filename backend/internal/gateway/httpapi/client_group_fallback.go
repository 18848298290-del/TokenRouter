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

// client_group_fallback.go resolves a compatible group for clients rejected by
// a Claude Code-only group. A Claude Code-only group can redirect an
// incompatible client to a configured group before route planning. Recognized
// Messages clients keep their current group, while other entry protocols
// require a resolver result that passes authorization, protocol, and funding
// checks. The adapter verifies the returned group is active, internally
// consistent, and accepts the inbound protocol.
//
// Successful resolution replaces request group, effective key, and subscription
// together for subsequent handler phases. apikey.WithRuntimeAPIKey updates a
// matching authenticated key snapshot while retaining its actor. Sticky prefetch
// is cleared because it was obtained for the previous group. Resolver errors or
// unusable results return before any of those replacements are published here.

// ClientGroupFallbackResolver returns a key and subscription after target-group
// authorization, protocol, and funding checks. Entry adapters call it before
// planning and publish its result only after validating the returned group.
// Resolvers keep the authenticated key ID so apikey.WithRuntimeAPIKey can update
// its request access snapshot while retaining the actor identity.
type ClientGroupFallbackResolver = func(context.Context, *apikey.APIKey, protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error)

// clientGroupFallbackBackend supplies authorized client-group resolution to HTTP request preparation.
type clientGroupFallbackBackend interface {
	// ResolveClientGroup returns an authorized target key and its funding subscription.
	// The source identifies the inbound protocol. Callers reject errors or unusable
	// group data before replacing their effective request snapshots.
	ResolveClientGroup(context.Context, *apikey.APIKey, protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error)
}

// resolveClientGroupForRequest runs after client detection and before group policies and route planning.
// After validation, it publishes the final group, key, and funding state.
// Missing keys or groups and unrestricted groups pass through; recognized Claude
// Code clients on Messages also retain their group. Other restricted requests
// require a backend implementing clientGroupFallbackBackend.
//
// The returned group is checked before request and Gin context values change.
// Success installs its protocol, key, subscription, and funding source, clears
// prior sticky prefetch, and returns the selected key. Resolver errors and
// structurally invalid results stop the caller before route planning; callbacks
// are responsible for the authorization and funding checks promised by their interface.
// Their resolved key retains the authenticated key ID, which WithRuntimeAPIKey
// requires before it updates the access snapshot. The adapter itself checks
// group consistency and protocol eligibility; retaining key identity is the
// resolver's calling rule.
func resolveClientGroupForRequest(c *gin.Context, backend any, key *apikey.APIKey, source protocol.ProtocolID) (*apikey.APIKey, error) {
	if key == nil || key.Group == nil || !key.Group.ClaudeCodeOnly || (source == protocol.ProtocolAnthropicMessages && requeststate.IsClaudeCodeClient(c.Request.Context())) {
		return key, nil
	}
	resolver, ok := backend.(clientGroupFallbackBackend)
	if !ok {
		// A restricted group requires a configured resolution backend for this
		// incompatible client; returning the policy error prevents implicit selection of another group.
		return nil, routing.ErrClaudeCodeOnly
	}
	resolved, subscription, err := resolver.ResolveClientGroup(c.Request.Context(), key, source)
	if err != nil {
		return nil, err
	}
	if resolved == nil || resolved.Group == nil || resolved.GroupID == nil || *resolved.GroupID != resolved.Group.ID || !resolved.Group.IsActive() || resolved.Group.ClaudeCodeOnly || !resolved.Group.AllowsClientProtocol(source) {
		return nil, routing.ErrClaudeCodeOnly
	}
	// Publish the checked group with its key and funding data before planning.
	// Clear the previous group's sticky prefetch so provider selection cannot reuse that hint.
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
// The exact Claude Code-only error uses forbidden permission_error; other failures
// use BillingErrorDetails and its optional retry header. The supplied writer
// performs protocol output after the local policy rejection is marked for Ops.
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

// ResolveClientGroup invokes the group resolver bound to Messages request preparation.
// Missing bindings return the Claude Code-only policy error. A configured
// resolver supplies the target key and subscription that preparation subsequently validates and publishes.
func (p messagesHTTPBackend) ResolveClientGroup(ctx context.Context, key *apikey.APIKey, source protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
	if p.bindings.ClientGroupFallback == nil {
		return nil, nil, routing.ErrClaudeCodeOnly
	}
	return p.bindings.ClientGroupFallback(ctx, key, source)
}

// ResolveClientGroup invokes the resolver used by the unified OpenAI text entry.
// Its result supplies the next key and funding subscription before route
// planning; an absent binding returns the Claude Code-only policy error to preparation.
func (p openAITextHTTPBackend) ResolveClientGroup(ctx context.Context, key *apikey.APIKey, source protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
	if p.bindings.ClientGroupFallback == nil {
		return nil, nil, routing.ErrClaudeCodeOnly
	}
	return p.bindings.ClientGroupFallback(ctx, key, source)
}
