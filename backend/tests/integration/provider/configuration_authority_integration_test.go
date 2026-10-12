//go:build integration

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Inject committed database changes after reads to reproduce configuration, refresh, usage, and health-write
// interleavings.
type configurationInterleave struct {
	*providerpostgres.ProviderStore
	afterRead func()
}

func (r *configurationInterleave) GetByID(ctx context.Context, id int64) (*provider.Record, error) {
	v, err := r.ProviderStore.GetByID(ctx, id)
	if err == nil && r.afterRead != nil {
		f := r.afterRead
		r.afterRead = nil
		f()
	}
	return v, err
}

func TestConfigurationWriteAuthority(t *testing.T) {
	for _, mode := range []string{"name", "extra", "status", "credentials"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			row, err := client.Provider.Create().SetName("test-config-authority").
				SetPlatform(capability.PlatformOpenAI).SetType(capability.ProviderTypeAPIKey).
				SetCredentials(map[string]any{"api_key": "old-test-key", "upstream_protocols": []string{"openai_responses"}}).
				SetExtra(map[string]any{"quota_used": 1.0, "quota_limit": 100.0}).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			repo := &configurationInterleave{ProviderStore: newProviderStoreContract(client, integrationDB, nil)}
			repo.afterRead = func() {
				_, err := integrationDB.ExecContext(ctx, `UPDATE providers SET
				 credentials=jsonb_set(credentials,'{api_key}','"new-test-key"'),
				 extra=extra || '{"quota_used":7,"quota_daily_used":5,"grok_billing_snapshot":{"new":true}}'::jsonb,
				 status='error', error_message='new-health-error', schedulable=false WHERE id=$1`, row.ID)
				require.NoError(t, err)
			}
			admin := provider.NewAdmin(repo, provider.AdminOptions{Creation: provider.CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: uuid.NewString}, Credentials: provideradapter.CreateCredentialHooks(nil, nil)})
			switch mode {
			case "name":
				_, err = admin.UpdateProvider(ctx, row.ID, &provider.UpdateProviderInput{Name: "edited-name"})
			case "extra":
				_, err = admin.UpdateProvider(ctx, row.ID, &provider.UpdateProviderInput{Extra: map[string]any{"quota_limit": 200.0}})
			case "credentials":
				_, err = admin.UpdateProvider(ctx, row.ID, &provider.UpdateProviderInput{Credentials: map[string]any{"model_mapping": map[string]any{"alias": "target"}}})
			case "status":
				_, err = admin.UpdateProvider(ctx, row.ID, &provider.UpdateProviderInput{Status: billing.StatusActive})
			}

			require.NoError(t, err)
			current, err := repo.ProviderStore.GetByID(ctx, row.ID)
			require.NoError(t, err)
			require.Equal(t, "new-test-key", current.GetCredential("api_key"))
			require.Equal(t, float64(7), current.Extra["quota_used"])
			require.Equal(t, float64(5), current.Extra["quota_daily_used"])
			require.Equal(t, map[string]any{"new": true}, current.Extra["grok_billing_snapshot"])
			require.False(t, current.Schedulable)
			if mode == "status" {
				require.Equal(t, billing.StatusActive, current.Status)
				require.Equal(t, "new-health-error", current.ErrorMessage)
			} else {
				require.Equal(t, provider.StatusError, current.Status)
				require.Equal(t, "new-health-error", current.ErrorMessage)
			}
			if mode == "extra" {
				require.Equal(t, float64(200), current.Extra["quota_limit"])
			}
		})
	}
}
