package httpapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestProtocolRouteMethodAndResourceBoundaries supplies methods and paths for
// generation, WebSocket and resource operations. It requires separate protocol
// admission for new work while existing-task paths remain outside those
// generation switches.
func TestProtocolRouteMethodAndResourceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		// method is the HTTP verb; path is its normalized resource target.
		method, path string
		want         ProtocolID // expected admission protocol identifier
	}{
		{http.MethodGet, "/responses", ProtocolResponsesWebSocket},
		{http.MethodPost, "/responses", ""},
		{http.MethodPost, "/responses/compact", ProtocolResponsesCompact},
		{http.MethodGet, "/responses/compact", ""},
		{http.MethodPost, "/videos", ProtocolVideosGenerations},
		{http.MethodGet, "/videos/id/content", ""},
		{http.MethodPost, "/images/batches", ProtocolImageBatches},
		{http.MethodPost, "/images/batches/id/cancel", ""},
		{http.MethodDelete, "/images/batches/id", ""},
		{http.MethodPost, "/live", ProtocolLive},
		{http.MethodGet, "/live/id", ""},
		{http.MethodGet, "/custom-voices", ProtocolCustomVoices},
		{http.MethodDelete, "/custom-voices/id", ProtocolCustomVoices},
		{http.MethodPatch, "/custom-voices/id", ProtocolCustomVoices},
		{http.MethodGet, "/custom-voices/id/audio", ProtocolCustomVoices},
		{http.MethodPost, "/custom-voices-other", ""},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			got, ok := ProtocolForRoute(tc.method, tc.path)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.want != "", ok)
		})
	}
}

// TestProtocolCatalogRoutesAreIndependent mutates a route returned by the
// catalogue accessor. Global admission matching must retain the original path,
// and embedding capability remains present for both checked platforms.
func TestProtocolCatalogRoutesAreIndependent(t *testing.T) {
	for _, protocol := range ProtocolEndpoints() {
		if protocol.ID == ProtocolEmbeddings {
			protocol.Routes[0].Path = "/changed"
		}
	}
	got, ok := ProtocolForRoute(http.MethodPost, "/embeddings")
	require.True(t, ok)
	require.Equal(t, ProtocolEmbeddings, got)
	_, ok = ProtocolForRoute(http.MethodPost, "/changed")
	require.False(t, ok)
	require.Contains(t, capability.SupportedGroupClientProtocols(capability.PlatformOpenAI), ProtocolEmbeddings)
	require.Contains(t, capability.SupportedGroupClientProtocols(capability.PlatformGrok), ProtocolEmbeddings)
}
