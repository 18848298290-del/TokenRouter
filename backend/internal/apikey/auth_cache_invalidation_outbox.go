package apikey

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// auth_cache_invalidation_outbox.go delivers durable authentication-cache
// invalidations through leased records and a delayed safety pass. Each record
// first deletes shared state and publishes its hash, then waits for a second
// pass that can remove a stale in-flight cache refill. Stage completion
// requires the current claim owner, so successful Redis work can be repeated
// when acknowledgement fails or a lease is reclaimed.
//
// The worker processes a bounded claimed batch concurrently and waits for
// started record handlers before returning. Redis failures are retried with
// capped jitter under a separate storage timeout; stopping cancels the active
// batch while persisted retry or unacknowledged records remain available to
// another claim. Health combines independent worker counters with a repository
// sample, and its displayed SLA durations describe configured timing estimates
// rather than a bound under backlog or unavailable dependencies.

// Delivery limits bound claimed batches, concurrent handlers, storage leases
// and Redis budgets; the safety delay separates initial and refill-cleanup passes.
const (
	KeyAuthInvalidationBatchSize    = 100
	KeyAuthInvalidationPollInterval = 500 * time.Millisecond
	KeyAuthInvalidationLease        = 30 * time.Second
	KeyAuthInvalidationRedisTimeout = 2 * time.Second
	KeyAuthInvalidationSafetyDelay  = 30 * time.Second
	KeyAuthInvalidationConcurrency  = 16
)

// AuthCacheInvalidationEvent carries a claimed cache identity and its persisted
// delivery progress.
type AuthCacheInvalidationEvent struct {
	ID       int64  // persisted invalidation row identifier
	CacheKey string // hashed credential cache identity
	Attempts int    // previous persisted retry count
	// Stage zero schedules a second delivery after success; every nonzero
	// stage is acknowledged by deletion after delivery. PostgreSQL restricts saved stages to zero or one.
	Stage     int       // persisted delivery progress stage
	CreatedAt time.Time // persisted event creation timestamp
}

// AuthCacheInvalidationOutboxStats summarizes retained invalidation records and
// their retry observations.
type AuthCacheInvalidationOutboxStats struct {
	Pending         int64      // retained invalidation record count
	OldestCreatedAt *time.Time // oldest retained creation timestamp
	MaxAttempts     int        // largest persisted retry count
	LastError       string     // latest available stored error
}

// AuthCacheInvalidationOutboxRepository claims leased invalidations and
// persists owner-checked delivery transitions.
type AuthCacheInvalidationOutboxRepository interface {
	// Claim takes available records whose leases can be acquired and returns their
	// current delivery fields. Callers provide a unique worker identity; expired
	// records can be reclaimed, and handlers complete transitions using that same
	// identity.
	Claim(ctx context.Context, workerID string, limit int, lease time.Duration) ([]AuthCacheInvalidationEvent, error)
	// DeleteClaimed acknowledges the row owned by workerID after the required
	// delivery passes. A missing or differently owned record reports failure, so
	// callers retain uncertainty and permit lease-based recovery.
	DeleteClaimed(ctx context.Context, id int64, workerID string) error
	// ScheduleSecondPass changes an owner-matched initial event to the delayed
	// safety stage and releases its claim. Wrong ownership or stage reports
	// failure; callers use the supplied availableAt for later reclamation.
	ScheduleSecondPass(ctx context.Context, id int64, workerID string, availableAt time.Time) error
	// RetryClaimed increments attempts, records failure text and releases the
	// owner-matched record for the supplied retry time. Wrong ownership reports
	// failure so another worker's claim cannot be replaced.
	RetryClaimed(ctx context.Context, id int64, workerID string, availableAt time.Time, lastError string) error
	// Stats returns retained row count, oldest creation time and retry
	// observations for health. It includes claimed and delayed safety records;
	// concurrent worker transitions can change the values after this query.
	Stats(ctx context.Context) (AuthCacheInvalidationOutboxStats, error)
}

// AuthCacheInvalidationHealth combines worker activity with separately read
// durable queue observations.
type AuthCacheInvalidationHealth struct {
	Running   bool   `json:"running"`   // observed loop activity flag
	Processed uint64 `json:"processed"` // successful delivery pass count
	Failures  uint64 `json:"failures"`  // observed processing failure count
	Pending   int64  `json:"pending"`   // retained outbox row count
	// OldestLag measures age using the worker clock. Missing or future oldest
	// timestamps produce zero, keeping displayed queue delay nonnegative.
	OldestLag  time.Duration `json:"oldest_lag"`            // age of oldest retained row
	LastError  string        `json:"last_error,omitempty"`  // worker or stored error observation
	StatsError string        `json:"stats_error,omitempty"` // failed repository observation message
	// HealthySLA and RecoverySLA are configured timing estimates for display. They
	// do not bound queue delay or processing when records backlog, claims expire
	// or dependencies remain unavailable.
	HealthySLA  time.Duration `json:"healthy_sla"`  // configured normal timing estimate
	RecoverySLA time.Duration `json:"recovery_sla"` // configured recovery timing estimate
	MaxAttempts int           `json:"max_attempts"` // largest persisted retry count
}

// OpsAuthCacheInvalidationHealth groups delivery, subscriber, lookup and
// invalid-client tracking observations.
type OpsAuthCacheInvalidationHealth struct {
	Outbox       AuthCacheInvalidationHealth           `json:"outbox"`        // durable delivery worker observations
	Subscriber   AuthCacheInvalidationSubscriberHealth `json:"subscriber"`    // local invalidation subscription observations
	Lookup       APIKeyAuthLookupMetrics               `json:"lookup"`        // direct authentication load observations
	InvalidAbuse InvalidAuthAbuseHealth                `json:"invalid_abuse"` // bounded invalid-client tracking observations
}

// AuthCacheInvalidationWorker claims durable invalidations and runs bounded
// concurrent delivery handlers.
type AuthCacheInvalidationWorker struct {
	// repo, cache and local are retained dependencies. Assembly keeps them usable
	// until joined handler work finishes; the worker cancels its own context when
	// stopped.
	repo     AuthCacheInvalidationOutboxRepository // durable invalidation claim storage
	cache    APIKeyCache                           // shared authentication cache adapter
	local    *APIKeyService                        // optional local cache and clock
	workerID string                                // stable claim ownership identity
	// ctx, cancel, wg, start, stop, lifecycleMu, stopping and stopped coordinate
	// one worker lifetime. Startup registers the loop under lifecycleMu; stopping
	// excludes later registration before the monitor waits.
	ctx         context.Context    // shared worker cancellation context
	cancel      context.CancelFunc // worker-owned context cancellation function
	wg          sync.WaitGroup     // registered delivery loop lifetime
	start       sync.Once          // single worker startup guard
	stop        sync.Once          // single worker shutdown guard
	lifecycleMu sync.Mutex         // startup shutdown registration lock
	stopping    bool               // permanent worker stop flag
	stopped     chan struct{}      // shared shutdown completion signal
	// running becomes true during registration before the loop launches.
	// KeyRun and the joined stop monitor clear it when that lifetime ends.
	running   atomic.Bool   // observed loop activity state
	processed atomic.Uint64 // successful delivery pass counter
	failures  atomic.Uint64 // observed delivery failure counter
	lastError atomic.Value  // latest replaceable error text
}

// NewAuthCacheInvalidationWorker constructs an unstarted delivery worker with
// its own cancellation context and claim identity. The first optional local
// service supplies local invalidation and clock reads; additional values are
// ignored, and callers stop the worker to release that context.
func NewAuthCacheInvalidationWorker(repo AuthCacheInvalidationOutboxRepository, cache APIKeyCache, local ...*APIKeyService) *AuthCacheInvalidationWorker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &AuthCacheInvalidationWorker{
		repo: repo, cache: cache, workerID: uuid.NewString(), ctx: ctx, cancel: cancel,
	}
	if len(local) > 0 {
		w.local = local[0]
	}
	w.lastError.Store("")
	return w
}

// Start registers and launches the delivery loop once when repository and cache
// dependencies exist. Nil workers or missing dependencies do nothing.
//
// Registration occurs under lifecycleMu so StopContext can mark the worker
// stopping before starting its wait. A stopped worker cannot restart, and
// repeated starts retain the same claim identity and cancellation context.
// Running becomes true before the loop launches; KeyRun clears that observation
// when its registered work eventually exits.
func (w *AuthCacheInvalidationWorker) Start() {
	if w == nil || w.repo == nil || w.cache == nil {
		return
	}
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if w.stopping {
		return
	}
	w.start.Do(func() {
		w.running.Store(true)
		w.wg.Add(1)
		// Register before unlocking lifecycleMu so shutdown cannot wait on an
		// empty count while this delivery loop is still about to start.
		go w.KeyRun()
	})
}

// StopContext prevents restart, cancels active batch work and waits for started
// handlers to finish. Nil workers return successfully; callers supply a nonnil
// wait context.
//
// The first stop marks stopping under lifecycleMu and launches a monitor after
// startup registration is excluded. The monitor joins the loop and signals
// completion shared by later stop callers. A canceled wait returns ctx.Err
// while teardown continues; repository cooperation still determines when claims
// and handlers finish, and persisted unfinished events remain recoverable
// through their lease or retry state.
func (w *AuthCacheInvalidationWorker) StopContext(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.lifecycleMu.Lock()
	w.stop.Do(func() {
		w.stopping = true
		w.stopped = make(chan struct{})
		if w.cancel != nil {
			w.cancel()
		}
		// Existing work receives cancellation, and the monitor outlives a
		// timed-out waiting caller until all started handlers and the loop finish.
		go func() { w.wg.Wait(); w.running.Store(false); close(w.stopped) }()
	})
	done := w.stopped
	w.lifecycleMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop cancels delivery and waits for worker teardown using a background
// context. Blocked dependencies can keep the call waiting; callers needing a
// bounded wait use StopContext with their own deadline.
func (w *AuthCacheInvalidationWorker) Stop() { _ = w.StopContext(context.Background()) }

// KeyRun processes a batch immediately, then polls again until the worker
// context is canceled. Start registers its wait-group ownership before
// launching it; claim failures are recorded when cancellation has not already
// ended the loop.
func (w *AuthCacheInvalidationWorker) KeyRun() {
	defer w.wg.Done()
	defer w.running.Store(false)
	ticker := time.NewTicker(KeyAuthInvalidationPollInterval)
	defer ticker.Stop()
	// Stopping cancels current claims, so record loop-level errors while its
	// context is running; per-event errors already update their own recovery
	// state.
	for {
		if err := w.KeyProcessBatch(w.ctx); err != nil && w.ctx.Err() == nil {
			w.KeyRecordFailure(err)
		}
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// KeyProcessBatch claims a bounded leased batch and joins each started event
// handler before returning. Claim failures and selected cancellation return an
// error; per-event delivery failures are recorded through health and retry
// state instead.
//
// A semaphore limits concurrent handlers while cancellation can stop admission
// of later claimed records. Already started handlers retain their record copies
// and finish their recovery or acknowledgement attempts before the batch
// returns. Unstarted claimed records keep their lease until reclamation; this
// method supplies no renewal of that batch claim lease.
func (w *AuthCacheInvalidationWorker) KeyProcessBatch(ctx context.Context) error {
	events, err := w.repo.Claim(ctx, w.workerID, KeyAuthInvalidationBatchSize, KeyAuthInvalidationLease)
	if err != nil {
		return fmt.Errorf("claim auth cache invalidations: %w", err)
	}
	// Limit started handlers before registering each goroutine; joined
	// completion retains claims that have already begun recovery or acknowledgement.
	semaphore := make(chan struct{}, KeyAuthInvalidationConcurrency)
	var wg sync.WaitGroup
	for i := range events {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case semaphore <- struct{}{}:
		}
		wg.Add(1)
		go func(event AuthCacheInvalidationEvent) {
			defer wg.Done()
			defer func() { <-semaphore }()
			w.KeyProcessEvent(ctx, event)
		}(events[i])
	}
	wg.Wait()
	return nil
}

// KeyProcessEvent attempts local invalidation, shared deletion and publication
// for one claimed event. Shared publication follows a successful deletion,
// while either remote failure records a bounded retry under an independent
// storage context.
//
// Stage zero schedules a delayed second pass after successful Redis delivery;
// later stages delete the owner-matched event. Each transition has its own
// timeout after the Redis context ends, allowing cancellation to retain a
// durable next action. Failed transitions keep the record for lease recovery.
// Processed counts successful passes, including safety scheduling, and
// concurrent successes can clear the observed last worker error.
func (w *AuthCacheInvalidationWorker) KeyProcessEvent(parent context.Context, event AuthCacheInvalidationEvent) {
	// Attempt local clearing first, then delete shared state before submitting
	// publication. Both remote operations share one Redis budget, and a failed
	// delete selects durable retry handling.
	if w.local != nil {
		w.local.KeyInvalidateLocalAuthCache(event.CacheKey)
	}
	ctx, cancel := context.WithTimeout(parent, KeyAuthInvalidationRedisTimeout)
	err := w.cache.DeleteAuthCache(ctx, event.CacheKey)
	if err == nil {
		err = w.cache.PublishAuthCacheInvalidation(ctx, event.CacheKey)
	}
	cancel()
	if err != nil {
		w.KeyRecordFailure(err)
		retryAt := w.now().UTC().Add(KeyAuthInvalidationRetryDelay(event.Attempts + 1))
		// Persist a retry after cancellation of the delivery budget so its
		// durable record can become available even when this worker is stopping.
		retryCtx, retryCancel := context.WithTimeout(context.Background(), 2*time.Second)
		retryErr := w.repo.RetryClaimed(retryCtx, event.ID, w.workerID, retryAt, KeyBoundedAuthInvalidationError(err))
		retryCancel()
		if retryErr != nil {
			w.KeyRecordFailure(fmt.Errorf("release failed auth invalidation %d: %w", event.ID, retryErr))
		}
		return
	}
	// A load that started before invalidation can refill stale shared state.
	// Delay a second deletion pass before acknowledging this initial delivery record.
	if event.Stage == 0 {
		nextCtx, nextCancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = w.repo.ScheduleSecondPass(nextCtx, event.ID, w.workerID, w.now().UTC().Add(KeyAuthInvalidationSafetyDelay))
		nextCancel()
		if err != nil {
			w.KeyRecordFailure(fmt.Errorf("schedule second auth invalidation pass %d: %w", event.ID, err))
			return
		}
		w.processed.Add(1)
		w.lastError.Store("")
		return
	}

	// Redis delivery has completed; acknowledge under a separate budget so
	// the stopped delivery context cannot automatically strand every successful record.
	ackCtx, ackCancel := context.WithTimeout(context.Background(), 2*time.Second)
	err = w.repo.DeleteClaimed(ackCtx, event.ID, w.workerID)
	ackCancel()
	if err != nil {
		w.KeyRecordFailure(fmt.Errorf("ack auth invalidation %d: %w", event.ID, err))
		return
	}
	w.processed.Add(1)
	w.lastError.Store("")
}

// KeyAuthInvalidationRetryDelay returns exponential retry delay with a sampled
// 20-percent variation. Attempts below one select the first step and those
// above nine select the last step, keeping the base delay capped at 256
// seconds.
func KeyAuthInvalidationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// Cap the exponential step before sampling jitter so repeated delivery
	// failures retain a bounded base delay while their retry times still vary.
	if attempt > 9 {
		attempt = 9
	}
	base := time.Second * time.Duration(1<<(attempt-1))
	return time.Duration(float64(base) * (0.8 + rand.Float64()*0.4))
}

// KeyBoundedAuthInvalidationError returns err text capped at 1024 bytes, or
// empty text for nil. Truncation follows bytes and can split a Unicode
// encoding; callers use this bounded result for stored and displayed error
// observations.
func KeyBoundedAuthInvalidationError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	// Retain the leading error bytes when a longer observation exceeds storage budget.
	if len(message) > 1024 {
		return message[:1024]
	}
	return message
}

// KeyRecordFailure increments the worker failure count, stores bounded error
// text and emits a warning. Nil errors do nothing; concurrent recording and
// successful delivery can replace or clear the last-error observation
// independently.
func (w *AuthCacheInvalidationWorker) KeyRecordFailure(err error) {
	if err == nil {
		return
	}
	w.failures.Add(1)
	w.lastError.Store(KeyBoundedAuthInvalidationError(err))
	slog.Warn("auth cache invalidation outbox processing failed", "error", err)
}

// Health returns configured timing estimates and separately sampled worker and
// outbox state. Nil workers return the estimates alone, and a failed stats
// query is recorded in StatsError beside any worker observations already
// sampled.
//
// The worker last-error value takes precedence when present; otherwise the
// repository error sample is used. OldestLag is clamped to zero when its
// creation time is ahead of the worker clock. Processed counts passes, and the
// SLA values do not incorporate current backlog or guarantee delivery during
// dependency failure.
func (w *AuthCacheInvalidationWorker) Health(ctx context.Context) AuthCacheInvalidationHealth {
	// HealthySLA adds a display margin to the safety delay; RecoverySLA is a fixed
	// six-minute estimate, independent of sampled backlog.
	KeyHealth := AuthCacheInvalidationHealth{
		HealthySLA:  KeyAuthInvalidationSafetyDelay + 5*time.Second,
		RecoverySLA: 6 * time.Minute,
	}
	if w == nil {
		return KeyHealth
	}
	KeyHealth.Running = w.running.Load()
	KeyHealth.Processed = w.processed.Load()
	KeyHealth.Failures = w.failures.Load()
	if value := w.lastError.Load(); value != nil {
		KeyHealth.LastError, _ = value.(string)
	}
	if w.repo == nil {
		return KeyHealth
	}
	// Keep sampled worker activity when the repository stats read fails, returning
	// that failure as a separate health field for observers.
	stats, err := w.repo.Stats(ctx)
	if err != nil {
		KeyHealth.StatsError = KeyBoundedAuthInvalidationError(err)
		return KeyHealth
	}
	KeyHealth.Pending = stats.Pending
	KeyHealth.MaxAttempts = stats.MaxAttempts
	if KeyHealth.LastError == "" {
		KeyHealth.LastError = stats.LastError
	}
	if stats.OldestCreatedAt != nil {
		KeyHealth.OldestLag = max(w.now().Sub(*stats.OldestCreatedAt), 0)
	}
	return KeyHealth
}

// ProvideAuthCacheInvalidationWorker returns an unstarted worker with the
// constructor's cancellation context and claim identity. Application assembly
// retains dependency ownership and invokes Start and Stop to coordinate the
// worker's processing lifetime.
func ProvideAuthCacheInvalidationWorker(repo AuthCacheInvalidationOutboxRepository, cache APIKeyCache, apiKeyService *APIKeyService) *AuthCacheInvalidationWorker {
	worker := NewAuthCacheInvalidationWorker(repo, cache, apiKeyService)

	return worker
}
