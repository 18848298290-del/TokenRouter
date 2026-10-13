//go:build integration

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Refresh cooldown recovery qualifies the observed credentials, provider
// status, window, and reason before clearing temporary suspension. Sequential
// PostgreSQL changes check which observations still match, including
// cancellation and a notification failure after a successful state write.

// TestRefreshCooldownDatabaseIdentity reads a cooldown then sequentially
// changes identity, status, window, or reason before recovery. A rename remains
// eligible; rejected observations stay unchanged, and a failed notification
// does not undo successful clearing.
func TestRefreshCooldownDatabaseIdentity(t *testing.T) {
	for _, change := range []string{"none", "name", "credentials", "status", "window", "reason", "outbox_failure", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			// Match database precision when comparing the observed and stored cooldown.
			until := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
			row, err := client.Provider.Create().SetName("test-refresh-cooldown").SetPlatform(provider.PlatformGemini).SetType(provider.ProviderTypeOAuth).SetStatus(provider.StatusActive).SetCredentials(map[string]any{"refresh_token": "observed"}).SetTempUnschedulableUntil(until).SetTempUnschedulableReason("old").Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			store := newProviderStoreContract(client, integrationDB, nil)
			observed, err := store.GetByID(ctx, row.ID)
			require.NoError(t, err)
			switch change {
			case "name":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetName("renamed").Exec(ctx))
			case "credentials":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetCredentials(map[string]any{"refresh_token": "new"}).Exec(ctx))
			case "status":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetStatus(provider.StatusDisabled).Exec(ctx))
			case "window":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetTempUnschedulableUntil(until.Add(time.Hour)).Exec(ctx))
			case "reason":
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetTempUnschedulableReason("new").Exec(ctx))
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
			applied, err := store.ClearRefreshCooldownIfUnchanged(ctx, provider.ObserveRefreshCooldown(observed))
			if change == "cancelled" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
			}
			want := change == "none" || change == "name" || change == "outbox_failure"
			require.Equal(t, want, applied)
			after, err := client.Provider.Get(context.Background(), row.ID)
			require.NoError(t, err)
			require.Equal(t, before.Credentials, after.Credentials)
			require.Equal(t, before.Name, after.Name)
			require.Equal(t, before.Status, after.Status)
			var eventsAfter int
			require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&eventsAfter))
			if want {
				require.Nil(t, after.TempUnschedulableUntil)
				require.Nil(t, after.TempUnschedulableReason)
				if change != "outbox_failure" {
					require.Equal(t, eventsBefore+1, eventsAfter)
				}
			} else {
				require.Equal(t, before.TempUnschedulableUntil, after.TempUnschedulableUntil)
				require.Equal(t, before.TempUnschedulableReason, after.TempUnschedulableReason)
				require.Equal(t, eventsBefore, eventsAfter)
			}
		})
	}
}
