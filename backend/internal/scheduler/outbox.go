package scheduler

import (
	"context"
	"time"
)

const (
	SchedulerOutboxEventProviderChanged       = "provider_changed"
	SchedulerOutboxEventProviderGroupsChanged = "provider_groups_changed"
	SchedulerOutboxEventProviderBulkChanged   = "provider_bulk_changed"
	SchedulerOutboxEventProviderLastUsed      = "provider_last_used"
	SchedulerOutboxEventGroupChanged          = "group_changed"
	SchedulerOutboxEventFullRebuild           = "full_rebuild"
)

type SchedulerOutboxEvent struct {
	ID         int64
	EventType  string
	ProviderID *int64
	GroupID    *int64
	Payload    map[string]any
	CreatedAt  time.Time
}

// SchedulerOutboxRepository reads scheduling events and coordinates consumed-event cleanup.
type SchedulerOutboxRepository interface {
	ListAfterAndReleaseDedup(ctx context.Context, afterID int64, limit int) ([]SchedulerOutboxEvent, error)
	// FirstCreatedAtAfter returns the creation time of the first pending event after the watermark.
	FirstCreatedAtAfter(ctx context.Context, afterID int64) (time.Time, bool, error)
	MaxID(ctx context.Context) (int64, error)
	DeleteConsumedUpTo(ctx context.Context, watermark int64, limit int) (int64, error)
	TryAcquireCleanupLock(ctx context.Context) (SchedulerOutboxCleanupLease, bool, error)
}

// SchedulerOutboxCleanupLease holds the PostgreSQL advisory lock for outbox cleanup.
type SchedulerOutboxCleanupLease interface {
	Release()
}

// GroupPayload returns untyped nil for no groups; this representation participates in persisted deduplication hashes.
func GroupPayload(groupIDs []int64) any {
	if len(groupIDs) == 0 {
		return nil
	}
	return map[string]any{"group_ids": groupIDs}
}
