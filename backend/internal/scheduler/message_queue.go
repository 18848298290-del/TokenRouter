package scheduler

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

// Anthropic message serialization uses a provider lock and spacing measured
// from the last completed request. An acquired lock belongs to one request ID,
// while its completion timestamp comes from Redis so every instance measures
// the same remaining delay.
//
// The service admits requests when lock or clock storage is unavailable. Such
// admission may have no confirmed lock identity to release, so an empty result
// ID is meaningful. With storage available, RPM load chooses a bounded base
// delay and jitter varies that interval before the remaining spacing is
// measured. Lock acquisition, waiting observers, runtime registration, and
// cleanup are connected by the returned lease in message_queue_wait.go;
// constructor calls start no worker.

// UserMsgQueueCache supplies provider lock ownership, completion timing, and
// indexed lock maintenance.
type UserMsgQueueCache interface {
	// AcquireLock attempts the provider lock using requestID as candidate
	// owner and lockTtlMs as lifetime. A successful boolean confirms
	// ownership, while a busy response retains the other owner.
	//
	// The Redis adapter renews a matching existing owner or creates an
	// absent lock in one script. Its response also supplies observed
	// expiry for a separate cleanup-index update. Callers release
	// confirmed ownership after work; a transport error can leave remote
	// acquisition uncertain, and index maintenance failure does not
	// replace the primary lock outcome.
	AcquireLock(ctx context.Context, providerID int64, requestID string, lockTtlMs int) (acquired bool, err error)
	// ReleaseLock removes a lock matching requestID and records
	// Redis-clock completion for spacing. An absent or different owner
	// returns false and retains the current ownership state.
	//
	// The Redis adapter combines owner comparison, lock removal, and
	// completion timestamp storage in one script. Cleanup-index removal
	// follows separately and can race a successor index insertion;
	// contention can repair that missing entry. Callers retain the
	// distinction between the primary release outcome and best-effort
	// maintenance, while storage failures are returned.
	ReleaseLock(ctx context.Context, providerID int64, requestID string) (released bool, err error)
	// GetLastCompletedMs returns the provider completion time as Unix
	// milliseconds from Redis. Missing history returns zero, and parsing
	// or storage errors let the service choose whether to skip spacing.
	GetLastCompletedMs(ctx context.Context, providerID int64) (int64, error)
	// GetCurrentTimeMs reads Redis Unix milliseconds for comparing
	// completion and current time. It uses the same server clock as lock
	// release, and storage errors are returned to the spacing consumer.
	GetCurrentTimeMs(ctx context.Context) (int64, error)
	// ReconcileExpiredLockCandidates inspects indexed locks whose expected
	// expiry is due. maxCount bounds that index batch, with nonpositive
	// values selecting the adapter default.
	//
	// Actual PTTL is rechecked before treating a candidate as expired. A
	// renewed live lock receives a later index score, an absent lock loses
	// its stale member, and a persistent lock is deleted to release
	// serialization capacity. The returned count includes those persistent
	// deletions, while individual index maintenance errors can be logged
	// separately from script failures.
	ReconcileExpiredLockCandidates(ctx context.Context, maxCount int) (cleaned int, err error)
}

// QueueLockResult records admission and the request identity used for the lock
// attempt.
type QueueLockResult struct {
	// Acquired reports admission, while RequestID names the candidate
	// owner used by a successful storage call. Only an acquired result
	// with a nonempty ID confirms lock ownership; cache bypass or failure
	// admits with an empty ID.
	Acquired  bool   // serialization admission was allowed
	RequestID string // candidate lock request identifier
}

// UserMessageQueueService serializes messages per provider
// and adjusts their spacing according to RPM load.
type UserMessageQueueService struct {
	runtime     WorkerRuntime     // tracks worker and request completion
	diagnostics Diagnostics       // serialization and timing failure sink
	cache       UserMsgQueueCache // optional distributed serialization storage
	rpmCache    RPMCache          // optional provider RPM observer
	// cfg retains the caller options pointer. Acquisition and delay
	// methods read these fields when needed, so callers keep the supplied
	// settings stable or synchronize any concurrent changes.
	cfg *MessageQueueOptions
}

// MessageQueueOptions contains lock lifetime and RPM-based spacing bounds
// measured in milliseconds.
type MessageQueueOptions struct {
	// LockTTLMs sets the Redis lock lifetime read by TryAcquire.
	// Nonpositive values select that method's default lifetime, which
	// bounds locks left after uncertain cleanup.
	LockTTLMs int // milliseconds
	// MinDelayMs and MaxDelayMs define the base spacing interval before
	// jitter. CalculateRPMAwareDelay substitutes each nonpositive bound
	// independently and swaps reversed bounds before mapping RPM load onto
	// that interval.
	MinDelayMs int // milliseconds
	MaxDelayMs int // milliseconds
}

// NewUserMessageQueueService binds serialization storage, RPM observation,
// timing settings, and optional diagnostics. It starts no worker; cfg is
// retained and needs to be non-nil for methods that read lock or delay
// settings.
func NewUserMessageQueueService(cache UserMsgQueueCache, rpmCache RPMCache, cfg *MessageQueueOptions, options ...Diagnostics) *UserMessageQueueService {
	var diagnostics Diagnostics
	if len(options) > 0 {
		diagnostics = options[0]
	}
	return &UserMessageQueueService{
		cache:       cache,
		rpmCache:    rpmCache,
		cfg:         cfg,
		diagnostics: diagnostics,
	}
}

// TryAcquire attempts immediate serialization admission under runtime
// tracking. Nil cache or a storage failure allows admission with no request
// ID, while stopped runtimes return their admission error.
//
// A confirmed cache response retains the generated owner ID even when the lock
// was busy. Successful callers pass that ID to Release after their work; the
// request-scoped acquisition wrapper can instead transfer it to a Lease.
// Nonpositive configured lock lifetime uses two minutes, and the underlying
// lock expires if owner cleanup cannot be confirmed.
func (s *UserMessageQueueService) TryAcquire(ctx context.Context, providerID int64) (*QueueLockResult, error) {
	operation, done, err := s.runtime.Enter(ctx, "TryAcquire")
	if err != nil {
		return nil, err
	}
	defer done()
	ctx = operation
	if s.cache == nil {
		// Serialization storage is absent, so forwarding is admitted
		// without an owner identity. The later Release call can then
		// bypass unconfirmed lock cleanup.
		return &QueueLockResult{Acquired: true}, nil
	}

	requestID := generateUMQRequestID()
	lockTTL := s.cfg.LockTTLMs
	if lockTTL <= 0 {
		lockTTL = 120000
	}

	acquired, err := s.cache.AcquireLock(ctx, providerID, requestID, lockTTL)
	if err != nil {
		s.diagnostics.printf("service.umq", "AcquireLock failed for provider %d: %v", providerID, err)
		// Acquisition failed before lock ownership could be confirmed.
		// Admit forwarding with an empty owner ID; any uncertain
		// remote lock is bounded by its expiry.
		return &QueueLockResult{Acquired: true}, nil
	}

	return &QueueLockResult{
		Acquired:  acquired,
		RequestID: requestID,
	}, nil
}

// Release asks storage to remove the lock matching requestID and record
// completion. Nil cache or an empty owner ID succeeds without a write;
// different or expired ownership is logged and left unchanged.
func (s *UserMessageQueueService) Release(ctx context.Context, providerID int64, requestID string) error {
	if s.cache == nil || requestID == "" {
		return nil
	}
	released, err := s.cache.ReleaseLock(ctx, providerID, requestID)
	if err != nil {
		s.diagnostics.printf("service.umq", "ReleaseLock failed for provider %d: %v", providerID, err)
		return err
	}
	if !released {
		s.diagnostics.printf("service.umq", "ReleaseLock no-op for provider %d (requestID mismatch or expired)", providerID)
	}
	return nil
}

// EnforceDelay waits for the unelapsed RPM-based spacing since this provider
// last completed work. Nil cache, absent history, or failed timing
// observations skip this spacing delay.
//
// Completion and current timestamps use Redis time, so local process clock
// differences do not change the elapsed interval. A negative elapsed value
// also skips delay. Otherwise the calculated interval is reduced by elapsed
// milliseconds, and a timer waits for the positive remainder or context
// cancellation. Runtime shutdown cancels the tracked operation, while the
// caller retains any already acquired serialization lock.
func (s *UserMessageQueueService) EnforceDelay(ctx context.Context, providerID int64, baseRPM int) error {
	operation, done, err := s.runtime.Enter(ctx, "EnforceDelay")
	if err != nil {
		return err
	}
	defer done()
	ctx = operation
	if s.cache == nil {
		return nil
	}

	// Read completion history before calculating RPM spacing. A missing
	// history has no reference interval, and read failure leaves the
	// service free to forward without this delay.
	lastMs, err := s.cache.GetLastCompletedMs(ctx, providerID)
	if err != nil {
		s.diagnostics.printf("service.umq", "GetLastCompletedMs failed for provider %d: %v", providerID, err)
		// The completion timestamp could not be read. Skip spacing
		// while retaining the serialization lease cleanup
		// responsibility in the calling acquisition wrapper.
		return nil
	}
	if lastMs == 0 {
		return nil // No completion history requires no delay.
	}

	delay := s.CalculateRPMAwareDelay(ctx, providerID, baseRPM)
	if delay <= 0 {
		return nil
	}

	// Compare timestamps from the same Redis clock. If its current sample
	// fails, skip spacing rather than computing elapsed time with an
	// unrelated local clock.
	nowMs, err := s.cache.GetCurrentTimeMs(ctx)
	if err != nil {
		s.diagnostics.printf("service.umq", "GetCurrentTimeMs failed: %v", err)
		return nil
	}

	// Translate Redis millisecond timestamps into the local duration used
	// for spacing.
	elapsed := time.Duration(nowMs-lastMs) * time.Millisecond
	if elapsed < 0 {
		// The observed current clock is earlier than completion
		// history. Skip the negative elapsed interval rather than
		// extending a delay from incomparable timing samples.
		return nil
	}
	remaining := delay - elapsed
	if remaining <= 0 {
		return nil
	}

	// When AcquireWithWait called this method, its serialization owner
	// stays held during spacing. Returning a cancellation error lets that
	// wrapper release preparation ownership before reporting acquisition.
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// CalculateRPMAwareDelay returns a jittered interval based on current RPM
// relative to baseRPM. Missing RPM storage, disabled base, or read errors use
// the lower base interval before jitter.
//
// Nonpositive bounds receive defaults and reversed bounds are swapped. Ratios
// below one half use the minimum, ratios at least four fifths use the maximum,
// and values between them interpolate linearly before rounding to a duration.
// Jitter then varies that base by up to fifteen percent, so the returned
// interval can lie outside the configured base bounds.
func (s *UserMessageQueueService) CalculateRPMAwareDelay(ctx context.Context, providerID int64, baseRPM int) time.Duration {
	// Queue configuration stores integer milliseconds. Convert both bounds
	// before default validation and interpolation so their durations match
	// the Redis-based spacing calculation.
	minDelay := time.Duration(s.cfg.MinDelayMs) * time.Millisecond
	maxDelay := time.Duration(s.cfg.MaxDelayMs) * time.Millisecond

	if minDelay <= 0 {
		minDelay = 200 * time.Millisecond
	}
	if maxDelay <= 0 {
		maxDelay = 2000 * time.Millisecond
	}
	// Normalize reversed bounds before interpolation so increasing RPM
	// load moves from the smaller interval toward the larger configured
	// interval.
	if minDelay > maxDelay {
		minDelay, maxDelay = maxDelay, minDelay
	}

	var baseDelay time.Duration

	if baseRPM <= 0 || s.rpmCache == nil {
		baseDelay = minDelay
	} else {
		currentRPM, err := s.rpmCache.GetRPM(ctx, providerID)
		if err != nil {
			s.diagnostics.printf("service.umq", "GetRPM failed for provider %d: %v", providerID, err)
			// RPM observation failed, leaving the load ratio
			// unknown. Use the configured lower spacing bound so
			// storage failure does not impose the maximum
			// load-based delay.
			baseDelay = minDelay
		} else {
			ratio := float64(currentRPM) / float64(baseRPM)
			if ratio < 0.5 {
				baseDelay = minDelay
			} else if ratio >= 0.8 {
				baseDelay = maxDelay
			} else {
				// Map the middle RPM band continuously onto
				// the normalized delay bounds, then round its
				// nanosecond duration before applying jitter.
				t := (ratio - 0.5) / 0.3
				interpolated := float64(minDelay) + t*(float64(maxDelay)-float64(minDelay))
				baseDelay = time.Duration(math.Round(interpolated))
			}
		}
	}

	// Jitter applies after the base interval is chosen, spreading request
	// timing even when several providers or requests have the same
	// observed RPM ratio.
	return applyJitter(baseDelay, 0.15)
}

// StartCleanupWorker registers periodic indexed-lock maintenance on the first
// runtime start. Invalid interval or absent cache skips startup; work begins
// after the first tick and each pass has its own bounded timeout.
func (s *UserMessageQueueService) StartCleanupWorker(interval time.Duration) {
	if s == nil || s.cache == nil || interval <= 0 {
		return
	}
	s.runtime.Start(RuntimeTask{Name: "message-queue-cleanup", Interval: interval, Run: func(ctx context.Context) {
		// Bound each maintenance pass by ten seconds while retaining
		// the worker's shutdown cancellation, so waiting for a slow
		// adapter respects both limits.
		cleanup, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		// Limit one pass to a thousand indexed candidates; additional
		// due entries remain for a later periodic pass.
		cleaned, err := s.cache.ReconcileExpiredLockCandidates(cleanup, 1000)
		if err != nil {
			// Report this failed pass and let the periodic worker
			// try again on a later tick. Ordinary expiring locks
			// still retain their own TTL cleanup.
			s.diagnostics.printf("service.umq", "Cleanup reconcile failed: %v", err)
			return
		}
		if cleaned > 0 {
			s.diagnostics.printf("service.umq", "Cleanup completed: released %d orphaned locks", cleaned)
		}
	}})
}

// Stop cancels runtime work and waits using a thirty-second shutdown budget.
// It discards the retained stop result; callers that need to distinguish
// unfinished work use StopContext with their application deadline.
func (s *UserMessageQueueService) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The runtime retains its first stop error for later StopContext
	// callers, even though this convenience entry point ignores that
	// result.
	_ = s.StopContext(ctx)
}

// StopContext delegates cancellation and completion waiting to the shared
// worker runtime. Nil service succeeds, and repeated calls retain the first
// shutdown result; callers provide a non-nil context for the wait budget.
func (s *UserMessageQueueService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.runtime.StopContext(ctx)
}

// applyJitter multiplies a positive duration by a random fractional adjustment
// around one. Nonpositive duration or jitter leaves the input unchanged, and
// floating multiplication is truncated when converted back to time.Duration.
func applyJitter(d time.Duration, jitterPct float64) time.Duration {
	if d <= 0 || jitterPct <= 0 {
		return d
	}
	// Scale a uniform sample around zero so the configured fraction is the
	// maximum proportional adjustment in either direction.
	jitter := (rand.Float64()*2 - 1) * jitterPct
	// Duration uses whole nanoseconds, so sampled fractional nanoseconds
	// are discarded before the queue constructs its spacing timer.
	return time.Duration(float64(d) * (1 + jitter))
}

// generateUMQRequestID returns sixteen random bytes encoded as the lock owner's
// hex string. The supported crypto/rand.Read fills the buffer or terminates the
// process on an entropy failure; it returns no error for timestamp recovery.
func generateUMQRequestID() string {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		// The timestamp expression supplies a result if Read returns an
		// error. The supported crypto/rand.Read terminates internally
		// on failure, so this branch cannot recover its entropy-source
		// errors.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
