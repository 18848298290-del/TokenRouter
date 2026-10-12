package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	SchedulerModeSingle = "single"
	SchedulerModeMixed  = "mixed"
	SchedulerModeForced = "forced"
)

var (
	ErrSchedulerBucketRetired              = errors.New("scheduler bucket retired")
	ErrSchedulerBucketWriteFenced          = errors.New("scheduler bucket write fenced")
	ErrSchedulerGroupLifecycleLeaseInvalid = errors.New("scheduler group lifecycle lease invalid")
	ErrSchedulerGroupLifecycleLeaseLost    = errors.New("scheduler group lifecycle lease lost")
)

// SchedulerBucketWriteToken fences a snapshot writer to one bucket epoch.
// Acquire the token before loading from the database or queuing a rebuild.
type SchedulerBucketWriteToken struct {
	Bucket SchedulerBucket
	Epoch  int64
}

// SchedulerGroupLifecycleLease identifies the owner of a group's retire/reopen critical section.
type SchedulerGroupLifecycleLease struct {
	GroupID    int64
	OwnerToken string
}

type SchedulerBucket struct {
	GroupID  int64
	Platform string
	Mode     string
}

func (t SchedulerBucketWriteToken) ValidFor(bucket SchedulerBucket) bool {
	return t.Epoch > 0 && t.Bucket == bucket
}

func (l SchedulerGroupLifecycleLease) ValidFor(groupID int64) bool {
	return groupID > 0 && l.GroupID == groupID && l.OwnerToken != ""
}

func (b SchedulerBucket) String() string {
	return fmt.Sprintf("%d:%s:%s", b.GroupID, b.Platform, b.Mode)
}

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
