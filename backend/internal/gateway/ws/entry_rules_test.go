package ws

import (
	"testing"

	"github.com/stretchr/testify/require"
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
