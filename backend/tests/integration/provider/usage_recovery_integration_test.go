//go:build integration

package provider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Usage recovery changes a provider's error state when the observed credential
// identity and error still match. Sequential PostgreSQL updates exercise
// rejected observations, cancellation, harmless renames, and notification
// failure, while checking that the manual scheduling switch stays disabled.

// TestUsageRecoveryDatabaseIdentity observes an errored OAuth provider before
// sequential identity or error changes. Recovery tolerates a rename and
// notification failure, rejects stale state, and keeps the manual scheduling
// switch disabled in every case.
func TestUsageRecoveryDatabaseIdentity(t *testing.T) {
	for _, change := range []string{"none", "name", "credentials", "status", "error", "proxy", "cancelled", "outbox_failure"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			row, err := client.Provider.Create().SetName("test-usage-recovery").SetPlatform(provider.PlatformOpenAI).SetType(provider.ProviderTypeOAuth).SetStatus(provider.StatusError).SetErrorMessage("token refresh failed").SetSchedulable(false).SetCredentials(map[string]any{"refresh_token": "observed"}).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			store := newProviderStoreContract(client, integrationDB, nil)
			observed, err := store.GetByID(ctx, row.ID)
			require.NoError(t, err)
			switch change {
			case "name":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetName("new name").Exec(ctx))
			case "credentials":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetCredentials(map[string]any{"refresh_token": "administrator"}).Exec(ctx))
			case "status":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetStatus(provider.StatusDisabled).Exec(ctx))
			case "error":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetErrorMessage("administrator forbidden").Exec(ctx))
			case "proxy":
				proxy, err := client.Proxy.Create().SetName("test-usage-recovery-proxy").SetProtocol("http").SetHost("127.0.0.1").SetPort(8182).Save(ctx)
				require.NoError(t, err)
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetProxyID(proxy.ID).Exec(ctx))
				t.Cleanup(func() {
					require.NoError(t, client.Provider.UpdateOneID(row.ID).ClearProxyID().Exec(context.Background()))
					require.NoError(t, client.Proxy.DeleteOneID(proxy.ID).Exec(context.Background()))
				})
			case "outbox_failure":
				store.SetEvents(failConfigurationOutbox{})
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before, err := client.Provider.Get(context.Background(), row.ID)
			require.NoError(t, err)
			var outboxBefore int
			require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&outboxBefore))
			applied, err := provider.RecoverUsageProviderError(ctx, observed, store)
			if change == "cancelled" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
			}
			current, readErr := client.Provider.Get(context.Background(), row.ID)
			require.NoError(t, readErr)
			want := change == "none" || change == "name" || change == "outbox_failure"
			require.Equal(t, want, applied)
			require.False(t, current.Schedulable, "恢复错误不等同于显式重新启用调度")
			var outboxAfter int
			require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&outboxAfter))
			if want {
				require.Equal(t, provider.StatusActive, current.Status)
				require.NotNil(t, current.ErrorMessage)
				require.Empty(t, *current.ErrorMessage)
				require.Equal(t, provider.StatusActive, observed.Status)
				if change != "outbox_failure" {
					require.Equal(t, outboxBefore+1, outboxAfter)
				}
			} else {
				require.Equal(t, before.Status, current.Status)
				require.Equal(t, before.ErrorMessage, current.ErrorMessage)
				require.Equal(t, before.Credentials, current.Credentials)
				require.Equal(t, outboxBefore, outboxAfter)
				require.Equal(t, provider.StatusError, observed.Status)
			}
		})
	}
}
