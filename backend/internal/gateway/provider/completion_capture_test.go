package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestCompletionCaptureKeepsTurnTimeAndIndependentInputs checks isolation from later request, tier, and price-card changes.
func TestCompletionCaptureKeepsTurnTimeAndIndependentInputs(t *testing.T) {
	multiplier := 1.5
	groupID := int64(17)
	key := &apikey.APIKey{ID: 2, GroupID: &groupID, Group: &routing.Group{
		ID: groupID, RateMultiplier: 0.25,
	}}
	user := &identity.User{ID: 3, Balance: 9}
	target := &providercore.Record{ID: 4, RateMultiplier: &multiplier, Extra: map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: "X-Request-ID"}}
	result := &forward.OpenAIResult{RequestID: "upstream", Model: "model", ImageOutputSizes: []string{"1K"}, ImageSizeBreakdown: map[string]int{"1K": 1}}
	turnAt := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	ctx := context.WithValue(context.Background(), telemetry.RequestID, "local")
	in := &OpenAICapture{
		APIKey: key, User: user, Provider: target, Result: result, PricingAt: turnAt,
		RequestBody: []byte(`{"reasoning":{"effort":"high"}}`), Subscription: &billing.UserSubscription{ID: 5},
	}
	// Changes made before capture must be included in the snapshot.
	user.Balance = 10
	out := CaptureOpenAI(ctx, in)
	user.Balance = 90
	key.Group.RateMultiplier = 9
	multiplier = 8
	groupID = 99
	result.ImageOutputSizes[0] = "4K"
	result.ImageSizeBreakdown["1K"] = 20
	in.RequestBody[0] = '!'
	require.Equal(t, "local", out.RequestID)
	require.Equal(t, "client:local", out.BillingKey)
	require.Equal(t, turnAt, out.PricingAt)
	require.Equal(t, 10.0, out.User.Balance)
	require.Equal(t, int64(17), *out.APIKey.GroupID)
	require.Equal(t, 0.25, out.APIKey.Group.RateMultiplier)
	require.Equal(t, 1.5, out.Provider.RateMultiplier)
	require.Equal(t, []string{"1K"}, out.Result.ImageOutputSizes)
	require.Equal(t, 1, out.Result.ImageSizeBreakdown["1K"])
	require.Equal(t, "high", *out.RequestedReasoningEffort)
}

// TestCompletionCapturePreservesQuotaCapabilityPresence checks that an interface containing a typed nil still denotes
// a supplied capability.
func TestCompletionCapturePreservesQuotaCapabilityPresence(t *testing.T) {
	var absent QuotaUpdater
	var typedNil *apikey.APIKeyService
	for _, tc := range []struct {
		name  string
		value QuotaUpdater
		want  bool
	}{{"absent", absent, false}, {"typed-nil", typedNil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, CaptureMessages(context.Background(), &MessagesCapture{APIKeyService: tc.value}).QuotaUpdates)
			require.Equal(t, tc.want, CaptureOpenAI(context.Background(), &OpenAICapture{APIKeyService: tc.value}).QuotaUpdates)
		})
	}
}

// TestCompletionCyberInputFreezesLegacyProjection checks snapshot isolation from user, provider, Key, and subscription inputs.
func TestCompletionCyberInputFreezesLegacyProjection(t *testing.T) {
	group := int64(7)
	rate := 1.25
	key := &apikey.APIKey{ID: 2, UserID: 1, User: &identity.User{ID: 1, Balance: 12}, GroupID: &group}
	provider := &providercore.Record{ID: 3, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, RateMultiplier: &rate}
	sub := &billing.UserSubscription{ID: 4, Plan: &billing.SubscriptionPlan{GroupIDs: []int64{7}, GroupRateMultipliers: map[int64]float64{7: 1.5}}}
	input := CaptureCyber(context.Background(), CyberCapture{APIKey: key, Provider: provider, Subscription: sub, Model: " model ", RequestID: "request", InputTokens: 5, NativeCompactionV2: true})
	require.NotNil(t, input)
	key.User.Balance = 99
	key.ID = 22
	group = 70
	rate = 9
	provider.ID = 33
	sub.Plan.GroupIDs[0] = 70
	sub.Plan.GroupRateMultipliers[7] = 8
	require.Equal(t, int64(2), input.APIKey.ID)
	require.Equal(t, int64(7), *input.APIKey.GroupID)
	require.Equal(t, 12.0, input.User.Balance)
	require.Equal(t, int64(3), input.Provider.ID)
	require.Equal(t, 1.25, input.Provider.RateMultiplier)
	require.Equal(t, []int64{7}, input.Subscription.Plan.GroupIDs)
	require.Equal(t, 1.5, input.Subscription.Plan.GroupRateMultipliers[7])
	require.Equal(t, "model", input.Result.Model)
	require.Equal(t, 5, input.Result.Usage.InputTokens)
	require.True(t, input.CyberBlocked)
	require.True(t, input.NativeCompactionV2)
}

func TestResolveUsageBillingRequestID_ForcedWebSearchBeatsClientID(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), telemetry.ClientRequestID, "client-shared-id")
	got := CompletionBillingKey(ctx, "web_search:uuid-1")
	require.Equal(t, "web_search:uuid-1", got)
}

func TestResolveUsageBillingRequestID_ClientWinsOverPlainUpstream(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), telemetry.ClientRequestID, "client-shared-id")
	got := CompletionBillingKey(ctx, "resp_abc")
	require.Equal(t, "client:client-shared-id", got)
}

func TestIsForcedUsageBillingRequestID(t *testing.T) {
	t.Parallel()
	require.True(t, completion.ForcedRequestID("web_search:x"))
	require.True(t, completion.ForcedRequestID("grok-video:task-1"))
	require.True(t, completion.ForcedRequestID("grok_audio:up-1"))
	require.True(t, completion.ForcedRequestID("grok_realtime:sess-1"))
	require.False(t, completion.ForcedRequestID("resp_abc"))
}

func TestStableGrokAudioBillingRequestID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "grok_audio:up-1", StableAudioBillingRequestID("up-1"))
	require.Equal(t, "grok_audio:up-1", StableAudioBillingRequestID("grok_audio:up-1"))
	got := StableAudioBillingRequestID("")
	require.True(t, strings.HasPrefix(got, "grok_audio:"))
	require.Greater(t, len(got), len("grok_audio:"))
}

func TestStableGrokRealtimeBillingRequestID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "grok_realtime:s1", StableRealtimeBillingRequestID("s1"))
	require.Equal(t, "grok_realtime:s1", StableRealtimeBillingRequestID("grok_realtime:s1"))
	got := StableRealtimeBillingRequestID("")
	require.True(t, strings.HasPrefix(got, "grok_realtime:"))
}

func TestResolveUsageBillingRequestID_ForcedGrokAudioBeatsClientID(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), telemetry.ClientRequestID, "client-shared-id")
	got := CompletionBillingKey(ctx, StableAudioBillingRequestID("up-9"))
	require.Equal(t, "grok_audio:up-9", got)
}
