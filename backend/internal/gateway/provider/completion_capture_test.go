package provider

import (
	"context"
	"net/http"
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

// videoDiagnosticUsageWriter accepts rows while retaining the real completion
// writer's diagnostic behavior for a derived video request record.
type videoDiagnosticUsageWriter struct{}

// TestCompletionCaptureKeepsTurnTimeAndIndependentInputs captures a WS pricing
// time with mutable payer, Key, multiplier, image metadata, and request bytes.
// Later mutations leave the asserted snapshot values and requested reasoning
// effort at their capture observations.
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

// TestCompletionCapturePreservesQuotaCapabilityPresence compares an absent
// interface with one containing a typed nil API Key service. Both Messages and
// OpenAI capture record capability presence rather than invoking that service.
func TestCompletionCapturePreservesQuotaCapabilityPresence(t *testing.T) {
	var absent QuotaUpdater
	var typedNil *apikey.APIKeyService
	for _, tc := range []struct {
		name  string       // quota interface presence case
		value QuotaUpdater // supplied quota capability interface
		want  bool         // expected quota capability presence
	}{{"absent", absent, false}, {"typed-nil", typedNil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, CaptureMessages(context.Background(), &MessagesCapture{APIKeyService: tc.value}).QuotaUpdates)
			require.Equal(t, tc.want, CaptureOpenAI(context.Background(), &OpenAICapture{APIKeyService: tc.value}).QuotaUpdates)
		})
	}
}

// TestCompletionCyberInputFreezesLegacyProjection captures a cyber failure
// with mutable Key, payer, provider multiplier, and subscription plan values.
// The worker input retains observed identifiers, rates, token usage, trimmed
// model, and failure/compaction flags.
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

// TestResolveUsageBillingRequestID_ForcedWebSearchBeatsClientID supplies a
// compatibility context request seed and a web-search operation key. Billing
// identity selects the recognized operation key for that completion.
func TestResolveUsageBillingRequestID_ForcedWebSearchBeatsClientID(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), telemetry.ClientRequestID, "client-shared-id")
	got := CompletionBillingKey(ctx, "web_search:uuid-1")
	require.Equal(t, "web_search:uuid-1", got)
}

// TestResolveUsageBillingRequestID_ClientWinsOverPlainUpstream supplies a
// compatibility request seed with an ordinary upstream response ID. Billing
// identity uses the client-prefixed seed instead of the ordinary upstream
// value.
func TestResolveUsageBillingRequestID_ClientWinsOverPlainUpstream(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), telemetry.ClientRequestID, "client-shared-id")
	got := CompletionBillingKey(ctx, "resp_abc")
	require.Equal(t, "client:client-shared-id", got)
}

// TestIsForcedUsageBillingRequestID compares recognized search, video, audio,
// and Realtime operation prefixes with an ordinary Response ID. The operation
// keys qualify as forced identities while the Response ID does not.
func TestIsForcedUsageBillingRequestID(t *testing.T) {
	t.Parallel()
	require.True(t, completion.ForcedRequestID("web_search:x"))
	require.True(t, completion.ForcedRequestID("grok-video:task-1"))
	require.True(t, completion.ForcedRequestID("grok_audio:up-1"))
	require.True(t, completion.ForcedRequestID("grok_realtime:sess-1"))
	require.False(t, completion.ForcedRequestID("resp_abc"))
}

// TestStableGrokAudioBillingRequestID compares plain, already prefixed, and
// blank upstream audio IDs. Existing identity remains stable, while blank
// input generates a nonempty suffix under the Grok audio namespace.
func TestStableGrokAudioBillingRequestID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "grok_audio:up-1", StableAudioBillingRequestID("up-1"))
	require.Equal(t, "grok_audio:up-1", StableAudioBillingRequestID("grok_audio:up-1"))
	got := StableAudioBillingRequestID("")
	require.True(t, strings.HasPrefix(got, "grok_audio:"))
	require.Greater(t, len(got), len("grok_audio:"))
}

// TestStableGrokRealtimeBillingRequestID compares plain, already prefixed, and
// blank Realtime session IDs. Existing identity remains stable and blank input
// returns the Realtime operation prefix with generated identity.
func TestStableGrokRealtimeBillingRequestID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "grok_realtime:s1", StableRealtimeBillingRequestID("s1"))
	require.Equal(t, "grok_realtime:s1", StableRealtimeBillingRequestID("grok_realtime:s1"))
	got := StableRealtimeBillingRequestID("")
	require.True(t, strings.HasPrefix(got, "grok_realtime:"))
}

// TestResolveUsageBillingRequestID_ForcedGrokAudioBeatsClientID supplies a
// compatibility request seed beside a prefixed audio operation ID. Billing
// capture keeps the operation-specific audio key as its deduplication
// identity.
func TestResolveUsageBillingRequestID_ForcedGrokAudioBeatsClientID(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), telemetry.ClientRequestID, "client-shared-id")
	got := CompletionBillingKey(ctx, StableAudioBillingRequestID("up-9"))
	require.Equal(t, "grok_audio:up-9", got)
}

// TestCompletionIdentifiersUseSelectedHeader exercises both completion adapters
// with competing response headers. Request diagnostics and usage select the
// same default or custom header, and missing custom headers leave both empty.
func TestCompletionIdentifiersUseSelectedHeader(t *testing.T) {
	for _, header := range []string{"", "X-Client-Request-ID", "X-Missing"} {
		for _, messages := range []bool{false, true} {
			var saved telemetry.RequestRecord
			ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "local"}, func(r telemetry.RequestRecord) { saved = r })
			ctx = telemetry.WithRequestProvider(ctx, 7, header)
			ctx = telemetry.BeginRequestAttempt(ctx, 1)
			headers := http.Header{"X-Request-Id": {"standard"}, "X-Client-Request-Id": {"custom"}}
			telemetry.ObserveRequestHeaders(ctx, telemetry.RequestAttemptFromContext(ctx), headers, 200)
			ctx = context.WithValue(ctx, telemetry.RequestID, "local")
			target := &providercore.Record{ID: 7, Extra: map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: header}}
			var actual *string
			if messages {
				out := CaptureMessages(ctx, &MessagesCapture{Provider: target, Result: &forward.MessagesResult{RequestID: "unselected", Model: "model", UpstreamHeaders: headers}})
				actual = out.Result.UpstreamRequestID
			} else {
				out := CaptureOpenAI(ctx, &OpenAICapture{Provider: target, Result: &forward.OpenAIResult{RequestID: "unselected", ResponseID: "resp_result", Model: "model", UpstreamHeaders: headers}})
				actual = out.Result.UpstreamRequestID
				require.Contains(t, saved.Aliases, telemetry.RequestAlias{Kind: "response", Value: "resp_result", Source: "response.id", AttemptNumber: 1, ProviderID: 7})
			}
			switch header {
			case "X-Client-Request-ID":
				require.NotNil(t, actual)
				require.Equal(t, "custom", *actual)
				require.Equal(t, "custom", saved.Attempts[0].RequestID)
			case "":
				require.NotNil(t, actual)
				require.Equal(t, "standard", *actual)
				require.Equal(t, "standard", saved.Attempts[0].RequestID)
			default:
				require.Nil(t, actual)
				require.Empty(t, saved.Attempts[0].RequestID)
			}
			for _, alias := range saved.Aliases {
				require.NotEqual(t, "unselected", alias.Value)
				if header != "" {
					require.NotEqual(t, "standard", alias.Value)
				}
			}
		}
	}
}

// TestCompletionMediaRetriesUseCapturedDispatch completes a media retry whose
// selected header is missing or reused. Its frozen dispatch reference chooses
// the successful attempt while usage keeps its existing configured-header value.
func TestCompletionMediaRetriesUseCapturedDispatch(t *testing.T) {
	for _, id := range []string{"", "reused"} {
		for _, video := range []bool{false, true} {
			var saved telemetry.RequestRecord
			root := context.WithValue(t.Context(), telemetry.RequestID, "local")
			root = telemetry.WithRequestCapture(root, telemetry.RequestRecord{RequestID: "local"}, func(r telemetry.RequestRecord) { saved = r })
			root = telemetry.WithRequestProvider(root, 7, "X-Custom")
			headers := http.Header{"X-Custom": {id}}
			first := telemetry.BeginRequestAttempt(root, 0)
			telemetry.ObserveRequestHeaders(first, telemetry.RequestAttemptFromContext(first), headers, 503)
			telemetry.UpdateRequest(root, func(r *telemetry.RequestRecord) { r.Attempts[0].Outcome = "failed" })
			second := telemetry.BeginRequestAttempt(root, 0)
			telemetry.ObserveRequestHeaders(second, telemetry.RequestAttemptFromContext(second), headers, 200)
			result := &forward.OpenAIResult{RequestAttempt: telemetry.RequestAttemptFromContext(second), ResponseID: "resp_media", Model: "embedding", UpstreamHeaders: headers}
			if video {
				result.VideoCount = 1
			}
			// A newer dispatch begins before delayed capture, proving that result ownership
			// remains fixed even when both provider identity and header values are repeated.
			third := telemetry.BeginRequestAttempt(root, 0)
			telemetry.ObserveRequestHeaders(third, telemetry.RequestAttemptFromContext(third), headers, 200)
			target := &providercore.Record{ID: 7, Extra: map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: "X-Custom"}}
			out := CaptureOpenAI(root, &OpenAICapture{Provider: target, Result: result})
			require.Nil(t, out.Result.Timings)
			if !video {
				require.Equal(t, "client:local", out.BillingKey)
			}
			require.Equal(t, "failed", saved.Attempts[0].Outcome)
			require.Equal(t, "completed", saved.Attempts[1].Outcome)
			require.Equal(t, "running", saved.Attempts[2].Outcome)
			if !video {
				require.Contains(t, saved.Aliases, telemetry.RequestAlias{Kind: "response", Value: "resp_media", Source: "response.id", ProviderID: 7, AttemptNumber: 2})
			}
			if id == "" {
				require.Nil(t, out.Result.UpstreamRequestID)
			} else {
				require.Equal(t, id, *out.Result.UpstreamRequestID)
			}
		}
	}
}

// Create acknowledges fixture rows without changing their configured upstream ID.
func (videoDiagnosticUsageWriter) Create(context.Context, *completion.UsageLog) (bool, error) {
	return true, nil
}

// TestVideoUsageKeepsOneDiagnosticID sends a captured video completion
// through the real usage writer. Its truncated usage value must not become a
// second unclassified diagnostic alias beside the full selected response header.
func TestVideoUsageKeepsOneDiagnosticID(t *testing.T) {
	records := make(map[string]telemetry.RequestRecord)
	observe := func(r telemetry.RequestRecord) {
		if previous, ok := records[r.RequestID]; ok {
			r = telemetry.MergeRequestRecord(previous, r)
		}
		records[r.RequestID] = r
	}
	ctx := context.WithValue(t.Context(), telemetry.RequestID, "poll")
	ctx = telemetry.WithRequestCapture(ctx, telemetry.RequestRecord{RequestID: "poll", StartedAt: time.Now()}, observe)
	ctx = telemetry.WithRequestProvider(ctx, 7, "X-Custom")
	exchange := telemetry.BeginRequestAttempt(ctx, 0)
	full := strings.Repeat("a", 200)
	headers := http.Header{"X-Custom": {full}}
	telemetry.ObserveRequestHeaders(exchange, telemetry.RequestAttemptFromContext(exchange), headers, 200)
	target := &providercore.Record{ID: 7, Extra: map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: "X-Custom"}}
	input := CaptureOpenAI(ctx, &OpenAICapture{Provider: target, Result: &forward.OpenAIResult{RequestAttempt: telemetry.RequestAttemptFromContext(exchange), RequestID: "grok-video:task", ResponseID: "task", Model: "video", VideoCount: 1, UpstreamHeaders: headers}})
	recorder := completion.NewRecorder(completion.Dependencies{Logs: videoDiagnosticUsageWriter{}, RequestRecords: observe}, completion.RecorderOptions{})
	row := &completion.UsageLog{RequestID: input.RequestID, BillingKey: input.BillingKey, ProviderID: 7, UpstreamRequestID: input.Result.UpstreamRequestID}
	completion.WrapTaskContext(ctx, func(worker context.Context) { recorder.WriteUsage(worker, row, "review") })(t.Context())
	t.Logf("video aliases: %+v", records[input.RequestID].Aliases)
	require.Equal(t, full[:128], *row.UpstreamRequestID)
	for _, alias := range records[input.RequestID].Aliases {
		require.NotEqual(t, "upstream", alias.Kind, "usage must not add an unclassified truncated alias")
	}
}
