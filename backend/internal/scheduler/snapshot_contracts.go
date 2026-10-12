package scheduler

import (
	"context"
	"time"
)

// SnapshotMetadata identifies a provider and its platform and groups for rebuilds and event expansion.
type SnapshotMetadata struct {
	ID       int64
	Name     string
	Platform string
	GroupIDs []int64
}

// SnapshotProvider exposes rebuild metadata while the storage adapter holds publishable data.
// Rebuilds within a batch reuse the object.
type SnapshotProvider interface {
	SnapshotMetadata() SnapshotMetadata
}

// SnapshotGroup holds the current state used to decide whether group buckets are active.
type SnapshotGroup struct {
	ID       int64
	Hydrated bool
	Name     string
	Status   string
}

// SnapshotProviderSource queries schedulable providers by group and platform as publishable snapshots.
type SnapshotProviderSource interface {
	GetByID(context.Context, int64) (SnapshotProvider, error)
	GetByIDs(context.Context, []int64) ([]SnapshotProvider, error)
	ListSchedulableByPlatform(context.Context, string) ([]SnapshotProvider, error)
	ListSchedulableUngroupedByPlatform(context.Context, string) ([]SnapshotProvider, error)
	ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]SnapshotProvider, error)
	ListSchedulableByPlatforms(context.Context, []string) ([]SnapshotProvider, error)
	ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]SnapshotProvider, error)
	ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]SnapshotProvider, error)
}

type SnapshotGroupSource interface {
	GetByID(context.Context, int64) (*SnapshotGroup, error)
	GetByIDLite(context.Context, int64) (*SnapshotGroup, error)
	ListActive(context.Context) ([]SnapshotGroup, error)
}

// SnapshotOptions configures the service. Nil uses defaults; false DbFallbackEnabled disables database
// fallback.
type SnapshotOptions struct {
	DbFallbackEnabled          bool
	DbFallbackMaxQPS           int
	DbFallbackTimeoutSeconds   int
	OutboxPollIntervalSeconds  int
	FullRebuildIntervalSeconds int
	OutboxLagWarnSeconds       int
	OutboxLagRebuildSeconds    int
	OutboxLagRebuildFailures   int
	OutboxBacklogRebuildRows   int
}

// SnapshotCache reads and writes scheduler snapshots decoded and published by the storage adapter.
type SnapshotCache interface {
	GetSnapshot(context.Context, SchedulerBucket) ([]SnapshotProvider, bool, error)
	CaptureBucketWriteToken(context.Context, SchedulerBucket) (SchedulerBucketWriteToken, error)
	SetSnapshot(context.Context, SchedulerBucket, SchedulerBucketWriteToken, []SnapshotProvider) error
	RetireBucket(context.Context, SchedulerBucket) error
	ReopenBucket(context.Context, SchedulerBucket) (SchedulerBucketWriteToken, error)
	TryAcquireGroupLifecycleLease(context.Context, int64, time.Duration) (SchedulerGroupLifecycleLease, bool, error)
	ReleaseGroupLifecycleLease(context.Context, SchedulerGroupLifecycleLease) error
	GetProvider(context.Context, int64) (SnapshotProvider, error)
	SetProvider(context.Context, SnapshotProvider) error
	DeleteProvider(context.Context, int64) error
	UpdateLastUsed(context.Context, map[int64]time.Time) error
	AcquireBucketLease(context.Context, SchedulerBucket, time.Duration) (*BucketLease, bool, error)
	ListBuckets(context.Context) ([]SchedulerBucket, error)
	GetOutboxWatermark(context.Context) (int64, error)
	SetOutboxWatermark(context.Context, int64) error
}

// SnapshotBindings supplies not-found errors and diagnostic callbacks.
type SnapshotBindings struct {
	ProviderNotFound error
	GroupNotFound    error
	Diagnostics      Diagnostics
}

func (g *SnapshotGroup) IsActive() bool { return g != nil && g.Status == "active" }
