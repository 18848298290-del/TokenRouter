package transfer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProviderArchiveVersionBoundary checks supported archive versions and rejects the legacy accounts collection.
func TestProviderArchiveVersionBoundary(t *testing.T) {
	var payload DataPayload
	require.NoError(t, json.Unmarshal([]byte(`{"type":"tokenrouter-data","version":2,"proxies":[],"providers":[]}`), &payload))
	require.NoError(t, ValidateHeader(payload))
	legacy := payload
	legacy.Type = LegacyDataType
	require.NoError(t, ValidateHeader(legacy))
	for _, version := range []int{0, 1, 3} {
		candidate := payload
		candidate.Version = version
		require.Error(t, ValidateHeader(candidate))
	}
	for _, format := range []string{"", "tokenrouter-bundle"} {
		candidate := payload
		candidate.Type = format
		require.Error(t, ValidateHeader(candidate))
	}
	require.Error(t, json.Unmarshal([]byte(`{"type":"tokenrouter-data","version":2,"proxies":[],"providers":[],"accounts":[]}`), &payload))
}
