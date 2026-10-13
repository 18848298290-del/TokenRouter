package grok

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// New-media qualification includes image generation/editing and video
// generation/editing/extension, while existing video status uses a different
// gate. The table checks those five new-work identities and the status identity
// so callers can keep generation qualification separate from lookup behavior.

// TestGrokMediaGenerationGateCoversImagesAndVideo compares five new-media
// endpoint identities with an existing video-status endpoint.
// IsGenerationRequest must accept generation, editing and extension while
// returning false for the status fixture.
func TestGrokMediaGenerationGateCoversImagesAndVideo(t *testing.T) {
	tests := []struct {
		name     string            // scenario display label
		endpoint GrokMediaEndpoint // media endpoint fixture
		want     bool              // expected generation qualification
	}{
		{name: "image generation", endpoint: GrokMediaEndpointImagesGenerations, want: true},
		{name: "image edit", endpoint: GrokMediaEndpointImagesEdits, want: true},
		{name: "video generation", endpoint: GrokMediaEndpointVideosGenerations, want: true},
		{name: "video edit", endpoint: GrokMediaEndpointVideosEdits, want: true},
		{name: "video extension", endpoint: GrokMediaEndpointVideosExtensions, want: true},
		{name: "video status", endpoint: GrokMediaEndpointVideoStatus, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.endpoint.IsGenerationRequest())
		})
	}
}
