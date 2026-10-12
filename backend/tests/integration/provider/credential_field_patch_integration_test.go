//go:build integration

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestCredentialFieldPatchPreservesLockTimeState rotates credentials before the configuration lock and checks
// that patches retain new tokens and untouched fields.
func TestCredentialFieldPatchPreservesLockTimeState(t *testing.T) {
	cases := []struct {
		name, field   string
		value         any
		outboxFailure bool
	}{{"org", "org_uuid", "patched", false}, {"clear_org", "org_uuid", nil, false}, {"provider", "account_uuid", "patched", false}, {"warmup_on", "intercept_warmup_requests", true, false}, {"warmup_off", "intercept_warmup_requests", false, false}, {"rollback", "org_uuid", "patched", true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			initial := map[string]any{"access_token": "old", "refresh_token": "old-refresh", "base_url": "https://old.invalid", "org_uuid": "old-org", "account_uuid": "old-provider", "intercept_warmup_requests": true}
			row, err := client.Provider.Create().SetName("test-credential-field").SetPlatform(provider.PlatformAnthropic).SetType(provider.ProviderTypeOAuth).SetStatus(provider.StatusActive).SetCredentials(initial).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			store := newProviderStoreContract(client, integrationDB, nil)
			if tc.outboxFailure {
				store.SetEvents(failConfigurationOutbox{})
			}
			rotated := provider.CloneValues(initial)
			rotated["access_token"] = "rotated"
			rotated["refresh_token"] = "rotated-refresh"
			rotated["base_url"] = "https://new.invalid"
			validations := 0
			options := provider.AdminOptions{Creation: provider.CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: func() string { return "00000000-0000-4000-8000-000000000001" }}, Credentials: provider.CreateCredentialHooks{Site: func(*provider.Record) (string, error) { return "", nil }, ValidateEdit: func(context.Context, *provider.Record, bool) error {
				validations++
				return client.Provider.UpdateOneID(row.ID).SetCredentials(rotated).Exec(ctx)
			}}}
			admin := provider.NewAdmin(store, options)
			var before int
			require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&before))
			_, err = admin.UpdateProvider(ctx, row.ID, &provider.UpdateProviderInput{Credentials: map[string]any{tc.field: tc.value}, PatchCredentials: true})
			if tc.outboxFailure {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, validations)
			current, err := client.Provider.Get(ctx, row.ID)
			require.NoError(t, err)
			require.Equal(t, "rotated", current.Credentials["access_token"])
			require.Equal(t, "rotated-refresh", current.Credentials["refresh_token"])
			require.Equal(t, "https://new.invalid", current.Credentials["base_url"])
			require.Contains(t, current.Credentials, tc.field)
			if tc.outboxFailure {
				require.Equal(t, rotated[tc.field], current.Credentials[tc.field])
			} else {
				require.Equal(t, tc.value, current.Credentials[tc.field])
			}
			var after int
			require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&after))
			if tc.outboxFailure {
				require.Equal(t, before, after)
			} else {
				require.Equal(t, before+1, after)
			}
		})
	}
}
