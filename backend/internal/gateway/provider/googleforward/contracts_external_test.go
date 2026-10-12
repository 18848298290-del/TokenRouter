package googleforward_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

func TestCountGeminiInlineImageOutputs(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    int
	}{
		{
			name: "camelCase inlineData",

			payload: geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiTestPNG + `"}}`),

			want: 1,
		},

		{
			// Accept snake_case fields returned by SDKs and compatible gateways.

			name: "snake_case inline_data",

			payload: geminiImageResponse(`{"inline_data":{"mime_type":"image/png","data":"` + geminiTestPNG + `"}}`),

			want: 1,
		},

		{
			name: "text and image mixed",

			payload: geminiImageResponse(`{"text":"here you go"},` +
				`{"inlineData":{"mimeType":"image/jpeg","data":"` + geminiTestPNG + `"}}`),

			want: 1,
		},

		{
			name: "multiple images",

			payload: geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiTestPNG + `"}},` +
				`{"inlineData":{"mimeType":"image/webp","data":"` + geminiTestPNG + `"}}`),

			want: 2,
		},

		{
			name: "uppercase mime type",

			payload: geminiImageResponse(`{"inlineData":{"mimeType":"IMAGE/PNG","data":"` + geminiTestPNG + `"}}`),

			want: 1,
		},

		{
			name:    "text only",
			payload: geminiImageResponse(`{"text":"no image here"}`),
			want:    0,
		},

		{
			// Non-image attachments, such as audio, do not count as images.

			name: "non image mime type",

			payload: geminiImageResponse(`{"inlineData":{"mimeType":"audio/mpeg","data":"` + geminiTestPNG + `"}}`),

			want: 0,
		},

		{
			name: "empty data is not billable",

			payload: geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":""}}`),

			want: 0,
		},

		{name: "empty payload", payload: "", want: 0},

		{name: "invalid json", payload: "not-json", want: 0},

		{name: "error response", payload: `{"error":{"code":429,"message":"quota"}}`, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, gemininative.CountGeminiInlineImageOutputs([]byte(tc.payload)))
		})
	}
}

// TestObserveGeminiImageOutputs_CumulativeChunksDoNotDoubleCount covers repeated images in cumulative SSE chunks.
// The counter uses the largest image count in one payload to prevent duplicate billing.
func TestObserveGeminiImageOutputs_CumulativeChunksDoNotDoubleCount(t *testing.T) {
	c := newGeminiImageTestContext(t)
	c.Images = 0

	oneImage := geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiTestPNG + `"}}`)
	for range 4 {
		c.ObserveImagesForTest([]byte(oneImage))
	}

	require.Equal(t, 1, c.Images)
}

func TestObserveGeminiImageOutputs_KeepsLargestChunk(t *testing.T) {
	c := newGeminiImageTestContext(t)
	c.Images = 0

	c.ObserveImagesForTest([]byte(geminiImageResponse(`{"text":"working"}`)))
	c.ObserveImagesForTest([]byte(geminiImageResponse(
		`{"inlineData":{"mimeType":"image/png","data":"` + geminiTestPNG + `"}},` +
			`{"inlineData":{"mimeType":"image/png","data":"` + geminiTestPNG + `"}}`)))
	// A usage-only trailing chunk leaves the observed image count unchanged.
	c.ObserveImagesForTest([]byte(`{"usageMetadata":{"promptTokenCount":9}}`))

	require.Equal(t, 2, c.Images)
}

// TestBeginGeminiImageOutputObservation_ResetsPerForward checks reset before another forwarding attempt.
// Images from a failed provider must not enter the next provider's bill.
func TestBeginGeminiImageOutputObservation_ResetsPerForward(t *testing.T) {
	c := newGeminiImageTestContext(t)
	oneImage := []byte(geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiTestPNG + `"}}`))

	c.Images = 0
	c.ObserveImagesForTest(oneImage)
	require.Equal(t, 1, c.Images)

	c.Images = 0
	require.Equal(t, 0, c.Images)
	c.ObserveImagesForTest(oneImage)
	require.Equal(t, 1, c.Images)
}

// TestResolveGeminiImageCount covers image output under custom model identifiers.
// Observed inline images must trigger per-image billing even when neither
// the client model nor the mapped model is a known image-model name.
func TestResolveGeminiImageCount(t *testing.T) {
	oneImage := []byte(geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiTestPNG + `"}}`))
	textOnly := []byte(geminiImageResponse(`{"text":"hello"}`))

	t.Run("custom model name bills by observed images", func(t *testing.T) {
		c := newGeminiImageTestContext(t)
		c.Images = 0
		c.ObserveImagesForTest(oneImage)

		require.False(t, antigravity.IsImageGenerationModel("nana-banana-2"), "前置条件：白名单判不出自定义名")
		require.Equal(t, 1, c.ImageCountForTest("nana-banana-2", "nana-banana-2"))
	})

	t.Run("falls back to requested model name", func(t *testing.T) {
		c := newGeminiImageTestContext(t)
		c.Images = 0
		c.ObserveImagesForTest(textOnly)

		require.Equal(t, 1, c.ImageCountForTest("gemini-3-pro-image-preview", "gemini-3-pro-image-preview"))
	})

	t.Run("falls back to mapped upstream model name", func(t *testing.T) {
		c := newGeminiImageTestContext(t)
		c.Images = 0
		c.ObserveImagesForTest(textOnly)

		require.Equal(t, 1, c.ImageCountForTest("my-image-alias", "gemini-2.5-flash-image"))
	})

	t.Run("text model stays unbilled", func(t *testing.T) {
		c := newGeminiImageTestContext(t)
		c.Images = 0
		c.ObserveImagesForTest(textOnly)

		require.Equal(t, 0, c.ImageCountForTest("gemini-2.5-pro", "gemini-2.5-pro"))
	})

	t.Run("no counter on context degrades to name heuristic", func(t *testing.T) {
		c := newGeminiImageTestContext(t)
		require.Equal(t, 0, c.ImageCountForTest("nana-banana-2", "nana-banana-2"))
		require.Equal(t, 1, c.ImageCountForTest("gemini-3-pro-image", "gemini-3-pro-image"))
	})
}
