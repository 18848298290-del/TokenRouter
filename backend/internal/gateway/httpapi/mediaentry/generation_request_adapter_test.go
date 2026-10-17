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
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// TestImagesRetryKeepsMixedCandidatePool selects Grok and then OpenAI for one
// ordinary Images request. The second selection sees the excluded first
// provider, and usesGrok follows the current target rather than retaining the
// first platform.
func TestImagesRetryKeepsMixedCandidatePool(t *testing.T) {
	groupID := int64(12)
	calls := 0
	runtime := &Runtime{bindings: Bindings{Platform: PlatformPorts{SelectImages: func(_ context.Context, group *int64, _ string, model string, excluded map[int64]struct{}, _ provider.OpenAIImagesCapability) (*gatewayadapter.SelectionResult, scheduler.PlatformDecision, error) {
		calls++
		require.Equal(t, groupID, *group)
		require.Equal(t, "public-image", model)
		platform := "grok"
		// The second selected target changes platform after exclusion, modeling the
		// image loop's shared candidate pool without invoking upstream execution or admission.
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

// TestCompleteGrokRecordsVideoTerminalState completes running, successful,
// failed and canceled video observations with no billable usage. Related task
// records retain parent correlation and stable billing alias, with finish
// time set for terminal states.
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
			// Round-trip the video state through both result representations so telemetry
			// still receives terminal task identity when the result carries zero usage.
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

// TestGenerationResultViewCopiesFieldsAndMutableStorage converts every media
// observation from a forwarding result. Changes to either representation keep
// the other representation's timing, image collections and header storage intact.
func TestGenerationResultViewCopiesFieldsAndMutableStorage(t *testing.T) {
	source := generationConversionForwardFixture()
	result := generationResultView(source)
	require.Equal(t, generationConversionMediaFixture(), result)

	source.Timings.Values["upstream_first_output_ms"] = 99
	source.ImageOutputSizes[0] = "source-size"
	source.ImageSizeBreakdown["1K"] = 7
	source.ImageSizeBreakdown["source-only"] = 1
	source.UpstreamHeaders["Trace"][0] = "source-upstream"
	source.UpstreamHeaders["SourceOnly"] = []string{"upstream"}
	source.ResponseHeaders["Result"][0] = "source-response"
	source.ResponseHeaders["SourceOnly"] = []string{"response"}
	require.Equal(t, generationConversionMediaFixture(), result)

	result.Timings.Values["upstream_first_output_ms"] = 101
	result.ImageOutputSizes[0] = "returned-size"
	result.ImageSizeBreakdown["1K"] = 9
	result.ImageSizeBreakdown["returned-only"] = 1
	result.Headers["Trace"][0] = "returned-upstream"
	result.Headers["ReturnedOnly"] = []string{"upstream"}
	result.ResponseHeaders["Result"][0] = "returned-response"
	result.ResponseHeaders["ReturnedOnly"] = []string{"response"}
	require.Equal(t, 99, *source.Timings.TTFTMs())
	require.Equal(t, "source-size", source.ImageOutputSizes[0])
	require.Equal(t, 7, source.ImageSizeBreakdown["1K"])
	require.NotContains(t, source.ImageSizeBreakdown, "returned-only")
	require.Equal(t, "source-upstream", source.UpstreamHeaders["Trace"][0])
	require.NotContains(t, source.UpstreamHeaders, "ReturnedOnly")
	require.Equal(t, "source-response", source.ResponseHeaders["Result"][0])
	require.NotContains(t, source.ResponseHeaders, "ReturnedOnly")
}

// TestLegacyGenerationResultCopiesFieldsAndMutableStorage converts every media
// observation for forwarding consumers. Changes to the source and returned
// result keep their timing, image collections and header storage independent.
func TestLegacyGenerationResultCopiesFieldsAndMutableStorage(t *testing.T) {
	source := generationConversionMediaFixture()
	result := legacyGenerationResult(source)
	require.Equal(t, generationConversionForwardFixture(), result)

	source.Timings.Values["upstream_first_output_ms"] = 99
	source.ImageOutputSizes[0] = "source-size"
	source.ImageSizeBreakdown["1K"] = 7
	source.ImageSizeBreakdown["source-only"] = 1
	source.Headers["Trace"][0] = "source-upstream"
	source.Headers["SourceOnly"] = []string{"upstream"}
	source.ResponseHeaders["Result"][0] = "source-response"
	source.ResponseHeaders["SourceOnly"] = []string{"response"}
	require.Equal(t, generationConversionForwardFixture(), result)

	result.Timings.Values["upstream_first_output_ms"] = 101
	result.ImageOutputSizes[0] = "returned-size"
	result.ImageSizeBreakdown["1K"] = 9
	result.ImageSizeBreakdown["returned-only"] = 1
	result.UpstreamHeaders["Trace"][0] = "returned-upstream"
	result.UpstreamHeaders["ReturnedOnly"] = []string{"upstream"}
	result.ResponseHeaders["Result"][0] = "returned-response"
	result.ResponseHeaders["ReturnedOnly"] = []string{"response"}
	require.Equal(t, 99, *source.Timings.TTFTMs())
	require.Equal(t, "source-size", source.ImageOutputSizes[0])
	require.Equal(t, 7, source.ImageSizeBreakdown["1K"])
	require.NotContains(t, source.ImageSizeBreakdown, "returned-only")
	require.Equal(t, "source-upstream", source.Headers["Trace"][0])
	require.NotContains(t, source.Headers, "ReturnedOnly")
	require.Equal(t, "source-response", source.ResponseHeaders["Result"][0])
	require.NotContains(t, source.ResponseHeaders, "ReturnedOnly")
}

// TestGenerationResultConversionsPreserveCollectionShapes passes absent,
// empty and mixed header values through both conversion directions. Each result
// retains collection presence and gives both header fields their own maps.
func TestGenerationResultConversionsPreserveCollectionShapes(t *testing.T) {
	t.Run("nil_result", func(t *testing.T) {
		require.Nil(t, generationResultView(nil))
		require.Nil(t, legacyGenerationResult(nil))
	})
	for _, testCase := range []struct {
		name               string              // conversion shape case
		headers            map[string][]string // supplied header collection
		imageOutputSizes   []string            // supplied image dimensions
		imageSizeBreakdown map[string]int      // supplied image counts
	}{
		{name: "nil_collections"},
		{
			name:               "empty_collections",
			headers:            map[string][]string{},
			imageOutputSizes:   []string{},
			imageSizeBreakdown: map[string]int{},
		},
		{
			name: "mixed_header_values",
			headers: map[string][]string{
				"Nil":    nil,
				"Empty":  {},
				"Values": {"first", "second"},
			},
		},
	} {
		t.Run(testCase.name+"/view", func(t *testing.T) {
			result := generationResultView(&forward.OpenAIResult{
				UpstreamHeaders:    testCase.headers,
				ResponseHeaders:    testCase.headers,
				ImageOutputSizes:   testCase.imageOutputSizes,
				ImageSizeBreakdown: testCase.imageSizeBreakdown,
			})
			require.Equal(t, testCase.headers, result.Headers)
			require.Equal(t, testCase.headers, result.ResponseHeaders)
			require.Equal(t, testCase.imageOutputSizes, result.ImageOutputSizes)
			require.Equal(t, testCase.imageSizeBreakdown, result.ImageSizeBreakdown)
			require.Nil(t, result.Timings.TTFTMs())
			if testCase.headers != nil {
				result.Headers["ReturnedOnly"] = []string{"upstream"}
				require.NotContains(t, testCase.headers, "ReturnedOnly")
				require.NotContains(t, result.ResponseHeaders, "ReturnedOnly")
				result.ResponseHeaders["ResponseOnly"] = []string{"response"}
				require.NotContains(t, testCase.headers, "ResponseOnly")
				require.NotContains(t, result.Headers, "ResponseOnly")
			}
		})
		t.Run(testCase.name+"/legacy", func(t *testing.T) {
			result := legacyGenerationResult(&media.GenerationResult{
				Headers:            testCase.headers,
				ResponseHeaders:    testCase.headers,
				ImageOutputSizes:   testCase.imageOutputSizes,
				ImageSizeBreakdown: testCase.imageSizeBreakdown,
			})
			require.Equal(t, testCase.headers, result.UpstreamHeaders)
			require.Equal(t, testCase.headers, result.ResponseHeaders)
			require.Equal(t, testCase.imageOutputSizes, result.ImageOutputSizes)
			require.Equal(t, testCase.imageSizeBreakdown, result.ImageSizeBreakdown)
			require.Nil(t, result.Timings.TTFTMs())
			if testCase.headers != nil {
				result.UpstreamHeaders["ReturnedOnly"] = []string{"upstream"}
				require.NotContains(t, testCase.headers, "ReturnedOnly")
				require.NotContains(t, result.ResponseHeaders, "ReturnedOnly")
				result.ResponseHeaders["ResponseOnly"] = []string{"response"}
				require.NotContains(t, testCase.headers, "ResponseOnly")
				require.NotContains(t, result.UpstreamHeaders, "ResponseOnly")
			}
		})
	}
}

// generationConversionForwardFixture supplies distinct forwarding observations
// for checking every mapped media field. Each call allocates its own timing,
// image collections and header values so expected results remain independent.
func generationConversionForwardFixture() *forward.OpenAIResult {
	first := 17
	return &forward.OpenAIResult{
		RequestID:     "upstream-request",
		ResponseID:    "media-response",
		Model:         "client-model",
		BillingModel:  "billing-model",
		UpstreamModel: "upstream-model",
		Usage: protocolopenai.ForwardUsage{
			InputTokens:              11,
			ImageInputTokens:         13,
			OutputTokens:             19,
			CacheCreationInputTokens: 23,
			CacheReadInputTokens:     29,
			ImageOutputTokens:        31,
		},
		Stream:               true,
		Duration:             43 * time.Millisecond,
		Timings:              &timing.Performance{Stream: true, Values: map[string]int64{"upstream_started_ms": 0, "upstream_first_output_ms": int64(first)}},
		ImageCount:           2,
		VideoCount:           3,
		VideoState:           "completed",
		VideoDurationSeconds: 37,
		ImageSize:            "1K",
		ImageInputSize:       "800x600",
		ImageOutputSize:      "1024x1024",
		ImageSizeSource:      "observed-output",
		VideoResolution:      "720p",
		ImageOutputSizes:     []string{"1024x1024", "2048x2048"},
		ImageSizeBreakdown:   map[string]int{"1K": 1, "2K": 1},
		UpstreamHeaders:      map[string][]string{"Trace": {"upstream", "extra-upstream"}},
		ResponseHeaders:      map[string][]string{"Result": {"response", "extra-response"}},
	}
}

// generationConversionMediaFixture supplies distinct generation observations
// for checking the forwarding result's media fields. Fresh mutable storage
// keeps later source changes from altering the expected timing, collections or headers.
func generationConversionMediaFixture() *media.GenerationResult {
	first := 17
	return &media.GenerationResult{
		RequestID:     "upstream-request",
		ResponseID:    "media-response",
		Model:         "client-model",
		BillingModel:  "billing-model",
		UpstreamModel: "upstream-model",
		Usage: protocolopenai.ForwardUsage{
			InputTokens:              11,
			ImageInputTokens:         13,
			OutputTokens:             19,
			CacheCreationInputTokens: 23,
			CacheReadInputTokens:     29,
			ImageOutputTokens:        31,
		},
		Stream:               true,
		Duration:             43 * time.Millisecond,
		Timings:              &timing.Performance{Stream: true, Values: map[string]int64{"upstream_started_ms": 0, "upstream_first_output_ms": int64(first)}},
		ImageCount:           2,
		VideoCount:           3,
		VideoState:           "completed",
		VideoDurationSeconds: 37,
		ImageSize:            "1K",
		ImageInputSize:       "800x600",
		ImageOutputSize:      "1024x1024",
		ImageSizeSource:      "observed-output",
		VideoResolution:      "720p",
		ImageOutputSizes:     []string{"1024x1024", "2048x2048"},
		ImageSizeBreakdown:   map[string]int{"1K": 1, "2K": 1},
		Headers:              map[string][]string{"Trace": {"upstream", "extra-upstream"}},
		ResponseHeaders:      map[string][]string{"Result": {"response", "extra-response"}},
	}
}
