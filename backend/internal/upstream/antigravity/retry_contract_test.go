package antigravity

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Provider-switch errors retain provider/model identity through ordinary
// wrapping for retry selection. Table fixtures distinguish nil and generic
// errors from direct and wrapped signals, while message formatting checks that
// the selected identities remain observable.

// TestIsAntigravityProviderSwitchError compares nil, generic, direct, and
// wrapped errors. Matching fixture signals must retain their provider and
// model fields, while unrelated cases produce no match.
func TestIsAntigravityProviderSwitchError(t *testing.T) {
	tests := []struct {
		name          string // human-readable table case name
		err           error  // input error or wrapped signal
		expectedOK    bool   // expected typed-match selection result
		expectedID    int64  // expected matched provider identifier
		expectedModel string // expected matched model identity
	}{
		{
			name:       "nil error",
			err:        nil,
			expectedOK: false,
		},
		{
			name:       "generic error",
			err:        fmt.Errorf("some error"),
			expectedOK: false,
		},
		{
			name: "provider switch error",
			err: &AntigravityProviderSwitchError{
				OriginalProviderID: 123,
				RateLimitedModel:   "claude-sonnet-4-5",
				IsStickySession:    true,
			},
			expectedOK:    true,
			expectedID:    123,
			expectedModel: "claude-sonnet-4-5",
		},
		{
			name: "wrapped provider switch error",
			err: fmt.Errorf("wrapped: %w", &AntigravityProviderSwitchError{
				OriginalProviderID: 456,
				RateLimitedModel:   "gemini-3-flash",
				IsStickySession:    false,
			}),
			expectedOK:    true,
			expectedID:    456,
			expectedModel: "gemini-3-flash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switchErr, ok := IsAntigravityProviderSwitchError(tt.err)
			require.Equal(t, tt.expectedOK, ok)
			if tt.expectedOK {
				require.NotNil(t, switchErr)
				require.Equal(t, tt.expectedID, switchErr.OriginalProviderID)
				require.Equal(t, tt.expectedModel, switchErr.RateLimitedModel)
			} else {
				require.Nil(t, switchErr)
			}
		})
	}
}

// TestAntigravityProviderSwitchError_Error formats a limited provider with
// sticky-session context. The resulting message must contain the fixture
// provider identifier and model name used for switching.
func TestAntigravityProviderSwitchError_Error(t *testing.T) {
	err := &AntigravityProviderSwitchError{
		OriginalProviderID: 789,
		RateLimitedModel:   "claude-opus-4-5",
		IsStickySession:    true,
	}
	msg := err.Error()
	require.Contains(t, msg, "789")
	require.Contains(t, msg, "claude-opus-4-5")
}
