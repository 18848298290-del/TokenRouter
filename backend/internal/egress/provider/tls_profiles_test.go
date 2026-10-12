package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
)

// TestEgressTLSProfileRoundTripAndIsolation converts a transport profile
// through egress policy and back again. It checks all field values and
// nil/empty forms, sampled cipher/extension mutation isolation, and nil
// conversion results.
func TestEgressTLSProfileRoundTripAndIsolation(t *testing.T) {
	source := &tlsfingerprint.Profile{Name: "cache-identity", EnableGREASE: true, CipherSuites: []uint16{1}, Curves: []uint16{}, PointFormats: []uint16{2}, SignatureAlgorithms: []uint16{3}, ALPNProtocols: []string{"h2", "http/1.1"}, SupportedVersions: []uint16{4}, KeyShareGroups: []uint16{5}, PSKModes: []uint16{6}, Extensions: []uint16{7}}
	policy := egress.RequestPolicy(egress.RequestPolicyInput{TLSProfile: FromTLSProfile(source)})
	result := ToTLSProfile(policy.TLSProfile)
	require.Equal(t, source, result)
	result.CipherSuites[0] = 9
	result.Extensions[0] = 9
	require.Equal(t, uint16(1), source.CipherSuites[0])
	require.Equal(t, uint16(7), policy.TLSProfile.Extensions[0])
	require.Nil(t, FromTLSProfile(nil))
	require.Nil(t, ToTLSProfile(nil))
}
