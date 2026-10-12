package egress

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRequestPolicyCopiesTLSHeadersAndHidesCredentials checks mutation isolation and credential-free formatting.
func TestRequestPolicyCopiesTLSHeadersAndHidesCredentials(t *testing.T) {
	profile := &TLSFingerprintProfile{Name: "identity", CipherSuites: []uint16{1, 2}, ALPNProtocols: []string{"h2", "http/1.1"}}
	headers := map[string]string{"X-Allowed": "original", "Authorization": "forbidden"}
	policy := RequestPolicy(RequestPolicyInput{ProxyURL: "http://fixture-user:fixture-password@localhost:8000", TLSProfile: profile, Headers: headers, PublicHostsOnly: true, DisableRedirects: true})
	headers["X-Allowed"] = "edited"
	profile.CipherSuites[0] = 9
	require.Equal(t, "original", policy.Headers["x-allowed"])
	require.NotContains(t, policy.Headers, "authorization")
	require.Equal(t, uint16(1), policy.TLSProfile.CipherSuites[0])
	policy.TLSProfile.ALPNProtocols[0] = "changed"
	require.Equal(t, "h2", profile.ALPNProtocols[0])
	require.True(t, policy.RequiresHostValidation())
	require.True(t, policy.DisableRedirects)
	raw, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "fixture-password")
	require.NotContains(t, fmt.Sprintf("%v %#v", policy, policy), "fixture-password")
	require.NotContains(t, policy.String(), "original")
}
