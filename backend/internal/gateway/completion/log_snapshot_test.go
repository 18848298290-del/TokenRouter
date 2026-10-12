package completion

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// mutatingUsageWriter mutates and rejects the first write to check that retries
// receive the original data.
type mutatingUsageWriter struct {
	bestCalls int       // best-effort invocation count
	syncCalls int       // synchronous invocation count
	recorded  *UsageLog // accepted retry row
}

// CreateBestEffort changes the supplied row and simulates queue rejection.
// It records an invocation and returns an error, letting the test establish that
// the later synchronous call receives separately retained input.
func (w *mutatingUsageWriter) CreateBestEffort(_ context.Context, row *UsageLog) error {
	w.bestCalls++
	row.ActualCost = 99
	return errors.New("queue rejected")
}

// Create retains the supplied retry row and reports successful creation.
// The fixture is used serially; the test reads that same row after WriteUsage
// returns to compare its cost and storage identity.
func (w *mutatingUsageWriter) Create(_ context.Context, row *UsageLog) (bool, error) {
	w.syncCalls++
	w.recorded = row
	return true, nil
}

// TestSnapshotLogWriterKeepsFallbackFactIsolated uses a repository that mutates
// and rejects its best-effort argument. It checks one synchronous retry and the
// unchanged source cost in separately allocated retry storage.
func TestSnapshotLogWriterKeepsFallbackFactIsolated(t *testing.T) {
	target := &mutatingUsageWriter{}
	row := &UsageLog{ActualCost: 1.25}
	recorder := NewRecorder(Dependencies{Logs: SnapshotLogWriter(target)}, RecorderOptions{})
	recorder.WriteUsage(context.Background(), row, "test")
	require.Equal(t, 1, target.bestCalls)
	require.Equal(t, 1, target.syncCalls)
	// Allow floating-point rounding while comparing the retained dollar amount.
	require.InDelta(t, 1.25, target.recorded.ActualCost, 1e-12)
	require.InDelta(t, 1.25, row.ActualCost, 1e-12)
	require.NotSame(t, row, target.recorded)
}
