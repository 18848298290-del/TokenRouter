package egress

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpstreamUsageBaseURLReusesSecurityPolicy supplies an enabled usage
// allowlist and mixed-case HTTPS schemes. It checks normalized successful URLs
// and rejection of HTTP, a private literal address, and an unlisted hostname.
func TestUpstreamUsageBaseURLReusesSecurityPolicy(t *testing.T) {
	policy := UsageURLPolicy{Configured: true, Enabled: true, UpstreamHosts: []string{"gateway.example"}}

	value, err := policy.Validate("https://gateway.example/v1/")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/v1", value)
	value, err = policy.Validate("HTTPS://gateway.example/v1/")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/v1", value)
	_, err = policy.Validate("http://gateway.example/v1")
	require.Error(t, err)
	_, err = policy.Validate("https://127.0.0.1/v1")
	require.Error(t, err)
	_, err = policy.Validate("https://unlisted.example/v1")
	require.Error(t, err)
}

// TestUpstreamUsageBaseURLUsesStrictDefaultsWithoutConfig leaves the usage
// policy unconfigured. It checks that HTTPS public hostnames pass while HTTP
// schemes and loopback literal addresses fail under the default rules.
func TestUpstreamUsageBaseURLUsesStrictDefaultsWithoutConfig(t *testing.T) {
	policy := UsageURLPolicy{}
	_, err := policy.Validate("http://gateway.example/v1")
	require.Error(t, err)
	_, err = policy.Validate("https://127.0.0.1/v1")
	require.Error(t, err)
	value, err := policy.Validate("https://gateway.example/v1")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/v1", value)
}
