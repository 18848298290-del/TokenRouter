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

// Anthropic active usage synchronization writes Extra observations and session-
// window columns in separate steps. PostgreSQL callbacks change credentials or
// window state between those steps, while injected failures check which
// successful write remains visible and which warning reaches the observer.

// passiveUsageInterleaveStore injects a callback between Extra observations and
// subsequent window writes.
type passiveUsageInterleaveStore struct {
	*providerpostgres.ProviderStore // delegated PostgreSQL store
	// afterExtra runs after the delegated Extra call, including an unsuccessful
	// call, so the fixture can alter identity or window state before the next step.
	afterExtra func()
}

// UpdateUsageExtraIfUnchanged delegates the Extra write, then invokes
// afterExtra when configured, including after a failed write. This synchronous
// injection precedes the service's independent session-window operation and
// exposes its separate identity comparison.
func (s passiveUsageInterleaveStore) UpdateUsageExtraIfUnchanged(ctx context.Context, v provider.UsageObservationVersion, updates map[string]any) (bool, error) {
	applied, err := s.ProviderStore.UpdateUsageExtraIfUnchanged(ctx, v, updates)
	if s.afterExtra != nil {
		s.afterExtra()
	}
	return applied, err
}

// TestPassiveUsageIdentityAndIndependentWindow injects changes between Extra
// and window writes or forces either path to fail. Assertions distinguish
// retained Extra from guarded window state and require a warning when the Extra
// update fails.
func TestPassiveUsageIdentityAndIndependentWindow(t *testing.T) {
	for _, scenario := range []string{"success", "extra_write_failed", "changed_before", "changed_between", "window_changed", "window_outbox_failed", "canceled", "missing_conditional_writer"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			// Keep old and replacement windows at directly comparable database instants.
			now := time.Now().UTC().Truncate(time.Second)
			oldEnd := now.Add(time.Hour)
			newEnd := now.Add(2 * time.Hour)
			row, err := client.Provider.Create().SetName("test-passive").SetPlatform(provider.PlatformAnthropic).SetType(provider.ProviderTypeOAuth).SetStatus(provider.StatusActive).SetCredentials(map[string]any{"access_token": "old"}).SetSessionWindowEnd(oldEnd).SetExtra(map[string]any{"unrelated": "keep"}).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			store := newProviderStoreContract(client, integrationDB, nil)
			observed, err := store.GetByID(ctx, row.ID)
			require.NoError(t, err)
			changeIdentity := func() {
				require.NoError(t, client.Provider.UpdateOneID(row.ID).SetCredentials(map[string]any{"access_token": "administrator"}).Exec(ctx))
			}
			writer := passiveUsageInterleaveStore{ProviderStore: store}
			switch scenario {
			case "changed_before":
				changeIdentity()
			case "changed_between":
				writer.afterExtra = changeIdentity
			case "window_changed":
				writer.afterExtra = func() {
					require.NoError(t, client.Provider.UpdateOneID(row.ID).SetSessionWindowEnd(newEnd.Add(time.Hour)).Exec(ctx))
				}
			case "window_outbox_failed":
				store.SetEvents(failConfigurationOutbox{})
			case "extra_write_failed":
				// Reject the utilization key for this fixture row while permitting
				// session-window columns, so the independently attempted window write can succeed.
				constraint := fmt.Sprintf("test_passive_extra_%d", row.ID)
				_, err := integrationDB.ExecContext(ctx, fmt.Sprintf("ALTER TABLE providers ADD CONSTRAINT %s CHECK (id<>%d OR NOT (COALESCE(extra,'{}'::jsonb) ? 'session_window_utilization')) NOT VALID", constraint, row.ID))
				require.NoError(t, err)
				t.Cleanup(func() {
					_, err := integrationDB.ExecContext(context.Background(), "ALTER TABLE providers DROP CONSTRAINT "+constraint)
					require.NoError(t, err)
				})
			}
			var reader provider.OAuthUsageReader = writer
			// Hide the optional conditional-write methods behind the reader interface
			// to check the service's behavior when identity-safe persistence is unavailable.
			if scenario == "missing_conditional_writer" {
				reader = struct {
					provider.OAuthUsageReader // delegated usage reader methods
				}{store}
			}
			operation, cancel := context.WithCancel(ctx)
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			var warnings []error
			core := provider.NewOAuthUsageService(reader, nil, nil, provider.OAuthUsageOptions{Warn: func(_ string, args ...any) {
				for _, arg := range args {
					if err, ok := arg.(error); ok {
						warnings = append(warnings, err)
					}
				}
			}})
			core.SyncActiveToPassive(operation, observed, &provider.UsageInfo{FiveHour: &provider.UsageProgress{Utilization: 37, ResetsAt: &newEnd}})
			got, err := store.GetByID(ctx, row.ID)
			require.NoError(t, err)
			require.Equal(t, "keep", got.Extra["unrelated"])
			switch scenario {
			case "success":
				require.Equal(t, .37, got.Extra["session_window_utilization"])
				require.True(t, newEnd.Equal(*got.SessionWindowEnd))
			case "changed_before", "canceled", "missing_conditional_writer":
				require.NotContains(t, got.Extra, "session_window_utilization")
				require.True(t, oldEnd.Equal(*got.SessionWindowEnd))
			case "changed_between":
				require.Equal(t, .37, got.Extra["session_window_utilization"])
				require.True(t, oldEnd.Equal(*got.SessionWindowEnd))
				require.Equal(t, "administrator", got.GetCredential("access_token"))
			case "window_changed":
				require.True(t, newEnd.Add(time.Hour).Equal(*got.SessionWindowEnd))
			case "window_outbox_failed":
				require.Equal(t, .37, got.Extra["session_window_utilization"])
				require.True(t, newEnd.Equal(*got.SessionWindowEnd))
			case "extra_write_failed":
				require.NotContains(t, got.Extra, "session_window_utilization")
				require.True(t, newEnd.Equal(*got.SessionWindowEnd))
				require.NotEmpty(t, warnings)
			}
			if scenario == "canceled" {
				require.True(t, errors.Is(warnings[0], context.Canceled))
			}
		})
	}
}
