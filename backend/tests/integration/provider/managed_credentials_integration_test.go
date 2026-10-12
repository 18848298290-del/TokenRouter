//go:build integration

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestManagedCredentialsDatabaseCAS checks credential identity under the configuration lock and rejects
// conflicting admin refreshes.
func TestManagedCredentialsDatabaseCAS(t *testing.T) {
	for _, change := range []string{"none", "name", "credentials", "status", "proxy", "outbox_failure", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			row, err := client.Provider.Create().SetName("test-managed-refresh").SetPlatform(provider.PlatformAnthropic).SetType(provider.ProviderTypeOAuth).SetStatus(provider.StatusActive).SetCredentials(map[string]any{"refresh_token": "observed", "access_token": "old"}).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			store := newProviderStoreContract(client, integrationDB, nil)
			observed, err := store.GetByID(ctx, row.ID)
			require.NoError(t, err)
			expected := provider.FailureVersion(observed).CredentialVersion
			switch change {
			case "name":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetName("renamed").Exec(ctx))
			case "credentials":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetCredentials(map[string]any{"refresh_token": "administrator"}).Exec(ctx))
			case "status":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetStatus(provider.StatusDisabled).Exec(ctx))
			case "proxy":
				proxy, err := client.Proxy.Create().SetName("test-managed-proxy").SetProtocol("http").SetHost("127.0.0.1").SetPort(8123).Save(ctx)
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
			var eventsBefore int
			require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&eventsBefore))
			desired := provider.CloneRecord(observed)
			desired.Credentials = map[string]any{"refresh_token": "exchanged", "access_token": "new"}
			err = store.UpdateConfiguration(ctx, desired, provider.ConfigurationChange{Fields: provider.ConfigCredentials, ExpectedCredentials: &expected})
			want := change == "none" || change == "name"
			switch {
			case want:
				require.NoError(t, err)
			case change == "cancelled":
				require.ErrorIs(t, err, context.Canceled)
			case change == "outbox_failure":
				require.Error(t, err)
			default:
				require.ErrorIs(t, err, provider.ErrRefreshProviderStateChanged)
			}
			after, readErr := client.Provider.Get(context.Background(), row.ID)
			require.NoError(t, readErr)
			require.Equal(t, before.Name, after.Name)
			require.Equal(t, before.Status, after.Status)
			var eventsAfter int
			require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&eventsAfter))
			if want {
				require.Equal(t, "exchanged", after.Credentials["refresh_token"])
				require.Equal(t, eventsBefore+1, eventsAfter)
			} else {
				require.Equal(t, before.Credentials, after.Credentials)
				require.Equal(t, eventsBefore, eventsAfter)
			}
			// The admin entry point rejects a known identity conflict before validation or persistence.
			if change == "credentials" {
				validations := 0
				admin := provider.NewAdmin(store, provider.AdminOptions{Creation: provider.CreationOptions{Now: time.Now}, Credentials: provider.CreateCredentialHooks{Site: func(*provider.Record) (string, error) { validations++; return "", nil }}})
				_, err := admin.UpdateProvider(context.Background(), row.ID, &provider.UpdateProviderInput{Credentials: desired.Credentials, ExpectedCredentials: &expected})
				require.ErrorIs(t, err, provider.ErrRefreshProviderStateChanged)
				require.Zero(t, validations)
			}
		})
	}
}
