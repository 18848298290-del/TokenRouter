package ws

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// TestEntryRequestRecordsTracksRetryAndRejectedTurns retries the first logical
// turn on another provider and opens a later unfinished turn. It checks
// retained identity, two attempts, parent and user attribution, completed
// state, and cleanup's incomplete error code.
func TestEntryRequestRecordsTracksRetryAndRejectedTurns(t *testing.T) {
	var snapshots []telemetry.RequestRecord
	root := telemetry.WithRequestCapture(context.WithValue(context.Background(), telemetry.RequestID, "connection"), telemetry.RequestRecord{RequestID: "connection", UserID: 7, APIKeyID: 8, StartedAt: time.Now(), State: "running"}, func(record telemetry.RequestRecord) { snapshots = append(snapshots, record) })
	session := newEntryRequestRecords(root)
	first := session.attempt()
	firstID := telemetry.RequestIDValue(first.context(root, 1))
	first.finish(root, TurnCapture{Turn: 1, Err: errors.New("retry")}, 9, "openai", true)
	retry := session.attempt()
	require.Equal(t, firstID, telemetry.RequestIDValue(retry.context(root, 1)))
	retry.finish(root, TurnCapture{Turn: 1, Result: &ForwardResult{RequestID: "response", OpenAIWSMode: true, UpstreamTerminalEvent: "response.completed"}}, 10, "openai", false)
	secondID := telemetry.RequestIDValue(retry.context(root, 2))
	require.NotEqual(t, firstID, secondID)
	session.close()
	latest := make(map[string]telemetry.RequestRecord)
	for _, record := range snapshots {
		latest[record.RequestID] = record
	}
	require.Equal(t, "completed", latest[firstID].State)
	require.Len(t, latest[firstID].Attempts, 2)
	require.Equal(t, "connection", latest[firstID].ParentRequestID)
	require.Equal(t, int64(7), latest[firstID].UserID)
	require.Equal(t, "failed", latest[secondID].State)
	require.Equal(t, "websocket_turn_incomplete", latest[secondID].ErrorCode)
}
