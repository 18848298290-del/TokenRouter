package scheduler

import (
	"context"
	"time"
)

// Provider and group changes reach scheduler rebuilds through persisted outbox
// events. Event identity and payload distinguish broad membership rebuilds
// from lightweight last-used updates, while repository methods expose ordered
// consumption and separate post-watermark cleanup coordination.

// Scheduler outbox event names choose the cache update or rebuild operation
// applied during event consumption.
const (
	// provider state or eligibility changed.
	SchedulerOutboxEventProviderChanged = "provider_changed"
	// provider group membership changed.
	SchedulerOutboxEventProviderGroupsChanged = "provider_groups_changed"
	// several provider records changed.
	SchedulerOutboxEventProviderBulkChanged = "provider_bulk_changed"
	// provider usage timestamps changed.
	SchedulerOutboxEventProviderLastUsed = "provider_last_used"
	// group activation or policy changed.
	SchedulerOutboxEventGroupChanged = "group_changed"
	// all registered scheduler buckets refresh.
	SchedulerOutboxEventFullRebuild = "full_rebuild"
)

// SchedulerOutboxEvent carries one persisted scheduling change for ordered
// cache consumption.
type SchedulerOutboxEvent struct {
	ID         int64  // persistent event ordering identifier
	EventType  string // scheduler operation dispatch name
	ProviderID *int64 // optional changed provider identifier
	GroupID    *int64 // optional changed group identifier
	// Payload supplies prior group IDs, bulk provider IDs, or last-used seconds
	// according to EventType. Consumers read its decoded values while repository
	// adapters own persistence encoding.
	Payload   map[string]any // operation-specific persisted event data
	CreatedAt time.Time      // persistent event creation time
}

// SchedulerOutboxRepository supplies committed scheduling events and separate
// consumed-row cleanup coordination.
type SchedulerOutboxRepository interface {
	// ListAfterAndReleaseDedup returns committed events after the watermark in
	// ascending ID order and releases their deduplication keys. A nonpositive
	// limit selects the PostgreSQL adapter's default batch. Callers keep event
	// handling and watermark advancement replayable when either step fails.
	//
	// The PostgreSQL statement releases keys before scanning and payload decoding
	// finish, allowing another matching change to enqueue during handling. A
	// later decode error can therefore follow successful key release. Event IDs
	// define lookup order; transactions that commit below an advanced watermark
	// need periodic rebuilding for recovery.
	ListAfterAndReleaseDedup(ctx context.Context, afterID int64, limit int) ([]SchedulerOutboxEvent, error)
	// FirstCreatedAtAfter returns the first pending event's creation time after
	// the supplied watermark. Its boolean distinguishes an absent event from a
	// zero time so lag checks measure work that remains pending.
	FirstCreatedAtAfter(ctx context.Context, afterID int64) (time.Time, bool, error)
	// MaxID returns the greatest currently visible outbox event ID.
	// SnapshotService subtracts its watermark to estimate backlog distance; gaps
	// between IDs mean that distance can exceed the actual pending row count.
	MaxID(ctx context.Context) (int64, error)
	// DeleteConsumedUpTo removes a bounded batch of rows at or below the consumed
	// watermark. A nonpositive watermark skips deletion and a nonpositive limit
	// selects the adapter's default batch; callers hold the cleanup lease while
	// repeating deletion.
	DeleteConsumedUpTo(ctx context.Context, watermark int64, limit int) (int64, error)
	// TryAcquireCleanupLock attempts to reserve consumed-event cleanup and
	// returns a releasable lease when successful. A false acquisition lets
	// consumption continue while another process performs cleanup, and callers
	// release an acquired lease after deletion work.
	TryAcquireCleanupLock(ctx context.Context) (SchedulerOutboxCleanupLease, bool, error)
}

// SchedulerOutboxCleanupLease represents the database lock held during
// consumed-event deletion batches.
type SchedulerOutboxCleanupLease interface {
	// Release attempts to unlock the cleanup lease and returns its pinned
	// database connection to the pool. The PostgreSQL implementation suppresses
	// unlock and close errors; callers serialize access and invoke release after
	// acquired cleanup work.
	Release()
}

// GroupPayload returns group IDs in the persisted outbox payload shape. Empty
// input produces untyped nil for deduplication encoding; a populated payload
// retains the supplied slice, so callers keep it unchanged through
// persistence.
func GroupPayload(groupIDs []int64) any {
	if len(groupIDs) == 0 {
		return nil
	}
	return map[string]any{"group_ids": groupIDs}
}
