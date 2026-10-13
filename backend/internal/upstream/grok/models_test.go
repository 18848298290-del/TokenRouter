package grok

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestExplicitGrokIDsRemainUnchanged supplies explicit aliases, suffixed
// models, and vendor-qualified IDs to the model helpers. It asserts each
// listed nonempty ID survives and the optional text default applies to
// omitted model input.
func TestExplicitGrokIDsRemainUnchanged(t *testing.T) {
	for _, model := range []string{"grok", "grok-latest", "grok-4.6-latest", "grok-build-latest", "grok-4.20-multi-agent", "xai/grok-4.6", "grok-imagine-video-1.5-preview"} {
		require.Equal(t, model, NormalizeModelID(model))
		require.Equal(t, model, ResolveGrokTextResponsesModelID(model, "custom-default"))
		require.Equal(t, model, CanonicalImagineVideoModel(model))
	}
	require.Equal(t, "custom-default", ResolveGrokTextResponsesModelID("", "custom-default"))
}

// TestCanonicalGrokImagineVideoPriceFamily supplies complete video model IDs
// spanning versions, previews, and vendor prefixes. It asserts none of these
// listed IDs is reduced to another pricing family by video normalization.
func TestCanonicalGrokImagineVideoPriceFamily(t *testing.T) {
	t.Parallel()
	require.Equal(t, "grok-imagine-video", CanonicalImagineVideoModel("grok-imagine-video"))
	require.Equal(t, "grok-imagine-video-1.5", CanonicalImagineVideoModel("grok-imagine-video-1.5"))
	require.Equal(t, "grok-imagine-video-1.5-preview", CanonicalImagineVideoModel("grok-imagine-video-1.5-preview"))
	require.Equal(t, "xai/grok-video-1.5", CanonicalImagineVideoModel("xai/grok-video-1.5"))
	require.Equal(t, "grok-imagine-video-2", CanonicalImagineVideoModel("grok-imagine-video-2"))
	require.Equal(t, "xai/grok-imagine-video-2", CanonicalImagineVideoModel("xai/grok-imagine-video-2"))
}
