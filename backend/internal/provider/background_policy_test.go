package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Background refresh can finish at a distributed lock or discover current
// credentials after rereading. These fixtures check the error returned for both
// situations under the default skip policy and the explicitly selected success
// actions.

// TestBackgroundRefreshPolicy_DefaultSkips exercises contention and fresh
// credential results with the default policy. Both handlers return
// ErrRefreshSkipped so workers can distinguish them from completed exchanges.
func TestBackgroundRefreshPolicy_DefaultSkips(t *testing.T) {
	p := DefaultBackgroundRefreshPolicy()

	require.ErrorIs(t, p.HandleLockHeld(), ErrRefreshSkipped)
	require.ErrorIs(t, p.HandleAlreadyRefreshed(), ErrRefreshSkipped)
}

// TestBackgroundRefreshPolicy_SuccessOverride selects the success action for
// both unperformed exchange cases. Both handlers return nil, allowing the caller
// to count each result as successful.
func TestBackgroundRefreshPolicy_SuccessOverride(t *testing.T) {
	p := BackgroundRefreshPolicy{
		OnLockHeld:       BackgroundSkipAsSuccess,
		OnAlreadyRefresh: BackgroundSkipAsSuccess,
	}

	require.NoError(t, p.HandleLockHeld())
	require.NoError(t, p.HandleAlreadyRefreshed())
}
