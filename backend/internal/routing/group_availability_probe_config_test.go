package routing

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// group_availability_probe_config_test.go checks stored protocol compatibility
// and group admission for recurring probes. JSON without the protocol retains
// automatic behavior, while explicit entries must belong to the test catalog.

// TestGroupAvailabilityProbeProtocolCompatibility decodes old and explicit probe configurations.
// Normalization retains automatic omission and each supported entry while rejecting unsupported protocols.
func TestGroupAvailabilityProbeProtocolCompatibility(t *testing.T) {
	protocols := append([]protocol.ProtocolID{""}, provider.GroupProbeProtocols()...)
	for _, entry := range protocols {
		cfg := GroupAvailabilityProbeConfig{}
		require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"model_id":"alias","prompt":"hi"}`), &cfg))
		require.Empty(t, cfg.Protocol)
		cfg.Protocol = entry
		normalized, err := NormalizeGroupAvailabilityProbeConfig(cfg)
		require.NoError(t, err)
		require.Equal(t, entry, normalized.Protocol)
		encoded, err := json.Marshal(normalized)
		require.NoError(t, err)
		if entry == "" {
			require.NotContains(t, string(encoded), `"protocol"`)
		}
		group := &Group{AllowedProtocols: []protocol.ProtocolID{entry}, AvailabilityProbeConfig: normalized}
		require.NoError(t, ValidateGroupProbeProtocol(group))
		group.AllowedProtocols = nil
		require.Equal(t, entry == "", ValidateGroupProbeProtocol(group) == nil)
	}
	for _, entry := range []protocol.ProtocolID{"unknown", protocol.ProtocolResponsesCompact, protocol.ProtocolEmbeddings, protocol.ProtocolImagesEdits} {
		_, err := NormalizeGroupAvailabilityProbeConfig(GroupAvailabilityProbeConfig{Enabled: true, ModelID: "alias", Prompt: "hi", Protocol: entry})
		require.Error(t, err)
	}
}
