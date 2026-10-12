//go:build integration

package provider_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestCRSRefreshDatabaseInterleaving checks that refresh compare-and-swap writes respect concurrent committed
// administrator changes.
func TestCRSRefreshDatabaseInterleaving(t *testing.T) {
	for _, mode := range []string{"success", "credentials", "disabled", "cancelled", "outbox_failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := testEntClient(t)
			row, err := client.Provider.Create().SetName("test-crs-refresh").SetPlatform(provider.PlatformOpenAI).SetType(provider.ProviderTypeOAuth).SetCredentials(map[string]any{"refresh_token": "source", "_token_version": 123}).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			store := newProviderStoreContract(client, integrationDB, nil)
			original, err := store.GetByID(ctx, row.ID)
			require.NoError(t, err)
			if mode == "outbox_failure" {
				store.SetEvents(failConfigurationOutbox{})
			}
			api := provider.NewOAuthRefreshAPI(store, nil, provider.RefreshOptions{})
			started, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			calls := 0
			go func() {
				done <- api.RefreshImported(ctx, original, fmt.Sprintf("test-crs:%d", row.ID), func(context.Context, *provider.Record) map[string]any {
					calls++
					close(started)
					<-release
					return map[string]any{"refresh_token": "exchanged", "_token_version": 123}
				})
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("导入交换未开始")
			}
			switch mode {
			case "credentials":
				require.NoError(t, store.UpdateCredentials(ctx, row.ID, map[string]any{"refresh_token": "administrator"}))
			case "disabled":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetStatus("inactive").Exec(ctx))
			case "cancelled":
				cancel()
			}
			close(release)
			var result error
			select {
			case result = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("导入交换未结束")
			}
			current, err := store.GetByID(context.Background(), row.ID)
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			switch mode {
			case "success":
				require.NoError(t, result)
				require.Equal(t, "exchanged", current.Credentials["refresh_token"])
				require.Equal(t, float64(123), current.Credentials["_token_version"])
			case "credentials":
				require.NoError(t, result)
				require.Equal(t, "administrator", current.Credentials["refresh_token"])
			case "disabled":
				require.NoError(t, result)
				require.Equal(t, "inactive", current.Status)
				require.Equal(t, "source", current.Credentials["refresh_token"])
			case "cancelled":
				require.ErrorIs(t, result, context.Canceled)
				require.Equal(t, "source", current.Credentials["refresh_token"])
			case "outbox_failure":
				require.Error(t, result)
				require.Equal(t, "source", current.Credentials["refresh_token"])
			}
		})
	}
}
