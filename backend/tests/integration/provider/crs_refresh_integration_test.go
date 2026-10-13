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

// A CRS import refresh can hold an exchanged token while the imported provider
// identity changes. Channel gates pause the exchange callback while the test
// changes credentials, disables the provider, or cancels the operation.
//
// RefreshImported then finishes against the PostgreSQL store, making its
// returned error and persisted credentials observable after the gate is
// released. Every case expects a single exchange, including identity conflicts
// that return current data instead of exchanging again. The outbox-failure case
// exercises a database error during persistence and requires the original
// refresh token to remain stored. The successful case also checks the retained
// token-version value.

// TestCRSRefreshDatabaseInterleaving blocks an imported token exchange while
// credentials, status, or cancellation change. One exchange either commits the
// expected token or leaves the competing state intact, and outbox failure rolls
// back credential persistence.
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
			// Signal exchange entry before the callback waits on release, allowing
			// committed identity changes or cancellation to precede its returned token.
			// The callback waits solely for release; a fatal failure before closing that
			// channel can leave the exchange goroutine blocked despite context cancellation.
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
