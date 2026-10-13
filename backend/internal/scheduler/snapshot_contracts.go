package scheduler

import (
	"context"
	"time"
)

// Scheduler snapshots separate provider rebuild metadata from the records
// encoded by storage adapters. Sources supply publishable records and hydrated
// group authority, while the cache exposes bucket fencing, lifecycle leases,
// provider updates, and outbox progress used by SnapshotService.

// SnapshotMetadata identifies a provider and its group memberships for rebuild
// event expansion.
type SnapshotMetadata struct {
	ID       int64   // persistent provider record identifier
	Name     string  // configured provider display name
	Platform string  // upstream provider platform identifier
	GroupIDs []int64 // associated persistent group identifiers
}

// SnapshotProvider exposes rebuild metadata while its storage adapter retains
// publishable provider data.
type SnapshotProvider interface {
	// SnapshotMetadata returns identity and group information used to select
	// affected rebuild buckets. SnapshotService can reuse the same provider
	// object across a batch; callers keep its publishable record stable
	// throughout those operations.
	SnapshotMetadata() SnapshotMetadata
}

// SnapshotGroup holds group status and hydration evidence used for
// authoritative bucket lifecycle decisions.
type SnapshotGroup struct {
	ID int64 // persistent group record identifier
	// Hydrated confirms that group status was populated by its source. Lifecycle
	// preparation rejects false or mismatched IDs before reopening or retiring
	// group buckets.
	Hydrated bool   // authoritative group read completeness
	Name     string // configured group display name
	Status   string // persistent group lifecycle status
}

// SnapshotProviderSource supplies database provider records eligible for
// scheduler cache rebuilding.
type SnapshotProviderSource interface {
	// GetByID reads a provider record for immediate snapshot refresh or database
	// fallback. Missing-provider errors match SnapshotService's configured
	// binding so event handling can remove stale cached data and rebuild affected
	// groups.
	GetByID(context.Context, int64) (SnapshotProvider, error)
	// GetByIDs returns existing provider records for a bulk change event.
	// SnapshotService compares returned IDs with the requested set, removes
	// missing cached providers, and broadens their affected groups' platform
	// rebuilds.
	GetByIDs(context.Context, []int64) ([]SnapshotProvider, error)
	// ListSchedulableByPlatform lists eligible records for a single upstream
	// platform.
	ListSchedulableByPlatform(context.Context, string) ([]SnapshotProvider, error)
	// ListSchedulableUngroupedByPlatform lists eligible ungrouped records for one
	// upstream platform.
	ListSchedulableUngroupedByPlatform(context.Context, string) ([]SnapshotProvider, error)
	// ListSchedulableByGroupIDAndPlatform lists eligible records in one group on
	// one platform.
	ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]SnapshotProvider, error)
	// ListSchedulableByPlatforms lists eligible records across the supplied
	// upstream platforms.
	ListSchedulableByPlatforms(context.Context, []string) ([]SnapshotProvider, error)
	// ListSchedulableUngroupedByPlatforms lists eligible ungrouped records across
	// the supplied platforms.
	ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]SnapshotProvider, error)
	// ListSchedulableByGroupIDAndPlatforms lists one group's eligible records
	// across the supplied upstream platforms.
	ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]SnapshotProvider, error)
}

// SnapshotGroupSource supplies persistent group authority for discovery and
// bucket lifecycle reconciliation.
type SnapshotGroupSource interface {
	// GetByID reads the requested persistent group state for scheduler consumers.
	GetByID(context.Context, int64) (*SnapshotGroup, error)
	// GetByIDLite reads authoritative status for the requested group during a
	// lifecycle lease. It returns matching ID and Hydrated evidence; a configured
	// not-found error permits retirement of that group's current and historical
	// buckets.
	GetByIDLite(context.Context, int64) (*SnapshotGroup, error)
	// ListActive lists currently active groups for full rebuild discovery.
	// SnapshotService uses this method when the optional active-ID lookup is
	// absent, propagating lookup errors before it starts group lifecycle
	// mutations.
	ListActive(context.Context) ([]SnapshotGroup, error)
}

// SnapshotOptions controls database fallback, rebuild scheduling, and outbox
// degradation recovery. An absent options pointer enables fallback with no
// local QPS cap, polls each second, and disables periodic rebuilds.
type SnapshotOptions struct {
	// DbFallbackEnabled controls fallback when an options value is supplied. A
	// false value rejects cache misses and failures with
	// ErrSchedulerCacheNotReady.
	DbFallbackEnabled bool // database recovery admission switch
	// DbFallbackMaxQPS caps admitted fallback operations in one-second local
	// windows. Nonpositive values disable this limiter, which counts reads
	// regardless of their later result.
	DbFallbackMaxQPS int // per-instance fallback operations limit
	// DbFallbackTimeoutSeconds limits database fallback while retaining the
	// caller's earlier deadline. Nonpositive values add cancellation cleanup
	// without installing a separate timeout.
	DbFallbackTimeoutSeconds int // seconds
	// OutboxPollIntervalSeconds schedules repeated outbox consumption.
	// Nonpositive values select one second, and Start performs the first poll
	// immediately.
	OutboxPollIntervalSeconds int // seconds
	// FullRebuildIntervalSeconds schedules periodic full reconciliation.
	// Nonpositive values disable that periodic task while the initial and
	// event-triggered rebuild paths remain available.
	FullRebuildIntervalSeconds int // seconds
	// OutboxLagWarnSeconds enables transition-limited warnings when pending event
	// age reaches the threshold. Nonpositive values disable age warnings in the
	// lag check.
	OutboxLagWarnSeconds int // seconds
	// OutboxLagRebuildSeconds defines the pending-age threshold for degraded lag
	// observations. Nonpositive values disable lag-triggered rebuilds while an
	// enabled backlog threshold can still trigger them.
	OutboxLagRebuildSeconds int // seconds
	// OutboxLagRebuildFailures sets the number of degraded age observations
	// needed before a lag rebuild. Nonpositive thresholds admit the first
	// degraded observation; once retries are pending, later polls follow the
	// retry time instead of increasing this initial count.
	OutboxLagRebuildFailures int // lag observations before rebuilding
	// OutboxBacklogRebuildRows compares MaxID minus watermark with the configured
	// threshold. Positive values enable backlog recovery; ID gaps mean the
	// comparison is an estimate rather than an exact row count.
	OutboxBacklogRebuildRows int // pending event ID distance
}

// SnapshotCache provides decoded scheduler reads and fenced bucket publication
// through a storage adapter.
type SnapshotCache interface {
	// GetSnapshot returns a bucket's published providers and reports whether a
	// snapshot is ready. A ready empty bucket differs from a miss;
	// SnapshotService invokes database fallback for misses or recoverable cache
	// errors.
	GetSnapshot(context.Context, SchedulerBucket) ([]SnapshotProvider, bool, error)
	// CaptureBucketWriteToken captures the bucket identity and current lifecycle
	// epoch before provider reads. Retirement or fencing errors prevent
	// publication with that token, while fallback callers may still return
	// database records.
	CaptureBucketWriteToken(context.Context, SchedulerBucket) (SchedulerBucketWriteToken, error)
	// SetSnapshot publishes provider membership using a token captured before
	// loading records. The token binds the intended bucket and lifecycle epoch so
	// a rebuild crossing retirement cannot reactivate obsolete membership. Redis
	// publication checks that token during version allocation and activation.
	//
	// Provider record writes precede membership activation and use shared
	// provider keys, so a publication error can follow earlier record writes. The
	// bucket fencing guarantee applies to membership publication; callers retain
	// durable changes for later reconstruction and keep supplied provider objects
	// stable throughout encoding.
	SetSnapshot(context.Context, SchedulerBucket, SchedulerBucketWriteToken, []SnapshotProvider) error
	// RetireBucket closes a bucket and invalidates previously captured writer
	// tokens through its lifecycle epoch. Group lifecycle callers hold the
	// matching lease while retiring canonical and registered historical buckets.
	RetireBucket(context.Context, SchedulerBucket) error
	// ReopenBucket enables a bucket under its current lifecycle epoch and returns
	// a publication token. Group lifecycle callers first read hydrated persistent
	// group authority while holding the matching lease, then use returned tokens
	// for required rebuilds.
	//
	// Retirement advances the epoch, so a later reopen leaves tokens from an
	// earlier active period fenced. Reopening multiple buckets can fail partway
	// through preparation; callers treat the lifecycle event as unfinished until
	// every required rebuild succeeds and retry through the durable event.
	ReopenBucket(context.Context, SchedulerBucket) (SchedulerBucketWriteToken, error)
	// TryAcquireGroupLifecycleLease attempts bounded ownership of one group's
	// reopen and retirement decisions. A false result reports contention, and
	// successful callers release the returned owner lease after reading group
	// authority and preparing bucket tasks.
	TryAcquireGroupLifecycleLease(context.Context, int64, time.Duration) (SchedulerGroupLifecycleLease, bool, error)
	// ReleaseGroupLifecycleLease attempts to release the matching group and owner
	// token. SnapshotService supplies a bounded context independent of the
	// original operation and propagates release errors so lifecycle work remains
	// unconfirmed.
	ReleaseGroupLifecycleLease(context.Context, SchedulerGroupLifecycleLease) error
	// GetProvider returns the decoded cached provider record or nil when
	// unavailable. SnapshotService can then use its database fallback policy;
	// cached full-record reads also apply the adapter's last-used side value.
	GetProvider(context.Context, int64) (SnapshotProvider, error)
	// SetProvider updates a shared cached provider record after a persistent
	// change. The Redis adapter can remove an unencodable provider, and callers
	// rely on durable events and later rebuilding to repair cache visibility.
	SetProvider(context.Context, SnapshotProvider) error
	// DeleteProvider removes the shared cached provider record for the supplied
	// ID. Bucket memberships are rebuilt separately through change events, so
	// readers may encounter a membership whose provider payload is already
	// unavailable.
	DeleteProvider(context.Context, int64) error
	// UpdateLastUsed applies provider usage times without rebuilding every
	// membership bucket. The Redis adapter keeps later valid side timestamps for
	// existing records; unencodable usage-time input can remove its provider
	// payload and side value.
	UpdateLastUsed(context.Context, map[int64]time.Time) error
	// AcquireBucketLease attempts one bucket's distributed rebuild lock for the
	// supplied lifetime. Contention returns false, and successful callers release
	// the owner-specific handle after querying and attempting fenced publication.
	AcquireBucketLease(context.Context, SchedulerBucket, time.Duration) (*BucketLease, bool, error)
	// ListBuckets returns bucket identities registered in the cache for
	// reconciliation. SnapshotService combines those historical identities with
	// canonical buckets and active persistent groups before choosing rebuild or
	// retirement work.
	ListBuckets(context.Context) ([]SchedulerBucket, error)
	// GetOutboxWatermark reads the last published consumption ID, with zero
	// representing an absent Redis watermark. Polling begins after that ID, and
	// an error prevents handling a new batch.
	GetOutboxWatermark(context.Context) (int64, error)
	// SetOutboxWatermark publishes the supplied consumption ID after all visible
	// batch events succeed. The Redis adapter stores that ID directly; callers
	// finish this step before cleanup and pending-lag measurement.
	SetOutboxWatermark(context.Context, int64) error
}

// SnapshotBindings supplies storage-specific missing-record errors and
// scheduler diagnostic callbacks.
type SnapshotBindings struct {
	// ProviderNotFound replaces the scheduler's default provider sentinel when
	// supplied. Event handling uses errors.Is against it to remove missing cached
	// providers.
	ProviderNotFound error // provider lookup missing-record cause
	// GroupNotFound replaces the scheduler's default group sentinel when
	// supplied. Lifecycle preparation uses errors.Is against it to authorize
	// retirement for a missing group.
	GroupNotFound error       // group lookup missing-record cause
	Diagnostics   Diagnostics // snapshot operation logging callbacks
}

// IsActive reports whether a nonnil group has active status. It does not
// assess hydration or matching identity; lifecycle callers check those
// separately before trusting the result to prepare bucket mutations.
func (g *SnapshotGroup) IsActive() bool { return g != nil && g.Status == "active" }
