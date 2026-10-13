package batchimage

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Persisted batch statuses define the edges available to repository updates.
// The table covers normal progression, repeated running observations,
// terminal output deletion and rejected jumps; separate cases exercise
// backend labels, terminal classification and the generated batch ID shape.

// TestCanTransitionBatchImageJob compares representative valid and invalid
// lifecycle edges. It protects direct status progression, repeat polling and
// terminal cleanup while rejecting completion-to-running and
// creation-to-running jumps.
func TestCanTransitionBatchImageJob(t *testing.T) {
	tests := []struct {
		name string // scenario display name
		from string // starting job status
		to   string // requested next status
		want bool   // expected observable result
	}{
		{name: "created_to_uploading", from: BatchImageJobStatusCreated, to: BatchImageJobStatusUploading, want: true},
		{name: "uploading_to_submitted", from: BatchImageJobStatusUploading, to: BatchImageJobStatusSubmitted, want: true},
		{name: "submitted_to_running", from: BatchImageJobStatusSubmitted, to: BatchImageJobStatusRunning, want: true},
		{name: "running_self_poll", from: BatchImageJobStatusRunning, to: BatchImageJobStatusRunning, want: true},
		{name: "running_to_indexing", from: BatchImageJobStatusRunning, to: BatchImageJobStatusIndexing, want: true},
		{name: "indexing_to_settling", from: BatchImageJobStatusIndexing, to: BatchImageJobStatusSettling, want: true},
		{name: "settling_to_completed", from: BatchImageJobStatusSettling, to: BatchImageJobStatusCompleted, want: true},
		{name: "submitted_to_cancelled", from: BatchImageJobStatusSubmitted, to: BatchImageJobStatusCancelled, want: true},
		{name: "non_terminal_to_failed", from: BatchImageJobStatusCreated, to: BatchImageJobStatusFailed, want: true},
		{name: "completed_to_output_deleted", from: BatchImageJobStatusCompleted, to: BatchImageJobStatusOutputDeleted, want: true},
		{name: "failed_to_output_deleted", from: BatchImageJobStatusFailed, to: BatchImageJobStatusOutputDeleted, want: true},
		{name: "cancelled_to_output_deleted", from: BatchImageJobStatusCancelled, to: BatchImageJobStatusOutputDeleted, want: true},
		{name: "created_to_running_invalid", from: BatchImageJobStatusCreated, to: BatchImageJobStatusRunning, want: false},
		{name: "completed_to_running_invalid", from: BatchImageJobStatusCompleted, to: BatchImageJobStatusRunning, want: false},
		{name: "output_deleted_to_failed_invalid", from: BatchImageJobStatusOutputDeleted, to: BatchImageJobStatusFailed, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, CanTransitionBatchImageJob(tt.from, tt.to))
		})
	}
}

// TestIsTerminalBatchImageJobStatus classifies completed, failed, cancelled
// and deleted-output outcomes as terminal. Running remains nonterminal,
// protecting the distinction used by provider processing and input cleanup.
func TestIsTerminalBatchImageJobStatus(t *testing.T) {
	require.True(t, IsTerminalBatchImageJobStatus(BatchImageJobStatusCompleted))
	require.True(t, IsTerminalBatchImageJobStatus(BatchImageJobStatusFailed))
	require.True(t, IsTerminalBatchImageJobStatus(BatchImageJobStatusCancelled))
	require.True(t, IsTerminalBatchImageJobStatus(BatchImageJobStatusOutputDeleted))
	require.False(t, IsTerminalBatchImageJobStatus(BatchImageJobStatusRunning))
}

// TestIsSupportedBatchImageProvider checks the accepted Gemini API and Vertex
// backend labels. OAuth-shaped and empty labels remain rejected before
// callers can select an unsupported batch provider.
func TestIsSupportedBatchImageProvider(t *testing.T) {
	require.True(t, IsSupportedBatchImageProvider(BatchImageProviderGeminiAPI))
	require.True(t, IsSupportedBatchImageProvider(BatchImageProviderVertex))
	require.False(t, IsSupportedBatchImageProvider("gemini_oauth"))
	require.False(t, IsSupportedBatchImageProvider(""))
}

// TestNewBatchImageID generates a new batch identity through the random ID
// helper. The assertions protect its public prefix and thirty-two-character
// hexadecimal-sized suffix for queue and API use.
func TestNewBatchImageID(t *testing.T) {
	id, err := NewBatchImageID()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(id, "imgbatch_"))
	require.Len(t, id, len("imgbatch_")+32)
}
