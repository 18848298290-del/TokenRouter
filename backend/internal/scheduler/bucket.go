package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// bucket.go defines scheduler bucket identities and validates publication
// tokens and lifecycle leases.
// A scheduler bucket identifies one group, platform filter, and selection
// mode. Redis keys and outbox rebuilds use the same colon-separated identity,
// while write tokens and lifecycle leases carry the epoch or owner needed to
// reject stale publication.

// Scheduler mode values distinguish ordinary, mixed-platform, and forced-
// platform buckets in snapshot keys and rebuild decisions.
const (
	SchedulerModeSingle = "single" // ordinary platform filter
	SchedulerModeMixed  = "mixed"  // combined platform pool
	SchedulerModeForced = "forced" // explicitly forced platform
)

// Snapshot lifecycle errors let rebuild callers distinguish retired buckets,
// stale writers, malformed group leases, and lost ownership.
var (
	ErrSchedulerBucketRetired              = errors.New("scheduler bucket retired")
	ErrSchedulerBucketWriteFenced          = errors.New("scheduler bucket write fenced")
	ErrSchedulerGroupLifecycleLeaseInvalid = errors.New("scheduler group lifecycle lease invalid")
	ErrSchedulerGroupLifecycleLeaseLost    = errors.New("scheduler group lifecycle lease lost")
)

// SchedulerBucketWriteToken associates a snapshot writer with one bucket
// epoch. Capture it before reading providers or queuing a rebuild so a later
// epoch can reject the earlier writer.
type SchedulerBucketWriteToken struct {
	// Bucket and Epoch are captured together for a single rebuild. A token
	// remains usable only while that bucket has the same positive epoch in
	// cache storage.
	Bucket SchedulerBucket // bucket receiving publication
	Epoch  int64           // captured publication epoch
}

// SchedulerGroupLifecycleLease identifies the owner of a group retire or
// reopen operation. The storage adapter verifies ownership before changing the
// lifecycle of that group's buckets.
type SchedulerGroupLifecycleLease struct {
	// GroupID and OwnerToken identify one distributed lifecycle
	// acquisition. Retire and reopen callers retain both until releasing
	// that acquisition so a replacement owner cannot be mistaken for the
	// earlier holder.
	GroupID    int64  // leased group identifier
	OwnerToken string // random acquisition identity
}

// SchedulerBucket is the group, platform, and mode identity of a schedulable
// provider pool.
type SchedulerBucket struct {
	GroupID  int64  // provider membership group
	Platform string // empty selects all platforms
	Mode     string // persisted scheduler mode
}

// ValidFor reports whether this token names bucket and has a positive epoch.
// Cache publication separately checks the epoch against current storage; this
// predicate alone does not verify ownership.
func (t SchedulerBucketWriteToken) ValidFor(bucket SchedulerBucket) bool {
	return t.Epoch > 0 && t.Bucket == bucket
}

// ValidFor reports whether the lease has an owner token for the positive
// groupID. Storage operations still need to compare that token with the
// currently held distributed lease.
func (l SchedulerGroupLifecycleLease) ValidFor(groupID int64) bool {
	return groupID > 0 && l.GroupID == groupID && l.OwnerToken != ""
}

// String encodes the group, platform, and mode as a colon-separated cache
// identity. Platform and mode are used verbatim, so callers supply values
// without colon separators.
func (b SchedulerBucket) String() string {
	return fmt.Sprintf("%d:%s:%s", b.GroupID, b.Platform, b.Mode)
}

// ParseSchedulerBucket decodes exactly three colon-separated parts and reports
// success. It requires a parseable signed group ID and nonempty mode, but
// accepts an empty platform and does not validate known modes.
func ParseSchedulerBucket(raw string) (SchedulerBucket, bool) {
	parts := strings.Split(raw, ":")
	if len(parts) != 3 {
		return SchedulerBucket{}, false
	}
	groupID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return SchedulerBucket{}, false
	}
	if parts[2] == "" {
		return SchedulerBucket{}, false
	}
	return SchedulerBucket{
		GroupID:  groupID,
		Platform: parts[1],
		Mode:     parts[2],
	}, true
}
