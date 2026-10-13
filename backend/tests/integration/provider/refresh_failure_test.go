//go:build integration

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Refresh failures apply health changes to the credential identity used by the
// failed exchange. Sequential PostgreSQL replacements exercise rejected
// observations, longer existing cooldowns, notification failure after health
// writes, and transactional clearing of Antigravity refresh-request fields.

// TestRefreshFailureVersionCAS observes an OAuth identity before sequential
// configuration changes and applies permanent or cooldown failure. Renames
// remain eligible, while changed credentials, status, scheduling, platform,
// type, or proxy leave health and event counts unchanged.
func TestRefreshFailureVersionCAS(t *testing.T) {
	for _, kind := range []provider.RefreshFailureKind{provider.RefreshFailurePermanent, provider.RefreshFailureCooldown} {
		for _, change := range []string{"none", "name", "credentials", "platform", "type", "status", "schedulable", "proxy"} {
			t.Run(string(rune('0'+kind))+"/"+change, func(t *testing.T) {
				ctx := context.Background()
				client := testEntClient(t)
				row, err := client.Provider.Create().SetName("refresh-failure-fixture").SetPlatform(provider.PlatformOpenAI).SetType(provider.ProviderTypeOAuth).SetStatus(provider.StatusActive).SetSchedulable(true).SetCredentials(map[string]any{"access_token": "old-fixture", "refresh_token": "old-refresh"}).Save(ctx)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
				repo := newProviderStoreContract(client, integrationDB, nil)
				store := repo
				snapshot, err := store.GetByID(ctx, row.ID)
				require.NoError(t, err)
				version := provider.FailureVersion(snapshot)
				switch change {
				case "name":
					_, err = client.Provider.UpdateOneID(row.ID).SetName("current-name").Save(ctx)
				case "credentials":
					_, err = client.Provider.UpdateOneID(row.ID).SetCredentials(map[string]any{"access_token": "admin-new-fixture"}).Save(ctx)
				case "platform":
					_, err = client.Provider.UpdateOneID(row.ID).SetPlatform(provider.PlatformGemini).Save(ctx)
				case "type":
					_, err = client.Provider.UpdateOneID(row.ID).SetType(provider.ProviderTypeAPIKey).Save(ctx)
				case "status":
					_, err = client.Provider.UpdateOneID(row.ID).SetStatus("inactive").Save(ctx)
				case "schedulable":
					_, err = client.Provider.UpdateOneID(row.ID).SetSchedulable(false).Save(ctx)
				case "proxy":
					proxy, createErr := client.Proxy.Create().SetName("refresh-failure-proxy").SetProtocol("http").SetHost("127.0.0.1").SetPort(8181).Save(ctx)
					require.NoError(t, createErr)
					t.Cleanup(func() {
						_, clearErr := integrationDB.ExecContext(context.Background(), "UPDATE providers SET proxy_id=NULL WHERE id=$1", row.ID)
						require.NoError(t, clearErr)
						require.NoError(t, client.Proxy.DeleteOneID(proxy.ID).Exec(context.Background()))
					})
					_, err = client.Provider.UpdateOneID(row.ID).SetProxyID(proxy.ID).Save(ctx)
				}
				require.NoError(t, err)
				count := func() int {
					t.Helper()
					var n int
					require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&n))
					return n
				}
				before := count()
				// Match database precision for the exact applied-cooldown equality check.
				failure := provider.RefreshFailure{Kind: kind, Message: "fixture rejected", Until: time.Now().Add(10 * time.Minute).Truncate(time.Microsecond)}
				matched, err := store.ApplyOAuthRefreshFailure(ctx, version, failure)
				require.NoError(t, err)
				current, err := client.Provider.Get(ctx, row.ID)
				require.NoError(t, err)
				if change != "none" && change != "name" {
					require.False(t, matched)
					require.Empty(t, current.ErrorMessage)
					require.Nil(t, current.TempUnschedulableUntil)
					require.Equal(t, before, count())
					return
				}
				require.True(t, matched)
				require.Equal(t, before+1, count())
				if kind == provider.RefreshFailurePermanent {
					require.Equal(t, provider.StatusError, current.Status)
					require.False(t, current.Schedulable)
					require.NotNil(t, current.ErrorMessage)
					require.Equal(t, failure.Message, *current.ErrorMessage)
				} else {
					require.True(t, failure.Until.Equal(*current.TempUnschedulableUntil))
					require.NotNil(t, current.TempUnschedulableReason)
					require.Equal(t, failure.Message, *current.TempUnschedulableReason)
					require.Equal(t, provider.StatusActive, current.Status)
				}
				if change == "name" {
					require.Equal(t, "current-name", current.Name)
				}
			})
		}
	}
}

// TestRefreshFailureLongerCooldownAndOutboxFailure applies a shorter cooldown
// to a provider with a longer existing window. The stored deadline, reason, and
// timestamp remain, then permanent failure still applies when the notification
// statement fails.
func TestRefreshFailureLongerCooldownAndOutboxFailure(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	// Establish the existing deadline at the precision read back from PostgreSQL.
	until := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	// Include nanoseconds in the input, then compare against the value persisted at microsecond precision.
	updatedAt := until.Add(-time.Hour).Add(123 * time.Nanosecond)
	row, err := client.Provider.Create().SetName("refresh-failure-semantics").SetPlatform(provider.PlatformOpenAI).SetType(provider.ProviderTypeOAuth).SetStatus(provider.StatusActive).SetSchedulable(true).SetCredentials(map[string]any{}).SetTempUnschedulableUntil(until).SetTempUnschedulableReason("existing-longer").SetUpdatedAt(updatedAt).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
	before, err := client.Provider.Get(ctx, row.ID)
	require.NoError(t, err)
	store := newProviderStoreContract(client, integrationDB, nil)
	snapshot, err := store.GetByID(ctx, row.ID)
	require.NoError(t, err)
	version := provider.FailureVersion(snapshot)
	matched, err := store.ApplyOAuthRefreshFailure(ctx, version, provider.RefreshFailure{Kind: provider.RefreshFailureCooldown, Message: "shorter", Until: until.Add(-time.Minute)})
	require.NoError(t, err)
	require.True(t, matched)
	current, err := client.Provider.Get(ctx, row.ID)
	require.NoError(t, err)
	require.NotNil(t, current.TempUnschedulableReason)
	require.Equal(t, "existing-longer", *current.TempUnschedulableReason)
	require.True(t, until.Equal(*current.TempUnschedulableUntil))
	require.Equal(t, before.UpdatedAt, current.UpdatedAt)
	store.SetEvents(failConfigurationOutbox{})
	matched, err = store.ApplyOAuthRefreshFailure(ctx, version, provider.RefreshFailure{Kind: provider.RefreshFailurePermanent, Message: "fixture auth rejected"})
	require.NoError(t, err)
	require.True(t, matched)
	current, err = client.Provider.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, provider.StatusError, current.Status)
	require.False(t, current.Schedulable)
}

// TestAntigravityRefreshRequestClearCAS observes credentials before clearing
// Antigravity force-refresh fields, then optionally reauthorizes or fails the
// outbox write. Successful clearing keeps quota data, while conflicts and event
// failure retain the requested refresh flag.
func TestAntigravityRefreshRequestClearCAS(t *testing.T) {
	for _, mode := range []string{"success", "reauthorized", "outbox_failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			row, err := client.Provider.Create().SetName("refresh-request-fixture").SetPlatform(provider.PlatformAntigravity).SetType(provider.ProviderTypeOAuth).SetStatus(provider.StatusActive).SetSchedulable(true).SetCredentials(map[string]any{"access_token": "fixture"}).SetExtra(map[string]any{provider.AntigravityForceTokenRefreshExtraKey: true, provider.AntigravityForceTokenRefreshReasonExtraKey: "fixture 401", provider.AntigravityForceTokenRefreshAtExtraKey: "fixture time", "quota_used": 17.0}).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			store := newProviderStoreContract(client, integrationDB, nil)
			snapshot, err := store.GetByID(ctx, row.ID)
			require.NoError(t, err)
			version := provider.FailureVersion(snapshot).CredentialVersion
			if mode == "reauthorized" {
				_, err = client.Provider.UpdateOneID(row.ID).SetCredentials(map[string]any{"access_token": "new-admin-fixture"}).Save(ctx)
				require.NoError(t, err)
			}
			if mode == "outbox_failure" {
				store.SetEvents(failConfigurationOutbox{})
			}
			matched, err := store.ClearAntigravityRefreshRequest(ctx, version)
			if mode == "outbox_failure" {
				require.Error(t, err)
				require.False(t, matched)
			} else {
				require.NoError(t, err)
				require.Equal(t, mode == "success", matched)
			}
			current, err := client.Provider.Get(ctx, row.ID)
			require.NoError(t, err)
			require.Equal(t, 17.0, current.Extra["quota_used"])
			require.Equal(t, mode != "success", current.Extra[provider.AntigravityForceTokenRefreshExtraKey])
			if mode == "success" {
				require.Equal(t, "", current.Extra[provider.AntigravityForceTokenRefreshReasonExtraKey])
				require.Equal(t, "", current.Extra[provider.AntigravityForceTokenRefreshAtExtraKey])
			}
		})
	}
}
