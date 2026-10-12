package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
)

// TestNewProxyExitInfoProberUsesConfiguredTargets checks that probes use the configured target URL.
func TestNewProxyExitInfoProberUsesConfiguredTargets(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ip":"203.0.113.42"}`))
	}))
	defer server.Close()
	prober := provideEgressProbe(&config.Config{Security: config.SecurityConfig{ProxyProbe: config.ProxyProbeConfig{URLs: []config.ProbeURLConfig{{URL: server.URL, Parser: "ipify"}}}}})
	result, _, err := prober.ProbeProxy(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, "203.0.113.42", result.IP)
	require.Equal(t, 1, calls)
}
