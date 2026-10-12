package transfer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Provider archive version two accepts the current and legacy product
// identifiers with provider and proxy collections. These archive.go cases
// reject other versions and type names, then confirm a legacy accounts field
// fails outer JSON decoding even when the supported header is present.

// TestProviderArchiveVersionBoundary decodes an empty version-two provider
// archive and accepts both product type identifiers. Other versions and
// identifiers fail validation, and adding the legacy accounts collection
// makes outer decoding reject the payload.
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
