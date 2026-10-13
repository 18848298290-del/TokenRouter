package settings_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/settings/composite"
	settingskit "github.com/TokenFlux/TokenRouter/internal/settings/testkit"
)

// Saving backend mode publishes the newly prepared setting into the local
// runtime reader after the repository batch succeeds. The map fixture starts
// with a cached true value, captures false in the write batch, and checks the
// subsequent local Enabled result.

// TestUpdateSettings_InvalidatesBackendModeCache primes local backend mode as
// enabled before saving a disabled snapshot. The captured repository batch
// records false and the local runtime reader subsequently reports disabled.
func TestUpdateSettings_InvalidatesBackendModeCache(t *testing.T) {
	repo := &bmUpdateRepoStub{
		getValueFn: func(ctx context.Context, key string) (string, error) {
			require.Equal(t, gateway.SettingKeyBackendModeEnabled, key)
			return "true", nil
		},
	}
	svc := settingskit.NewComposite(repo, &config.Config{})
	svc.Backend.Publish(true)

	err := svc.Save(context.Background(), &composite.Snapshot{
		BackendModeEnabled: false,
	})
	require.NoError(t, err)
	require.Equal(t, "false", repo.updates[gateway.SettingKeyBackendModeEnabled])
	require.False(t, svc.Backend.Enabled(context.Background()))
}
