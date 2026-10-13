package creative_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
)

// Task-state fixtures cover accepted and rejected transitions independently
// from storage operations. Workspace checks exercise scope validation, while
// task identifiers are tested through the prefix guard and random constructor
// without claiming stronger hexadecimal validation than the guard performs.

// TestNormalizeCreativeWorkspaceID supplies missing, malformed, and accepted
// workspace values, then builds user scopes. It checks workspace error
// classes, normalized output, and rejection of a nonpositive user identifier.
func TestNormalizeCreativeWorkspaceID(t *testing.T) {
	_, err := creative.NormalizeCreativeWorkspaceID("")
	require.ErrorIs(t, err, creative.ErrCreativeWorkspaceRequired)
	_, err = creative.NormalizeCreativeWorkspaceID("not-a-uuid")
	require.ErrorIs(t, err, creative.ErrCreativeWorkspaceInvalid)
	normalized, err := creative.NormalizeCreativeWorkspaceID("11111111-1111-4111-8111-111111111111")
	require.NoError(t, err)
	require.Equal(t, testCreativeWorkspaceID, normalized)
	scope, err := creative.NormalizeCreativeRunScope(creative.CreativeRunScope{UserID: 7, WorkspaceID: "11111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	require.Equal(t, testCreativeWorkspaceID, scope.WorkspaceID)
	_, err = creative.NormalizeCreativeRunScope(creative.CreativeRunScope{UserID: 0, WorkspaceID: testCreativeWorkspaceID})
	require.ErrorIs(t, err, creative.ErrCreativeRunNotFound)
}

// TestCanTransitionCreativeRun enumerates the listed valid and invalid status
// pairs. The assertions include queued loss and terminal success-to-loss,
// while rejecting restart and same-running-state transitions.
func TestCanTransitionCreativeRun(t *testing.T) {
	valid := []struct {
		// from names the source state; to names the destination state.
		from, to string
	}{
		{creative.CreativeRunStatusQueued, creative.CreativeRunStatusRunning},
		{creative.CreativeRunStatusQueued, creative.CreativeRunStatusCancelled},
		{creative.CreativeRunStatusQueued, creative.CreativeRunStatusFailed},
		{creative.CreativeRunStatusQueued, creative.CreativeRunStatusResultLost},
		{creative.CreativeRunStatusRunning, creative.CreativeRunStatusSucceeded},
		{creative.CreativeRunStatusRunning, creative.CreativeRunStatusFailed},
		{creative.CreativeRunStatusRunning, creative.CreativeRunStatusCancelled},
		{creative.CreativeRunStatusRunning, creative.CreativeRunStatusResultLost},
		{creative.CreativeRunStatusSucceeded, creative.CreativeRunStatusResultLost},
	}
	for _, tc := range valid {
		require.True(t, creative.CanTransitionCreativeRun(tc.from, tc.to), "%s -> %s 应当合法", tc.from, tc.to)
	}

	invalid := []struct {
		// from names the source state; to names the destination state.
		from, to string
	}{
		{"", creative.CreativeRunStatusRunning},
		{creative.CreativeRunStatusQueued, ""},
		{creative.CreativeRunStatusQueued, creative.CreativeRunStatusSucceeded},
		{creative.CreativeRunStatusRunning, creative.CreativeRunStatusQueued},
		{creative.CreativeRunStatusRunning, creative.CreativeRunStatusRunning},
		{creative.CreativeRunStatusSucceeded, creative.CreativeRunStatusRunning},
		{creative.CreativeRunStatusFailed, creative.CreativeRunStatusRunning},
		{creative.CreativeRunStatusCancelled, creative.CreativeRunStatusRunning},
		{creative.CreativeRunStatusResultLost, creative.CreativeRunStatusRunning},
		{creative.CreativeRunStatusSucceeded, creative.CreativeRunStatusFailed},
		{creative.CreativeRunStatusFailed, creative.CreativeRunStatusResultLost},
	}
	for _, tc := range invalid {
		require.False(t, creative.CanTransitionCreativeRun(tc.from, tc.to), "%s -> %s 应当非法", tc.from, tc.to)
	}
}

// TestIsTerminalCreativeRunStatus checks the four listed terminal states
// against queued, running, and empty input. It requires true only for the
// explicitly enumerated terminal values in this fixture.
func TestIsTerminalCreativeRunStatus(t *testing.T) {
	for _, status := range []string{creative.CreativeRunStatusSucceeded, creative.CreativeRunStatusFailed, creative.CreativeRunStatusCancelled, creative.CreativeRunStatusResultLost} {
		require.True(t, creative.IsTerminalCreativeRunStatus(status))
	}
	for _, status := range []string{creative.CreativeRunStatusQueued, creative.CreativeRunStatusRunning, ""} {
		require.False(t, creative.IsTerminalCreativeRunStatus(status))
	}
}

// TestIsValidCreativeRunID checks an accepted creative prefix, a foreign
// prefix, an empty suffix, and empty input. The assertions describe the prefix
// guard cases exercised here.
func TestIsValidCreativeRunID(t *testing.T) {
	require.True(t, creative.IsValidCreativeRunID("crun_0123456789abcdef"))
	require.False(t, creative.IsValidCreativeRunID("imgbatch_0123"))
	require.False(t, creative.IsValidCreativeRunID("crun_"))
	require.False(t, creative.IsValidCreativeRunID(""))
}

// TestNewCreativeRunID generates two task identifiers with the random
// constructor. Both calls succeed, produce accepted creative prefixes, and
// return different values in this fixture.
func TestNewCreativeRunID(t *testing.T) {
	runID, err := creative.NewCreativeRunID()
	require.NoError(t, err)
	require.True(t, creative.IsValidCreativeRunID(runID))
	other, err := creative.NewCreativeRunID()
	require.NoError(t, err)
	require.NotEqual(t, runID, other)
}
