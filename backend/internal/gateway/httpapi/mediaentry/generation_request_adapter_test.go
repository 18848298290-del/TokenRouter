package mediaentry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// TestImagesRetryKeepsMixedCandidatePool checks that a Grok attempt can retry on another platform.
func TestImagesRetryKeepsMixedCandidatePool(t *testing.T) {
	groupID := int64(12)
	calls := 0
	runtime := &Runtime{bindings: Bindings{Platform: PlatformPorts{SelectImages: func(_ context.Context, group *int64, _ string, model string, excluded map[int64]struct{}, _ provider.OpenAIImagesCapability) (*gatewayadapter.SelectionResult, scheduler.PlatformDecision, error) {
		calls++
		require.Equal(t, groupID, *group)
		require.Equal(t, "public-image", model)
		platform := "grok"
		if calls == 2 {
			_, ok := excluded[1]
			require.True(t, ok)
			platform = "openai"
		}
		return &gatewayadapter.SelectionResult{Provider: gatewayadapter.NewExecutionProvider(&provider.Record{ID: int64(calls), Platform: platform}), Acquired: true}, scheduler.PlatformDecision{}, nil
	}}}}
	adapter := &generationRequestAdapter{h: runtime, apiKey: &apikey.APIKey{GroupID: &groupID}, requestModel: "public-image", parsed: &media.ImageRequest{RequiredCapability: media.ImageCapabilityBasic}}
	_, ok, err := adapter.SelectGeneration(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, adapter.usesGrok())
	_, ok, err = adapter.SelectGeneration(context.Background(), map[int64]struct{}{1: {}})
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, adapter.usesGrok())
	require.Equal(t, 2, calls)
}

// TestCompleteGrokRecordsVideoTerminalState checks task state reporting even when there is no billable usage.
func TestCompleteGrokRecordsVideoTerminalState(t *testing.T) {
	for _, state := range []string{"running", "completed", "failed", "canceled"} {
		t.Run(state, func(t *testing.T) {
			var child telemetry.RequestRecord
			ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "poll", APIKeyID: 1, State: "running", StartedAt: time.Now()}, func(record telemetry.RequestRecord) {
				if record.ParentRequestID != "" {
					child = record
				}
			})
			tasks := media.NewVideoTasks(nil, nil, media.VideoOptions{})
			runtime := New(Bindings{Dependencies: gatewayhttp.OpenAIDependencies{Gateway: true}, VideoTasks: func() *media.VideoTasks { return tasks }})
			adapter := &generationRequestAdapter{h: runtime, apiKey: &apikey.APIKey{ID: 1}, selection: &gatewayadapter.SelectionResult{Provider: &gatewayadapter.ExecutionProvider{}}, endpoint: grok.GrokMediaEndpointVideoStatus, requestID: "video-1"}
			// Convert once at the media result boundary and again at completion.
			value := generationResultView(&forward.OpenAIResult{VideoState: state})
			adapter.completeGrok(ctx, value)
			require.Equal(t, state, child.State)
			require.Equal(t, "poll", child.ParentRequestID)
			require.Contains(t, child.Aliases, telemetry.RequestAlias{Kind: "billing", Value: "grok-video:video-1"})
			if state == "running" {
				require.Nil(t, child.FinishedAt)
			} else {
				require.NotNil(t, child.FinishedAt)
			}
		})
	}
}
