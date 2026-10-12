package ws

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
)

// TestBuildOpenAIFastPolicyBlockedWSEvent_HasEventIDAndCode builds two local
// policy rejection events. It checks the selected message, stable error fields,
// evt_ prefix, and distinct returned identifiers for these two calls.
func TestBuildOpenAIFastPolicyBlockedWSEvent_HasEventIDAndCode(t *testing.T) {
	bytes := BuildFastPolicyBlockedEvent(&tierpolicy.BlockedError{Message: "blocked because reasons"})
	require.NotNil(t, bytes)

	require.Equal(t, "error", gjson.GetBytes(bytes, "type").String())
	require.Equal(t, "invalid_request_error", gjson.GetBytes(bytes, "error.type").String())
	require.Equal(t, "policy_violation", gjson.GetBytes(bytes, "error.code").String())
	require.Equal(t, "blocked because reasons", gjson.GetBytes(bytes, "error.message").String())

	eventID := gjson.GetBytes(bytes, "event_id").String()
	require.NotEmpty(t, eventID, "event_id must be present so clients can correlate the rejection in their logs")
	require.True(t, strings.HasPrefix(eventID, "evt_"), "event_id should follow the evt_<rand> Realtime convention; got %q", eventID)

	// Sanity check: two consecutive events get distinct IDs.
	other := BuildFastPolicyBlockedEvent(&tierpolicy.BlockedError{Message: "second"})
	otherID := gjson.GetBytes(other, "event_id").String()
	require.NotEqual(t, eventID, otherID, "event_id must be random per-event")
}

// TestBuildOpenAIFastPolicyBlockedWSEvent_NilSafe supplies no policy rejection
// to the event builder. The returned nil bytes let callers skip writing a
// policy event when no rejection exists.
func TestBuildOpenAIFastPolicyBlockedWSEvent_NilSafe(t *testing.T) {
	require.Nil(t, BuildFastPolicyBlockedEvent(nil))
}
