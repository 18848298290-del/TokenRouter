package media

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestImageRequestRoutingAndMultipart checks alias parsing and image capability validation after routing.
func TestImageRequestRoutingAndMultipart(t *testing.T) {
	request, err := ParseImageRequest("/v1/images/generations", "application/json", []byte(`{"model":"my-alias","prompt":"draw"}`), false)
	require.NoError(t, err)
	require.Error(t, request.ValidateRoutingModel("text-model"))
	require.NoError(t, request.ValidateRoutingModel("gpt-image-2"))
	require.Equal(t, ImageCapabilityNative, request.RequiredCapability)
	require.Equal(t, "my-alias", request.Model)
	basic, err := ParseImageRequest("/v1/images/generations", "application/json", []byte(`{"prompt":"draw"}`), true)
	require.NoError(t, err)
	require.Equal(t, ImageCapabilityBasic, basic.RequiredCapability)
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("prompt", "edit"))
	require.NoError(t, writer.WriteField("size", "1536x1024"))
	part, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("source-image"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	parsed, err := ParseImageRequest("/v1/images/edits", writer.FormDataContentType(), buf.Bytes(), true)
	require.NoError(t, err)
	require.True(t, parsed.IsEdits())
	require.Equal(t, ImageCapabilityNative, parsed.RequiredCapability)
	require.Len(t, parsed.Uploads, 1)
	require.Contains(t, string(parsed.ModerationBody()), "edit")
	require.NotEmpty(t, parsed.StickySessionSeed())
	require.Equal(t, parsed.StickySessionSeed(), NativeImageRequest(parsed).StickySessionSeed())
	encoded, err := json.Marshal(parsed)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "BodyHash")
}

func TestImageOutcomePreservesPartialAndTransportBoundaries(t *testing.T) {
	failure := errors.New("read failure")
	cases := []struct {
		name               string
		stream, oauth, sse bool
		observed           int
		err                error
		count              int
		retain             bool
	}{
		{"nonstream error", false, false, false, 1, failure, 0, false},
		{"stream partial", true, false, true, 2, failure, 2, true},
		{"stream empty error", true, true, true, 0, failure, 0, false},
		{"API SSE no fallback", true, false, true, 0, nil, 0, true},
		{"API JSON fallback", true, false, false, 0, nil, 3, true},
		{"OAuth fallback", true, true, true, 0, nil, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			count, retain := ImageOutcome(tc.stream, tc.oauth, tc.sse, 3, tc.observed, tc.err)
			require.Equal(t, tc.count, count)
			require.Equal(t, tc.retain, retain)
		})
	}
}

func TestIsGrokImageGenerationModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model string
		want  bool
	}{
		{"grok-imagine", true},
		{"grok-imagine-image-quality", true},
		{"grok-imagine-edit", true},
		{"grok-imagine-image-hd", true},
		{" Grok-Imagine ", true},
		{"grok-imagine-video", false},
		{"grok-4.5", false},
		{"grok-composer", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			require.Equal(t, tt.want, IsGrokImageGenerationModel(tt.model))
		})
	}
}
