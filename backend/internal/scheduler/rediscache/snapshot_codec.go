package rediscache

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// SnapshotCodec encodes provider records; SnapshotCache manages Redis keys, versions, locks, and publication order.
type SnapshotCodec interface {
	Encode(scheduler.SnapshotProvider) ([]byte, []byte, error)
	Decode(any) (scheduler.SnapshotProvider, error)
	LastUsedAt(scheduler.SnapshotProvider) (*time.Time, error)
	SetLastUsedAt(scheduler.SnapshotProvider, *time.Time) error
}
