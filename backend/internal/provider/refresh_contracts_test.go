package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Coordinator construction chooses the lease used for distributed refresh
// exclusion. These fixtures compare the default duration with an
// administrator-supplied duration and repeat the zero-input case, protecting the
// value captured before any exchange occurs.

// TestNewOAuthRefreshAPI_DefaultTTL constructs a coordinator with empty options
// and dependencies. Its captured lock lease equals defaultRefreshLockTTL, so the
// default is available before any exchange starts.
func TestNewOAuthRefreshAPI_DefaultTTL(t *testing.T) {
	api := NewOAuthRefreshAPI(nil, nil, RefreshOptions{})
	require.Equal(t, defaultRefreshLockTTL, api.lockTTL)
}

// TestNewOAuthRefreshAPI_CustomTTL supplies a ninety-second lock lease at
// construction. The captured duration equals that supplied value, protecting the
// administrator override from default replacement.
func TestNewOAuthRefreshAPI_CustomTTL(t *testing.T) {
	api := NewOAuthRefreshAPI(nil, nil, RefreshOptions{LockTTL: 90 * time.Second})
	require.Equal(t, 90*time.Second, api.lockTTL)
}

// TestNewOAuthRefreshAPI_ZeroTTLUsesDefault supplies options with the zero lock
// duration. Construction records defaultRefreshLockTTL, protecting default lease
// selection for an omitted or zero-valued setting.
func TestNewOAuthRefreshAPI_ZeroTTLUsesDefault(t *testing.T) {
	api := NewOAuthRefreshAPI(nil, nil, RefreshOptions{})
	require.Equal(t, defaultRefreshLockTTL, api.lockTTL)
}
