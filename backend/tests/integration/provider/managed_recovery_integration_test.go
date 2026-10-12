//go:build integration

package provider_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
)

// managedRecoveryInterleaveStore injects administrator changes before each recovery write to test PostgreSQL guards.
type managedRecoveryInterleaveStore struct {
	*providerpostgres.ProviderStore
	before  func(provider.ManagedRecoveryStep) error
	applied []provider.ManagedRecoveryStep
}

func (s *managedRecoveryInterleaveStore) ApplyManagedRecoveryStep(ctx context.Context, step provider.ManagedRecoveryStep, v provider.ManagedRecoveryVersion) (bool, error) {
	if err := s.before(step); err != nil {
		return false, err
	}
	applied, err := s.ProviderStore.ApplyManagedRecoveryStep(ctx, step, v)
	if applied {
		s.applied = append(s.applied, step)
	}
	return applied, err
}

func TestManagedRecoveryIndependentCommitsAndIdentity(t *testing.T) {
	for step := provider.ManagedRecoveryError; step <= provider.ManagedRecoveryTemporary; step++ {
		for _, scenario := range []string{"unchanged", "credentials", "error", "owned_state", "write_failure", "outbox_failure", "cancel"} {
			t.Run(fmt.Sprintf("step_%d/%s", step, scenario), func(t *testing.T) {
				ctx := context.Background()
				client := testEntClient(t)
				now := time.Now().UTC().Truncate(time.Second)
				until := now.Add(time.Hour)
				row, err := client.Provider.Create().SetName("test-ag-recovery").SetPlatform(provider.PlatformAntigravity).SetType(provider.ProviderTypeOAuth).SetStatus(provider.StatusError).SetErrorMessage("missing_project_id: original").SetCredentials(map[string]any{"access_token": "old", "refresh_token": "old"}).SetRateLimitedAt(now).SetRateLimitResetAt(until).SetOverloadUntil(until).SetTempUnschedulableUntil(until).SetTempUnschedulableReason("old reason").SetExtra(map[string]any{"antigravity_quota_scopes": map[string]any{"old": true}, "model_rate_limits": map[string]any{"old": true}, "unrelated": "keep"}).Save(ctx)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
				store := newProviderStoreContract(client, integrationDB, nil)
				if scenario == "outbox_failure" {
					store.SetEvents(failConfigurationOutbox{})
				}
				observed, err := store.GetByID(ctx, row.ID)
				require.NoError(t, err)
				var before int
				require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&before))
				operation, cancel := context.WithCancel(ctx)
				defer cancel()
				writer := &managedRecoveryInterleaveStore{ProviderStore: store, before: func(current provider.ManagedRecoveryStep) error {
					if current != step {
						return nil
					}
					switch scenario {
					case "credentials":
						return client.Provider.UpdateOneID(row.ID).SetCredentials(map[string]any{"access_token": "administrator"}).SetStatus(provider.StatusDisabled).Exec(ctx)
					case "error":
						return client.Provider.UpdateOneID(row.ID).SetStatus(provider.StatusError).SetErrorMessage("administrator error").Exec(ctx)
					case "owned_state":
						switch step {
						case provider.ManagedRecoveryError:
							return client.Provider.UpdateOneID(row.ID).SetErrorMessage("new error").Exec(ctx)
						case provider.ManagedRecoveryRateLimit:
							return client.Provider.UpdateOneID(row.ID).SetRateLimitResetAt(until.Add(time.Hour)).Exec(ctx)
						case provider.ManagedRecoveryQuotaScopes:
							_, err := integrationDB.ExecContext(ctx, "UPDATE providers SET extra=jsonb_set(extra,'{antigravity_quota_scopes}','null'::jsonb) WHERE id=$1", row.ID)
							return err
						case provider.ManagedRecoveryModelLimits:
							_, err := integrationDB.ExecContext(ctx, "UPDATE providers SET extra=extra-'model_rate_limits' WHERE id=$1", row.ID)
							return err
						case provider.ManagedRecoveryTemporary:
							return client.Provider.UpdateOneID(row.ID).SetTempUnschedulableReason("new reason").Exec(ctx)
						}
					case "write_failure":
						return errors.New("fixture step failed")
					case "cancel":
						cancel()
					case "unchanged":
						return client.Provider.UpdateOneID(row.ID).SetName("administrator rename").Exec(ctx)
					}
					return nil
				}}
				_, applied, err := provider.NewAdmin(writer, provider.AdminOptions{}).ClearManagedRefreshError(operation, observed)
				if scenario == "write_failure" || scenario == "cancel" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				success := scenario == "unchanged" || scenario == "outbox_failure"
				require.Equal(t, success, applied)
				current, err := store.GetByID(ctx, row.ID)
				require.NoError(t, err)
				require.Equal(t, "keep", current.Extra["unrelated"])
				var after int
				require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&after))
				expectedEvents := int(step)
				if success {
					expectedEvents = 5
				}
				require.Len(t, writer.applied, expectedEvents)
				// The outbox coalesces pending events per provider, so five publications leave one pending row.
				if expectedEvents > 0 {
					expectedEvents = 1
				}
				if scenario == "outbox_failure" {
					expectedEvents = 0
				}
				require.Equal(t, before+expectedEvents, after)
				if success {
					require.Equal(t, provider.StatusActive, current.Status)
					require.Empty(t, current.ErrorMessage)
					require.Nil(t, current.RateLimitedAt)
					require.Nil(t, current.RateLimitResetAt)
					require.Nil(t, current.OverloadUntil)
					require.Nil(t, current.TempUnschedulableUntil)
					require.NotContains(t, current.Extra, "antigravity_quota_scopes")
					require.NotContains(t, current.Extra, "model_rate_limits")
				} else if scenario == "credentials" {
					require.Equal(t, provider.StatusDisabled, current.Status)
					require.Equal(t, "administrator", current.GetCredential("access_token"))
				} else if scenario == "error" {
					require.Equal(t, "administrator error", current.ErrorMessage)
				} else if scenario == "write_failure" || scenario == "cancel" {
					// Earlier independently committed steps survive failure in a later step.
					if step > provider.ManagedRecoveryError {
						require.Equal(t, provider.StatusActive, current.Status)
					} else {
						require.Equal(t, provider.StatusError, current.Status)
					}
					if step > provider.ManagedRecoveryRateLimit {
						require.Nil(t, current.RateLimitedAt)
					} else {
						require.NotNil(t, current.RateLimitedAt)
					}
					require.NotNil(t, current.TempUnschedulableUntil)
				}
			})
		}
	}
}
