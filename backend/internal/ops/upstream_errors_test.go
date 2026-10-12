package ops

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProviderHistoryReadsLegacyUpstreamEvents checks that current fields take precedence over legacy fields.
func TestProviderHistoryReadsLegacyUpstreamEvents(t *testing.T) {
	events, err := ParseOpsUpstreamErrors(`[{"account_id":7,"account_name":"legacy"},{"account_id":8,"provider_id":9,"provider_name":"current"},null]`)
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Equal(t, int64(7), events[0].ProviderID)
	require.Equal(t, "legacy", events[0].ProviderName)
	require.Equal(t, int64(9), events[1].ProviderID)
	require.Equal(t, "current", events[1].ProviderName)
	require.Nil(t, events[2])
}
