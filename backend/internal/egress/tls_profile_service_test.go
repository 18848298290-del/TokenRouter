package egress

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTLSProfileSnapshotIsolation checks that source and returned objects cannot mutate cached profiles.
func TestTLSProfileSnapshotIsolation(t *testing.T) {
	source := &TLSFingerprintProfile{ID: 1, Name: "profile", CipherSuites: []uint16{4865}, ALPNProtocols: []string{"h2"}}
	svc := NewTLSFingerprintProfileService(nil, nil)
	svc.setLocalCache([]*TLSFingerprintProfile{source})
	first := svc.GetProfileByID(1)
	first.CipherSuites[0] = 4866
	first.ALPNProtocols[0] = "http/1.1"
	again := svc.GetProfileByID(1)
	require.Equal(t, uint16(4865), again.CipherSuites[0])
	require.Equal(t, "h2", again.ALPNProtocols[0])
	source.CipherSuites[0] = 4867
	require.Equal(t, uint16(4865), svc.GetProfileByID(1).CipherSuites[0])
}
