package openaiattempt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	openaiwire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// Group fallback replaces request routing after a recoverable prompt-size
// failure. The fixtures inspect the destination's mapping and sticky identity,
// the retained forced platform, and output/cancellation guards that prevent a
// second authorization path from starting after recovery is no longer
// available.

// TestGroupFallbackRebuildsPlanAndKeepsForcedPlatform switches from one group
// to an authorized mapped destination. It checks rebuilt input, destination
// sticky prefetch, retained forced platform, unchanged original key group, and
// a single resolution attempt.
func TestGroupFallbackRebuildsPlanAndKeepsForcedPlatform(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := requeststate.WithClientProtocol(context.Background(), protocol.ProtocolAnthropicMessages)
	ctx = apikey.WithForcePlatform(ctx, "antigravity")
	ctx = requeststate.WithPrefetchedStickySession(ctx, 12, 1)
	c.Request = httptest.NewRequest(http.MethodPost, "/antigravity/v1/messages", nil).WithContext(ctx)
	targetID := int64(2)
	original := &apikey.APIKey{ID: 10, GroupID: new(int64(1)), Group: &routing.Group{ID: 1, FallbackGroupIDOnInvalidRequest: &targetID}}
	target := apikey.CopyAPIKey(original)
	target.GroupID = &targetID
	target.Group = &routing.Group{ID: 2, Status: routing.StatusActive, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages}}
	resolveCount := 0
	fixed := &openAIExecutionDependencies{
		replaceModelInBody: openaiwire.ReplaceModelInBody,
		fallback: GroupFallbackPorts{
			Resolve: func(ctx context.Context, key *apikey.APIKey, id int64, source protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
				resolveCount++
				require.Same(t, original, key)
				require.Equal(t, targetID, id)
				require.Equal(t, protocol.ProtocolAnthropicMessages, source)
				return target, nil, nil
			},
			Plan: func(ctx context.Context, key *apikey.APIKey, requested string) routing.RoutePlan {
				require.Equal(t, "public-model", requested)
				return routing.Plan(routing.PlanInput{Group: key.Group, RequestedModel: requested, ClientProtocol: protocol.ProtocolAnthropicMessages, GroupMapping: routing.GroupMappingResult{MappedModel: "target-model", Mapped: true}})
			},
		},
		sessions: SessionPorts{StickyProviderID: func(ctx context.Context, groupID *int64, _ string) int64 {
			require.Equal(t, targetID, *groupID)
			oldGroup, _ := requeststate.PrefetchedStickyGroupIDFromContext(ctx)
			require.Zero(t, oldGroup)
			return 23
		}},
	}
	started := false
	bridge := &responsesAttemptBridge{fixed: fixed, c: c, apiKey: original, reqModel: "public-model", body: []byte(`{"model":"public-model"}`), forwardBody: []byte(`{"model":"old-model"}`), streamStarted: &started}
	handled, retry := bridge.TryGroupFallback(&antigravity.PromptTooLongError{})
	require.True(t, handled)
	require.True(t, retry)
	require.Equal(t, "target-model", gjson.GetBytes(bridge.forwardBody, "model").String())
	require.Equal(t, int64(1), *original.GroupID)
	require.Same(t, target, bridge.apiKey)
	forced, _ := apikey.ForcePlatformFromContext(bridge.Context())
	require.Equal(t, "antigravity", forced)
	sticky, _ := requeststate.PrefetchedStickyProviderIDFromContext(bridge.Context())
	require.Equal(t, int64(23), sticky)
	require.True(t, bridge.hasBoundSession)
	handled, retry = bridge.TryGroupFallback(&antigravity.PromptTooLongError{})
	require.False(t, handled)
	require.False(t, retry)
	require.Equal(t, 1, resolveCount)
}

// TestGroupFallbackStopsAfterOutputOrCancellation supplies committed headers, a
// started stream, and a cancelled request. It checks each condition returns
// unhandled without requiring fallback authorization or planning callbacks.
func TestGroupFallbackStopsAfterOutputOrCancellation(t *testing.T) {
	for _, scenario := range []string{"output", "stream", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			started := scenario == "stream"
			if scenario == "output" {
				c.Writer.WriteHeaderNow()
			}
			if scenario == "cancelled" {
				cancel()
			}
			targetID := int64(2)
			bridge := &responsesAttemptBridge{c: c, apiKey: &apikey.APIKey{Group: &routing.Group{ID: 1, FallbackGroupIDOnInvalidRequest: &targetID}}, streamStarted: &started}
			handled, retry := bridge.TryGroupFallback(&antigravity.PromptTooLongError{})
			require.False(t, handled)
			require.False(t, retry)
		})
	}
}
