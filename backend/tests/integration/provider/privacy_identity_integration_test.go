//go:build integration

package provider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// OpenAI and Antigravity privacy observations are applied to the provider
// identity used for the query. Fixture callbacks change credentials or status
// before writeback, and a failing outbox adapter checks that rejected or
// rolled-back results leave the existing privacy mode and event count
// unchanged.

// TestPrivacyObservationIdentityAndOutbox changes credentials or status during
// a fixture privacy observation on OpenAI and Antigravity providers. Rejected
// identities and failing event writes retain the previous privacy mode and
// outbox count; the unchanged case commits both.
func TestPrivacyObservationIdentityAndOutbox(t *testing.T) {
	for _, platform := range []string{provider.PlatformOpenAI, provider.PlatformAntigravity} {
		for _, scenario := range []string{"success", "credential_changed", "status_changed", "outbox_failure"} {
			t.Run(platform+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				client := testEntClient(t)
				row, err := client.Provider.Create().SetName("test-privacy").SetPlatform(platform).SetType(provider.ProviderTypeOAuth).SetStatus(provider.StatusActive).SetCredentials(map[string]any{"access_token": "old", "project_id": "fixture"}).SetExtra(map[string]any{"privacy_mode": "previous"}).Save(ctx)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(ctx)) })
				store := newProviderStoreContract(client, integrationDB, nil)
				if scenario == "outbox_failure" {
					store.SetEvents(failConfigurationOutbox{})
				}
				v, err := store.GetByID(ctx, row.ID)
				require.NoError(t, err)
				// The observer returns a successful mode after committing the
				// selected identity change, placing writeback behind that committed replacement.
				observe := func() string {
					switch scenario {
					case "credential_changed":
						require.NoError(t, client.Provider.UpdateOneID(row.ID).SetCredentials(map[string]any{"access_token": "new", "project_id": "new-project"}).Exec(ctx))
					case "status_changed":
						require.NoError(t, client.Provider.UpdateOneID(row.ID).SetStatus(provider.StatusDisabled).Exec(ctx))
					}
					return "privacy_set"
				}
				core := provider.NewPrivacyService(store, nil, provider.PrivacyOptions{OpenAI: func(context.Context, string, string) string { return observe() }, Antigravity: func(context.Context, string, string, string) string { return observe() }})
				var before int
				require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&before))
				if platform == provider.PlatformOpenAI {
					core.ForceOpenAIPrivacy(ctx, v)
				} else {
					core.ForceAntigravityPrivacy(ctx, v)
				}
				current, err := client.Provider.Get(ctx, row.ID)
				require.NoError(t, err)
				var after int
				require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&after))
				if scenario == "success" {
					require.Equal(t, "privacy_set", current.Extra["privacy_mode"])
					require.Equal(t, before+1, after)
				} else {
					require.Equal(t, "previous", current.Extra["privacy_mode"])
					require.Equal(t, before, after)
				}
				require.NoError(t, core.StopContext(ctx))
			})
		}
	}
}
