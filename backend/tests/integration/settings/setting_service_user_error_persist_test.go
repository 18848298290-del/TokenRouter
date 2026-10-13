package settings_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/settings/composite"
	settingskit "github.com/TokenFlux/TokenRouter/internal/settings/testkit"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// The user error-request visibility flag needs to enter the composite settings
// write batch. A capturing map fixture checks the key's presence and true
// string value after Save, establishing the repository handoff while unexpected
// single-value reads panic.

// TestAllowUserViewErrorRequests_PersistsToDB saves an enabled error-request
// visibility flag through a capturing repository fixture. Its recorded batch
// contains the required key with true text; this case performs no database
// write.
func TestAllowUserViewErrorRequests_PersistsToDB(t *testing.T) {
	// Leaving GetValue unstubbed also catches unexpected single-setting reads.
	repo := &bmUpdateRepoStub{}
	svc := settingskit.NewComposite(repo, &config.Config{})

	err := svc.Save(context.Background(), &composite.Snapshot{
		AllowUserViewErrorRequests: true,
	})
	require.NoError(t, err)

	val, ok := repo.updates[usage.SettingKeyAllowUserViewErrorRequests]
	require.True(t, ok, "updates map 中应包含 SettingKeyAllowUserViewErrorRequests，但未找到（bug：buildSystemSettingsUpdates 漏写）")
	require.Equal(t, "true", val)
}
