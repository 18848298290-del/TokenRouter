//go:build integration

package routing_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Group duplication copies provider bindings before enqueueing the scheduler
// event. A PostgreSQL trigger rejects the event for one duplicate operation ID,
// letting SQL queries verify that the newly created group, copied bindings,
// and outbox row are all absent after the failure.

// TestCreateGroupFromSourceRollsBackWhenOutboxInsertFails injects a scheduler
// outbox failure after group duplication begins. The repository call must return
// the injected error, and SQL counts must show no duplicate group, copied links,
// or corresponding outbox row.
func TestCreateGroupFromSourceRollsBackWhenOutboxInsertFails(t *testing.T) {
	ctx := context.Background()
	client, integrationDB := routingDatabase(t)
	repo := newGroupStoreFixture(client, integrationDB)
	suffix := time.Now().UnixNano()
	operationID := strings.Repeat("b", 64)

	source, err := client.Group.Create().
		SetName(fmt.Sprintf("duplicate-rollback-source-%d", suffix)).
		Save(ctx)
	require.NoError(t, err)
	provider, err := client.Provider.Create().
		SetName(fmt.Sprintf("duplicate-rollback-provider-%d", suffix)).
		SetPlatform(capability.PlatformAnthropic).
		SetType(capability.ProviderTypeOAuth).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.ProviderGroup.Create().
		SetProviderID(provider.ID).
		SetGroupID(source.ID).
		Save(ctx)
	require.NoError(t, err)

	// Match the duplicate operation ID inside the trigger so source setup still
	// succeeds and failure occurs after group insertion and provider-link copying.
	functionName := fmt.Sprintf("fail_group_duplicate_outbox_%d", suffix)
	triggerName := fmt.Sprintf("fail_group_duplicate_outbox_trigger_%d", suffix)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.group_id IS NOT NULL AND EXISTS (
				SELECT 1 FROM groups
				WHERE id = NEW.group_id AND duplicate_operation_id = '%s'
			) THEN
				RAISE EXCEPTION 'forced duplicate outbox failure';
			END IF;
			RETURN NEW;
		END;
		$$`, functionName, operationID))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
		"CREATE TRIGGER %s BEFORE INSERT ON scheduler_outbox FOR EACH ROW EXECUTE FUNCTION %s()",
		triggerName,
		functionName,
	))
	require.NoError(t, err)

	duplicateName := fmt.Sprintf("duplicate-rollback-copy-%d", suffix)
	// Drop the trigger before its referenced function, then attempt each source-row
	// cleanup. The ignored errors let later cleanup statements run after an earlier
	// removal fails.
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON scheduler_outbox", triggerName))
		_, _ = integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName))
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE group_id = $1", source.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM provider_groups WHERE provider_id = $1", provider.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM providers WHERE id = $1", provider.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE name IN ($1, $2)", source.Name, duplicateName)
	})

	duplicate := &routing.Group{
		Name: duplicateName,

		RateMultiplier:       1,
		Status:               "inactive",
		DuplicateOperationID: operationID,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(""),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(""),
		ResponsesImagePolicy: "inherit",
	}
	err = repo.CreateFromSource(ctx, duplicate, source.ID)
	require.ErrorContains(t, err, "forced duplicate outbox failure")

	// The duplicate ID is assigned before the injected outbox failure. Query
	// each participating table to distinguish transaction rollback from a returned error alone.
	var groupCount, bindingCount, outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM groups WHERE name = $1", duplicateName).Scan(&groupCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM provider_groups WHERE group_id = $1", duplicate.ID).Scan(&bindingCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE group_id = $1", duplicate.ID).Scan(&outboxCount))
	require.Zero(t, groupCount)
	require.Zero(t, bindingCount)
	require.Zero(t, outboxCount)
}
