package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// CN Qoder credentials can contain machine fields carried over from a Global
// or legacy import. This case exercises qoder_machine.go and checks that
// preparation retains the supplied CN machine_id while removing machine_token
// and machine_type from the stored credential map.

// TestEnsureQoderCNMachineCredentialsRemovesLegacyFields prepares direct-token CN
// credentials that include legacy machine fields. It checks that machine_id is
// retained and both unsupported additional machine fields are removed.
func TestEnsureQoderCNMachineCredentialsRemovesLegacyFields(t *testing.T) {
	provider := &providercore.Record{Credentials: map[string]any{
		"site":                 "cn",
		"security_oauth_token": "cosy-token",
		"machine_id":           "machine-cn",
		"machine_token":        "legacy-machine-token",
		"machine_type":         "legacy-machine-type",
	}}

	ensureQoderMachineCredentials(provider)

	require.Equal(t, "machine-cn", provider.GetCredential("machine_id"))
	require.NotContains(t, provider.Credentials, "machine_token")
	require.NotContains(t, provider.Credentials, "machine_type")
}
