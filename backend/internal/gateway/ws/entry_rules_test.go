package ws

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// TestOpenAIWSNextAttemptMessageUsesCurrentTurnPayload supplies distinct first
// and later-turn requests. The selected retry equals the current turn, and
// mutating returned bytes leaves the supplied retry buffer intact.
func TestOpenAIWSNextAttemptMessageUsesCurrentTurnPayload(t *testing.T) {
	firstMessage := []byte(`{"type":"response.create","input":"first"}`)
	currentTurn := []byte(`{"type":"response.create","input":"turn-281"}`)

	next, ok := EntryNextAttemptMessage(firstMessage, currentTurn, true)

	require.True(t, ok)
	require.Equal(t, currentTurn, next)
	next[0] = 'x'
	require.Equal(t, byte('{'), currentTurn[0], "retry payload must be cloned")
}

// TestOpenAIWSNextAttemptMessageRejectsMissingCurrentTurnPayload marks a
// later-turn failure with no replay bytes. Selection returns false and nil,
// preventing the first frame from being reused for that turn.
func TestOpenAIWSNextAttemptMessageRejectsMissingCurrentTurnPayload(t *testing.T) {
	next, ok := EntryNextAttemptMessage([]byte(`{"type":"response.create"}`), nil, true)

	require.False(t, ok)
	require.Nil(t, next)
}

// TestOpenAIWSNextAttemptMessageKeepsInitialMessageForFirstTurnFailover
// supplies an initial request without later-turn replay data. Selection
// succeeds and returns bytes equal to that first request for a replacement
// provider.
func TestOpenAIWSNextAttemptMessageKeepsInitialMessageForFirstTurnFailover(t *testing.T) {
	firstMessage := []byte(`{"type":"response.create","input":"first"}`)

	next, ok := EntryNextAttemptMessage(firstMessage, nil, false)

	require.True(t, ok)
	require.Equal(t, firstMessage, next)
}

// TestEntryCapabilityErrorsDoNotPenalizeProviders wraps local capability failures
// as they leave turn preparation. Neither missing declarations nor configuration
// outages establish an upstream failure for scheduler feedback.
func TestEntryCapabilityErrorsDoNotPenalizeProviders(t *testing.T) {
	for _, reason := range []string{"MODEL_CAPABILITY_UNKNOWN", "MODEL_CAPABILITY_UNSUPPORTED", "MODEL_ATTRIBUTES_UNAVAILABLE"} {
		err := fmt.Errorf("prepare websocket turn: %w", apperror.BadRequest(reason, "capability rejected"))
		require.False(t, EntryShouldReportFailure(err), reason)
		closed := EntryModelAttributeClose(err)
		require.True(t, closed.Present)
		require.Contains(t, closed.Reason, reason)
		if reason == "MODEL_ATTRIBUTES_UNAVAILABLE" {
			require.Equal(t, 1013, closed.Status)
		} else {
			require.Equal(t, 1008, closed.Status)
		}
	}
}
