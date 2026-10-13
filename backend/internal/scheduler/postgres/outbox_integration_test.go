//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// A pending idempotent event blocks another insert with the same
// deduplication key until a poll releases that key. PostgreSQL-backed
// scenarios exercise that release and compare payload-sensitive coalescing
// with last-used events that retain each observation.

// TestEnqueueSchedulerOutbox_DeduplicatesIdempotentEvents inserts the same
// provider change twice before polling. It checks one pending row, then a
// second insertion after selection releases the first row's key.
func TestEnqueueSchedulerOutbox_DeduplicatesIdempotentEvents(t *testing.T) {
	integrationDB := postgrescontainer.New(t)
	ctx := context.Background()
	// Reset this isolated fixture's rows and ID sequence before counting
	// inserts; the later assertions expose any unexpected rows even though reset
	// errors are discarded.
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox RESTART IDENTITY")

	providerID := int64(12345)
	require.NoError(t, EnqueueSchedulerChange(ctx, integrationDB, scheduler.SchedulerOutboxEventProviderChanged, &providerID, nil, nil))
	require.NoError(t, EnqueueSchedulerChange(ctx, integrationDB, scheduler.SchedulerOutboxEventProviderChanged, &providerID, nil, nil))

	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1", scheduler.SchedulerOutboxEventProviderChanged).Scan(&count))
	require.Equal(t, 1, count)

	var firstID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM scheduler_outbox WHERE event_type = $1", scheduler.SchedulerOutboxEventProviderChanged).Scan(&firstID))
	events, err := NewSchedulerOutboxRepository(integrationDB).ListAfterAndReleaseDedup(ctx, 0, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, firstID, events[0].ID)

	require.NoError(t, EnqueueSchedulerChange(ctx, integrationDB, scheduler.SchedulerOutboxEventProviderChanged, &providerID, nil, nil))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1", scheduler.SchedulerOutboxEventProviderChanged).Scan(&count))
	require.Equal(t, 2, count)
}

// TestSchedulerOutbox_ListAfterAndReleaseDedup_AllowsSameKeyWhileEventInFlight
// selects an event before inserting the same change again. It checks two
// persisted rows with exactly one remaining pending deduplication key.
func TestSchedulerOutbox_ListAfterAndReleaseDedup_AllowsSameKeyWhileEventInFlight(t *testing.T) {
	integrationDB := postgrescontainer.New(t)
	ctx := context.Background()
	// Reset this isolated fixture's rows and ID sequence before counting
	// inserts; the later assertions expose any unexpected rows even though reset
	// errors are discarded.
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox RESTART IDENTITY")

	providerID := int64(17345)
	require.NoError(t, EnqueueSchedulerChange(ctx, integrationDB, scheduler.SchedulerOutboxEventProviderChanged, &providerID, nil, nil))

	events, err := NewSchedulerOutboxRepository(integrationDB).ListAfterAndReleaseDedup(ctx, 0, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)

	require.NoError(t, EnqueueSchedulerChange(ctx, integrationDB, scheduler.SchedulerOutboxEventProviderChanged, &providerID, nil, nil))

	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1", scheduler.SchedulerOutboxEventProviderChanged).Scan(&count))
	require.Equal(t, 2, count)

	var pendingKeys int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE dedup_key IS NOT NULL").Scan(&pendingKeys))
	require.Equal(t, 1, pendingKeys)
}

// TestEnqueueSchedulerOutbox_CoalescesProviderStateBurst inserts fifty
// identical provider-change events before polling. It checks that their
// shared pending deduplication key leaves one row in PostgreSQL.
func TestEnqueueSchedulerOutbox_CoalescesProviderStateBurst(t *testing.T) {
	integrationDB := postgrescontainer.New(t)
	ctx := context.Background()
	// Reset this isolated fixture's rows and ID sequence before counting
	// inserts; the later assertions expose any unexpected rows even though reset
	// errors are discarded.
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox RESTART IDENTITY")

	providerID := int64(22345)
	for range 50 {
		require.NoError(t, EnqueueSchedulerChange(ctx, integrationDB, scheduler.SchedulerOutboxEventProviderChanged, &providerID, nil, nil))
	}

	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1", scheduler.SchedulerOutboxEventProviderChanged).Scan(&count))
	t.Logf("same-provider provider_changed burst: calls=50 inserted=%d", count)
	require.Equal(t, 1, count)
}

// TestEnqueueSchedulerOutbox_DoesNotDeduplicateDifferentPayload uses the same
// provider with two group payloads. It checks that different encoded group
// identities produce two persisted change events.
func TestEnqueueSchedulerOutbox_DoesNotDeduplicateDifferentPayload(t *testing.T) {
	integrationDB := postgrescontainer.New(t)
	ctx := context.Background()
	// Reset this isolated fixture's rows and ID sequence before counting
	// inserts; the later assertions expose any unexpected rows even though reset
	// errors are discarded.
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox RESTART IDENTITY")

	providerID := int64(32345)
	payload1 := map[string]any{"group_ids": []int64{1}}
	payload2 := map[string]any{"group_ids": []int64{2}}
	require.NoError(t, EnqueueSchedulerChange(ctx, integrationDB, scheduler.SchedulerOutboxEventProviderChanged, &providerID, nil, payload1))
	require.NoError(t, EnqueueSchedulerChange(ctx, integrationDB, scheduler.SchedulerOutboxEventProviderChanged, &providerID, nil, payload2))

	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1", scheduler.SchedulerOutboxEventProviderChanged).Scan(&count))
	require.Equal(t, 2, count)
}

// TestEnqueueSchedulerOutbox_DoesNotDeduplicateLastUsed inserts two last-used
// observations for one provider. It checks that both timestamp updates remain
// rows because this event type bypasses pending deduplication.
func TestEnqueueSchedulerOutbox_DoesNotDeduplicateLastUsed(t *testing.T) {
	integrationDB := postgrescontainer.New(t)
	ctx := context.Background()
	// Reset this isolated fixture's rows and ID sequence before counting
	// inserts; the later assertions expose any unexpected rows even though reset
	// errors are discarded.
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox RESTART IDENTITY")

	providerID := int64(67890)
	payload1 := map[string]any{"last_used": map[string]int64{"67890": 100}}
	payload2 := map[string]any{"last_used": map[string]int64{"67890": 200}}
	require.NoError(t, EnqueueSchedulerChange(ctx, integrationDB, scheduler.SchedulerOutboxEventProviderLastUsed, &providerID, nil, payload1))
	require.NoError(t, EnqueueSchedulerChange(ctx, integrationDB, scheduler.SchedulerOutboxEventProviderLastUsed, &providerID, nil, payload2))

	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1", scheduler.SchedulerOutboxEventProviderLastUsed).Scan(&count))
	require.Equal(t, 2, count)
}
