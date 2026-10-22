package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFailureVersionCapturesScheduling records enabled and disabled providers
// before later source changes. Failure comparisons retain both the captured
// scheduling switch and an independently owned credential object.
func TestFailureVersionCapturesScheduling(t *testing.T) {
	require.Equal(t, RefreshFailureVersion{}, FailureVersion(nil))
	for _, schedulable := range []bool{false, true} {
		proxyID := int64(7)
		value := &Record{
			ID:          41,
			Platform:    PlatformOpenAI,
			Type:        ProviderTypeOAuth,
			Status:      StatusActive,
			Schedulable: schedulable,
			ProxyID:     &proxyID,
		}
		observed := FailureVersion(value)
		value.Schedulable = !schedulable
		proxyID = 8
		require.Equal(t, schedulable, observed.Schedulable)
		require.Equal(t, int64(41), observed.ID)
		require.Equal(t, PlatformOpenAI, observed.Platform)
		require.Equal(t, ProviderTypeOAuth, observed.Type)
		require.Equal(t, StatusActive, observed.Status)
		require.NotNil(t, observed.Credentials)
		require.Empty(t, observed.Credentials)
		require.Equal(t, int64(7), *observed.ProxyID)
		observed.Credentials["token"] = "captured"
		require.Nil(t, value.Credentials)
	}
}
