//go:build integration

package provider_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// A copied provider may be active while its scheduling switch is paused.
// PostgreSQL checks provider creation together with group membership and its
// scheduler event, then supplies a nonexistent group to verify that the failed
// copy leaves no provider, membership, or event rows.

// TestCreateWithProviderGroupsPersistsPausedCopyAtomically creates an active
// provider whose scheduling switch is false together with a group binding. A
// nonexistent group then forces a failed creation, leaving no provider,
// membership, or scheduler event.
func TestCreateWithProviderGroupsPersistsPausedCopyAtomically(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newProviderStoreContract(client, integrationDB, nil)
	suffix := time.Now().UnixNano()

	group, err := client.Group.Create().
		SetName(fmt.Sprintf("duplicate-atomic-%d", suffix)).
		Save(ctx)
	require.NoError(t, err)

	success := &provider.Record{
		Name:        fmt.Sprintf("duplicate-success-%d", suffix),
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: false,
		Credentials: map[string]any{"api_key": "secret"},
		Extra:       map[string]any{},
	}
	require.NoError(t, repo.CreateWithProviderGroups(ctx, success, []provider.GroupMembership{{GroupID: group.ID}}))
	// These rows commit outside testEntTx; remove event and membership rows
	// before their provider and group so cleanup follows the fixture dependencies.
	// Errors are discarded for each deletion, allowing the remaining cleanup
	// attempts to run even when an earlier statement fails.
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE provider_id = $1", success.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM provider_groups WHERE provider_id = $1", success.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM providers WHERE id = $1", success.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id = $1", group.ID)
	})

	var schedulable bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT schedulable FROM providers WHERE id = $1", success.ID).Scan(&schedulable))
	require.False(t, schedulable)
	var bindingCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM provider_groups WHERE provider_id = $1 AND group_id = $2", success.ID, group.ID).Scan(&bindingCount))
	require.Equal(t, 1, bindingCount)
	var outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id = $1", success.ID).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount)

	failure := &provider.Record{
		Name:        fmt.Sprintf("duplicate-failure-%d", suffix),
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: false,
		Credentials: map[string]any{"api_key": "secret"},
		Extra:       map[string]any{},
	}
	// An absent group forces membership persistence to fail after provider
	// creation begins, making the following row counts observe transactional rollback.
	err = repo.CreateWithProviderGroups(ctx, failure, []provider.GroupMembership{{GroupID: int64(^uint64(0) >> 1)}})
	require.Error(t, err)

	var providerCount, groupCount, failedOutboxCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM providers WHERE name = $1", failure.Name).Scan(&providerCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM provider_groups WHERE provider_id = $1", failure.ID).Scan(&groupCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id = $1", failure.ID).Scan(&failedOutboxCount))
	require.Zero(t, providerCount)
	require.Zero(t, groupCount)
	require.Zero(t, failedOutboxCount)
}
