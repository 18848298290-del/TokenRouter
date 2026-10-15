package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// snapshot.go rebuilds scheduler buckets and consumes persisted changes while
// coordinating bucket and group lifecycles.
// A provider change can arrive while an older scheduler rebuild is still
// reading database records. Each bucket write carries the bucket identity and
// lifecycle epoch captured before its query; retirement fences obsolete
// membership activation. Shared provider payload writes can precede that
// activation check, so this fencing protects bucket membership and does not
// make an entire multi-bucket rebuild atomic.
//
// Group lifecycle work reads hydrated database authority under a lease,
// prepares canonical reopen or retirement decisions, and releases the lease
// before provider queries. PostgreSQL stores the durable outbox events; the
// shared Redis watermark advances after every visible batch handler succeeds.
// Ordinary rebuilds can skip busy or fenced buckets, while reopened lifecycle
// buckets treat those outcomes as incomplete. A batch reuses successful
// group/platform queries and encoded IDs across single and forced buckets,
// retaining per-bucket tokens and releasing cached data after its last
// consumer.

// Snapshot recovery uses bounded operation lifetimes, chunked outbox cleanup,
// and delayed retries so persistent events remain available when a rebuild or
// cleanup cannot finish.
const (
	outboxEventTimeout                    = 2 * time.Minute  // maximum work budget for one handled outbox event.
	schedulerOutboxCleanupBatch           = 5000             // maximum consumed events requested in one cleanup deletion.
	schedulerGroupLifecycleTimeout        = 30 * time.Second // group authority and bucket preparation operation budget.
	schedulerGroupLifecycleLeaseTTL       = 60 * time.Second // distributed group lifecycle owner lease lifetime.
	schedulerGroupLifecycleReleaseTimeout = 2 * time.Second  // independent lifecycle owner release operation budget.
	outboxRebuildRetryBaseDelay           = 5 * time.Second  // initial failed degradation rebuild retry delay.
	outboxRebuildRetryMaxDelay            = 5 * time.Minute  // maximum failed degradation rebuild retry delay.
	outboxMaxIDErrorLogSampleInterval     = time.Minute      // minimum time between sampled backlog-read errors.
)

// Snapshot recovery errors distinguish unavailable dependencies, denied
// database fallback, and contended group lifecycle or bucket rebuilding so
// callers can classify a failed operation.
var (
	ErrSchedulerCacheNotReady           = errors.New("scheduler cache not ready")            // snapshot or configured recovery is unavailable.
	ErrSchedulerFallbackLimited         = errors.New("scheduler db fallback limited")        // the local fallback window denied admission.
	ErrSchedulerGroupLifecycleLeaseBusy = errors.New("scheduler group lifecycle lease busy") // another owner holds group lifecycle preparation.
	ErrSchedulerBucketRebuildBusy       = errors.New("scheduler bucket rebuild busy")        // a strict rebuild could not acquire its bucket.

	// ErrSnapshotProviderNotFound and ErrSnapshotGroupNotFound are the default
	// missing-record causes matched by event handlers when their corresponding
	// source bindings are nil.
	ErrSnapshotProviderNotFound = errors.New("scheduler provider not found") // default missing-provider source error.
	ErrSnapshotGroupNotFound    = errors.New("scheduler group not found")    // default missing-group source error.
)

// batchSeenKey separates a poll batch's ordinary platform work from completed
// group lifecycle decisions.
type batchSeenKey struct {
	groupID  int64  // affected scheduling group ID
	platform string // platform or shared empty platform
	// lifecycle distinguishes completed authoritative group handling from
	// ordinary pair scheduling; scheduling a platform pair does not establish
	// that the group's reopen/retire work finished.
	lifecycle bool // completed lifecycle work marker
}

// schedulerBucketWriteTask pairs one bucket with the lifecycle token captured
// before provider reads.
type schedulerBucketWriteTask struct {
	// bucket and token are paired before provider queries. Publication uses the
	// bucket's captured identity and epoch, so retirement or an epoch mismatch
	// can fence obsolete membership activation.
	bucket SchedulerBucket           // target snapshot bucket identity
	token  SchedulerBucketWriteToken // captured lifecycle publication token
}

// schedulerProviderQueryKey identifies equivalent provider queries for one
// group and platform across reusable modes.
type schedulerProviderQueryKey struct {
	groupID  int64  // queried scheduling group ID
	platform string // queried provider platform identity
}

// schedulerProviderQueryCache retains successful records and encoded IDs for
// serial single/forced batch queries. Executed tasks release counted
// consumers; the final consumer removes both representations, while an
// early-aborting caller discards its remaining batch cache.
type schedulerProviderQueryCache struct {
	// remaining, providers, and snapshotProviderIDs belong to one prepared batch.
	// Every executed reusable task releases its remaining count on exit,
	// including busy or failed tasks; the final consumer removes both retained
	// slices.
	remaining map[schedulerProviderQueryKey]int // unfinished consumers per query
	// providers retains successfully returned objects, including an empty result,
	// for equivalent single/forced queries. Consumers keep those source objects
	// stable during publication.
	// successful query provider slices
	providers map[schedulerProviderQueryKey][]SnapshotProvider
	// snapshotProviderIDs is keyed only after a successful full writer result;
	// map presence distinguishes a valid empty encoding result from IDs that have
	// not been established.
	snapshotProviderIDs map[schedulerProviderQueryKey][]int64 // successfully encoded membership IDs
}

// schedulerSnapshotProviderIDWriter publishes subsequent bucket membership
// from IDs successfully encoded earlier in this batch.
type schedulerSnapshotProviderIDWriter interface {
	// SetSnapshotAndReturnProviderIDs attempts provider payload and bucket
	// membership publication under this write token, returning the ordered IDs
	// actually encoded after a non-error activation response. The caller owns the
	// returned slice for reuse until the batch finishes; input objects stay
	// stable while encoding.
	//
	// Membership activation validates bucket and epoch. Shared record writes may
	// precede an activation error, which cannot authorize ID reuse. A successful
	// empty ID slice remains a valid encoding result. An older activation can
	// leave newer membership active while still returning the encoded IDs from
	// its write.
	SetSnapshotAndReturnProviderIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providers []SnapshotProvider) ([]int64, error)
	// SetSnapshotByProviderIDs publishes this bucket's membership from ordered
	// IDs successfully encoded earlier in the same batch. It still validates this
	// bucket's own token; callers keep the ID slice stable while publication
	// runs.
	SetSnapshotByProviderIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providerIDs []int64) error
}

// schedulerGroupLifecyclePlan describes trusted group activity and
// token-bearing work prepared before provider queries.
type schedulerGroupLifecyclePlan struct {
	// active and tasks describe one prepared authority decision. For a positive
	// group, active selects canonical reopen tasks; inactive or missing authority
	// leaves tasks empty after retirement.
	active bool // authoritative group activity result
	// tasks retains bucket tokens prepared while lifecycle ownership is held.
	// Publication follows lease release, so later group retirement can still
	// fence these tokens.
	tasks []schedulerBucketWriteTask // reopened bucket publication tasks
}

// schedulerActiveGroupIDLister optionally supplies lightweight active group
// identities for full rebuild discovery.
type schedulerActiveGroupIDLister interface {
	// ListActiveIDs returns active group identities for full rebuild discovery
	// using the caller's context. Implementations return read failures instead of
	// silently substituting an empty active set, which could authorize unintended
	// retirement.
	ListActiveIDs(ctx context.Context) ([]int64, error)
}

// SnapshotService reconciles provider snapshots, group lifecycle, and durable
// outbox progress under the worker runtime.
type SnapshotService struct {
	// bindings retains optional source-specific ProviderNotFound and
	// GroupNotFound causes. Event and lifecycle handlers resolve each against its
	// scheduler default when the supplied cause is nil.
	bindings     SnapshotBindings          // missing-record errors and diagnostics
	cache        SnapshotCache             // shared snapshot storage adapter
	outboxRepo   SchedulerOutboxRepository // durable scheduler event repository
	providerRepo SnapshotProviderSource    // database provider record source
	groupRepo    SnapshotGroupSource       // hydrated group authority source
	// cfg is retained by pointer; callers keep options stable while workers read
	// them. The constructor captures DbFallbackMaxQPS into fallbackLimit, while
	// timeout and scheduling helpers read cfg directly.
	cfg *SnapshotOptions // borrowed snapshot recovery options
	// runtime owns the shared cancellation context and launched workers.
	// SnapshotService delegates startup/stop admission to it; Stop permanently
	// cancels that context.
	runtime WorkerRuntime // worker shutdown and task owner
	// fallbackLimit is initialized from the constructor's QPS value. A nil
	// limiter admits every fallback operation; accepted attempts count even if
	// their subsequent database read fails.
	fallbackLimit *fallbackLimiter // local database recovery admission
	// lagMu protects lagFailures, outboxRebuildLatched, outboxRebuildRunning,
	// outboxRebuildFailures, outboxRebuildRetryAt, outboxRebuildRetryReason,
	// outboxLagWarningActive, and outboxMaxIDErrorLastLoggedAt. Rebuild I/O runs
	// after releasing this mutex; running reserves that work across concurrent
	// health checks.
	lagMu sync.Mutex // protects degradation episode state
	// lagFailures counts initial degraded age samples outside running, latched,
	// or pending lag-retry work. Healthy age and selection of a lag rebuild reset
	// this count.
	lagFailures int // initial degraded age observation count
	// outboxRebuildLatched suppresses new degradation rebuilds after a successful
	// run. Healthy age with known healthy backlog, or an empty visible poll,
	// clears this latch.
	outboxRebuildLatched bool // successful episode rebuild observed
	// outboxRebuildRunning remains set while a triggered rebuild executes. Empty
	// visible polls clear episode and retry state but retain this in-flight
	// reservation.
	outboxRebuildRunning bool // degradation-triggered rebuild reserved
	// outboxRebuildFailures, outboxRebuildRetryAt, and outboxRebuildRetryReason
	// describe the latest failed degradation rebuild. Recovery or a reason
	// change can reset this retry; unknown backlog alone retains an existing
	// backlog retry's count and due time.
	outboxRebuildFailures    int       // consecutive failed episode rebuild count
	outboxRebuildRetryAt     time.Time // next failed rebuild retry time
	outboxRebuildRetryReason string    // lag or backlog retry cause
	// outboxLagWarningActive tracks age-warning transitions. Repeated active
	// samples suppress warnings until an inactive sample or empty visible poll
	// clears the flag.
	outboxLagWarningActive bool // age warning currently active
	// outboxMaxIDErrorLastLoggedAt records an admitted MaxID-error log sample.
	// Zero permits admission; later nonzero samples observe the fixed logging
	// interval.
	outboxMaxIDErrorLastLoggedAt time.Time // last sampled backlog-read error time

	// fullRebuildGateOnce initializes fullRebuildGate before concurrent callers
	// acquire its single permit. The gate stays held across database and cache
	// work but its acquisition observes runtime cancellation.
	fullRebuildGateOnce sync.Once     // initializes serialized rebuild gate
	fullRebuildGate     chan struct{} // single full-rebuild execution permit
	// fullRebuildStateMu protects fullRebuildRequested, fullRebuildCompleted, and
	// fullRebuildLastErr. A gate holder captures covered generations before
	// running I/O, then publishes that result for covered waiting callers.
	fullRebuildStateMu   sync.Mutex // protects full-rebuild generation state
	fullRebuildRequested uint64     // latest requested rebuild generation
	fullRebuildCompleted uint64     // latest covered rebuild generation
	fullRebuildLastErr   error      // result for covered generations
}

// fallbackLimiter counts admitted database recovery operations in per-instance
// one-second windows.
type fallbackLimiter struct {
	// maxQPS is captured when the limiter is constructed. Nonpositive values
	// make Allow admit every operation regardless of the stored window count.
	maxQPS int // admitted operations per window
	// mu protects window and count together while Allow resets expired windows
	// and admits one operation. maxQPS remains the constructor-supplied limit.
	mu     sync.Mutex // protects local window admission
	window time.Time  // current one-second window start
	count  int        // admitted operations this window
}

// newSchedulerProviderQueryCache allocates serial batch caches and counts
// every reusable task across the supplied task sets. Single and forced buckets
// share one group/platform key; unsupported modes are absent from all counts.
// Callers include every task that may consume the shared query.
//
// Successful provider slices and encoded ID slices can then be reused while
// remaining consumers exist. Each executed task releases its count even on
// skipped or failed work. A final consumer removes both slices; an
// early-aborting lifecycle caller discards its remaining batch cache when the
// call ends.
func newSchedulerProviderQueryCache(taskSets ...[]schedulerBucketWriteTask) *schedulerProviderQueryCache {
	queries := &schedulerProviderQueryCache{
		remaining:           make(map[schedulerProviderQueryKey]int),
		providers:           make(map[schedulerProviderQueryKey][]SnapshotProvider),
		snapshotProviderIDs: make(map[schedulerProviderQueryKey][]int64),
	}
	for _, tasks := range taskSets {
		for _, task := range tasks {
			if key, ok := schedulerProviderQueryKeyForBucket(task.bucket); ok {
				queries.remaining[key]++
			}
		}
	}
	return queries
}

// schedulerProviderQueryKeyForBucket returns a reusable group/platform key for
// single or forced mode. Other modes return ok=false and their rebuild queries
// run outside the batch cache. Eligible keys retain the group ID and platform
// exactly while omitting mode.
func schedulerProviderQueryKeyForBucket(bucket SchedulerBucket) (schedulerProviderQueryKey, bool) {
	if bucket.Mode != SchedulerModeSingle && bucket.Mode != SchedulerModeForced {
		return schedulerProviderQueryKey{}, false
	}
	return schedulerProviderQueryKey{groupID: bucket.GroupID, platform: bucket.Platform}, true
}

// release ends one executed task's use of this serial batch query. Nil caches
// and unsupported modes do nothing. Callers release exactly once per executed
// reusable task, including a task skipped for bucket contention or rejected
// publication.
//
// The remaining count tracks scheduled consumers rather than successful
// writes. On the final consumer, records and encoded IDs are deleted together
// so later query pairs need not retain finished data. Decrementing an absent
// count also removes its entries; callers prevent unintended duplicate release
// by attaching cleanup at task entry.
func (c *schedulerProviderQueryCache) release(bucket SchedulerBucket) {
	if c == nil {
		return
	}
	key, ok := schedulerProviderQueryKeyForBucket(bucket)
	if !ok {
		return
	}
	remaining := c.remaining[key] - 1
	// Other group/platform pairs may still be rebuilding. Remove this finished
	// pair's provider and ID slices together so the live batch stops retaining
	// objects whose consumers ended.
	if remaining <= 0 {
		delete(c.remaining, key)
		delete(c.providers, key)
		delete(c.snapshotProviderIDs, key)
		return
	}
	c.remaining[key] = remaining
}

// NewSnapshotService retains snapshot adapters, options, and the first
// optional binding set. Additional binding arguments are ignored. Absent
// options enable database fallback with no local QPS cap, one-second polling,
// and no periodic rebuild; the constructor captures the initial QPS value for
// its limiter.
func NewSnapshotService(
	cache SnapshotCache,
	outboxRepo SchedulerOutboxRepository,
	providerRepo SnapshotProviderSource,
	groupRepo SnapshotGroupSource,
	cfg *SnapshotOptions,
	bindings ...SnapshotBindings,
) *SnapshotService {
	maxQPS := 0
	if cfg != nil {
		maxQPS = cfg.DbFallbackMaxQPS
	}
	var binding SnapshotBindings
	if len(bindings) > 0 {
		binding = bindings[0]
	}
	return &SnapshotService{
		bindings:      binding,
		cache:         cache,
		outboxRepo:    outboxRepo,
		providerRepo:  providerRepo,
		groupRepo:     groupRepo,
		cfg:           cfg,
		fallbackLimit: newFallbackLimiter(maxQPS),
	}
}

// Start registers an asynchronous initial rebuild, immediate outbox polling
// when a repository exists, and an enabled periodic rebuild with
// WorkerRuntime. Nil service/cache skips startup. Repeated calls and calls
// after Stop have no effect through the runtime's permanent admission state.
func (s *SnapshotService) Start() {
	if s == nil || s.cache == nil {
		return
	}
	tasks := []RuntimeTask{{Name: "initial-rebuild", Run: func(context.Context) { s.runInitialRebuild() }}}
	if interval := s.outboxPollInterval(); s.outboxRepo != nil && interval > 0 {
		tasks = append(tasks, RuntimeTask{Name: "outbox", Interval: interval, Immediate: true, Run: func(context.Context) { s.pollOutbox() }})
	}
	if interval := s.fullRebuildInterval(); interval > 0 {
		tasks = append(tasks, RuntimeTask{Name: "periodic-rebuild", Interval: interval, Run: func(context.Context) {
			if err := s.triggerFullRebuild("interval"); err != nil {
				s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] full rebuild failed: %v", err)
			}
		}})
	}
	s.runtime.Start(tasks...)
}

// Stop cancels snapshot workers and permits thirty seconds for their runtime
// shutdown wait. It discards the retained shutdown error; application callers
// needing unfinished-work reporting supply their remaining budget through
// StopContext.
func (s *SnapshotService) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The convenience method has no error result. Call StopContext directly to
	// choose the wait budget; repeated stops report the first runtime outcome
	// instead of installing a fresh wait.
	_ = s.StopContext(ctx)
}

// StopContext permanently cancels snapshot worker admission and waits under
// the supplied nonnil shutdown context through WorkerRuntime. Nil service
// returns nil. The runtime retains its first stop result, and callbacks can
// remain unfinished after the wait budget ends.
func (s *SnapshotService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.runtime.StopContext(ctx)
}

// ListSchedulableProviders returns cached or database provider objects for a
// positive admitted group, with useMixed=false. It first rejects a completed
// caller context; absent or nonpositive group IDs return no candidates. A
// ready cache hit allocates filtered slice storage while retaining provider
// objects.
//
// A miss or recoverable cache error can use configured database fallback after
// local admission. Publication captures its bucket/epoch token before the
// query, and retired or fenced tokens disable publication while the database
// result remains usable. A later cache-write failure also leaves that result
// available. Caller cancellation recognized on cache error stops recovery; the
// cache's membership fencing does not undo earlier shared provider record
// writes.
// @project-doc docs/architecture/provider_scheduling_and_cache.md#scheduler_snapshot_consistency
func (s *SnapshotService) ListSchedulableProviders(ctx context.Context, groupID *int64, platform string, hasForcePlatform bool) ([]SnapshotProvider, bool, error) {
	useMixed := false
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if groupID == nil || *groupID <= 0 {
		return nil, false, nil
	}
	mode := s.resolveMode(platform, hasForcePlatform)
	bucket := s.bucketFor(groupID, platform, mode)
	var writeToken SchedulerBucketWriteToken
	canPublish := false

	if s.cache != nil {
		// A recognized caller cancellation stops cache recovery. Other read failures
		// or misses retain the option to read the admitted group's database pool.
		cached, hit, err := s.cache.GetSnapshot(ctx, bucket)
		if err != nil {
			if isContextDoneError(ctx, err) {
				return nil, useMixed, contextDoneError(ctx, err)
			}
			s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] cache read failed: bucket=%s err=%v", bucket.String(), err)
		} else if hit {
			return derefProviders(cached), useMixed, nil
		}
		// Capture the bucket generation before the fallback query. Failed capture
		// prevents publishing that result, while admitted database recovery can
		// still serve this caller.
		token, err := s.cache.CaptureBucketWriteToken(ctx, bucket)
		if err != nil {
			if errors.Is(err, ErrSchedulerBucketRetired) || errors.Is(err, ErrSchedulerBucketWriteFenced) {
				s.diagnostics().event("debug", "[Scheduler] cache publish fenced", "bucket", bucket.String())
			} else {
				s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] cache publish token failed: bucket=%s err=%v", bucket.String(), err)
			}
		} else {
			writeToken = token
			canPublish = true
		}
	}

	if err := s.guardFallback(ctx); err != nil {
		return nil, useMixed, err
	}

	fallbackCtx, cancel := s.withFallbackTimeout(ctx)
	defer cancel()

	providers, err := s.loadProvidersFromDB(fallbackCtx, bucket, useMixed)
	if err != nil {
		return nil, useMixed, err
	}

	if s.cache != nil && canPublish {
		// The database result is already usable for this request. Keep returning it
		// after fenced or failed publication, whose earlier shared payload writes
		// may remain.
		if err := s.cache.SetSnapshot(fallbackCtx, bucket, writeToken, providers); err != nil {
			if errors.Is(err, ErrSchedulerBucketRetired) || errors.Is(err, ErrSchedulerBucketWriteFenced) {
				s.diagnostics().event("debug", "[Scheduler] cache publish fenced", "bucket", bucket.String())
			} else {
				s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] cache write failed: bucket=%s err=%v", bucket.String(), err)
			}
		}
	}

	return providers, useMixed, nil
}

// GetProvider returns cached provider data or performs admitted database
// fallback for a positive ID. Nonpositive IDs return nil before source access.
// Recognized cancellation on a cache error stops recovery; other cache errors
// are logged, and a miss uses the configured fallback timeout and local
// limiter. Positive fallback calls require a provider source.
func (s *SnapshotService) GetProvider(ctx context.Context, providerID int64) (SnapshotProvider, error) {
	if providerID <= 0 {
		return nil, nil
	}
	if s.cache != nil {
		// An unavailable provider cache can still recover through the source. Stop
		// on recognized cancellation errors; log other failures and retain
		// configured fallback admission.
		provider, err := s.cache.GetProvider(ctx, providerID)
		if err != nil {
			if isContextDoneError(ctx, err) {
				return nil, contextDoneError(ctx, err)
			}
			s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] provider cache read failed: id=%d err=%v", providerID, err)
		} else if provider != nil {
			return provider, nil
		}
	}

	if err := s.guardFallback(ctx); err != nil {
		return nil, err
	}
	fallbackCtx, cancel := s.withFallbackTimeout(ctx)
	defer cancel()
	return s.providerRepo.GetByID(fallbackCtx, providerID)
}

// UpdateProviderInCache delegates immediate shared provider publication after
// a persistent or runtime change. Missing cache or nil provider does nothing;
// cache write errors pass through for the caller to report or retry.
func (s *SnapshotService) UpdateProviderInCache(ctx context.Context, provider SnapshotProvider) error {
	if s.cache == nil || provider == nil {
		return nil
	}
	return s.cache.SetProvider(ctx, provider)
}

// runInitialRebuild joins generation-coalesced startup reconciliation under a
// two-minute runtime-derived context. Missing cache skips it. Failures are
// logged inside the coalesced callback; this worker discards the returned
// error while durable events and configured later rebuilding remain recovery
// paths.
func (s *SnapshotService) runInitialRebuild() {
	if s.cache == nil {
		return
	}
	// Startup uses the same serialized generations as later full rebuilds.
	// Callback failures are logged below; a queued cancellation result is ignored
	// by this one-shot worker.
	_ = s.coalesceFullRebuild(func() error {
		ctx, cancel := context.WithTimeout(s.runtime.Context(), 2*time.Minute)
		defer cancel()
		if err := s.rebuildFullSnapshot(ctx, "startup"); err != nil {
			s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] rebuild startup failed: %v", err)
			return err
		}
		return nil
	})
}

// pollOutbox consumes one visible event batch and advances its watermark after
// every handler succeeds. Missing repository/cache skips the poll. Initial
// reads use one ten-second context, but each event receives an independent
// two-minute runtime-derived budget so later event work is not constrained by
// the read timeout.
//
// Ordinary group/platform pairs are deduplicated in this batch, while
// lifecycle completion uses separate markers. Any handling failure leaves the
// watermark unchanged for retry. Watermark publication gets three
// independently timed attempts before cleanup is allowed; pending lag is
// measured after the newly consumed batch, avoiding rebuilds triggered by its
// own elapsed processing. An empty visible batch clears degradation episode
// state and retains any in-flight rebuild reservation.
func (s *SnapshotService) pollOutbox() {
	if s.outboxRepo == nil || s.cache == nil {
		return
	}
	// The poll budget applies to watermark and visible-batch reads. Each later
	// event starts a fresh runtime-derived budget so read elapsed time cannot
	// consume its work allowance.
	ctx, cancel := context.WithTimeout(s.runtime.Context(), 10*time.Second)
	defer cancel()

	watermark, err := s.cache.GetOutboxWatermark(ctx)
	if err != nil {
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox watermark read failed: %v", err)
		return
	}

	// One poll reads a bounded prefix of visible events; later IDs remain
	// pending.
	events, err := s.outboxRepo.ListAfterAndReleaseDedup(ctx, watermark, 200)
	if err != nil {
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox poll failed: %v", err)
		return
	}
	if len(events) == 0 {
		// This query saw no events beyond the watermark. Clear its degradation
		// episode and retry state, retaining any rebuild already executing under the
		// shared running flag.
		s.clearOutboxDegradedEpisode()
		return
	}

	seen := make(map[batchSeenKey]struct{})
	// Each event owns an independent runtime-derived timeout and finishes before
	// the next event. A failure preserves the old watermark for a fresh batch
	// retry.
	for _, event := range events {
		eventCtx, cancel := context.WithTimeout(s.runtime.Context(), outboxEventTimeout)
		err := s.handleOutboxEvent(eventCtx, event, seen)
		cancel()
		if err != nil {
			s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox handle failed: id=%d type=%s err=%v", event.ID, event.EventType, err)
			return
		}
	}

	lastID := events[len(events)-1].ID
	var wmErr error
	// Handling completed before watermark publication. Retry the same ID with
	// independent timeouts and short delays; a delay that observes runtime
	// cancellation returns before cleanup.
	for i := range 3 {
		wmCtx, wmCancel := context.WithTimeout(s.runtime.Context(), 5*time.Second)
		wmErr = s.cache.SetOutboxWatermark(wmCtx, lastID)
		wmCancel()
		if wmErr == nil {
			break
		}
		if i < 2 {
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-s.runtime.Context().Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
	if wmErr != nil {
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox watermark write failed: %v", wmErr)
		return
	}
	// Only a confirmed watermark permits consumed-row deletion. Pending-age
	// measurement then starts after this published ID, so this batch's processing
	// delay is excluded.
	s.cleanupConsumedOutbox(lastID)

	lagCtx, lagCancel := context.WithTimeout(s.runtime.Context(), 5*time.Second)
	s.checkOutboxLag(lagCtx, lastID)
	lagCancel()
}

// cleanupConsumedOutbox removes consumed events through a positive published
// watermark under the repository's cleanup lease. Missing dependencies or
// nonpositive watermark skips work, and contention leaves cleanup to another
// owner. The lease is released when this operation exits.
//
// Deletion runs in bounded chunks until a short or empty result under a
// ten-second runtime budget. An error preserves whatever consumed rows remain
// for later cleanup; this method does not advance the watermark or delete
// newer events. Repository retention rules still apply inside each deletion,
// so the watermark alone does not imply every older transaction is immediately
// removed.
func (s *SnapshotService) cleanupConsumedOutbox(watermark int64) {
	if s == nil || s.outboxRepo == nil || watermark <= 0 {
		return
	}

	ctx, cancel := context.WithTimeout(s.runtime.Context(), 10*time.Second)
	defer cancel()

	lease, acquired, err := s.outboxRepo.TryAcquireCleanupLock(ctx)
	if err != nil {
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox cleanup lock failed: %v", err)
		return
	}
	if !acquired {
		return
	}
	defer lease.Release()

	// Cleanup can stop partway on timeout or repository failure. Delete bounded
	// consumed chunks under the lease; retained rows remain eligible for a later
	// cleanup attempt.
	for {
		deleted, err := s.outboxRepo.DeleteConsumedUpTo(ctx, watermark, schedulerOutboxCleanupBatch)
		if err != nil {
			s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox cleanup failed: watermark=%d err=%v", watermark, err)
			return
		}
		// A short deletion ends this pass, including rows retained by repository
		// grace rules.
		if deleted == 0 || deleted < schedulerOutboxCleanupBatch {
			return
		}
	}
}

// handleOutboxEvent routes a durable event to usage updates, provider/group
// reconciliation, or a generation-coalesced full rebuild. Unknown event types
// are ignored. Full-rebuild events use the service runtime budget, while other
// handlers receive this event's context and batch markers.
func (s *SnapshotService) handleOutboxEvent(ctx context.Context, event SchedulerOutboxEvent, seen map[batchSeenKey]struct{}) error {
	switch event.EventType {
	case SchedulerOutboxEventProviderLastUsed:
		return s.handleLastUsedEvent(ctx, event.Payload)
	case SchedulerOutboxEventProviderBulkChanged:
		return s.handleBulkProviderEvent(ctx, event.Payload, seen)
	case SchedulerOutboxEventProviderGroupsChanged:
		return s.handleProviderEvent(ctx, event.ProviderID, event.Payload, seen)
	case SchedulerOutboxEventProviderChanged:
		return s.handleProviderEvent(ctx, event.ProviderID, event.Payload, seen)
	case SchedulerOutboxEventGroupChanged:
		return s.handleGroupEvent(ctx, event.GroupID, seen)
	case SchedulerOutboxEventFullRebuild:
		return s.triggerFullRebuild("outbox")
	default:
		return nil
	}
}

// handleLastUsedEvent publishes usage timestamps from a last_used object
// without rebuilding membership buckets. Missing cache/payload, another
// payload shape, invalid provider keys, or nonpositive converted seconds are
// ignored. Values follow toInt64's supported numeric conversions before Unix
// seconds are converted.
//
// The resulting map uses parsed provider IDs, so alternate textual keys for
// the same positive ID can overwrite each other in iteration order; no
// latest-value reduction occurs here. Floating values can truncate fractions,
// while fractional json.Number text is rejected. The cache adapter owns how
// these supplied observations update existing records.
func (s *SnapshotService) handleLastUsedEvent(ctx context.Context, payload map[string]any) error {
	if s.cache == nil || payload == nil {
		return nil
	}
	raw, ok := payload["last_used"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	updates := make(map[int64]time.Time, len(raw))
	// Decode provider IDs separately from their numeric timestamps, discarding
	// unusable entries. Parsed-ID aliases share one update key and follow this
	// unsorted map's iteration order.
	for key, value := range raw {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		sec, ok := toInt64(value)
		if !ok || sec <= 0 {
			continue
		}
		// Outbox usage values supply Unix seconds rather than nanoseconds.
		updates[id] = time.Unix(sec, 0)
	}
	if len(updates) == 0 {
		return nil
	}
	return s.cache.UpdateLastUsed(ctx, updates)
}

// handleBulkProviderEvent updates found provider records, deletes missing
// requested records, then rebuilds affected membership buckets. It accepts
// positive deduplicated provider_ids from the payload and uses former payload
// group_ids plus current memberships. Missing payload/source or no accepted
// IDs does nothing; source and cache mutation errors stop the event.
//
// A missing provider or unknown current platform requires every platform for
// affected groups because its stale membership cannot be narrowed reliably.
// Known platforms rebuild their shared-platform and specific-platform buckets,
// including former groups for every affected platform. Provider changes are
// already published before bucket tasks run, so later failures leave partial
// cache work and the durable event unfinished. Ordinary pair deduplication
// remains scoped to this poll batch.
func (s *SnapshotService) handleBulkProviderEvent(ctx context.Context, payload map[string]any, seen map[batchSeenKey]struct{}) error {
	if payload == nil {
		return nil
	}
	if s.providerRepo == nil {
		return nil
	}

	rawIDs := parseInt64Slice(payload["provider_ids"])
	if len(rawIDs) == 0 {
		return nil
	}

	ids := make([]int64, 0, len(rawIDs))
	seenIDs := make(map[int64]struct{}, len(rawIDs))
	for _, id := range rawIDs {
		if id <= 0 {
			continue
		}
		if _, exists := seenIDs[id]; exists {
			continue
		}
		seenIDs[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}

	preloadGroupIDs := parseInt64Slice(payload["group_ids"])
	providers, err := s.providerRepo.GetByIDs(ctx, ids)
	if err != nil {
		return err
	}

	found := make(map[int64]struct{}, len(providers))
	rebuildGroupSet := make(map[int64]struct{}, len(preloadGroupIDs))
	for _, gid := range preloadGroupIDs {
		if gid > 0 {
			rebuildGroupSet[gid] = struct{}{}
		}
	}

	// Publish found records before membership rebuilding and remember their
	// current groups. Later deletion or bucket failure can follow these writes,
	// so the event remains retryable until all work succeeds.
	for _, provider := range providers {
		if provider == nil || provider.SnapshotMetadata().ID <= 0 {
			continue
		}
		found[provider.SnapshotMetadata().ID] = struct{}{}
		if s.cache != nil {
			if err := s.cache.SetProvider(ctx, provider); err != nil {
				return err
			}
		}
		for _, gid := range provider.SnapshotMetadata().GroupIDs {
			if gid > 0 {
				rebuildGroupSet[gid] = struct{}{}
			}
		}
	}

	// Requested IDs missing from the source have stale shared payloads removed
	// before rebuilding their former groups. Without their current platform,
	// canonical rebuild scope must broaden.
	allProvidersFound := true
	for _, id := range ids {
		if _, ok := found[id]; ok {
			continue
		}
		allProvidersFound = false
		if s.cache != nil {
			if err := s.cache.DeleteProvider(ctx, id); err != nil {
				return err
			}
		}
	}

	rebuildGroupIDs := make([]int64, 0, len(rebuildGroupSet))
	for gid := range rebuildGroupSet {
		rebuildGroupIDs = append(rebuildGroupIDs, gid)
	}

	// A missing requested provider has no current platform to narrow its old
	// memberships. Rebuild every canonical platform and the shared pool for
	// affected groups.
	if !allProvidersFound {
		return s.rebuildByGroupIDs(ctx, rebuildGroupIDs, "provider_bulk_change", seen)
	}

	platformGroupSets := make(map[string]map[int64]struct{}, len(providers))
	addPlatformGroups := func(platform string, groupIDs []int64) {
		groupSet := platformGroupSets[platform]
		if groupSet == nil {
			groupSet = make(map[int64]struct{}, len(groupIDs))
			platformGroupSets[platform] = groupSet
		}
		for _, groupID := range groupIDs {
			groupSet[groupID] = struct{}{}
		}
	}
	for _, provider := range providers {
		if provider == nil || provider.SnapshotMetadata().ID <= 0 {
			continue
		}
		// An unrecognized platform cannot supply a trusted narrow rebuild set.
		// Refresh all canonical and shared pools for the affected memberships
		// instead.
		if !slices.Contains(capability.ProviderPlatforms(), provider.SnapshotMetadata().Platform) {
			return s.rebuildByGroupIDs(ctx, rebuildGroupIDs, "provider_bulk_change", seen)
		}
		providerGroupIDs := s.normalizeGroupIDs(provider.SnapshotMetadata().GroupIDs)
		addPlatformGroups("", providerGroupIDs)
		addPlatformGroups(provider.SnapshotMetadata().Platform, providerGroupIDs)
	}

	// Former memberships arrive in the payload even after the provider moved
	// groups. Add them to each affected shared/specific platform so
	// departed-group membership is rebuilt too.
	if len(preloadGroupIDs) > 0 {
		preloadGroupIDs = s.normalizeGroupIDs(preloadGroupIDs)
		for platform := range platformGroupSets {
			addPlatformGroups(platform, preloadGroupIDs)
		}
	}

	bucketCapacity := 0
	for _, groupSet := range platformGroupSets {
		bucketCapacity += len(groupSet) * 3
	}
	// Capacity includes extra room; generated canonical modes remain single and
	// forced.
	buckets := make([]SchedulerBucket, 0, bucketCapacity)
	for _, platform := range schedulerSnapshotPlatforms() {
		groupSet, ok := platformGroupSets[platform]
		if !ok {
			continue
		}
		platformGroupIDs := make([]int64, 0, len(groupSet))
		for groupID := range groupSet {
			platformGroupIDs = append(platformGroupIDs, groupID)
		}
		slices.Sort(platformGroupIDs)
		buckets = append(buckets, s.bucketsForPlatform(platform, platformGroupIDs, seen)...)
	}
	return s.rebuildBuckets(ctx, buckets, "provider_bulk_change")
}

// handleProviderEvent refreshes one positive provider ID and rebuilds its
// affected group buckets. Missing ID/source does nothing. A configured
// missing-record error removes the cached record before rebuilding
// payload-supplied former groups; other read errors stop handling.
//
// For a present provider, the shared record is published before group
// rebuilds. Payload memberships take precedence when supplied, while an empty
// payload list uses current metadata memberships. The rebuild covers all
// platforms for those groups, and a later failure leaves the watermark
// unchanged so this ordered partial work can be retried.
func (s *SnapshotService) handleProviderEvent(ctx context.Context, providerID *int64, payload map[string]any, seen map[batchSeenKey]struct{}) error {
	if providerID == nil || *providerID <= 0 {
		return nil
	}
	if s.providerRepo == nil {
		return nil
	}

	var groupIDs []int64
	if payload != nil {
		groupIDs = parseInt64Slice(payload["group_ids"])
	}

	provider, err := s.providerRepo.GetByID(ctx, *providerID)
	if err != nil {
		// A missing source record authorizes shared payload deletion before
		// former-group rebuilding. Other read failures retain the old cache and stop
		// this event.
		if errors.Is(err, s.providerNotFound()) {
			if s.cache != nil {
				if err := s.cache.DeleteProvider(ctx, *providerID); err != nil {
					return err
				}
			}
			return s.rebuildByGroupIDs(ctx, groupIDs, "provider_miss", seen)
		}
		return err
	}
	// Publish this current provider before querying its affected buckets. A later
	// rebuild failure leaves that payload write intact while the event's
	// watermark stays unadvanced.
	if s.cache != nil {
		if err := s.cache.SetProvider(ctx, provider); err != nil {
			return err
		}
	}
	if len(groupIDs) == 0 {
		groupIDs = provider.SnapshotMetadata().GroupIDs
	}
	return s.rebuildByProvider(ctx, provider, groupIDs, "provider_change", seen)
}

// handleGroupEvent reconciles a positive group ID unless its lifecycle
// completion marker already exists in this poll batch. Ordinary platform
// markers do not skip this authoritative group decision; errors leave
// lifecycle work unmarked for retry.
func (s *SnapshotService) handleGroupEvent(ctx context.Context, groupID *int64, seen map[batchSeenKey]struct{}) error {
	if groupID == nil || *groupID <= 0 {
		return nil
	}
	if seen != nil {
		if _, ok := seen[batchSeenKey{groupID: *groupID, lifecycle: true}]; ok {
			return nil
		}
	}
	return s.reconcileGroupLifecycle(ctx, *groupID, seen)
}

// reconcileGroupLifecycle prepares authoritative reopen or retirement work,
// then marks the group complete after required rebuilding succeeds. Active
// groups use one batch query cache across their reopened canonical tasks;
// retirement needs no provider query. Preparation errors and strict
// busy/fenced outcomes stop this event.
//
// The lifecycle lease has already been released before these queries run, so a
// later retirement can fence queued publication tokens. Strict tasks abort on
// their first error, keeping the durable lifecycle event retryable. Only
// completed work adds the lifecycle marker and all platform markers, allowing
// later ordinary events in this batch to reuse that completed decision.
func (s *SnapshotService) reconcileGroupLifecycle(ctx context.Context, groupID int64, seen map[batchSeenKey]struct{}) error {
	plan, err := s.prepareGroupLifecycle(ctx, groupID, nil)
	if err != nil {
		return err
	}
	if plan.active {
		queries := newSchedulerProviderQueryCache(plan.tasks)
		for _, task := range plan.tasks {
			if err := s.rebuildBucketWithTokenPolicyAndQueryCache(ctx, task, "group_change", true, queries); err != nil {
				return err
			}
		}
	}
	markGroupLifecycleSeen(seen, groupID)
	return nil
}

// prepareGroupLifecycle reads hydrated group authority under an acquired
// lifecycle lease and prepares bucket tokens or retirement. Nonpositive IDs
// return an empty plan; missing cache/source, contention, or authority errors
// fail before a trusted decision. knownHistorical=nil requests a registry read
// for retirement, while a supplied empty slice skips that read.
//
// An active group reopens all canonical buckets and returns their tokens;
// missing or inactive authority retires canonical and same-group historical
// buckets. Mutations can succeed partway before a later error, so callers
// retain the durable event until rebuilding and cleanup succeed. The lease is
// released with an independent context before provider queries. Deferred
// release errors join an operation error, and a failed successful-path release
// also rejects the prepared plan.
func (s *SnapshotService) prepareGroupLifecycle(ctx context.Context, groupID int64, knownHistorical []SchedulerBucket) (plan schedulerGroupLifecyclePlan, retErr error) {
	if groupID <= 0 {
		return schedulerGroupLifecyclePlan{}, nil
	}
	if s.cache == nil || s.groupRepo == nil {
		return schedulerGroupLifecyclePlan{}, ErrSchedulerCacheNotReady
	}

	lifecycleCtx, cancel := context.WithTimeout(ctx, schedulerGroupLifecycleTimeout)
	defer cancel()
	lease, acquired, err := s.cache.TryAcquireGroupLifecycleLease(lifecycleCtx, groupID, schedulerGroupLifecycleLeaseTTL)
	if err != nil {
		return schedulerGroupLifecyclePlan{}, err
	}
	if !acquired {
		return schedulerGroupLifecyclePlan{}, fmt.Errorf("%w: group=%d", ErrSchedulerGroupLifecycleLeaseBusy, groupID)
	}
	// Authority reads and bucket preparation can fail after acquiring the owner
	// lease. Retain ownership for deferred independent release, joining cleanup
	// failure with the operation cause.
	leaseHeld := true
	defer func() {
		if leaseHeld {
			retErr = errors.Join(retErr, s.releaseGroupLifecycleLease(lease))
		}
	}()

	group, err := s.groupRepo.GetByIDLite(lifecycleCtx, groupID)
	missing := errors.Is(err, s.groupNotFound())
	if err != nil && !missing {
		return schedulerGroupLifecyclePlan{}, err
	}
	// A nil, mismatched, or unhydrated successful read cannot establish group
	// authority. Reject it before any canonical reopen or retirement changes.
	if err == nil && (group == nil || group.ID != groupID || !group.Hydrated) {
		return schedulerGroupLifecyclePlan{}, fmt.Errorf("untrusted scheduler group lifecycle state: group=%d", groupID)
	}

	plan = schedulerGroupLifecyclePlan{active: !missing && group.IsActive()}
	if plan.active {
		buckets := schedulerBucketsForGroup(groupID)
		plan.tasks = make([]schedulerBucketWriteTask, 0, len(buckets))
		for _, bucket := range buckets {
			token, err := s.cache.ReopenBucket(lifecycleCtx, bucket)
			if err != nil {
				return schedulerGroupLifecyclePlan{}, err
			}
			plan.tasks = append(plan.tasks, schedulerBucketWriteTask{bucket: bucket, token: token})
		}
	} else {
		// A supplied historical slice records discovery already completed by the
		// caller. Nil requests a fresh registry read; an empty supplied slice avoids
		// repeating that global read.
		registered := knownHistorical
		if registered == nil {
			registered, err = s.cache.ListBuckets(lifecycleCtx)
			if err != nil {
				return schedulerGroupLifecyclePlan{}, err
			}
		}
		buckets := schedulerBucketsForGroup(groupID)
		for _, bucket := range registered {
			if bucket.GroupID == groupID {
				buckets = append(buckets, bucket)
			}
		}
		for _, bucket := range dedupeBuckets(buckets) {
			if err := s.cache.RetireBucket(lifecycleCtx, bucket); err != nil {
				return schedulerGroupLifecyclePlan{}, err
			}
		}
	}

	// Release lifecycle ownership before returning token-bearing tasks for
	// provider queries. Mark the release attempted even on error so deferred
	// cleanup does not issue a second release.
	releaseErr := s.releaseGroupLifecycleLease(lease)
	leaseHeld = false
	if releaseErr != nil {
		return schedulerGroupLifecyclePlan{}, releaseErr
	}
	return plan, nil
}

// releaseGroupLifecycleLease attempts owner-specific release using a
// two-second context independent of request and runtime cancellation. The
// adapter error passes back to lifecycle preparation so cleanup failure can
// leave the event unconfirmed.
func (s *SnapshotService) releaseGroupLifecycleLease(lease SchedulerGroupLifecycleLease) error {
	// Cancellation of authority work cannot cancel owner cleanup. Give release
	// its own bounded background context and propagate failure so lifecycle
	// completion remains unconfirmed.
	releaseCtx, cancel := context.WithTimeout(context.Background(), schedulerGroupLifecycleReleaseTimeout)
	defer cancel()
	return s.cache.ReleaseGroupLifecycleLease(releaseCtx, lease)
}

// markGroupLifecycleSeen records completed lifecycle work and all canonical
// platform pairs for this positive group. A nil marker map is ignored; callers
// invoke it after successful authority preparation and every required strict
// rebuild.
func markGroupLifecycleSeen(seen map[batchSeenKey]struct{}, groupID int64) {
	if seen == nil {
		return
	}
	seen[batchSeenKey{groupID: groupID, lifecycle: true}] = struct{}{}
	for _, platform := range schedulerSnapshotPlatforms() {
		seen[batchSeenKey{groupID: groupID, platform: platform}] = struct{}{}
	}
}

// rebuildByProvider delegates all-platform group rebuilding for a nonnil
// provider. The provider's current platform does not narrow this path; a nil
// provider does nothing, and supplied memberships determine the affected
// groups.
func (s *SnapshotService) rebuildByProvider(ctx context.Context, provider SnapshotProvider, groupIDs []int64, reason string, seen map[batchSeenKey]struct{}) error {
	if provider == nil {
		return nil
	}
	return s.rebuildByGroupIDs(ctx, groupIDs, reason, seen)
}

// schedulerSnapshotPlatforms returns the shared empty platform followed by all
// canonical provider platforms.
func schedulerSnapshotPlatforms() []string {
	return append([]string{""}, capability.ProviderPlatforms()...)
}

// schedulerBucketsForGroup returns canonical single/forced buckets for a
// positive group ID. Nonpositive IDs return nil; full rebuild explicitly calls
// schedulerCanonicalBuckets for group zero to maintain its empty canonical
// buckets.
func schedulerBucketsForGroup(groupID int64) []SchedulerBucket {
	if groupID <= 0 {
		return nil
	}
	return schedulerCanonicalBuckets(groupID)
}

// schedulerCanonicalBuckets allocates single and forced buckets for the shared
// and every canonical provider platform. The supplied ID is used unchanged,
// including group zero in full rebuilding; no mixed-mode buckets are added.
func schedulerCanonicalBuckets(groupID int64) []SchedulerBucket {
	// Capacity includes extra room; generated canonical modes remain single and
	// forced.
	buckets := make([]SchedulerBucket, 0, len(schedulerSnapshotPlatforms())*3)
	for _, platform := range schedulerSnapshotPlatforms() {
		buckets = append(buckets,
			SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeSingle},
			SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeForced},
		)
	}
	return buckets
}

// rebuildByGroupIDs normalizes memberships and prepares every platform's
// single/forced buckets not already scheduled in this batch. Empty or entirely
// nonpositive membership input normalizes to group zero, whose database
// provider query returns no records.
func (s *SnapshotService) rebuildByGroupIDs(ctx context.Context, groupIDs []int64, reason string, seen map[batchSeenKey]struct{}) error {
	groupIDs = s.normalizeGroupIDs(groupIDs)
	if len(groupIDs) == 0 {
		return nil
	}
	// Capacity includes extra room; generated canonical modes remain single and
	// forced.
	buckets := make([]SchedulerBucket, 0, len(groupIDs)*len(schedulerSnapshotPlatforms())*3)
	for _, platform := range schedulerSnapshotPlatforms() {
		buckets = append(buckets, s.bucketsForPlatform(platform, groupIDs, seen)...)
	}
	return s.rebuildBuckets(ctx, buckets, reason)
}

// bucketsForPlatform creates single and forced tasks for unscheduled
// group/platform pairs. A supplied seen map is updated before rebuilding,
// preventing repeated ordinary queries for later same-pair events in this
// batch. Nil maps allow every supplied pair.
//
// This marker records scheduling rather than completed publication. A handler
// failure aborts the poll and retains its watermark, so the next poll has a
// new marker map and can retry. Lifecycle completion uses its separate key and
// is not authorized by an ordinary pair marker.
func (s *SnapshotService) bucketsForPlatform(platform string, groupIDs []int64, seen map[batchSeenKey]struct{}) []SchedulerBucket {
	// Capacity includes extra room; generated canonical modes remain single and
	// forced.
	buckets := make([]SchedulerBucket, 0, len(groupIDs)*3)
	for _, gid := range groupIDs {
		// Pair scheduling happens before publication and covers later ordinary
		// same-pair events in this poll. A handling failure aborts the batch, whose
		// next poll rebuilds a fresh seen map.
		if seen != nil {
			key := batchSeenKey{groupID: gid, platform: platform}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
		}
		buckets = append(buckets, SchedulerBucket{GroupID: gid, Platform: platform, Mode: SchedulerModeSingle})
		buckets = append(buckets, SchedulerBucket{GroupID: gid, Platform: platform, Mode: SchedulerModeForced})
	}
	return buckets
}

// rebuildBuckets captures publication tokens before querying providers and
// uses one serial query cache across the resulting ordinary tasks. Retired or
// fenced capture failures skip those buckets. Other preparation errors are
// retained while prepared tasks still run.
//
// The source query can therefore succeed for a prepared pair even when another
// token capture failed. Each task attempts its own membership publication and
// consumes its query-cache share on exit. The earliest preparation error takes
// precedence over a later rebuild error; the caller retains this combined
// result as unconfirmed work.
func (s *SnapshotService) rebuildBuckets(ctx context.Context, buckets []SchedulerBucket, reason string) error {
	tasks, firstErr := s.prepareBucketWriteTasks(ctx, buckets)
	queries := newSchedulerProviderQueryCache(tasks)
	if err := s.rebuildPreparedBucketTasks(ctx, tasks, reason, false, queries); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// prepareBucketWriteTasks captures ordinary publication tokens before provider
// queries. Missing cache returns ErrSchedulerCacheNotReady. Retired or fenced
// buckets are omitted; other capture failures retain the first error while
// preparation continues, allowing callers to decide whether prepared work
// should run.
func (s *SnapshotService) prepareBucketWriteTasks(ctx context.Context, buckets []SchedulerBucket) ([]schedulerBucketWriteTask, error) {
	if s.cache == nil {
		return nil, ErrSchedulerCacheNotReady
	}
	tasks := make([]schedulerBucketWriteTask, 0, len(buckets))
	var firstErr error
	for _, bucket := range buckets {
		token, err := s.cache.CaptureBucketWriteToken(ctx, bucket)
		if err != nil {
			// Ordinary token capture cannot reopen retired or mismatched generations.
			// Skip those buckets while collecting other usable tokens, leaving
			// lifecycle reopening to authoritative group work.
			if errors.Is(err, ErrSchedulerBucketRetired) || errors.Is(err, ErrSchedulerBucketWriteFenced) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		tasks = append(tasks, schedulerBucketWriteTask{bucket: bucket, token: token})
	}
	return tasks, firstErr
}

// rebuildPreparedBucketTasks executes every supplied task serially with the
// caller's strict policy and shared query cache. It retains the first error
// while later tasks continue, so a failed bucket does not suppress unrelated
// prepared publication.
//
// Each invoked task releases its query consumer on exit. Strict contention or
// fencing contributes an error; ordinary contention or fencing is skipped by
// the task handler. Full rebuilding uses this executor for both sets, so
// ordinary work can still run after a strict set failed. The caller keeps this
// mixed outcome unconfirmed through the returned error.
func (s *SnapshotService) rebuildPreparedBucketTasks(
	ctx context.Context,
	tasks []schedulerBucketWriteTask,
	reason string,
	strict bool,
	queries *schedulerProviderQueryCache,
) error {
	var firstErr error
	for _, task := range tasks {
		if err := s.rebuildBucketWithTokenPolicyAndQueryCache(ctx, task, reason, strict, queries); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// rebuildBucketWithTokenPolicyAndQueryCache queries and attempts one
// token-bearing publication under a bucket lease. Every executed reusable task
// defers query-cache release, including contention and failure. A
// thirty-second child budget inherits the supplied context; lease release uses
// a separate five-second context.
//
// Strict lifecycle work returns typed contention or fencing errors so the
// event remains unfinished. Ordinary work treats those outcomes as skipped
// buckets, with later rebuilding providing another opportunity. Source and
// other write failures propagate. Lease-release failure is ignored after the
// publication attempt; bucket membership token validation still belongs to the
// storage adapter.
func (s *SnapshotService) rebuildBucketWithTokenPolicyAndQueryCache(
	ctx context.Context,
	task schedulerBucketWriteTask,
	reason string,
	strict bool,
	queries *schedulerProviderQueryCache,
) error {
	// Attach query cleanup before attempting the lock. Busy, failed-query, and
	// fenced-write exits all consume this executed task's share of batch data.
	if queries != nil {
		defer queries.release(task.bucket)
	}
	if s.cache == nil {
		return ErrSchedulerCacheNotReady
	}
	bucket := task.bucket
	lease, ok, err := s.cache.AcquireBucketLease(ctx, bucket, 30*time.Second)
	if err != nil {
		return err
	}
	// Contention leaves ordinary reconciliation retryable elsewhere, but reopened
	// lifecycle work must remain unconfirmed. Strict callers return a typed busy
	// cause rather than accepting skipped publication.
	if !ok {
		if strict {
			return fmt.Errorf("%w: bucket=%s", ErrSchedulerBucketRebuildBusy, bucket.String())
		}
		return nil
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Release uses its own deadline after query/write exit. Its error is ignored
		// here; the owner lease can expire while publication's typed result remains
		// the caller's outcome.
		_ = lease.Release(releaseCtx)
	}()

	rebuildCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	providers, err := s.loadProvidersForRebuild(rebuildCtx, bucket, queries)
	if err != nil {
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] rebuild failed: bucket=%s reason=%s err=%v", bucket.String(), reason, err)
		return err
	}
	if err := s.setRebuildSnapshot(rebuildCtx, task, providers, queries); err != nil {
		// An epoch change can fence a token after the provider query. Ordinary work
		// skips obsolete membership; strict lifecycle callers retain the event until
		// their required publication succeeds.
		if errors.Is(err, ErrSchedulerBucketRetired) || errors.Is(err, ErrSchedulerBucketWriteFenced) {
			s.diagnostics().event("debug", "[Scheduler] rebuild fenced", "bucket", bucket.String(), "reason", reason)
			if strict {
				return err
			}
			return nil
		}
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] rebuild cache failed: bucket=%s reason=%s err=%v", bucket.String(), reason, err)
		return err
	}
	s.diagnostics().event("debug", "[Scheduler] rebuild ok", "bucket", bucket.String(), "reason", reason, "size", len(providers))
	return nil
}

// setRebuildSnapshot publishes one bucket and can reuse earlier successfully
// encoded IDs for its equivalent batch query. Caches lacking the optional
// writer, unsupported modes, or absent query caches use SetSnapshot. A final
// consumer also uses the ordinary writer when no reusable IDs have been
// established.
//
// With remaining consumers, the first successful full writer supplies its
// actual ordered encoding result, including a valid empty slice. An error
// leaves IDs uncached even if payload writes occurred. Subsequent consumers
// publish membership from those IDs under their own tokens, so encoding
// omissions stay consistent across single and forced buckets. A non-error
// older-version activation may keep newer membership while still supplying its
// encoded ID result.
func (s *SnapshotService) setRebuildSnapshot(
	ctx context.Context,
	task schedulerBucketWriteTask,
	providers []SnapshotProvider,
	queries *schedulerProviderQueryCache,
) error {
	writer, ok := s.cache.(schedulerSnapshotProviderIDWriter)
	key, reusable := schedulerProviderQueryKeyForBucket(task.bucket)
	if !ok || queries == nil || !reusable {
		return s.cache.SetSnapshot(ctx, task.bucket, task.token, providers)
	}

	if providerIDs, exists := queries.snapshotProviderIDs[key]; exists {
		return writer.SetSnapshotByProviderIDs(ctx, task.bucket, task.token, providerIDs)
	}
	if queries.remaining[key] <= 1 {
		return s.cache.SetSnapshot(ctx, task.bucket, task.token, providers)
	}

	providerIDs, err := writer.SetSnapshotAndReturnProviderIDs(ctx, task.bucket, task.token, providers)
	if err != nil {
		return err
	}
	if queries.remaining[key] > 1 {
		// Reuse the writer's actual successful encoding result, including an empty
		// slice. Deriving IDs from input metadata would revive encoding omissions in
		// later bucket membership.
		queries.snapshotProviderIDs[key] = providerIDs
	}
	return nil
}

// triggerFullRebuild joins generation-coalesced reconciliation under a
// two-minute runtime-derived budget and passes its reason to diagnostics.
// Missing cache returns ErrSchedulerCacheNotReady; queued acquisition and
// provider/cache operations observe the runtime context when their
// implementations cooperate.
func (s *SnapshotService) triggerFullRebuild(reason string) error {
	if s.cache == nil {
		return ErrSchedulerCacheNotReady
	}
	return s.coalesceFullRebuild(func() error {
		ctx, cancel := context.WithTimeout(s.runtime.Context(), 2*time.Minute)
		defer cancel()
		return s.rebuildFullSnapshot(ctx, reason)
	})
}

// rebuildFullSnapshot reconciles canonical and registered historical buckets
// against current active-group discovery. Registry and active-ID reads
// complete before bucket mutations or provider queries; either failure aborts
// that discovery phase. Group-zero and negative historical buckets remain
// ordinary rebuild work.
//
// Active canonical buckets first capture tokens. Retirement/fencing instead
// requires fresh hydrated group authority under a lifecycle lease, discarding
// any partial captures for that group. Apparently stale positive groups are
// sorted and rechecked under their own leases before retirement, allowing
// concurrent reactivation to reopen them. All remaining ordinary tokens are
// prepared before provider queries; strict reopened and ordinary tasks share
// batch query data. Later preparation or publication failure can follow
// earlier lifecycle mutations and leaves durable recovery necessary.
func (s *SnapshotService) rebuildFullSnapshot(ctx context.Context, reason string) error {
	if s.cache == nil {
		return ErrSchedulerCacheNotReady
	}

	// Read the bucket registry and active group IDs before mutating buckets or querying providers.
	// If either read fails, leave buckets untouched.
	// Recheck apparently stale groups under a lease before retiring them.
	registered, err := s.cache.ListBuckets(ctx)
	if err != nil {
		return err
	}
	registered = dedupeBuckets(registered)

	activeGroupIDs, err := s.listActiveSchedulerGroupIDs(ctx)
	if err != nil {
		return err
	}
	activeGroups := make(map[int64]struct{}, len(activeGroupIDs))
	for _, groupID := range activeGroupIDs {
		activeGroups[groupID] = struct{}{}
	}

	registeredByGroup := make(map[int64][]SchedulerBucket)
	for _, bucket := range registered {
		registeredByGroup[bucket.GroupID] = append(registeredByGroup[bucket.GroupID], bucket)
	}

	groupZeroCanonical := schedulerCanonicalBuckets(0)
	capturedTasks, err := s.captureFullRebuildCanonicalTasks(ctx, groupZeroCanonical)
	if err != nil {
		return err
	}
	ordinaryBuckets := appendBucketsExcept(nil, registeredByGroup[0], groupZeroCanonical)
	for groupID, buckets := range registeredByGroup {
		if groupID < 0 {
			ordinaryBuckets = append(ordinaryBuckets, buckets...)
		}
	}

	reopenedTasks := make([]schedulerBucketWriteTask, 0)
	for _, groupID := range activeGroupIDs {
		canonical := schedulerBucketsForGroup(groupID)
		canonicalTasks, captureErr := s.captureFullRebuildCanonicalTasks(ctx, canonical)
		if captureErr == nil {
			capturedTasks = append(capturedTasks, canonicalTasks...)
			ordinaryBuckets = appendBucketsExcept(ordinaryBuckets, registeredByGroup[groupID], canonical)
			continue
		}
		if !errors.Is(captureErr, ErrSchedulerBucketRetired) && !errors.Is(captureErr, ErrSchedulerBucketWriteFenced) {
			return captureErr
		}

		// An earlier full_rebuild event may observe activation committed for a later group_changed event.
		// Use the current database state so the earlier event can advance the outbox watermark.
		knownHistorical := registeredByGroup[groupID]
		if knownHistorical == nil {
			knownHistorical = []SchedulerBucket{}
		}
		plan, err := s.prepareGroupLifecycle(ctx, groupID, knownHistorical)
		if err != nil {
			return err
		}
		if plan.active {
			reopenedTasks = append(reopenedTasks, plan.tasks...)
			ordinaryBuckets = appendBucketsExcept(ordinaryBuckets, registeredByGroup[groupID], canonical)
		}
	}

	staleGroupIDs := make([]int64, 0)
	for groupID := range registeredByGroup {
		if groupID <= 0 {
			continue
		}
		if _, active := activeGroups[groupID]; !active {
			staleGroupIDs = append(staleGroupIDs, groupID)
		}
	}
	slices.Sort(staleGroupIDs)

	for _, groupID := range staleGroupIDs {
		plan, err := s.prepareGroupLifecycle(ctx, groupID, registeredByGroup[groupID])
		if err != nil {
			return err
		}
		if plan.active {
			reopenedTasks = append(reopenedTasks, plan.tasks...)
			ordinaryBuckets = appendBucketsExcept(ordinaryBuckets, registeredByGroup[groupID], schedulerBucketsForGroup(groupID))
		}
	}

	return s.prepareAndRebuildFullSnapshot(ctx, capturedTasks, reopenedTasks, ordinaryBuckets, reason)
}

// listActiveSchedulerGroupIDs returns sorted unique positive active IDs. It
// prefers the optional lightweight interface and propagates its errors; when
// absent, ListActive supplies group values instead. Missing source returns
// ErrSchedulerCacheNotReady before full-rebuild mutations.
func (s *SnapshotService) listActiveSchedulerGroupIDs(ctx context.Context) ([]int64, error) {
	if s.groupRepo == nil {
		return nil, ErrSchedulerCacheNotReady
	}

	// A lightweight reader can reduce discovery data, but its read failure must
	// abort reconciliation. Fall back to hydrated ListActive only when that
	// interface is absent.
	var groupIDs []int64
	if lister, ok := s.groupRepo.(schedulerActiveGroupIDLister); ok {
		ids, err := lister.ListActiveIDs(ctx)
		if err != nil {
			return nil, err
		}
		groupIDs = ids
	} else {
		groups, err := s.groupRepo.ListActive(ctx)
		if err != nil {
			return nil, err
		}
		groupIDs = make([]int64, 0, len(groups))
		for _, group := range groups {
			groupIDs = append(groupIDs, group.ID)
		}
	}

	seen := make(map[int64]struct{}, len(groupIDs))
	normalized := make([]int64, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID <= 0 {
			continue
		}
		if _, ok := seen[groupID]; ok {
			continue
		}
		seen[groupID] = struct{}{}
		normalized = append(normalized, groupID)
	}
	slices.Sort(normalized)
	return normalized, nil
}

// prepareAndRebuildFullSnapshot captures every outstanding ordinary bucket
// token before querying providers, excluding already captured or reopened
// identities. Any non-fencing preparation error aborts provider work, although
// earlier lifecycle decisions made by the caller may already have mutated
// buckets.
//
// Reopened tasks then use strict publication, and captured tasks use ordinary
// skip policy; both share one counted query cache. The task executor tries the
// complete strict set and still processes ordinary tasks after a strict
// failure. The first strict or ordinary error determines the returned result,
// keeping this full rebuild unconfirmed for its caller.
func (s *SnapshotService) prepareAndRebuildFullSnapshot(
	ctx context.Context,
	captured []schedulerBucketWriteTask,
	reopened []schedulerBucketWriteTask,
	ordinaryBuckets []SchedulerBucket,
	reason string,
) error {
	// Provider work begins after every ordinary token is prepared. Strict
	// reopened tasks and ordinary captured tasks share query data, while a
	// preparation error leaves later provider writes untouched.
	preparedBuckets := make(map[SchedulerBucket]struct{}, len(captured)+len(reopened))
	for _, task := range captured {
		preparedBuckets[task.bucket] = struct{}{}
	}
	for _, task := range reopened {
		preparedBuckets[task.bucket] = struct{}{}
	}

	ordinaryBuckets = dedupeBuckets(ordinaryBuckets)
	toCapture := make([]SchedulerBucket, 0, len(ordinaryBuckets))
	for _, bucket := range ordinaryBuckets {
		if _, ok := preparedBuckets[bucket]; !ok {
			toCapture = append(toCapture, bucket)
		}
	}
	ordinary, firstErr := s.prepareBucketWriteTasks(ctx, toCapture)
	if firstErr != nil {
		return firstErr
	}
	captured = append(captured, ordinary...)
	queries := newSchedulerProviderQueryCache(reopened, captured)
	if err := s.rebuildPreparedBucketTasks(ctx, reopened, reason, true, queries); err != nil {
		firstErr = err
	}
	if err := s.rebuildPreparedBucketTasks(ctx, captured, reason, false, queries); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// captureFullRebuildCanonicalTasks captures every canonical token or returns
// an error with no partial task slice. Full rebuild can then recheck a
// retired/fenced group's authority instead of publishing a mixture of old
// captured and reopened tokens.
func (s *SnapshotService) captureFullRebuildCanonicalTasks(ctx context.Context, buckets []SchedulerBucket) ([]schedulerBucketWriteTask, error) {
	if s.cache == nil {
		return nil, ErrSchedulerCacheNotReady
	}
	tasks := make([]schedulerBucketWriteTask, 0, len(buckets))
	for _, bucket := range buckets {
		token, err := s.cache.CaptureBucketWriteToken(ctx, bucket)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, schedulerBucketWriteTask{bucket: bucket, token: token})
	}
	return tasks, nil
}

// appendBucketsExcept appends buckets whose complete struct identities are
// absent from excluded. Callers supply input bucket storage that appended dst
// values cannot overwrite. Under that ownership rule, allowed entries retain
// input order; the helper may reuse dst storage and keeps allowed duplicates.
func appendBucketsExcept(dst, buckets, excluded []SchedulerBucket) []SchedulerBucket {
	excludedKeys := make(map[SchedulerBucket]struct{}, len(excluded))
	for _, bucket := range excluded {
		excludedKeys[bucket] = struct{}{}
	}
	for _, bucket := range buckets {
		if _, ok := excludedKeys[bucket]; !ok {
			dst = append(dst, bucket)
		}
	}
	return dst
}

// coalesceFullRebuild serializes full rebuild execution and shares results for
// request generations covered by a completed run. Every caller first records a
// generation, then acquires the single permit or returns runtime cancellation.
// A queued covered caller returns the prior run's result, including its error.
//
// The permit holder captures coveredThrough before invoking run outside the
// state mutex. Requests arriving during that I/O are consequently not marked
// completed by the earlier run; a later waiting holder performs a trailing
// rebuild for them. Completion publishes the covered generation and result
// under the state mutex, and deferred permit release allows another caller to
// progress. This method does not loop automatically for newer generations
// inside one holder.
func (s *SnapshotService) coalesceFullRebuild(run func() error) error {
	// Register this caller's generation before waiting for the execution permit.
	// A running rebuild can cover earlier registrations while later arrivals
	// remain eligible for a trailing run.
	s.fullRebuildStateMu.Lock()
	s.fullRebuildRequested++
	requestID := s.fullRebuildRequested
	s.fullRebuildStateMu.Unlock()

	// One persistent permit serializes rebuild I/O outside the generation mutex.
	// Runtime cancellation wakes queued callers, and deferred receipt returns the
	// permit after any held-path exit.
	s.fullRebuildGateOnce.Do(func() { s.fullRebuildGate = make(chan struct{}, 1) })
	select {
	case s.fullRebuildGate <- struct{}{}:
		defer func() { <-s.fullRebuildGate }()
	case <-s.runtime.Context().Done():
		return s.runtime.Context().Err()
	}
	if err := s.runtime.Context().Err(); err != nil {
		return err
	}

	s.fullRebuildStateMu.Lock()
	if s.fullRebuildCompleted >= requestID {
		err := s.fullRebuildLastErr
		s.fullRebuildStateMu.Unlock()
		return err
	}
	// A source transaction can commit after this run began. Capture coverage
	// before I/O so requests arriving during that read cannot claim the earlier
	// database result covered them.
	coveredThrough := s.fullRebuildRequested
	s.fullRebuildStateMu.Unlock()

	err := run()

	// Publish this run's result only for the captured generations. Waiting
	// covered callers receive the same failure, while uncovered callers can still
	// execute the trailing rebuild.
	s.fullRebuildStateMu.Lock()
	s.fullRebuildCompleted = coveredThrough
	s.fullRebuildLastErr = err
	s.fullRebuildStateMu.Unlock()
	return err
}

// checkOutboxLag samples pending age and optional maximum-ID distance after a
// published watermark, then reserves at most one degradation rebuild. Missing
// configuration/source or pending-age read errors skip the check. Backlog is
// an ID-distance estimate; an unreadable MaxID leaves its state unknown and
// preserves a backlog retry.
//
// Under lagMu, recovery clears the successful latch and retry state only when
// age is healthy and backlog is known healthy. Initial degraded age
// observations reach their configured count; a lag-ready reason can preempt a
// different backlog retry. A selected reason reserves running before releasing
// the mutex for rebuild I/O. Success latches the episode until observed
// recovery; failure clears the latch and schedules capped exponential retry,
// keeping reason-specific activity checks and concurrent polls separate from
// the running operation.
func (s *SnapshotService) checkOutboxLag(ctx context.Context, watermark int64) {
	if s.cfg == nil || s.outboxRepo == nil {
		return
	}
	now := time.Now()
	oldestCreatedAt, ok, err := s.outboxRepo.FirstCreatedAtAfter(ctx, watermark)
	if err != nil {
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox pending event read failed: %v", err)
		return
	}
	var lag time.Duration
	if ok && !oldestCreatedAt.IsZero() {
		lag = now.Sub(oldestCreatedAt)
	}
	// Age thresholds discard fractional seconds before comparing configured whole
	// seconds.
	lagSeconds := int(lag.Seconds())
	lagWarning := ok && !oldestCreatedAt.IsZero() &&
		s.cfg.OutboxLagWarnSeconds > 0 &&
		lagSeconds >= s.cfg.OutboxLagWarnSeconds

	lagDegraded := ok && !oldestCreatedAt.IsZero() &&
		s.cfg.OutboxLagRebuildSeconds > 0 &&
		lagSeconds >= s.cfg.OutboxLagRebuildSeconds

	backlogThreshold := s.cfg.OutboxBacklogRebuildRows
	backlogKnown := true
	var backlog int64
	if backlogThreshold > 0 {
		// Maximum-ID failure leaves backlog unknown rather than confirmed healthy.
		// Existing backlog retries survive that uncertainty, while sampled logging
		// limits repeated cache/source error output.
		maxID, maxErr := s.outboxRepo.MaxID(ctx)
		if maxErr != nil {
			backlogKnown = false
			if s.shouldLogOutboxMaxIDError(now) {
				s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox max id read failed: %v", maxErr)
			}
		} else {
			// ID distance includes allocation gaps and approximates pending row count.
			backlog = maxID - watermark
		}
	}
	backlogDegraded := backlogKnown && backlogThreshold > 0 && backlog >= int64(backlogThreshold)

	// The mutex reserves one rebuild across independently progressing polls.
	// Successful work stays latched until confirmed recovery; failed work follows
	// reason-specific delayed retry while I/O executes outside this lock.
	logLagWarning := s.shouldLogOutboxLagWarning(lagWarning)
	s.lagMu.Lock()
	// Unknown backlog cannot establish recovery. Clear the prior latch only when
	// age is healthy and the enabled backlog observation is known healthy.
	fullyRecovered := !lagDegraded && backlogKnown && !backlogDegraded
	if fullyRecovered {
		s.lagFailures = 0
		s.outboxRebuildLatched = false
		s.outboxRebuildFailures = 0
		s.outboxRebuildRetryAt = time.Time{}
		s.outboxRebuildRetryReason = ""
	}

	// A pending retry belongs to its degradation reason. Healthy age cancels an
	// age retry, while unreadable backlog keeps its existing backlog retry
	// active.
	if s.outboxRebuildRetryReason != "" {
		retryReasonActive := (s.outboxRebuildRetryReason == "outbox_lag" && lagDegraded) ||
			(s.outboxRebuildRetryReason == "outbox_backlog" && (!backlogKnown || backlogDegraded))
		if !retryReasonActive {
			s.outboxRebuildFailures = 0
			s.outboxRebuildRetryAt = time.Time{}
			s.outboxRebuildRetryReason = ""
		}
	}

	// Initial age failures count only before a lag retry is scheduled and outside
	// reserved/latched work. Backlog retries still allow enough age observations
	// to preempt their reason.
	lagRetryPending := s.outboxRebuildRetryReason == "outbox_lag" && !s.outboxRebuildRetryAt.IsZero()
	if lagDegraded {
		if !s.outboxRebuildLatched && !s.outboxRebuildRunning && !lagRetryPending {
			s.lagFailures++
		}
	} else {
		s.lagFailures = 0
	}
	failures := s.lagFailures
	lagReady := lagDegraded && failures >= s.cfg.OutboxLagRebuildFailures
	retryDue := s.outboxRebuildRetryReason != "" &&
		!s.outboxRebuildRetryAt.IsZero() && !now.Before(s.outboxRebuildRetryAt)

	reason := ""
	// A newly ready age condition can outrank a different pending backlog retry.
	// Reserve running before unlocking so another health poll cannot start the
	// same episode concurrently.
	lagCanPreemptRetry := lagReady && s.outboxRebuildRetryReason != "outbox_lag"
	if !s.outboxRebuildLatched && !s.outboxRebuildRunning &&
		(s.outboxRebuildRetryAt.IsZero() || retryDue || lagCanPreemptRetry) {
		switch {
		case lagReady || (retryDue && s.outboxRebuildRetryReason == "outbox_lag" && lagDegraded):
			if s.outboxRebuildRetryReason != "" && s.outboxRebuildRetryReason != "outbox_lag" {
				s.outboxRebuildFailures = 0
				s.outboxRebuildRetryAt = time.Time{}
				s.outboxRebuildRetryReason = ""
			}
			reason = "outbox_lag"
			s.lagFailures = 0
		case backlogDegraded && (s.outboxRebuildRetryReason == "" ||
			(retryDue && s.outboxRebuildRetryReason == "outbox_backlog")):
			reason = "outbox_backlog"
		}
		if reason != "" {
			s.outboxRebuildRunning = true
		}
	}
	s.lagMu.Unlock()

	if logLagWarning {
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox lag warning: %ds", lagSeconds)
	}

	if reason == "" {
		return
	}

	var rebuildErr error
	switch reason {
	case "outbox_lag":
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox lag rebuild triggered: lag=%s failures=%d", lag, failures)
		rebuildErr = s.triggerFullRebuild(reason)
	case "outbox_backlog":
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox backlog rebuild triggered: backlog=%d", backlog)
		rebuildErr = s.triggerFullRebuild(reason)
	}

	// The reserved operation finished its I/O. Release running and publish either
	// a successful latch or the failed reason's next retry time under the same
	// episode mutex.
	s.lagMu.Lock()
	s.outboxRebuildRunning = false
	if rebuildErr == nil {
		s.outboxRebuildLatched = true
		s.outboxRebuildFailures = 0
		s.outboxRebuildRetryAt = time.Time{}
		s.outboxRebuildRetryReason = ""
	} else {
		s.outboxRebuildLatched = false
		s.outboxRebuildFailures++
		s.outboxRebuildRetryAt = time.Now().Add(outboxRebuildRetryDelay(s.outboxRebuildFailures))
		s.outboxRebuildRetryReason = reason
	}
	s.lagMu.Unlock()

	if rebuildErr != nil {
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] %s rebuild failed: %v", reason, rebuildErr)
	}
}

// outboxRebuildRetryDelay returns a doubling failed-rebuild delay beginning at
// five seconds and capped at five minutes. Failure counts at most one,
// including nonpositive values, use the base delay.
func outboxRebuildRetryDelay(failures int) time.Duration {
	delay := outboxRebuildRetryBaseDelay
	// Repeated degraded rebuild failures double the waiting budget only to the
	// fixed cap, keeping subsequent health polls from continuously issuing
	// full rebuilds.
	for i := 1; i < failures && delay < outboxRebuildRetryMaxDelay; i++ {
		delay *= 2
		if delay >= outboxRebuildRetryMaxDelay {
			return outboxRebuildRetryMaxDelay
		}
	}
	return delay
}

// clearOutboxDegradedEpisode clears age, latch, failure, retry, and warning
// state under lagMu after an empty visible poll. Nil service is ignored. It
// retains outboxRebuildRunning, so an already reserved rebuild cannot be
// duplicated during its remaining I/O.
//
// That running operation owns clearing its reservation after completion and
// may then publish its own result state. The empty poll establishes only that
// this query saw no pending events; later commits and the active operation can
// still require another observation to establish recovery.
func (s *SnapshotService) clearOutboxDegradedEpisode() {
	if s == nil {
		return
	}
	s.lagMu.Lock()
	if s.lagFailures != 0 || s.outboxRebuildLatched || s.outboxRebuildRunning ||
		s.outboxRebuildFailures != 0 || !s.outboxRebuildRetryAt.IsZero() ||
		s.outboxRebuildRetryReason != "" || s.outboxLagWarningActive {
		s.lagFailures = 0
		s.outboxRebuildLatched = false
		s.outboxRebuildFailures = 0
		s.outboxRebuildRetryAt = time.Time{}
		s.outboxRebuildRetryReason = ""
		s.outboxLagWarningActive = false
	}
	s.lagMu.Unlock()
}

// shouldLogOutboxMaxIDError admits a backlog-read warning using the supplied
// time sample under lagMu. Initial state permits logging; a later call
// within one minute of the previously admitted sample returns false. A
// backward time sample can remain suppressed until the stored interval has
// elapsed.
//
// For nonzero samples, admission is recorded before returning true, so
// concurrent callers cannot all claim the same sampling interval. Logging
// happens after the caller receives the result and does not hold this mutex.
// This timestamp is shared with degradation state but does not establish
// backlog health.
func (s *SnapshotService) shouldLogOutboxMaxIDError(now time.Time) bool {
	s.lagMu.Lock()
	defer s.lagMu.Unlock()
	if !s.outboxMaxIDErrorLastLoggedAt.IsZero() && now.Sub(s.outboxMaxIDErrorLastLoggedAt) < outboxMaxIDErrorLogSampleInterval {
		return false
	}
	s.outboxMaxIDErrorLastLoggedAt = now
	return true
}

// shouldLogOutboxLagWarning records whether the supplied age warning condition
// is active and admits a false-to-true transition under lagMu. Initial
// inactive state allows the first active call to report. Subsequent active
// calls remain suppressed until an inactive observation or episode clearing
// resets the flag.
//
// The state update occurs before returning the admission result, so concurrent
// observations share one transition decision. Logging happens in the caller
// after this lock is released. The method reports warning transitions
// independently from whether a full rebuild is latched, running, or scheduled
// for retry.
func (s *SnapshotService) shouldLogOutboxLagWarning(active bool) bool {
	s.lagMu.Lock()
	defer s.lagMu.Unlock()
	shouldLog := active && !s.outboxLagWarningActive
	s.outboxLagWarningActive = active
	return shouldLog
}

// loadProvidersFromDB reads eligible records for a positive group and its
// bucket platform. Missing source returns ErrSchedulerCacheNotReady before
// checking the group ID. With a source present, nonpositive groups return no
// records and an empty platform queries all canonical platforms. useMixed does
// not change this query implementation.
func (s *SnapshotService) loadProvidersFromDB(ctx context.Context, bucket SchedulerBucket, useMixed bool) ([]SnapshotProvider, error) {
	if s.providerRepo == nil {
		return nil, ErrSchedulerCacheNotReady
	}
	if bucket.GroupID <= 0 {
		return nil, nil
	}
	if bucket.Platform == "" {
		return s.providerRepo.ListSchedulableByGroupIDAndPlatforms(ctx, bucket.GroupID, capability.ProviderPlatforms())
	}
	return s.providerRepo.ListSchedulableByGroupIDAndPlatform(ctx, bucket.GroupID, bucket.Platform)
}

// loadProvidersForRebuild reuses successful single/forced group-platform
// queries within the supplied serial batch cache. Unsupported modes or absent
// caches query independently, and a last uncached consumer reads directly
// rather than retaining data for nobody.
//
// Successful records, including an empty result, are retained only when
// another consumer remains. Query errors are not cached, allowing a later
// same-key task to retry; a later publication failure does not invalidate a
// successful source result. Provider objects stay stable while multiple bucket
// encodings consume them.
func (s *SnapshotService) loadProvidersForRebuild(
	ctx context.Context,
	bucket SchedulerBucket,
	queries *schedulerProviderQueryCache,
) ([]SnapshotProvider, error) {
	key, cacheable := schedulerProviderQueryKeyForBucket(bucket)
	if queries == nil || !cacheable {
		return s.loadProvidersFromDB(ctx, bucket, bucket.Mode == SchedulerModeMixed)
	}

	// A successful empty query is still reusable, and publication failures do not
	// invalidate its provider objects. Map presence distinguishes that result
	// from a query never completed.
	if providers, ok := queries.providers[key]; ok {
		return providers, nil
	}
	if queries.remaining[key] <= 1 {
		return s.loadProvidersFromDB(ctx, bucket, false)
	}
	providers, err := s.loadProvidersFromDB(ctx, bucket, false)
	if err != nil {
		return nil, err
	}
	// Retain only successful source results for another consumer. A query error
	// leaves the key absent so a following equivalent bucket can retry it.
	queries.providers[key] = providers
	return providers, nil
}

// bucketFor builds a bucket using normalized group identity and supplied
// platform and mode. Missing or nonpositive group IDs become zero; platform
// and mode are retained exactly as provided.
func (s *SnapshotService) bucketFor(groupID *int64, platform string, mode string) SchedulerBucket {
	return SchedulerBucket{
		GroupID:  s.normalizeGroupID(groupID),
		Platform: platform,
		Mode:     mode,
	}
}

// normalizeGroupID returns a positive pointed-to group ID or zero for
// missing/nonpositive input. Bucket construction uses zero as its canonical
// empty-group identity, whose provider query returns no records.
func (s *SnapshotService) normalizeGroupID(groupID *int64) int64 {
	if groupID == nil || *groupID <= 0 {
		return 0
	}
	return *groupID
}

// normalizeGroupIDs returns positive unique memberships in input order. Empty
// input or input containing no positive IDs returns an allocated group-zero
// list so cache reconciliation can publish canonical empty-group buckets.
func (s *SnapshotService) normalizeGroupIDs(groupIDs []int64) []int64 {
	if len(groupIDs) == 0 {
		return []int64{0}
	}
	seen := make(map[int64]struct{}, len(groupIDs))
	out := make([]int64, 0, len(groupIDs))
	for _, id := range groupIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return []int64{0}
	}
	return out
}

// resolveMode chooses forced mode when the request marks a forced platform,
// otherwise single mode. The platform value does not affect this decision, and
// mixed mode is not selected here.
func (s *SnapshotService) resolveMode(platform string, hasForcePlatform bool) string {
	if hasForcePlatform {
		return SchedulerModeForced
	}
	return SchedulerModeSingle
}

// guardFallback admits recovery when configuration is absent or
// DbFallbackEnabled, subject to the constructor's local limiter. Disabled
// fallback returns cache-not-ready; denied window admission returns
// fallback-limited. ctx is not checked here, and admitted operations consume
// the window before database work.
func (s *SnapshotService) guardFallback(ctx context.Context) error {
	if s.cfg == nil || s.cfg.DbFallbackEnabled {
		if s.fallbackLimit == nil || s.fallbackLimit.Allow() {
			return nil
		}
		return ErrSchedulerFallbackLimited
	}
	return ErrSchedulerCacheNotReady
}

// withFallbackTimeout derives a database recovery context and its caller-owned
// cancellation function. Nonpositive or absent timeout adds cancellation
// cleanup without a new deadline. A positive configured budget is clipped to
// an earlier caller deadline; expired callers retain their parent's
// cancellation state.
func (s *SnapshotService) withFallbackTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.cfg == nil || s.cfg.DbFallbackTimeoutSeconds <= 0 {
		return context.WithCancel(ctx)
	}
	// Convert configured recovery seconds before clipping to the caller's earlier
	// deadline.
	timeout := time.Duration(s.cfg.DbFallbackTimeoutSeconds) * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.WithCancel(ctx)
		}
		// The recovery context inherits the caller's earlier deadline. Clip its
		// added timeout to that remaining interval; source implementations still
		// need to observe cancellation during their work.
		if remaining < timeout {
			timeout = remaining
		}
	}
	return context.WithTimeout(ctx, timeout)
}

// outboxPollInterval returns configured positive polling seconds as a
// duration. Absent configuration or nonpositive values choose one second,
// while Start separately enables the first outbox poll immediately.
func (s *SnapshotService) outboxPollInterval() time.Duration {
	if s.cfg == nil {
		return time.Second
	}
	sec := s.cfg.OutboxPollIntervalSeconds
	if sec <= 0 {
		return time.Second
	}
	// Scheduling configuration is stored in whole seconds, then converted to
	// duration.
	return time.Duration(sec) * time.Second
}

// fullRebuildInterval returns configured positive periodic rebuilding seconds
// as a duration. Missing configuration or nonpositive values disable the
// periodic task, while startup and durable-event reconciliation remain
// separate paths.
func (s *SnapshotService) fullRebuildInterval() time.Duration {
	if s.cfg == nil {
		return 0
	}
	sec := s.cfg.FullRebuildIntervalSeconds
	if sec <= 0 {
		return 0
	}
	// Scheduling configuration is stored in whole seconds, then converted to
	// duration.
	return time.Duration(sec) * time.Second
}

// dedupeBuckets returns an allocated slice of first-seen bucket String
// identities in input order. Its key uses the adapter-visible serialized
// bucket identity, so callers supply platform and mode values with their
// intended encoding.
func dedupeBuckets(in []SchedulerBucket) []SchedulerBucket {
	seen := make(map[string]struct{}, len(in))
	out := make([]SchedulerBucket, 0, len(in))
	for _, bucket := range in {
		key := bucket.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, bucket)
	}
	return out
}

// derefProviders creates a nonnil result slice containing entries whose
// provider interface is nonnil, in input order. Provider objects remain
// borrowed; callers keep their nested data stable while consumers use the
// result.
func derefProviders(providers []SnapshotProvider) []SnapshotProvider {
	if len(providers) == 0 {
		return []SnapshotProvider{}
	}
	out := make([]SnapshotProvider, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		out = append(out, provider)
	}
	return out
}

// parseInt64Slice accepts only []any payload shape and returns supported
// positive numeric values in order. Other shapes return nil; duplicates remain
// for consumer-specific deduplication, and floating-point elements follow
// toInt64's truncating conversion.
func parseInt64Slice(value any) []int64 {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(raw))
	for _, item := range raw {
		if v, ok := toInt64(item); ok && v > 0 {
			out = append(out, v)
		}
	}
	return out
}

// toInt64 accepts float64, int64, int, or decimal integer json.Number input.
// Representable floating values truncate their fractions; nonfinite or
// out-of-range floating values still report success with an unchecked
// conversion result. json.Number parsing rejects fractional or out-of-range
// text, and unsupported types return zero and false.
func toInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		// This conversion accepts floating payloads without range or finite checks.
		// Representable fractions truncate; the later positive-ID/seconds filters
		// decide whether the unchecked result is retained.
		return int64(v), true
	case int64:
		return v, true
	case int:
		// Native integer payloads widen to the signed ID representation.
		return int64(v), true
	case json.Number:
		parsed, err := strconv.ParseInt(v.String(), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// newFallbackLimiter creates a per-instance one-second admission window for
// positive QPS limits. Nonpositive values return nil, which Allow treats as
// unlimited; the initial window starts at construction time.
func newFallbackLimiter(maxQPS int) *fallbackLimiter {
	if maxQPS <= 0 {
		return nil
	}
	return &fallbackLimiter{
		maxQPS: maxQPS,
		window: time.Now(),
	}
}

// Allow admits an operation under one local one-second window, or every
// operation for a nil/nonpositive limiter. Window start and count change under
// the same mutex, so concurrent admissions cannot independently reset or
// exceed that stored count.
//
// Elapsed windows reset at the next call's time, producing fixed
// admission intervals rather than a sliding request history. An admitted
// fallback consumes count before its later database result is known. Calls
// inside a full current window return false; the limit is local to this
// service instance and does not coordinate other processes.
func (l *fallbackLimiter) Allow() bool {
	if l == nil || l.maxQPS <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	// Advance the fixed window at the next admission call, resetting its count
	// under the same mutex so concurrent callers cannot each claim a separate
	// reset.
	if now.Sub(l.window) >= time.Second {
		l.window = now
		l.count = 0
	}
	if l.count >= l.maxQPS {
		return false
	}
	l.count++
	return true
}

// diagnostics returns the snapshot service's bound operation logging
// callbacks.
func (s *SnapshotService) diagnostics() Diagnostics { return s.bindings.Diagnostics }

// providerNotFound resolves the configured missing-provider cause or the
// scheduler's default sentinel. Provider event handling uses errors.Is against
// this result before deleting a cached provider whose source record
// disappeared.
func (s *SnapshotService) providerNotFound() error {
	if s.bindings.ProviderNotFound != nil {
		return s.bindings.ProviderNotFound
	}
	return ErrSnapshotProviderNotFound
}

// groupNotFound resolves the configured missing-group cause or the scheduler's
// default sentinel. Lifecycle preparation uses errors.Is against it to treat a
// missing authoritative group as inactive for bucket retirement.
func (s *SnapshotService) groupNotFound() error {
	if s.bindings.GroupNotFound != nil {
		return s.bindings.GroupNotFound
	}
	return ErrSnapshotGroupNotFound
}

// isContextDoneError recognizes wrapped context.Canceled or a canceled/expired
// supplied caller context. A standalone context.DeadlineExceeded with a
// still-live caller is not recognized here, allowing it to follow the
// recoverable cache-error path.
func isContextDoneError(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if ctx == nil {
		return false
	}
	ctxErr := ctx.Err()
	// A cache or database adapter can report its own error after the caller
	// ended. The caller context establishes cancellation even when the returned
	// cause is adapter-specific.
	return errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded)
}

// contextDoneError returns the supplied context's current error when present,
// then err, then context.Canceled. It accepts nil context and error so
// recognized cache cancellation can always return a concrete stopping cause.
func contextDoneError(ctx context.Context, err error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	if err != nil {
		return err
	}
	return context.Canceled
}
