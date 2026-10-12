package textattempt

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// Group fallback must preserve admitted caller and platform restrictions while
// replacing only authorized target-group funding and routing state. These
// fixtures start with an old sticky provider and explicit metadata isolation,
// then verify target-group lookup, context snapshots and effective HTTP Key
// pointers after a prompt-too-long response.
//
// Replay is rejected after cancellation, output, prior fallback or invalid
// authorization, and an absent resolver presents the mapped original error.
// The cache-billing fixture triggers a provider switch before fallback and
// checks that rebuilding attempt state retains that billing choice. A failed
// new-group sticky lookup must leave old hints cleared rather than restore the
// prior provider association.

// newFallbackMessageBridge opens and begins a Messages fixture with old-group
// sticky state and explicit isolation metadata. It installs session tracking
// and default route planning when absent; callers supply fallback policy
// callbacks and keep the returned bridge serial.
func newFallbackMessageBridge(t *testing.T, bindings Bindings) (*messageAttemptBridge, *apikey.APIKey, *httptest.ResponseRecorder) {
	t.Helper()
	targetID, groupID := int64(20), int64(10)
	key := &apikey.APIKey{ID: 30, UserID: 40, GroupID: &groupID, User: &identity.User{ID: 40}, Group: &routing.Group{ID: groupID, Status: "active", AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages}, FallbackGroupIDOnInvalidRequest: &targetID}}
	ctx := requeststate.WithGroup(context.Background(), key.Group)
	ctx = requeststate.WithClientProtocol(ctx, protocol.ProtocolAnthropicMessages)
	ctx = apikey.WithForcePlatform(ctx, "antigravity")
	// Start with an old-group binding so successful fallback must explicitly
	// clear it before target-group cache lookup can install any replacement hint.
	ctx = requeststate.WithPrefetchedStickySession(ctx, 100, groupID)
	ctx = apikey.WithAccessSnapshot(ctx, apikey.AccessSnapshot{KeyID: key.ID, OwnerUserID: key.UserID, ActorUserID: 50, PayerUserID: key.User.ID})
	ctx = apikey.WithRuntimeAPIKey(ctx, key)
	ctx = apikey.WithFastModePolicy(ctx, "off")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/antigravity/v1/messages", nil)
	started := false
	bindings.Selection.NewSessionAttempts = func() *scheduler.SessionAttempts { return scheduler.NewSessionAttempts(nil, scheduler.Diagnostics{}) }
	bindings.Selection.SingleProviderGroup = func(context.Context, *int64) bool {
		return false
	}
	if bindings.PlanRoute == nil {
		bindings.PlanRoute = func(_ context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
			return routing.Plan(routing.PlanInput{Group: key.Group, RequestedModel: model, ClientProtocol: protocol.ProtocolAnthropicMessages, GroupMapping: routing.GroupMappingResult{Mapped: true, MappedModel: "target-model"}})
		}
	}
	sink := &gatewayhttp.MessagesOutput{ResponseSink: gatewayhttp.ResponseSink{Writer: c.Writer}, HTTP: c, Log: zap.NewNop(), StreamStarted: &started}
	ports, err := New(bindings).Open(ctx, execution.Request{
		Model: "client-model", SessionHash: "sticky-hash", UserID: 40, Funding: execution.FundingState{Key: key},
		Text: execution.TextState{Kind: execution.TextMessages, HasBoundSession: true, BoundProviderID: 100, Parsed: &requeststate.ParsedRequest{MetadataUserID: `{"device_id":"d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677","session_id":"c72554f2-1234-5678-abcd-123456789abc"}`}},
	}, sink)
	require.NoError(t, err)
	bridge, ok := ports.(*messageAttemptBridge)
	require.True(t, ok)
	bridge.Begin()
	return bridge, key, recorder
}

// authorizedMessageFallback returns a Key copy bound to an active target group
// permitting Messages. It replaces Group and GroupID while retaining the
// copied caller/funding identity; callers use it as the resolver’s authorized
// fixture result.
func authorizedMessageFallback(key *apikey.APIKey, id int64) *apikey.APIKey {
	resolved := apikey.CopyAPIKey(key)
	resolved.GroupID = &id
	resolved.Group = &routing.Group{ID: id, Status: "active", AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages}}
	return resolved
}

// TestMessageGroupFallbackPreservesBoundaryAndRefreshesSnapshots authorizes a
// new group with subscription and sticky binding. Fallback must retain
// caller/platform/cache/isolation policy, replace effective funding/routing
// views consistently and leave the original Key’s group unchanged.
func TestMessageGroupFallbackPreservesBoundaryAndRefreshesSnapshots(t *testing.T) {
	resolveCalls, stickyCalls := 0, 0
	sub := &billing.UserSubscription{ID: 90}
	bridge, original, _ := newFallbackMessageBridge(t, Bindings{
		ResolveFallback: func(ctx context.Context, key *apikey.APIKey, id int64, source protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
			resolveCalls++
			require.Equal(t, int64(10), *key.GroupID)
			require.Equal(t, protocol.ProtocolAnthropicMessages, source)
			platform, forced := apikey.ForcePlatformFromContext(ctx)
			require.True(t, forced)
			require.Equal(t, "antigravity", platform)
			require.True(t, requeststate.IsForceCacheBilling(ctx))
			hints := requeststate.ExecutionHintsFromContext(ctx)
			require.Equal(t, session.SessionIsolationSourceGateway, hints.SessionIsolationSource)
			require.Equal(t, "c72554f2-1234-5678-abcd-123456789abc", hints.SessionIsolationHash)
			return authorizedMessageFallback(key, id), sub, nil
		},
		Selection: SelectionPorts{CachedSession: func(ctx context.Context, groupID *int64, hash string) (int64, error) {
			stickyCalls++
			require.Equal(t, int64(20), *groupID)
			require.Equal(t, "sticky-hash", hash)
			require.Zero(t, requeststate.ExecutionHintsFromContext(ctx).PrefetchedStickyProviderID.Value)
			return 200, nil
		}},
	})
	bridge.c.Request = bridge.c.Request.WithContext(requeststate.WithForceCacheBilling(bridge.Context()))
	require.True(t, bridge.Fallback(&antigravity.PromptTooLongError{StatusCode: 400}, false))
	require.Equal(t, 1, resolveCalls)
	require.Equal(t, 1, stickyCalls)
	require.Equal(t, int64(10), *original.GroupID)
	require.Equal(t, int64(20), *bridge.currentAPIKey.GroupID)
	require.Same(t, sub, bridge.currentSubscription)
	platform, forced := apikey.ForcePlatformFromContext(bridge.Context())
	require.True(t, forced)
	require.Equal(t, "antigravity", platform)
	require.True(t, requeststate.IsForceCacheBilling(bridge.Context()))
	require.Equal(t, "sticky-hash", bridge.sessionKey)
	require.Equal(t, int64(200), bridge.sessionBoundProviderID)
	require.True(t, bridge.hasBoundSession)
	hints := requeststate.ExecutionHintsFromContext(bridge.Context())
	require.Equal(t, int64(20), hints.PrefetchedStickyGroupID.Value)
	require.Equal(t, int64(200), hints.PrefetchedStickyProviderID.Value)
	plan, ok := requeststate.RoutePlanFromContext(bridge.Context())
	require.True(t, ok)
	require.Equal(t, int64(20), plan.GroupID())
	require.Equal(t, "target-model", bridge.attemptGroupMapping.MappedModel)
	access, ok := apikey.AccessSnapshotFromContext(bridge.Context())
	require.True(t, ok)
	require.Equal(t, int64(20), *access.KeyView().GroupID)
	require.Equal(t, int64(40), access.OwnerUserID)
	require.Equal(t, int64(50), access.ActorUserID)
	require.Equal(t, "off", access.FastModePolicy())
	effective, _ := bridge.c.Get("gateway_effective_key")
	authKey, _ := bridge.c.Get(string(keyhttp.ContextKeyAPIKey))
	require.Same(t, bridge.currentAPIKey, effective)
	require.Same(t, bridge.currentAPIKey, authKey)
}

// TestMessageGroupFallbackStopsBeforeReplay varies cancellation, output,
// stream state, prior use, invalid target and unrelated failures. Each case
// must reject replay before resolver invocation and retain the admitted group.
func TestMessageGroupFallbackStopsBeforeReplay(t *testing.T) {
	for _, name := range []string{"cancelled", "written", "stream_started", "already_used", "self_target", "missing_target", "unrelated_error"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			bridge, _, _ := newFallbackMessageBridge(t, Bindings{ResolveFallback: func(context.Context, *apikey.APIKey, int64, protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
				calls++
				return nil, nil, nil
			}})
			var cause error = &antigravity.PromptTooLongError{StatusCode: 400}
			switch name {
			case "cancelled":
				ctx, cancel := context.WithCancel(bridge.Context())
				cancel()
				bridge.c.Request = bridge.c.Request.WithContext(ctx)
			case "written":
				_, _ = bridge.c.Writer.Write([]byte("partial"))
			case "stream_started":
				*bridge.streamStarted = true
			case "self_target":
				bridge.fallbackGroupID = bridge.currentAPIKey.GroupID
			case "missing_target":
				bridge.fallbackGroupID = nil
			case "unrelated_error":
				cause = errors.New("other")
			}
			require.False(t, bridge.Fallback(cause, name == "already_used"))
			require.Zero(t, calls)
			require.Equal(t, int64(10), *bridge.currentAPIKey.GroupID)
		})
	}
}

// TestMessageGroupFallbackRejectsAuthorizationAndFundingErrors returns group,
// subscription, isolation and RPM failures from target resolution. Fallback
// must present their mapped status/message and applicable retry guidance while
// retaining original group funding.
func TestMessageGroupFallbackRejectsAuthorizationAndFundingErrors(t *testing.T) {
	for _, denied := range []error{apikey.ErrGroupNotAllowed, billing.ErrPreferredSubscriptionGroup, session.ErrSessionIsolationConflict, scheduler.ErrGroupRPMExceeded} {
		t.Run(denied.Error(), func(t *testing.T) {
			bridge, _, recorder := newFallbackMessageBridge(t, Bindings{ResolveFallback: func(context.Context, *apikey.APIKey, int64, protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
				return nil, nil, denied
			}})
			require.False(t, bridge.Fallback(&antigravity.PromptTooLongError{StatusCode: 400}, false))
			status, _, message, _ := gatewayhttp.BillingErrorDetails(denied)
			require.Equal(t, status, recorder.Code)
			require.Contains(t, recorder.Body.String(), message)
			require.Equal(t, int64(10), *bridge.currentAPIKey.GroupID)
			if errors.Is(denied, scheduler.ErrGroupRPMExceeded) {
				require.NotEmpty(t, recorder.Header().Get("Retry-After"))
			}
		})
	}
}

// TestMessageGroupFallbackRequiresAuthorizedResolverAndTargetProtocol first
// omits the resolver, then returns a target permitting only Responses. The
// mapped error or HTTP 403 must end replay without replacing the original
// Messages group.
func TestMessageGroupFallbackRequiresAuthorizedResolverAndTargetProtocol(t *testing.T) {
	mapped := 0
	bridge, _, _ := newFallbackMessageBridge(t, Bindings{Forward: ForwardPorts{WriteMappedClaudeError: func(*gin.Context, *gatewaycapture.ExecutionProvider, int, string, []byte) error {
		mapped++
		return nil
	}}})
	require.False(t, bridge.Fallback(&antigravity.PromptTooLongError{StatusCode: 400}, false))
	require.Equal(t, 1, mapped)
	bridge, _, recorder := newFallbackMessageBridge(t, Bindings{ResolveFallback: func(_ context.Context, key *apikey.APIKey, id int64, _ protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
		resolved := authorizedMessageFallback(key, id)
		resolved.Group.AllowedProtocols = []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}
		return resolved, nil, nil
	}})
	require.False(t, bridge.Fallback(&antigravity.PromptTooLongError{StatusCode: 400}, false))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Equal(t, int64(10), *bridge.currentAPIKey.GroupID)
}

// TestMessageGroupFallbackKeepsCacheBillingAcrossAttemptReset triggers cache
// billing on a failed switched-provider attempt before group fallback. The
// subsequent reset attempt must retain the context billing flag and forward
// successfully under the new state.
func TestMessageGroupFallbackKeepsCacheBillingAcrossAttemptReset(t *testing.T) {
	forwards := 0
	bridge, _, _ := newFallbackMessageBridge(t, Bindings{
		ResolveFallback: func(_ context.Context, key *apikey.APIKey, id int64, _ protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
			return authorizedMessageFallback(key, id), nil, nil
		},
		Forward: ForwardPorts{
			BedrockCompat: func(_ *gin.Context, body []byte, _ string, _ *gatewaycapture.ExecutionProvider, _ *int64) []byte {
				return body
			},
			ForwardAntigravity: func(ctx context.Context, _ *gin.Context, _ *gatewaycapture.ExecutionProvider, _ []byte, _ bool) (*forwardcore.MessagesResult, error) {
				forwards++
				require.True(t, requeststate.IsForceCacheBilling(ctx))
				if forwards == 1 {
					return nil, &antigravity.PromptTooLongError{StatusCode: 400}
				}
				return &forwardcore.MessagesResult{Model: "target-model"}, nil
			},
		},
		Selection: SelectionPorts{ReportSchedule: func(*gatewaycapture.SelectionResult, int64, bool, *forwardcore.MessagesResult) {}},
	})
	bridge.provider = gatewaycapture.NewExecutionProvider(&provider.Record{ID: 100, Platform: "antigravity", Type: "oauth"})
	bridge.attemptParsedReq = &requeststate.ParsedRequest{Model: "client-model", Body: requeststate.NewRequestBodyRef([]byte(`{"model":"client-model"}`))}
	require.False(t, requeststate.IsForceCacheBilling(bridge.Context()))
	first := bridge.Forward(textflow.AttemptState{SwitchCount: 1, ForceCacheBilling: true})
	require.Equal(t, textflow.FailurePromptTooLong, first.Kind)
	require.True(t, bridge.Fallback(first.Err, false))
	require.True(t, requeststate.IsForceCacheBilling(bridge.Context()))
	second := bridge.Forward(textflow.AttemptState{})
	require.NoError(t, second.Err)
	require.Equal(t, 2, forwards)
}

// TestMessageGroupFallbackClearsStickyWhenTargetHasNoBinding fails sticky
// lookup after successful target authorization. Fallback must still succeed
// while clearing bound-provider flags and prefetch hints instead of retaining
// the old group’s association.
func TestMessageGroupFallbackClearsStickyWhenTargetHasNoBinding(t *testing.T) {
	bridge, _, _ := newFallbackMessageBridge(t, Bindings{
		ResolveFallback: func(_ context.Context, key *apikey.APIKey, id int64, _ protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
			return authorizedMessageFallback(key, id), nil, nil
		},
		Selection: SelectionPorts{CachedSession: func(context.Context, *int64, string) (int64, error) {
			return 0, errors.New("missing")
		}},
	})
	require.True(t, bridge.Fallback(&antigravity.PromptTooLongError{StatusCode: 400}, false))
	require.False(t, bridge.hasBoundSession)
	require.Zero(t, bridge.sessionBoundProviderID)
	require.Zero(t, requeststate.ExecutionHintsFromContext(bridge.Context()).PrefetchedStickyProviderID.Value)
}
