package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestProviderRoutingSnapshotExcludesCredentialsAndCopies checks that snapshots omit secrets and own mutable
// values.
func TestProviderRoutingSnapshotExcludesCredentialsAndCopies(t *testing.T) {
	parent := int64(2)
	expires := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	r := &Record{
		ID: 1, Platform: PlatformOpenAI, Type: ProviderTypeAPIKey, ParentProviderID: &parent, ExpiresAt: &expires,
		Credentials: map[string]any{"api_key": "private-token-marker", UpstreamProtocolsKey: []string{"openai_responses"}},
		Extra:       map[string]any{"unknown-secret": "private-extra-marker"},
	}
	snapshot := r.RoutingSnapshot()
	require.Equal(t, []capability.ProtocolID{capability.ProtocolOpenAIResponses}, snapshot.EnabledProtocols)
	payload, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "private-")
	require.NotContains(t, string(payload), "Credentials")
	*snapshot.ParentProviderID = 3
	*snapshot.ExpiresAt = expires.Add(time.Hour)
	snapshot.EnabledProtocols[0] = capability.ProtocolOpenAIChatCompletions
	require.Equal(t, int64(2), parent)
	require.Equal(t, 0, expires.Hour())
	require.Equal(t, []capability.ProtocolID{capability.ProtocolOpenAIResponses}, r.UpstreamProtocols())
}
