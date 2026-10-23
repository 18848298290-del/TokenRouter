package media

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestImageRequestRoutingAndMultipart parses an alias before mapped-model
// validation, then parses multipart editing with an upload. It checks
// capability changes without model replacement, moderation/seed delegation
// and BodyHash omission from serialized request output.
func TestImageRequestRoutingAndMultipart(t *testing.T) {
	request, err := ParseImageRequest("/v1/images/generations", "application/json", []byte(`{"model":"my-alias","prompt":"draw"}`), false)
	require.NoError(t, err)
	require.NoError(t, request.ValidateRoutingModel("text-model"))
	require.Error(t, request.ValidateRoutingModel(""))
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
	// Finish the multipart body before parsing its bytes, so the edit fixture
	// includes its terminal boundary along with the supplied prompt, size and
	// image upload.
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

// TestImageOutcomePreservesPartialAndTransportBoundaries combines failed
// partial streams with successful OAuth, API-key JSON and API-key SSE
// outcomes. It checks count and retention separately, including a retained
// SSE success with zero observed images.
func TestImageOutcomePreservesPartialAndTransportBoundaries(t *testing.T) {
	failure := errors.New("read failure")
	cases := []struct {
		name string // case scenario label
		// stream, oauth and sse jointly select the retention and fallback path
		// under test. The table varies those flags independently while a
		// requested count of three remains fixed.
		stream, oauth, sse bool  // request uses streaming, transport uses OAuth and response uses SSE
		observed           int   // observed completed image count
		err                error // injected response read failure
		count              int   // expected retained image count
		retain             bool  // expected result retention decision
	}{
		{"native buffered delivery error", false, false, false, 1, failure, 1, true},
		{"OAuth buffered error", false, true, true, 1, failure, 0, false},
		{"native aggregated stream error", false, false, true, 1, failure, 1, true},
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
