package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
)

// TestResponseHeaderFilterConfigurationProjection checks nil and default options, allow/remove rules, and
// isolation from input mutations.
func TestResponseHeaderFilterConfigurationProjection(t *testing.T) {
	require.Nil(t, provideResponseHeaderFilter(nil))
	cfg := &config.Config{}
	cfg.Security.ResponseHeaders.AdditionalAllowed = []string{"X-Trace-Custom"}
	cfg.Security.ResponseHeaders.ForceRemove = []string{"X-Request-Id"}
	disabled := provideResponseHeaderFilter(cfg)
	require.True(t, disabled.Allows("X-Request-Id"))
	require.False(t, disabled.Allows("X-Trace-Custom"))
	cfg.Security.ResponseHeaders.Enabled = true
	enabled := provideResponseHeaderFilter(cfg)
	require.False(t, enabled.Allows("X-Request-Id"))
	require.True(t, enabled.Allows("X-Trace-Custom"))
	require.False(t, enabled.Allows("Connection"))
	cfg.Security.ResponseHeaders.AdditionalAllowed[0] = "X-Replaced"
	cfg.Security.ResponseHeaders.ForceRemove[0] = "Content-Type"
	require.True(t, enabled.Allows("X-Trace-Custom"))
	require.True(t, enabled.Allows("Content-Type"))
	require.False(t, enabled.Allows("X-Replaced"))
}

func TestResolveUpstreamResponseReadLimit(t *testing.T) {
	t.Run("use default when config missing", func(t *testing.T) {
		require.Equal(t, config.DefaultUpstreamResponseReadMaxBytes, provideOpenAIResponseOutput(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil).Options.ReadLimit)
	})

	t.Run("use configured value", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Gateway.UpstreamResponseReadMaxBytes = 1234
		require.Equal(t, int64(1234), provideOpenAIResponseOutput(cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil).Options.ReadLimit)
	})
}
