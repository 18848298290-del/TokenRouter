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

// UserMsgQueueCache stores provider serialization locks and completion times in Redis.
type UserMsgQueueCache interface {
	// AcquireLock attempts to acquire a provider's serialization lock.
	AcquireLock(ctx context.Context, providerID int64, requestID string, lockTtlMs int) (acquired bool, err error)
	// ReleaseLock releases the owned lock and records its completion time.
	ReleaseLock(ctx context.Context, providerID int64, requestID string) (released bool, err error)
	// GetLastCompletedMs returns the last completion's Unix timestamp in milliseconds from Redis TIME.
	GetLastCompletedMs(ctx context.Context, providerID int64) (int64, error)
	// GetCurrentTimeMs returns Redis time in milliseconds, using the same clock as ReleaseLock.
	GetCurrentTimeMs(ctx context.Context) (int64, error)
	// ReconcileExpiredLockCandidates removes or refreshes expired index entries according to Redis PTTL.
	ReconcileExpiredLockCandidates(ctx context.Context, maxCount int) (cleaned int, err error)
}

// QueueLockResult contains the acquisition status and lock owner ID.
type QueueLockResult struct {
	Acquired  bool
	RequestID string
}

// UserMessageQueueService serializes messages per provider
// and adjusts their spacing according to RPM load.
type UserMessageQueueService struct {
	runtime     WorkerRuntime
	diagnostics Diagnostics
	cache       UserMsgQueueCache
	rpmCache    RPMCache
	cfg         *MessageQueueOptions
}

// MessageQueueOptions contains static queue timing parameters in milliseconds.
type MessageQueueOptions struct {
	LockTTLMs  int
	MinDelayMs int
	MaxDelayMs int
}

// NewUserMessageQueueService creates the provider message queue service.
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

// TryAcquire attempts immediate lock acquisition and allows requests through when Redis is unavailable.
func (s *UserMessageQueueService) TryAcquire(ctx context.Context, providerID int64) (*QueueLockResult, error) {
	operation, done, err := s.runtime.Enter(ctx, "TryAcquire")
	if err != nil {
		return nil, err
	}
	defer done()
	ctx = operation
	if s.cache == nil {
		return &QueueLockResult{Acquired: true}, nil // fail-open
	}

	requestID := generateUMQRequestID()
	lockTTL := s.cfg.LockTTLMs
	if lockTTL <= 0 {
		lockTTL = 120000
	}

	acquired, err := s.cache.AcquireLock(ctx, providerID, requestID, lockTTL)
	if err != nil {
		s.diagnostics.printf("service.umq", "AcquireLock failed for provider %d: %v", providerID, err)
		return &QueueLockResult{Acquired: true}, nil // fail-open
	}

	return &QueueLockResult{
		Acquired:  acquired,
		RequestID: requestID,
	}, nil
}

// Release releases the serialization lock owned by requestID.
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

// EnforceDelay waits for the remaining RPM-based spacing after the last completion.
// Redis TIME provides the same clock used to record completion.
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

	// No completion history means no spacing delay is needed.
	lastMs, err := s.cache.GetLastCompletedMs(ctx, providerID)
	if err != nil {
		s.diagnostics.printf("service.umq", "GetLastCompletedMs failed for provider %d: %v", providerID, err)
		return nil // fail-open
	}
	if lastMs == 0 {
		return nil // No completion history requires no delay.
	}

	delay := s.CalculateRPMAwareDelay(ctx, providerID, baseRPM)
	if delay <= 0 {
		return nil
	}

	// Use the same Redis clock as lastMs.
	nowMs, err := s.cache.GetCurrentTimeMs(ctx)
	if err != nil {
		s.diagnostics.printf("service.umq", "GetCurrentTimeMs failed: %v", err)
		return nil // fail-open
	}

	elapsed := time.Duration(nowMs-lastMs) * time.Millisecond
	if elapsed < 0 {
		// Allow the request through if the Redis clock moves backward, such as after failover.
		return nil
	}
	remaining := delay - elapsed
	if remaining <= 0 {
		return nil
	}

	// Wait until the spacing interval elapses or the context is canceled.
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// CalculateRPMAwareDelay computes a jittered delay from current RPM load.
// ratio = currentRPM / baseRPM
// ratio < 0.5 uses MinDelay.
// 0.5 <= ratio < 0.8 interpolates linearly between MinDelay and MaxDelay.
// ratio >= 0.8 uses MaxDelay.
// A random adjustment of up to 15% in either direction spreads request timing.
func (s *UserMessageQueueService) CalculateRPMAwareDelay(ctx context.Context, providerID int64, baseRPM int) time.Duration {
	minDelay := time.Duration(s.cfg.MinDelayMs) * time.Millisecond
	maxDelay := time.Duration(s.cfg.MaxDelayMs) * time.Millisecond

	if minDelay <= 0 {
		minDelay = 200 * time.Millisecond
	}
	if maxDelay <= 0 {
		maxDelay = 2000 * time.Millisecond
	}
	// Swap reversed configured bounds before interpolation.
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
			baseDelay = minDelay // fail-open
		} else {
			ratio := float64(currentRPM) / float64(baseRPM)
			if ratio < 0.5 {
				baseDelay = minDelay
			} else if ratio >= 0.8 {
				baseDelay = maxDelay
			} else {
				// Interpolate from minDelay at 0.5 to maxDelay at 0.8.
				t := (ratio - 0.5) / 0.3
				interpolated := float64(minDelay) + t*(float64(maxDelay)-float64(minDelay))
				baseDelay = time.Duration(math.Round(interpolated))
			}
		}
	}

	// Vary the delay by up to 15% in either direction.
	return applyJitter(baseDelay, 0.15)
}

// StartCleanupWorker starts cleanup after the first interval; stopping cancels work and waits for exit.
func (s *UserMessageQueueService) StartCleanupWorker(interval time.Duration) {
	if s == nil || s.cache == nil || interval <= 0 {
		return
	}
	s.runtime.Start(RuntimeTask{Name: "message-queue-cleanup", Interval: interval, Run: func(ctx context.Context) {
		cleanup, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cleaned, err := s.cache.ReconcileExpiredLockCandidates(cleanup, 1000)
		if err != nil {
			s.diagnostics.printf("service.umq", "Cleanup reconcile failed: %v", err)
			return
		}
		if cleaned > 0 {
			s.diagnostics.printf("service.umq", "Cleanup completed: released %d orphaned locks", cleaned)
		}
	}})
}

func (s *UserMessageQueueService) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.StopContext(ctx)
}

func (s *UserMessageQueueService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.runtime.StopContext(ctx)
}

func applyJitter(d time.Duration, jitterPct float64) time.Duration {
	if d <= 0 || jitterPct <= 0 {
		return d
	}
	// [-jitterPct, +jitterPct]
	jitter := (rand.Float64()*2 - 1) * jitterPct
	return time.Duration(float64(d) * (1 + jitter))
}

// generateUMQRequestID returns a random ID, using a timestamp if random bytes cannot be read.
func generateUMQRequestID() string {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
