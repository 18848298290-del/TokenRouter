package rediscache

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// snapshot_codec.go defines the provider encoding and timestamp operations
// required by the Redis snapshot cache.
// SnapshotCache stores a full provider payload beside the metadata used for
// bucket selection, then merges a last-used side value after decoding. The
// codec supplies those representations and time accessors while Redis key
// allocation and activation remain in SnapshotCache.

// SnapshotCodec converts scheduler provider values into cached payloads and
// exposes last-used updates.
type SnapshotCodec interface {
	// Encode returns full-provider and selection-metadata payloads for one
	// snapshot value. Callers keep the provider data stable during encoding and
	// publish the pair only when encoding succeeds.
	Encode(scheduler.SnapshotProvider) ([]byte, []byte, error)
	// Decode restores a snapshot value from cached Redis payload data.
	// Implementations validate their supported input representation; decoding
	// errors let SnapshotCache abort the bucket read before returning
	// candidates.
	Decode(any) (scheduler.SnapshotProvider, error)
	// LastUsedAt returns the stored scheduling time for comparison with a
	// separately updated Redis side value. Nil means there is no embedded
	// timestamp. The returned pointer may be shared; SnapshotCache treats it as
	// read-only while comparing.
	LastUsedAt(scheduler.SnapshotProvider) (*time.Time, error)
	// SetLastUsedAt installs a later scheduling time on a decoded snapshot
	// value. SnapshotCache supplies a time pointer and handles errors before
	// returning the updated provider to a reader.
	SetLastUsedAt(scheduler.SnapshotProvider, *time.Time) error
}
