//go:build integration

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestTierObservationUsesCurrentIdentityAndFieldPatch checks that tier and Drive storage updates preserve admin fields
// and reject credentials changed during observation or before the configuration lock.
func TestTierObservationUsesCurrentIdentityAndFieldPatch(t *testing.T) {
	for _, scenario := range []string{"success", "changed_during_observation", "changed_before_lock", "outbox_failure", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := testEntClient(t)
			initial := map[string]any{"oauth_type": "google_one", "access_token": "original", "tier_id": "old"}
			row, err := client.Provider.Create().SetName("test-tier-observation").SetPlatform(provider.PlatformGemini).SetType(provider.ProviderTypeOAuth).SetStatus(provider.StatusActive).SetCredentials(initial).SetExtra(map[string]any{"admin_setting": "old"}).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			store := newProviderStoreContract(client, integrationDB, nil)
			if scenario == "outbox_failure" {
				store.SetEvents(failConfigurationOutbox{})
			}
			rotated := provider.CloneValues(initial)
			rotated["access_token"] = "admin-new"
			options := provider.AdminOptions{Creation: provider.CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: func() string { return "00000000-0000-4000-8000-000000000001" }}, Credentials: provider.CreateCredentialHooks{Site: func(*provider.Record) (string, error) { return "", nil }, ValidateEdit: func(context.Context, *provider.Record, bool) error {
				if scenario == "changed_before_lock" {
					return client.Provider.UpdateOneID(row.ID).SetCredentials(rotated).Exec(ctx)
				}
				return nil
			}}}
			admin := provider.NewAdmin(store, options)
			observed, err := admin.GetProvider(ctx, row.ID)
			require.NoError(t, err)
			tier := provider.NewTierManagement(admin, provider.TierManagementOptions{Observe: func(context.Context, *provider.Record) (provider.GoogleOneTierObservation, error) {
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetExtra(map[string]any{"admin_setting": "new"}).Exec(ctx))
				if scenario == "changed_during_observation" {
					require.NoError(t, client.Provider.UpdateOneID(row.ID).SetCredentials(rotated).Exec(ctx))
				}
				if scenario == "canceled" {
					cancel()
				}
				return provider.GoogleOneTierObservation{TierID: "google_ai_pro", Storage: &provider.GoogleOneStorage{Limit: 2000, Usage: 12}, ObservedAt: time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC)}, nil
			}})
			var before int
			require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&before))
			_, err = tier.Refresh(ctx, observed)
			if scenario == "success" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			current, readErr := client.Provider.Get(context.Background(), row.ID)
			require.NoError(t, readErr)
			require.Equal(t, "new", current.Extra["admin_setting"])
			if scenario == "changed_during_observation" || scenario == "changed_before_lock" {
				require.Equal(t, "admin-new", current.Credentials["access_token"])
			} else {
				require.Equal(t, "original", current.Credentials["access_token"])
			}
			var after int
			require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&after))
			if scenario == "success" {
				require.Equal(t, "google_ai_pro", current.Credentials["tier_id"])
				require.EqualValues(t, 2000, current.Extra["drive_storage_limit"])
				require.EqualValues(t, 12, current.Extra["drive_storage_usage"])
				require.Equal(t, before+1, after)
			} else {
				require.Equal(t, "old", current.Credentials["tier_id"])
				require.NotContains(t, current.Extra, "drive_storage_limit")
				require.Equal(t, before, after)
			}
			require.NoError(t, tier.StopContext(context.Background()))
		})
	}
}
