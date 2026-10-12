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

const (
	outboxEventTimeout                    = 2 * time.Minute
	schedulerOutboxCleanupBatch           = 5000
	schedulerGroupLifecycleTimeout        = 30 * time.Second
	schedulerGroupLifecycleLeaseTTL       = 60 * time.Second
	schedulerGroupLifecycleReleaseTimeout = 2 * time.Second
	outboxRebuildRetryBaseDelay           = 5 * time.Second
	outboxRebuildRetryMaxDelay            = 5 * time.Minute
	outboxMaxIDErrorLogSampleInterval     = time.Minute
)

var (
	ErrSchedulerCacheNotReady           = errors.New("scheduler cache not ready")
	ErrSchedulerFallbackLimited         = errors.New("scheduler db fallback limited")
	ErrSchedulerGroupLifecycleLeaseBusy = errors.New("scheduler group lifecycle lease busy")
	ErrSchedulerBucketRebuildBusy       = errors.New("scheduler bucket rebuild busy")

	ErrSnapshotProviderNotFound = errors.New("scheduler provider not found")
	ErrSnapshotGroupNotFound    = errors.New("scheduler group not found")
)

// batchSeenKey identifies platform rebuilds and completed group lifecycle work within one pollOutbox
// call.
type batchSeenKey struct {
	groupID   int64
	platform  string
	lifecycle bool
}

type schedulerBucketWriteTask struct {
	bucket SchedulerBucket
	token  SchedulerBucketWriteToken
}

type schedulerProviderQueryKey struct {
	groupID  int64
	platform string
}

// schedulerProviderQueryCache reuses successful single/forced queries by group and platform within a
// batch.
// Other modes query separately. Each task decrements remaining on exit; the last consumer releases
// cached slices.
type schedulerProviderQueryCache struct {
	remaining           map[schedulerProviderQueryKey]int
	providers           map[schedulerProviderQueryKey][]SnapshotProvider
	snapshotProviderIDs map[schedulerProviderQueryKey][]int64
}

// schedulerSnapshotProviderIDWriter lets a batch reuse successfully encoded provider IDs.
// Later buckets with the same query publish membership from those IDs.
// Caches without this interface publish complete snapshots through SetSnapshot.
type schedulerSnapshotProviderIDWriter interface {
	SetSnapshotAndReturnProviderIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providers []SnapshotProvider) ([]int64, error)
	SetSnapshotByProviderIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providerIDs []int64) error
}

type schedulerGroupLifecyclePlan struct {
	active bool
	tasks  []schedulerBucketWriteTask
}

type schedulerActiveGroupIDLister interface {
	ListActiveIDs(ctx context.Context) ([]int64, error)
}

type SnapshotService struct {
	bindings                     SnapshotBindings
	cache                        SnapshotCache
	outboxRepo                   SchedulerOutboxRepository
	providerRepo                 SnapshotProviderSource
	groupRepo                    SnapshotGroupSource
	cfg                          *SnapshotOptions
	runtime                      WorkerRuntime
	fallbackLimit                *fallbackLimiter
	lagMu                        sync.Mutex
	lagFailures                  int
	outboxRebuildLatched         bool
	outboxRebuildRunning         bool
	outboxRebuildFailures        int
	outboxRebuildRetryAt         time.Time
	outboxRebuildRetryReason     string
	outboxLagWarningActive       bool
	outboxMaxIDErrorLastLoggedAt time.Time

	fullRebuildGateOnce  sync.Once
	fullRebuildGate      chan struct{}
	fullRebuildStateMu   sync.Mutex
	fullRebuildRequested uint64
	fullRebuildCompleted uint64
	fullRebuildLastErr   error
}

type fallbackLimiter struct {
	maxQPS int
	mu     sync.Mutex
	window time.Time
	count  int
}

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

func schedulerProviderQueryKeyForBucket(bucket SchedulerBucket) (schedulerProviderQueryKey, bool) {
	if bucket.Mode != SchedulerModeSingle && bucket.Mode != SchedulerModeForced {
		return schedulerProviderQueryKey{}, false
	}
	return schedulerProviderQueryKey{groupID: bucket.GroupID, platform: bucket.Platform}, true
}

func (c *schedulerProviderQueryCache) release(bucket SchedulerBucket) {
	if c == nil {
		return
	}
	key, ok := schedulerProviderQueryKeyForBucket(bucket)
	if !ok {
		return
	}
	remaining := c.remaining[key] - 1
	if remaining <= 0 {
		delete(c.remaining, key)
		delete(c.providers, key)
		delete(c.snapshotProviderIDs, key)
		return
	}
	c.remaining[key] = remaining
}

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

// Start launches the initial rebuild asynchronously and polls outbox immediately; repeated calls have
// no effect.
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

// Stop allows 30 seconds for shutdown. Use StopContext to supply the application's remaining budget.
func (s *SnapshotService) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.StopContext(ctx)
}

func (s *SnapshotService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.runtime.StopContext(ctx)
}

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
		cached, hit, err := s.cache.GetSnapshot(ctx, bucket)
		if err != nil {
			if isContextDoneError(ctx, err) {
				return nil, useMixed, contextDoneError(ctx, err)
			}
			s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] cache read failed: bucket=%s err=%v", bucket.String(), err)
		} else if hit {
			return derefProviders(cached), useMixed, nil
		}
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

func (s *SnapshotService) GetProvider(ctx context.Context, providerID int64) (SnapshotProvider, error) {
	if providerID <= 0 {
		return nil, nil
	}
	if s.cache != nil {
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

// UpdateProviderInCache publishes a provider immediately, such as after a model rate-limit update.
func (s *SnapshotService) UpdateProviderInCache(ctx context.Context, provider SnapshotProvider) error {
	if s.cache == nil || provider == nil {
		return nil
	}
	return s.cache.SetProvider(ctx, provider)
}

func (s *SnapshotService) runInitialRebuild() {
	if s.cache == nil {
		return
	}
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

func (s *SnapshotService) pollOutbox() {
	if s.outboxRepo == nil || s.cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.runtime.Context(), 10*time.Second)
	defer cancel()

	watermark, err := s.cache.GetOutboxWatermark(ctx)
	if err != nil {
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox watermark read failed: %v", err)
		return
	}

	events, err := s.outboxRepo.ListAfterAndReleaseDedup(ctx, watermark, 200)
	if err != nil {
		s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox poll failed: %v", err)
		return
	}
	if len(events) == 0 {
		// An empty query reports no visible events after the watermark, so clear degradation
		// and retry state based on this poll result.
		s.clearOutboxDegradedEpisode()
		return
	}

	seen := make(map[batchSeenKey]struct{})
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
	s.cleanupConsumedOutbox(lastID)

	// A batch is consumed after its watermark advances. Measure lag from the next pending event
	// so slow batch processing does not itself trigger costly full rebuilds.
	lagCtx, lagCancel := context.WithTimeout(s.runtime.Context(), 5*time.Second)
	s.checkOutboxLag(lagCtx, lastID)
	lagCancel()
}

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

	for {
		deleted, err := s.outboxRepo.DeleteConsumedUpTo(ctx, watermark, schedulerOutboxCleanupBatch)
		if err != nil {
			s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox cleanup failed: watermark=%d err=%v", watermark, err)
			return
		}
		if deleted == 0 || deleted < schedulerOutboxCleanupBatch {
			return
		}
	}
}

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

func (s *SnapshotService) handleLastUsedEvent(ctx context.Context, payload map[string]any) error {
	if s.cache == nil || payload == nil {
		return nil
	}
	raw, ok := payload["last_used"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	updates := make(map[int64]time.Time, len(raw))
	for key, value := range raw {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		sec, ok := toInt64(value)
		if !ok || sec <= 0 {
			continue
		}
		updates[id] = time.Unix(sec, 0)
	}
	if len(updates) == 0 {
		return nil
	}
	return s.cache.UpdateLastUsed(ctx, updates)
}

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

	// A missing provider's platform is unknown; rebuild every platform to clear stale membership.
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
		if !slices.Contains(capability.ProviderPlatforms(), provider.SnapshotMetadata().Platform) {
			return s.rebuildByGroupIDs(ctx, rebuildGroupIDs, "provider_bulk_change", seen)
		}
		providerGroupIDs := s.normalizeGroupIDs(provider.SnapshotMetadata().GroupIDs)
		addPlatformGroups("", providerGroupIDs)
		addPlatformGroups(provider.SnapshotMetadata().Platform, providerGroupIDs)
	}

	// The payload includes former groups; rebuild them for each platform affected by this event.
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

// prepareGroupLifecycle reads hydrated group state under a lease before reopening or retiring buckets.
// Active groups reopen canonical buckets; missing or inactive groups retire canonical and registered
// buckets.
// The caller marks a group event as seen after this decision and all required rebuilds succeed.
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

	releaseErr := s.releaseGroupLifecycleLease(lease)
	leaseHeld = false
	if releaseErr != nil {
		return schedulerGroupLifecyclePlan{}, releaseErr
	}
	return plan, nil
}

func (s *SnapshotService) releaseGroupLifecycleLease(lease SchedulerGroupLifecycleLease) error {
	// Use a bounded independent context to release the lease even after request cancellation.
	releaseCtx, cancel := context.WithTimeout(context.Background(), schedulerGroupLifecycleReleaseTimeout)
	defer cancel()
	return s.cache.ReleaseGroupLifecycleLease(releaseCtx, lease)
}

func markGroupLifecycleSeen(seen map[batchSeenKey]struct{}, groupID int64) {
	if seen == nil {
		return
	}
	seen[batchSeenKey{groupID: groupID, lifecycle: true}] = struct{}{}
	for _, platform := range schedulerSnapshotPlatforms() {
		seen[batchSeenKey{groupID: groupID, platform: platform}] = struct{}{}
	}
}

func (s *SnapshotService) rebuildByProvider(ctx context.Context, provider SnapshotProvider, groupIDs []int64, reason string, seen map[batchSeenKey]struct{}) error {
	if provider == nil {
		return nil
	}
	return s.rebuildByGroupIDs(ctx, groupIDs, reason, seen)
}

func schedulerSnapshotPlatforms() []string {
	return append([]string{""}, capability.ProviderPlatforms()...)
}

// schedulerBucketsForGroup returns canonical buckets for positive group IDs.
// Full rebuilds use schedulerCanonicalBuckets directly for group zero.
func schedulerBucketsForGroup(groupID int64) []SchedulerBucket {
	if groupID <= 0 {
		return nil
	}
	return schedulerCanonicalBuckets(groupID)
}

func schedulerCanonicalBuckets(groupID int64) []SchedulerBucket {
	buckets := make([]SchedulerBucket, 0, len(schedulerSnapshotPlatforms())*3)
	for _, platform := range schedulerSnapshotPlatforms() {
		buckets = append(buckets,
			SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeSingle},
			SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeForced},
		)
	}
	return buckets
}

func (s *SnapshotService) rebuildByGroupIDs(ctx context.Context, groupIDs []int64, reason string, seen map[batchSeenKey]struct{}) error {
	groupIDs = s.normalizeGroupIDs(groupIDs)
	if len(groupIDs) == 0 {
		return nil
	}
	buckets := make([]SchedulerBucket, 0, len(groupIDs)*len(schedulerSnapshotPlatforms())*3)
	for _, platform := range schedulerSnapshotPlatforms() {
		buckets = append(buckets, s.bucketsForPlatform(platform, groupIDs, seen)...)
	}
	return s.rebuildBuckets(ctx, buckets, reason)
}

func (s *SnapshotService) bucketsForPlatform(platform string, groupIDs []int64, seen map[batchSeenKey]struct{}) []SchedulerBucket {
	buckets := make([]SchedulerBucket, 0, len(groupIDs)*3)
	for _, gid := range groupIDs {
		// Schedule each group/platform pair once per poll batch. Its query loads all providers
		// for that pair, covering later events for the same pair in the batch.
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

func (s *SnapshotService) rebuildBuckets(ctx context.Context, buckets []SchedulerBucket, reason string) error {
	tasks, firstErr := s.prepareBucketWriteTasks(ctx, buckets)
	queries := newSchedulerProviderQueryCache(tasks)
	if err := s.rebuildPreparedBucketTasks(ctx, tasks, reason, false, queries); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *SnapshotService) prepareBucketWriteTasks(ctx context.Context, buckets []SchedulerBucket) ([]schedulerBucketWriteTask, error) {
	if s.cache == nil {
		return nil, ErrSchedulerCacheNotReady
	}
	tasks := make([]schedulerBucketWriteTask, 0, len(buckets))
	var firstErr error
	for _, bucket := range buckets {
		token, err := s.cache.CaptureBucketWriteToken(ctx, bucket)
		if err != nil {
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

func (s *SnapshotService) rebuildBucketWithTokenPolicyAndQueryCache(
	ctx context.Context,
	task schedulerBucketWriteTask,
	reason string,
	strict bool,
	queries *schedulerProviderQueryCache,
) error {
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
	if !ok {
		if strict {
			return fmt.Errorf("%w: bucket=%s", ErrSchedulerBucketRebuildBusy, bucket.String())
		}
		return nil
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
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
		// Retain the ordered IDs actually encoded and written. Deriving them from the input
		// would include unencodable providers in later buckets and produce inconsistent membership.
		// This batch owns the returned slice.
		queries.snapshotProviderIDs[key] = providerIDs
	}
	return nil
}

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

func (s *SnapshotService) listActiveSchedulerGroupIDs(ctx context.Context) ([]int64, error) {
	if s.groupRepo == nil {
		return nil, ErrSchedulerCacheNotReady
	}

	// Use ListActive when the lightweight interface is absent; propagate errors when it is implemented.
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

func (s *SnapshotService) prepareAndRebuildFullSnapshot(
	ctx context.Context,
	captured []schedulerBucketWriteTask,
	reopened []schedulerBucketWriteTask,
	ordinaryBuckets []SchedulerBucket,
	reason string,
) error {
	// Capture all ordinary bucket tokens before querying providers; abort if preparation fails.
	// Reopened tasks fail on busy locks or fenced tokens; captured tasks skip those buckets.
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

func (s *SnapshotService) coalesceFullRebuild(run func() error) error {
	s.fullRebuildStateMu.Lock()
	s.fullRebuildRequested++
	requestID := s.fullRebuildRequested
	s.fullRebuildStateMu.Unlock()

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
	// A running rebuild may predate a new outbox transaction's commit. Record covered request
	// generations before each run and coalesce requests arriving during it into the next run.
	coveredThrough := s.fullRebuildRequested
	s.fullRebuildStateMu.Unlock()

	err := run()

	s.fullRebuildStateMu.Lock()
	s.fullRebuildCompleted = coveredThrough
	s.fullRebuildLastErr = err
	s.fullRebuildStateMu.Unlock()
	return err
}

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
		maxID, maxErr := s.outboxRepo.MaxID(ctx)
		if maxErr != nil {
			backlogKnown = false
			if s.shouldLogOutboxMaxIDError(now) {
				s.diagnostics().printf("service.scheduler_snapshot", "[Scheduler] outbox max id read failed: %v", maxErr)
			}
		} else {
			backlog = maxID - watermark
		}
	}
	backlogDegraded := backlogKnown && backlogThreshold > 0 && backlog >= int64(backlogThreshold)

	// Latch a successful rebuild until recovery is observed. Failed rebuilds retry after
	// exponential backoff to limit repeated rebuild attempts during degradation.
	logLagWarning := s.shouldLogOutboxLagWarning(lagWarning)
	s.lagMu.Lock()
	fullyRecovered := !lagDegraded && backlogKnown && !backlogDegraded
	if fullyRecovered {
		s.lagFailures = 0
		s.outboxRebuildLatched = false
		s.outboxRebuildFailures = 0
		s.outboxRebuildRetryAt = time.Time{}
		s.outboxRebuildRetryReason = ""
	}

	if s.outboxRebuildRetryReason != "" {
		retryReasonActive := (s.outboxRebuildRetryReason == "outbox_lag" && lagDegraded) ||
			(s.outboxRebuildRetryReason == "outbox_backlog" && (!backlogKnown || backlogDegraded))
		if !retryReasonActive {
			s.outboxRebuildFailures = 0
			s.outboxRebuildRetryAt = time.Time{}
			s.outboxRebuildRetryReason = ""
		}
	}

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

func outboxRebuildRetryDelay(failures int) time.Duration {
	delay := outboxRebuildRetryBaseDelay
	for i := 1; i < failures && delay < outboxRebuildRetryMaxDelay; i++ {
		delay *= 2
		if delay >= outboxRebuildRetryMaxDelay {
			return outboxRebuildRetryMaxDelay
		}
	}
	return delay
}

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

func (s *SnapshotService) shouldLogOutboxMaxIDError(now time.Time) bool {
	s.lagMu.Lock()
	defer s.lagMu.Unlock()
	if !s.outboxMaxIDErrorLastLoggedAt.IsZero() && now.Sub(s.outboxMaxIDErrorLastLoggedAt) < outboxMaxIDErrorLogSampleInterval {
		return false
	}
	s.outboxMaxIDErrorLastLoggedAt = now
	return true
}

func (s *SnapshotService) shouldLogOutboxLagWarning(active bool) bool {
	s.lagMu.Lock()
	defer s.lagMu.Unlock()
	shouldLog := active && !s.outboxLagWarningActive
	s.outboxLagWarningActive = active
	return shouldLog
}

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

func (s *SnapshotService) loadProvidersForRebuild(
	ctx context.Context,
	bucket SchedulerBucket,
	queries *schedulerProviderQueryCache,
) ([]SnapshotProvider, error) {
	key, cacheable := schedulerProviderQueryKeyForBucket(bucket)
	if queries == nil || !cacheable {
		return s.loadProvidersFromDB(ctx, bucket, bucket.Mode == SchedulerModeMixed)
	}

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
	queries.providers[key] = providers
	return providers, nil
}

func (s *SnapshotService) bucketFor(groupID *int64, platform string, mode string) SchedulerBucket {
	return SchedulerBucket{
		GroupID:  s.normalizeGroupID(groupID),
		Platform: platform,
		Mode:     mode,
	}
}

func (s *SnapshotService) normalizeGroupID(groupID *int64) int64 {
	if groupID == nil || *groupID <= 0 {
		return 0
	}
	return *groupID
}

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

func (s *SnapshotService) resolveMode(platform string, hasForcePlatform bool) string {
	if hasForcePlatform {
		return SchedulerModeForced
	}
	return SchedulerModeSingle
}

func (s *SnapshotService) guardFallback(ctx context.Context) error {
	if s.cfg == nil || s.cfg.DbFallbackEnabled {
		if s.fallbackLimit == nil || s.fallbackLimit.Allow() {
			return nil
		}
		return ErrSchedulerFallbackLimited
	}
	return ErrSchedulerCacheNotReady
}

func (s *SnapshotService) withFallbackTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.cfg == nil || s.cfg.DbFallbackTimeoutSeconds <= 0 {
		return context.WithCancel(ctx)
	}
	timeout := time.Duration(s.cfg.DbFallbackTimeoutSeconds) * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.WithCancel(ctx)
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	return context.WithTimeout(ctx, timeout)
}

func (s *SnapshotService) outboxPollInterval() time.Duration {
	if s.cfg == nil {
		return time.Second
	}
	sec := s.cfg.OutboxPollIntervalSeconds
	if sec <= 0 {
		return time.Second
	}
	return time.Duration(sec) * time.Second
}

func (s *SnapshotService) fullRebuildInterval() time.Duration {
	if s.cfg == nil {
		return 0
	}
	sec := s.cfg.FullRebuildIntervalSeconds
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

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

func toInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	case json.Number:
		parsed, err := strconv.ParseInt(v.String(), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func newFallbackLimiter(maxQPS int) *fallbackLimiter {
	if maxQPS <= 0 {
		return nil
	}
	return &fallbackLimiter{
		maxQPS: maxQPS,
		window: time.Now(),
	}
}

func (l *fallbackLimiter) Allow() bool {
	if l == nil || l.maxQPS <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
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

func (s *SnapshotService) diagnostics() Diagnostics { return s.bindings.Diagnostics }
func (s *SnapshotService) providerNotFound() error {
	if s.bindings.ProviderNotFound != nil {
		return s.bindings.ProviderNotFound
	}
	return ErrSnapshotProviderNotFound
}

func (s *SnapshotService) groupNotFound() error {
	if s.bindings.GroupNotFound != nil {
		return s.bindings.GroupNotFound
	}
	return ErrSnapshotGroupNotFound
}

// isContextDoneError recognizes cancellation errors or a canceled or expired caller context.
func isContextDoneError(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if ctx == nil {
		return false
	}
	ctxErr := ctx.Err()
	// Check the context because database drivers may return their own cancellation errors.
	return errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded)
}

// contextDoneError prefers the context error, then err, and returns context.Canceled if both are nil.
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
