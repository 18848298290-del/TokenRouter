package usage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Usage records can carry an explicit request category beside older stream
// and WebSocket flags. These cases exercise transport normalization, legacy
// recovery and nil receivers, checking the effective category or updated
// booleans in each selected fixture.

// TestUsageLogSyncRequestTypeAndLegacyFields supplies WS v2 with false legacy
// flags. It requires the stored category to stay WS v2 and both flags to
// become true.
func TestUsageLogSyncRequestTypeAndLegacyFields(t *testing.T) {
	t.Parallel()

	log := &UsageLog{RequestType: RequestTypeWSV2, Stream: false, OpenAIWSMode: false}
	log.SyncRequestTypeAndLegacyFields()

	require.Equal(t, RequestTypeWSV2, log.RequestType)
	require.True(t, log.Stream)
	require.True(t, log.OpenAIWSMode)
}

// TestUsageLogEffectiveRequestTypeFallback supplies an unknown category with
// both legacy transport flags set. It requires the effective category to
// resolve to WS v2.
func TestUsageLogEffectiveRequestTypeFallback(t *testing.T) {
	t.Parallel()

	log := &UsageLog{RequestType: RequestTypeUnknown, Stream: true, OpenAIWSMode: true}
	require.Equal(t, RequestTypeWSV2, log.EffectiveRequestType())
}

// TestUsageLogEffectiveRequestTypeNilReceiver reads a nil UsageLog pointer.
// It requires the returned category to be unknown so callers can inspect
// absent records.
func TestUsageLogEffectiveRequestTypeNilReceiver(t *testing.T) {
	t.Parallel()

	var log *UsageLog
	require.Equal(t, RequestTypeUnknown, log.EffectiveRequestType())
}

// TestUsageLogSyncRequestTypeAndLegacyFieldsNilReceiver invokes
// synchronization on a nil UsageLog pointer. Returning without panic is the
// sole observation protected by this absent-record smoke case.
func TestUsageLogSyncRequestTypeAndLegacyFieldsNilReceiver(t *testing.T) {
	t.Parallel()

	var log *UsageLog
	log.SyncRequestTypeAndLegacyFields()
}
