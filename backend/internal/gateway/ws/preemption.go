package ws

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
)

// A newer pooled OAuth session can replace an existing request for the same
// group, API Key, and session hash. The displaced request needs cancellation
// and its connection/turn-state affinity must be cleared so subsequent work can
// establish fresh state.
//
// PreemptRegistry replaces the local owner under a mutex, then invokes the
// displaced owner's cancellation outside that lock. Generation-matched cleanup
// leaves a newer registration available. Preemption also claims an optional
// remote owner token and polls compare-and-refresh to detect replacement by
// another instance. Cache errors keep local registration operating; a confirmed
// ownership mismatch invokes a once-guarded state clear and cancellation.
// Cleanup signals the watcher, removes local registration, compare-deletes the
// claimed token, and cancels its context; stopping the watcher does not join an
// in-flight cache check.

const (
	// Remote ownership uses a bounded lifetime renewed by polling so another
	// instance can replace the session and displaced requests can observe that
	// replacement.
	preemptOwnerTTL      = 2 * time.Hour   // remote owner token lifetime
	preemptWatchInterval = 2 * time.Second // remote ownership polling interval
)

// ErrSessionPreempted identifies cancellation by a newer request for the same
// scoped session, allowing entry to avoid ordinary upstream failure reporting.
var ErrSessionPreempted = errors.New("openai ws session preempted by newer request")

// PreemptKey identifies local ownership by group, API Key, and session hash.
type PreemptKey struct {
	// GroupID, APIKeyID, and SessionHash together separate local registrations.
	// HTTP callers validate positive identifiers and a nonblank hash before
	// invoking Preemption.Begin.
	GroupID     int64  // request routing group identifier
	APIKeyID    int64  // authenticated API Key identifier
	SessionHash string // normalized shared session hash
}

// PreemptState removes cached affinity when this scoped session loses
// ownership.
type PreemptState interface {
	// DeleteSessionTurnState clears cached turn metadata for the supplied group
	// and session hash.
	DeleteSessionTurnState(int64, string)
	// DeleteSessionConn clears cached connection affinity for the supplied group
	// and session hash.
	DeleteSessionConn(int64, string)
}

// Preemption combines local replacement cancellation with optional shared-cache
// ownership observation.
type Preemption struct {
	// Registry may be nil to disable local replacement tracking. Remote ownership
	// can still be claimed and watched through Cache when that dependency is
	// available.
	Registry *PreemptRegistry                       // local ownership generation registry
	Cache    session.OpenAIWSSessionPreemptionCache // optional remote ownership cache
	// State is consulted when loss is confirmed, clearing the scoped turn state
	// and connection affinity before cancellation. Implementations must support
	// concurrent session use.
	State        PreemptState  // optional cached session affinity
	RedisTimeout time.Duration // remote operation context timeout
}

// preemptEntry associates one local ownership generation with its displacement
// callback.
type preemptEntry struct {
	generation uint64 // local registration sequence number
	cancel     func() // displaced owner cancellation callback
}

// PreemptRegistry is a zero-value-ready registry of locally active scoped
// owners.
type PreemptRegistry struct {
	// mu protects next and active. Replacement callbacks run after unlocking;
	// cleanup deletes an entry only while its generation still matches the
	// original registration.
	mu     sync.Mutex                  // owner registry access lock
	next   uint64                      // next registration sequence number
	active map[PreemptKey]preemptEntry // active scoped owner registrations
}

// CacheHash prefixes the trimmed session hash with API Key identity for remote
// ownership.
func CacheHash(apiKeyID int64, hash string) string {
	return fmt.Sprintf("wspreempt:%d:%s", apiKeyID, strings.TrimSpace(hash))
}

// Begin registers cancel as the current local owner and reports whether a prior
// entry existed. Nil registries and blank session hashes return a no-op cleanup
// and false.
//
// Replacement stores a new generation under the mutex before invoking the prior
// callback outside it, so callbacks can perform cleanup without holding
// registry access. The returned cleanup removes its own generation only when
// still current; invoking an old cleanup after another Begin leaves that newer
// owner registered. A prior entry with a nil callback still makes the returned
// replacement flag true.
func (r *PreemptRegistry) Begin(key PreemptKey, cancel func()) (cleanup func(), preemptedPrevious bool) {
	if r == nil || strings.TrimSpace(key.SessionHash) == "" {
		return func() {}, false
	}
	r.mu.Lock()
	if r.active == nil {
		r.active = make(map[PreemptKey]preemptEntry)
	}
	r.next++
	generation := r.next
	previous, hadPrevious := r.active[key]
	r.active[key] = preemptEntry{generation: generation, cancel: cancel}
	r.mu.Unlock()
	// Invoke displacement after publishing the new owner and releasing the mutex,
	// so previous cleanup cannot erase that generation or hold registry access
	// during cancellation.
	if hadPrevious && previous.cancel != nil {
		previous.cancel()
	}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		// Cleanup belongs to its captured generation; an older request ending after
		// replacement must leave the newer owner registered for further preemption.
		current, ok := r.active[key]
		if ok && current.generation == generation {
			delete(r.active, key)
		}
	}, hadPrevious
}

// Begin creates a cancel-cause context and registers ownership for a validated
// scoped key. Callers supply a nonnil Preemption and context, then run the
// returned cleanup when the session ends.
//
// Remote claim runs before local registration; either a different remote token
// or a prior local entry reports preemptedPrevious. A once-guarded displacement
// clears cached turn state and connection affinity before canceling with
// ErrSessionPreempted. Remote failures leave local registration active, and
// successful claim starts an ownership watcher. Cleanup signals that watcher
// without joining it, removes the local generation, compare-deletes the remote
// token when claimed, and finally cancels the derived context. The third result
// is true for this invoked registration path.
func (s *Preemption) Begin(ctx context.Context, key PreemptKey) (context.Context, func(), bool, bool) {
	preemptCtx, cancel := context.WithCancelCause(ctx)
	ownerToken := uuid.NewString()
	var preemptOnce sync.Once
	preempt := func() {
		// Local displacement and remote mismatch can both report loss, so guard the
		// cache clear and cancel sequence against duplicate execution.
		preemptOnce.Do(func() {
			if stateStore := s.State; stateStore != nil {
				stateStore.DeleteSessionTurnState(key.GroupID, key.SessionHash)
				stateStore.DeleteSessionConn(key.GroupID, key.SessionHash)
			}
			cancel(ErrSessionPreempted)
		})
	}
	previousRemoteOwner, remoteClaimed := s.Claim(ctx, key, ownerToken)
	preemptedPrevious := remoteClaimed && previousRemoteOwner != "" && previousRemoteOwner != ownerToken
	cleanupLocal, hadLocalPrevious := s.Registry.Begin(key, preempt)
	preemptedPrevious = preemptedPrevious || hadLocalPrevious
	stopWatch := func() {}
	if remoteClaimed {
		stopWatch = s.Watch(preemptCtx, key, ownerToken, preempt)
	}

	return preemptCtx, func() {
		// Stop future polling and remove this local generation before
		// compare-deleting its remote token; cancel context after those ownership
		// cleanup operations have been attempted.
		stopWatch()
		cleanupLocal()
		if remoteClaimed {
			s.Release(context.Background(), key, ownerToken)
		}
		cancel(nil)
	}, true, preemptedPrevious
}

// Claim writes the trimmed remote owner token and returns its prior value when
// successful. Missing cache, blank token, or cache error returns false, letting
// Begin continue with local registry ownership. The cache receives a
// RedisTimeout context deadline; the configured Redis client's socket I/O uses
// its connection timeouts, while acquisition and retry waits can consult that
// context.
func (s *Preemption) Claim(ctx context.Context, key PreemptKey, ownerToken string) (string, bool) {
	cache := s.Cache
	if cache == nil || strings.TrimSpace(ownerToken) == "" {
		return "", false
	}
	cacheCtx, cancel := context.WithTimeout(ctx, s.RedisTimeout)
	defer cancel()
	previous, err := cache.ClaimOpenAIResponsesSessionWindow(
		cacheCtx,
		key.GroupID,
		CacheHash(key.APIKeyID, key.SessionHash),
		[]byte(strings.TrimSpace(ownerToken)),
		preemptOwnerTTL,
	)
	// A shared-cache failure leaves remote ownership unconfirmed, allowing Begin
	// to keep its local replacement mechanism and omit the remote watcher.
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(previous)), true
}

// Release compare-deletes the remote token using a RedisTimeout context when
// cache and token are available. Errors and the match result are ignored;
// token comparison leaves a newer remote owner in place when cleanup is stale.
// The configured Redis client uses connection timeouts for socket I/O, so this
// supplied context deadline does not bound the entire call.
func (s *Preemption) Release(ctx context.Context, key PreemptKey, ownerToken string) {
	cache := s.Cache
	if cache == nil || strings.TrimSpace(ownerToken) == "" {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, s.RedisTimeout)
	defer cancel()
	// Use token comparison during best-effort release so stale cleanup cannot
	// delete the remote registration established by a newer session.
	_, _ = cache.CompareAndDeleteOpenAIResponsesSessionWindow(
		cacheCtx,
		key.GroupID,
		CacheHash(key.APIKeyID, key.SessionHash),
		[]byte(strings.TrimSpace(ownerToken)),
	)
}

// Watch polls the claimed remote token and invokes onLost after a successful
// mismatch. Missing cache, blank token, or nil callback returns a no-op stop
// function.
//
// Each check receives a Background-derived context with a RedisTimeout
// deadline. The configured Redis client uses connection I/O timeouts because
// ContextTimeoutEnabled is false; acquisition and retry waits can still use
// the supplied context. A check can therefore outlast that context deadline.
//
// Cache errors continue polling, while confirmed replacement invokes onLost
// and ends the worker. Context cancellation and the idempotent returned stop
// function signal termination without joining the worker. A selected check, or
// a ready tick chosen alongside that signal, can still call onLost after stop
// returns; callers keep that callback safe during and after ownership cleanup.
func (s *Preemption) Watch(ctx context.Context, key PreemptKey, ownerToken string, onLost func()) func() {
	cache := s.Cache
	if cache == nil || onLost == nil || strings.TrimSpace(ownerToken) == "" {
		return func() {}
	}
	stopCh := make(chan struct{})
	var once sync.Once
	// Polling distinguishes confirmed token replacement from temporary cache
	// errors; only a successful mismatch calls onLost and terminates ownership
	// observation.
	go func() {
		ticker := time.NewTicker(preemptWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				cacheCtx, cancel := context.WithTimeout(context.Background(), s.RedisTimeout)
				owned, err := cache.CompareAndRefreshOpenAIResponsesSessionWindow(
					cacheCtx,
					key.GroupID,
					CacheHash(key.APIKeyID, key.SessionHash),
					[]byte(strings.TrimSpace(ownerToken)),
					preemptOwnerTTL,
				)
				cancel()
				// Retain the watcher after cache failures because an unreadable token does
				// not establish replacement; a successful mismatch supplies the loss
				// evidence.
				if err == nil && !owned {
					onLost()
					return
				}
			}
		}
	}()
	return func() { once.Do(func() { close(stopCh) }) }
}

// IsSessionPreemptedError recognizes the preemption sentinel anywhere in an
// error chain. It also accepts a FallbackError reason equal to
// session_preempted after trimming and removing one prewarm_ prefix; nil
// returns false.
func IsSessionPreemptedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSessionPreempted) {
		return true
	}
	var fallback *FallbackError
	return errors.As(err, &fallback) && fallback != nil && strings.TrimPrefix(strings.TrimSpace(fallback.Reason), "prewarm_") == "session_preempted"
}
