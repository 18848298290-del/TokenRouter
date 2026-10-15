package httpapi

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// openai_ws_session_preemption.go registers eligible pooled OAuth connections
// for session replacement and clears displaced affinity.
// A pooled OpenAI OAuth session can be replaced by a newer connection using
// the same group, Key and session hash. Entry owns its registration across
// provider attempts, and nested forwarding calls recognize the armed context
// so an attempt's cleanup cannot release the connection's registration.
// Dedicated per-session transport opts out of this shared replacement rule.
//
// The gateway core supplies local generations and optional shared-cache
// observation. This adapter validates the scope, passes the executor's
// registry and affinity storage, and clears old turn-state and connection
// affinity after reported replacement. Returned cleanup signals remote
// observation and releases ownership; the core does not join a cache check
// already in progress, so a confirmed late mismatch can still clear affinity
// through its callback.

// openAIWSSessionPreemptKey identifies an active OAuth session within group and Key scope.
type openAIWSSessionPreemptKey struct {
	// groupID, apiKeyID and sessionHash jointly identify a local registration.
	// newOpenAIWSSessionPreemptKey requires positive IDs and trims a nonblank hash
	// before these values are passed to the gateway registry.
	groupID     int64  // selected request routing group
	apiKeyID    int64  // authenticated connection Key identifier
	sessionHash string // normalized shared session hash
}

// openAIWSSessionPreemptContextKey marks a context with an entry-owned preemption registration.
type openAIWSSessionPreemptContextKey struct{}

// openAIWSSessionPreemptRegistry is usable at its zero value and embeds
// gateway-owned generations and cancellation state.
type openAIWSSessionPreemptRegistry struct {
	gatewayws.PreemptRegistry /* local generation and cancellation state */
}

// BeginOpenAIWSIngressSessionPreemption registers pooled OpenAI OAuth
// connection ownership and returns its context and cleanup. Nil context uses
// Background, an already armed context returns no-op cleanup, and dedicated
// per-session mode returns unarmed. Eligible connections derive scope from the
// request group, authenticated Key and explicit or body-derived session hash.
//
// The marker is published after registration and any reported prior-owner
// affinity is cleared. Nested provider attempts therefore reuse the same
// context and cannot unregister entry's owner when they return. The outer
// caller invokes the real cleanup after the connection ends; the core's remote
// watcher stop signals termination without joining an in-flight ownership
// check.
func (s *OpenAIWebSocketExecutor) BeginOpenAIWSIngressSessionPreemption(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	firstClientMessage []byte,
) (context.Context, func(), bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if armed, _ := ctx.Value(openAIWSSessionPreemptContextKey{}).(bool); armed {
		return ctx, func() {}, true
	}
	if s != nil &&
		provider != nil && provider.View().ResponsesWSConnectionMode() == providercore.ResponsesWSPerSession {
		return ctx, func() {}, false
	}

	preemptSessionHash := ""
	preemptGroupID := gatewayhttp.OpenAIResponseGroupID(c)
	if provider != nil && provider.Record.Platform == capability.PlatformOpenAI && provider.Record.Type == capability.ProviderTypeOAuth {
		preemptSessionHash = gatewayhttp.GenerateOpenAISessionHash(c, firstClientMessage)
	}
	preemptCtx, cleanup, armed, preemptedPrevious := s.beginOpenAIWSSessionPreemptContext(
		ctx,
		provider,
		preemptGroupID, gatewayhttp.APIKeyIDFromContext(c), preemptSessionHash,
		false,
	)
	if !armed {
		return ctx, func() {}, false
	}
	// A replacement starts a fresh continuation association; clear old turn-state
	// and connection hints before marking the new context as entry-owned.
	if preemptedPrevious {
		if stateStore := s.State; stateStore != nil {
			stateStore.DeleteSessionTurnState(preemptGroupID, preemptSessionHash)
			stateStore.DeleteSessionConn(preemptGroupID, preemptSessionHash)
		}
	}
	return context.WithValue(preemptCtx, openAIWSSessionPreemptContextKey{}, true), cleanup, true
}

// newOpenAIWSSessionPreemptKey returns a normalized local ownership key and a
// valid flag. Nonpositive group or Key IDs and a blank trimmed session hash
// return false, preventing incomplete scope from entering the registry.
func newOpenAIWSSessionPreemptKey(groupID, apiKeyID int64, sessionHash string) (openAIWSSessionPreemptKey, bool) {
	sessionHash = strings.TrimSpace(sessionHash)
	if groupID <= 0 || apiKeyID <= 0 || sessionHash == "" {
		return openAIWSSessionPreemptKey{}, false
	}
	return openAIWSSessionPreemptKey{groupID: groupID, apiKeyID: apiKeyID, sessionHash: sessionHash}, true
}

// Begin registers the scoped cancellation callback through the gateway's
// generation registry. A nil wrapper returns no-op cleanup and false;
// otherwise cleanup and prior-owner replacement results retain the core's
// generation-aware behavior.
func (r *openAIWSSessionPreemptRegistry) Begin(key openAIWSSessionPreemptKey, cancel func()) (func(), bool) {
	if r == nil {
		return func() {}, false
	}
	return r.PreemptRegistry.Begin(gatewayws.PreemptKey{GroupID: key.groupID, APIKeyID: key.apiKeyID, SessionHash: key.sessionHash}, cancel)
}

// beginOpenAIWSSessionPreemptContext validates eligibility and delegates
// ownership registration to the gateway core. Nil context uses Background;
// missing executor or provider, a non-OpenAI-OAuth target, an HTTP one-shot,
// or invalid scope returns no-op cleanup with both flags false. Success
// reports armed and whether a prior local or remote owner existed.
func (s *OpenAIWebSocketExecutor) beginOpenAIWSSessionPreemptContext(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	groupID, apiKeyID int64,
	sessionHash string,
	httpIngressWSOneShot bool,
) (context.Context, func(), bool, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || provider == nil || provider.Record.Platform != capability.PlatformOpenAI || provider.Record.Type != capability.ProviderTypeOAuth || httpIngressWSOneShot {
		return ctx, func() {}, false, false
	}
	key, ok := newOpenAIWSSessionPreemptKey(groupID, apiKeyID, sessionHash)
	if !ok {
		return ctx, func() {}, false, false
	}

	return s.wsPreemption().Begin(ctx, gatewayws.PreemptKey{GroupID: key.groupID, APIKeyID: key.apiKeyID, SessionHash: key.sessionHash})
}

// openAIWSSessionPreemptionCache returns the optional shared-cache ownership
// capability. Missing executor or Cache returns nil, and a cache that lacks
// the preemption interface also returns nil so local registry replacement can
// operate independently.
func (s *OpenAIWebSocketExecutor) openAIWSSessionPreemptionCache() session.OpenAIWSSessionPreemptionCache {
	if s == nil || s.Cache == nil {
		return nil
	}
	// Storage can provide ordinary affinity without ownership observation; an
	// unsupported capability leaves remote claiming disabled and local replacement available.
	cache, _ := s.Cache.(session.OpenAIWSSessionPreemptionCache)
	return cache
}

// wsPreemption returns gateway ownership operations bound to this executor's
// registry, optional remote cache and session state. The receiver is required;
// RedisTimeout supplies context deadlines to shared-cache calls, whose socket
// timeouts remain owned by the cache client.
func (s *OpenAIWebSocketExecutor) wsPreemption() *gatewayws.Preemption {
	return &gatewayws.Preemption{Registry: &s.openaiWSSessionPreemptions.PreemptRegistry, Cache: s.openAIWSSessionPreemptionCache(), State: s.State, RedisTimeout: session.StateStoreRedisTimeout}
}

// isOpenAIWSSessionPreempted reports whether the context's cancellation cause
// wraps ErrSessionPreempted. A nil or live context returns false; the check
// reads the cause selected by local displacement or confirmed remote ownership
// loss.
func isOpenAIWSSessionPreempted(ctx context.Context) bool {
	return ctx != nil && errors.Is(context.Cause(ctx), gatewayws.ErrSessionPreempted)
}
