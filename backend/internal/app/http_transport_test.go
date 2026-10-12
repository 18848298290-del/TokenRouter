package app

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
)

// Private-host permission can change between calls through one transport
// instance. A loopback server counts requests across reject, allow, and reject
// phases, making both refreshed policy decisions and network access observable
// in the fixture.

// TestHTTPTransportConfigurationReadBoundary toggles private-host access on
// one captured configuration. Requests must be rejected before reaching the
// server while disabled, reach it once while allowed, and be rejected again
// after the setting is restored.
func TestHTTPTransportConfigurationReadBoundary(t *testing.T) {
	var calls atomic.Int64
	// The HTTP server handles requests in its own goroutines. An atomic counter
	// lets each completed test phase inspect network arrivals independently of them.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = true
	client := provideHTTPUpstream(cfg)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	response, err := client.Do(request, "", 1, 1)
	require.Error(t, err)
	require.Nil(t, response)
	require.Zero(t, calls.Load())

	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	response, err = client.Do(request, "", 1, 1)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	// Finish the successful request before changing policy again. Closing the
	// body releases its request resources before the next rejected transport call.
	require.NoError(t, response.Body.Close())
	require.Equal(t, int64(1), calls.Load())

	cfg.Security.URLAllowlist.AllowPrivateHosts = false
	response, err = client.Do(request, "", 1, 1)
	require.Error(t, err)
	require.Nil(t, response)
	require.Equal(t, int64(1), calls.Load())
}
