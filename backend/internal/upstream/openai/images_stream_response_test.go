package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestReadImagesSSECompletionAndFailures checks final-image aggregation and terminal
// validation with synthetic SSE frames. Progressive events, repeated completions,
// errors, truncated frames, missing images, and limits must not fabricate success.
func TestReadImagesSSECompletionAndFailures(t *testing.T) {
	completed := "event: image_generation.completed\ndata: {\"b64_json\":\"Zmlyc3Q=\",\ndata: \"created_at\":123,\"size\":\"1024x1024\",\"usage\":{\"input_tokens\":4,\"output_tokens\":7}}\n\n"
	partial := "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"preview\"}\n\n"
	second := "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"c2Vjb25k\",\"output_format\":\"webp\"}\n\n"
	for _, tc := range []struct {
		name     string // fixture scenario label
		body     string // upstream event bytes
		expected int    // requested completion count
		limit    int64  // response byte budget
		count    int    // observed completed images
		failed   bool   // expected terminal failure
	}{
		{"deduplicated multiline", partial + completed + completed + second + "data: [DONE]\n\n", 2, 1 << 20, 2, false},
		{"normal EOF", completed, 1, 1 << 20, 1, false},
		{"partial only", partial, 1, 1 << 20, 0, true},
		{"missing second", completed, 2, 1 << 20, 1, true},
		{"error after image", completed + "event: error\ndata: {\"message\":\"failed\",\"code\":\"upstream_error\"}\n\n", 1, 1 << 20, 1, true},
		{"typed error after image", completed + "event: error\ndata: {\"type\":\"server_error\",\"message\":\"failed\"}\n\n", 1, 1 << 20, 1, true},
		{"truncated frame", completed + "data: {\"type\":", 1, 1 << 20, 1, true},
		{"invalid JSON", "data: not-json\n\n", 1, 1 << 20, 0, true},
		{"oversized frame", completed, 1, 32, 0, true},
		{"empty completion", "data: {\"type\":\"image_generation.completed\"}\n\n", 1, 1 << 20, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(tc.body))}
			defer func() { _ = resp.Body.Close() }()
			options := imagesTestResponseOptions()
			options.ExpectedImages = tc.expected
			options.ReadLimit = func() int64 { return tc.limit }
			emissions := 0
			result, err := ReadImagesSSE(t.Context(), resp, options, func(_ string, _ []byte) { emissions++ }, nil)
			require.Equal(t, tc.failed, err != nil)
			require.Equal(t, tc.count, result.Counter.Count())
			if tc.failed {
				return
			}
			body, err := result.JSON("b64_json")
			require.NoError(t, err)
			require.Len(t, gjson.GetBytes(body, "data").Array(), tc.count)
			require.Equal(t, 4, result.Usage.InputTokens)
			require.Equal(t, 7, result.Usage.OutputTokens)
			require.EqualValues(t, 123, gjson.GetBytes(body, "created").Int())
			require.NotContains(t, string(body), "preview")
			require.LessOrEqual(t, emissions, tc.count+1)
			urlBody, err := result.JSON("url")
			require.NoError(t, err)
			require.Equal(t, "data:image/png;base64,Zmlyc3Q=", gjson.GetBytes(urlBody, "data.0.url").String())
		})
	}
}

// TestReadImagesSSEURLFailureRetainsUsage observes a completed URL before download
// fails. The reader must retain the generation count and usage while returning
// a terminal error that callers cannot mistake for a retryable generation.
func TestReadImagesSSEURLFailureRetainsUsage(t *testing.T) {
	body := "data: {\"type\":\"image_edit.completed\",\"url\":\"https://image.invalid/out.png\",\"usage\":{\"output_tokens\":9}}\n\n"
	for _, failed := range []bool{false, true} {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
		options := imagesTestResponseOptions()
		calls := 0
		options.FetchImage = func(rawURL string) (string, error) {
			calls++
			require.Equal(t, "https://image.invalid/out.png", rawURL)
			if failed {
				return "", errors.New("download failed")
			}
			return "aW1hZ2U=", nil
		}
		result, err := ReadImagesSSE(t.Context(), resp, options, nil, nil)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, failed, err != nil)
		require.Equal(t, 1, calls)
		require.Equal(t, 1, result.Counter.Count())
		require.Equal(t, 9, result.Usage.OutputTokens)
		if !failed {
			payload, err := result.JSON("")
			require.NoError(t, err)
			require.Equal(t, "aW1hZ2U=", gjson.GetBytes(payload, "data.0.b64_json").String())
		}
	}
}

// TestReadImagesSSECancellationAndIdle releases blocked readers under virtual time.
// Cancellation keeps its cause, idle expiration fails completion, and a failed
// upstream read cannot turn a previous completed image into a successful response.
func TestReadImagesSSECancellationAndIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, cancelRequest := range []bool{false, true} {
			reader, writer := io.Pipe()
			ctx, cancel := context.WithCancel(t.Context())
			options := imagesTestResponseOptions()
			options.StreamInterval = func() time.Duration { return time.Second }
			if cancelRequest {
				cancel()
			}
			_, err := ReadImagesSSE(ctx, &http.Response{Body: reader}, options, nil, nil)
			require.Error(t, err)
			if cancelRequest {
				require.ErrorIs(t, err, context.Canceled)
			}
			// Closing both pipe ends releases scanning before the virtual-time fixture
			// exits; cancellation alone does not own the upstream response resource.
			require.NoError(t, reader.Close())
			require.NoError(t, writer.Close())
			cancel()
		}
	})
	body := "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"image\"}\n\n"
	resp := &http.Response{Body: io.NopCloser(io.MultiReader(strings.NewReader(body), iotest.ErrReader(errors.New("broken socket"))))}
	defer func() { _ = resp.Body.Close() }()
	result, err := ReadImagesSSE(t.Context(), resp, imagesTestResponseOptions(), nil, nil)
	require.Error(t, err)
	require.Equal(t, 1, result.Counter.Count())
}

// TestReadImagesSSECompletionMetadata sends an ordinary completion whose
// revised prompt contains a diagnostic identifier. It must remain an actual
// completed image regardless of incidental text inside unrelated metadata.
func TestReadImagesSSECompletionMetadata(t *testing.T) {
	for _, payload := range []string{
		`{"type":"image_generation.completed","b64_json":"aW1hZ2U=","revised_prompt":"draw a diagram labeled partial_image","usage":{"output_tokens":9}}`,
		`{"type":"image_generation.completed","item":{"type":"image","b64_json":"aW1hZ2U=","revised_prompt":"partial_image"},"usage":{"output_tokens":9}}`,
		`{"type":"image_generation.completed","b64_json":"aW1hZ2U=","data":[{"b64_json":"unrelated"}],"usage":{"output_tokens":9}}`,
	} {
		response := "data: " + payload + "\n\ndata: " + payload + "\n\n"
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(response))}
		result, err := ReadImagesSSE(t.Context(), resp, imagesTestResponseOptions(), nil, nil)
		require.NoError(t, resp.Body.Close())
		require.NoError(t, err)
		require.Equal(t, 9, result.Usage.OutputTokens)
		require.Equal(t, 1, result.Counter.Count())
		require.Len(t, result.Images, 1)
	}
}

// TestReadImagesBufferedResponseSkipsPreviews supplies an unavailable URL
// preview followed by a valid final image. Buffered consumers discard previews
// and therefore must not fail their final image because preview delivery failed.
func TestReadImagesBufferedResponseSkipsPreviews(t *testing.T) {
	payload := "data: {\"type\":\"image_generation.partial_image\",\"url\":\"https://image.invalid/preview\"}\n\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"aW1hZ2U=\"}\n\n"
	options := imagesTestResponseOptions()
	calls := 0
	options.FetchImage = func(string) (string, error) { calls++; return "", errors.New("preview expired") }
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(payload)), Header: http.Header{"Content-Type": {"text/event-stream"}}}
	defer func() { _ = resp.Body.Close() }()
	body, _, err := ReadImagesBufferedResponse(t.Context(), resp, options, "b64_json")

	require.NoError(t, err)
	require.Zero(t, calls)
	require.Equal(t, "aW1hZ2U=", gjson.GetBytes(body, "data.0.b64_json").String())
}
