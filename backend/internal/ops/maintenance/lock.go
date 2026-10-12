package maintenance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Maintenance actions share one persistent lease row, even when callers reuse an
// operation ID. Each successful acquisition supplies fresh random owner metadata;
// renewal and completion compare that captured text so replacement acquisitions
// can reject an earlier worker's subsequent state updates. Expired leases are
// available to new claimants independently of record retention.
//
// Accepted work inherits request values while discarding request cancellation and
// deadlines. Renewal errors are logged and retried without cancelling that work;
// a returned ownership mismatch cancels its context. Release stops renewal and
// waits for its loop before completing the stored claim once. Its context limits
// the completion call, while waiting for renewal termination is a separate phase
// that needs the repository and logging callback to return.

// The fixed scope and hashed key select the shared lease used to arbitrate
// maintenance ownership across service instances through the operation lease store.
const (
	systemOperationLockScope = "admin.system.operations.global_lock"
	systemOperationLockKey   = "global-system-operation-lock"
)

// Maintenance errors distinguish a live shared lease, unavailable claim storage,
// and a completion attempt whose captured owner no longer matches the processing row.
var (
	ErrSystemOperationBusy = infraerrors.Conflict("SYSTEM_OPERATION_BUSY", "another system operation is in progress")

	ErrIdempotencyStoreUnavail = idempotency.ErrIdempotencyStoreUnavail
	ErrOperationOwnershipLost  = infraerrors.Conflict("SYSTEM_OPERATION_OWNERSHIP_LOST", "system operation ownership lost")
)

// SystemOperationLock represents one captured maintenance lease and its accepted-work lifetime.
type SystemOperationLock struct {
	// recordID and ownership retain the acquired row and exact owner metadata.
	// Renewal and completion also compare operationID, distinguishing an acquisition from a replacement of its row.
	recordID  int64  // acquired lease record identifier
	ownership string // captured owner metadata JSON
	// ctx and cancel describe accepted work independently of request cancellation.
	// Release, observed ownership loss, or the Operations stop link can cancel this context.
	cancel context.CancelFunc // accepted work cancellation function
	ctx    context.Context    // accepted maintenance work context
	// done closes when the renewal goroutine returns. Release waits on it before
	// calling the store, so renewal cannot later update a successfully finished claim.
	done chan struct{}
	// releaseOnce protects releaseErr and selects the first completion outcome.
	// Later releases return that cached result and do not change success, reason, or retention again.
	releaseOnce sync.Once // one completion attempt guard
	releaseErr  error     // cached lease completion result
	operationID string    // requested maintenance operation label

	// stopOnce closes stopCh once. Release also cancels ctx, letting an active
	// renewal query observe cancellation while the loop's next selection sees its stop signal.
	stopOnce sync.Once     // one renewal stop signal guard
	stopCh   chan struct{} // renewal loop stop notification
}

// SystemOperationLockService acquires, renews, and finishes shared maintenance ownership.
type SystemOperationLockService struct {
	// log receives renewal messages inline on the renewal goroutine. Its callbacks
	// avoid synchronously releasing that same lock, which would wait for this goroutine to return.
	log func(string, string, ...any)

	repo idempotency.OperationLeaseStore // ownership-checked persistent lease store

	// lease, renewInterval, and ttl control lock availability, polling, and record
	// retention separately. Renewal updates lease and ttl deadlines; polling can outlast a subsecond lease.
	lease         time.Duration // processing lease window duration
	renewInterval time.Duration // renewal ticker scheduling interval
	ttl           time.Duration // lease record retention duration
}

// Options configures maintenance lock processing timeout, TTL, and logging.
type Options struct {
	// Log receives renewal events on that loop's goroutine. Callbacks return and
	// avoid releasing the same lock synchronously, because release waits for the loop.
	Log func(string, string, ...any)
	// ProcessingTimeout selects the lease, defaulting nonpositive values to thirty
	// seconds. SystemOperationTTL selects record retention, defaulting nonpositive values to one hour.
	// ProcessingTimeout is the lock availability window; SystemOperationTTL is the stored record lifetime.
	ProcessingTimeout, SystemOperationTTL time.Duration
}

// OperationID returns the label captured for this maintenance acquisition.
// A nil lock returns an empty string, allowing diagnostics to inspect an absent
// acquisition without manufacturing another operation identity.
func (l *SystemOperationLock) OperationID() string {
	if l == nil {
		return ""
	}
	return l.operationID
}

// NewSystemOperationLockService retains the lease store and normalizes lease,
// retention, and optional logging. Renewal polls at one third of the lease with
// a one-second minimum, so a supplied subsecond lease can expire before its first
// scheduled renewal; nonpositive durations choose thirty-second lease and hour retention.
func NewSystemOperationLockService(repo idempotency.OperationLeaseStore, cfg Options) *SystemOperationLockService {
	lease := cfg.ProcessingTimeout
	if lease <= 0 {
		lease = 30 * time.Second
	}
	// Leave room for renewal before ordinary lease expiry while preventing a
	// ticker shorter than one second; the constructor does not raise shorter supplied leases to match.
	renewInterval := max(lease/3, time.Second)
	ttl := cfg.SystemOperationTTL
	if ttl <= 0 {
		ttl = time.Hour
	}

	if cfg.Log == nil {
		cfg.Log = func(string, string, ...any) {}
	}
	return &SystemOperationLockService{
		log:           cfg.Log,
		repo:          repo,
		lease:         lease,
		renewInterval: renewInterval,
		ttl:           ttl,
	}
}

// Acquire claims the shared lease for a nonempty operation ID and returns its
// captured owner plus a renewal-backed work context. Missing service or storage
// returns unavailability; a live competing row supplies busy operation and delay metadata.
//
// Fresh random bytes are encoded into owner JSON and retained exactly for later
// comparisons, including repeated operation IDs. The store decides takeover using
// its current lock predicate; busy metadata comes from its returned record and
// can reflect a separate read. Successful work detaches request cancellation and
// starts renewal, so callers release the returned lock after their maintenance action finishes.
func (s *SystemOperationLockService) Acquire(ctx context.Context, operationID string) (*SystemOperationLock, error) {
	if s == nil || s.repo == nil {
		return nil, ErrIdempotencyStoreUnavail
	}
	if operationID == "" {
		return nil, infraerrors.BadRequest("SYSTEM_OPERATION_ID_REQUIRED", "operation id is required")
	}

	now := time.Now()
	expiresAt := now.Add(s.ttl)
	lockedUntil := now.Add(s.lease)
	keyHash := idempotency.HashIdempotencyKey(systemOperationLockKey)

	// Keep acquisition ownership separate from reusable operation labels by creating
	// twenty-four random bytes for this claim attempt before encoding its stored owner metadata.
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	// String-only owner metadata is JSON-encodable; retain its exact serialized bytes
	// so later store comparisons use the same acquisition token representation rather than another encoding.
	metadata, _ := json.Marshal(map[string]string{"owner_token": hex.EncodeToString(token)})
	ownership := string(metadata)
	record, owner, err := s.repo.ClaimOperation(ctx, idempotency.OperationClaim{Scope: systemOperationLockScope, KeyHash: keyHash, OperationID: operationID, Ownership: ownership, Now: now, LockedUntil: lockedUntil, ExpiresAt: expiresAt})
	if err != nil {
		return nil, ErrIdempotencyStoreUnavail.WithCause(err)
	}
	if !owner {
		if record == nil {
			return nil, ErrIdempotencyStoreUnavail
		}
		return nil, s.busyError(record.RequestFingerprint, record.LockedUntil, now)
	}

	if record.ID == 0 {
		return nil, ErrIdempotencyStoreUnavail
	}

	// Preserve request values while removing its cancellation and deadline, allowing
	// accepted maintenance to continue after the HTTP caller disconnects until ownership or service lifetime ends.
	operationCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	lock := &SystemOperationLock{
		ownership: ownership, ctx: operationCtx, cancel: cancel, done: make(chan struct{}),
		recordID:    record.ID,
		operationID: operationID,
		stopCh:      make(chan struct{}),
	}
	go s.renewLoop(lock)

	return lock, nil
}

// Release stops renewal and finishes the captured claim once, caching its result
// for repeated calls. Nil service, repository, or lock returns success; nil ctx
// becomes background and an empty failure reason chooses SYSTEM_OPERATION_FAILED.
//
// The stop signal and work cancellation precede waiting for the renewal goroutine,
// then the store compares row, operation, and owner metadata while finishing.
// False without a store error becomes ErrOperationOwnershipLost. Context applies to that
// final store call, not the preceding renewal wait, so a blocked repository or
// renewal logger can delay reaching it beyond the requested context's deadline.
func (s *SystemOperationLockService) Release(ctx context.Context, lock *SystemOperationLock, succeeded bool, failureReason string) error {
	if s == nil || s.repo == nil || lock == nil {
		return nil
	}

	lock.releaseOnce.Do(func() {
		// Stop scheduling and cancel an in-flight renewal before waiting for it,
		// keeping the final owner comparison from racing a later renewal of this acquisition.
		lock.stopOnce.Do(func() { close(lock.stopCh); lock.cancel() })
		<-lock.done
		if ctx == nil {
			ctx = context.Background()
		}
		if failureReason == "" {
			failureReason = "SYSTEM_OPERATION_FAILED"
		}
		ok, err := s.repo.FinishOperation(ctx, lock.recordID, lock.operationID, lock.ownership, succeeded, failureReason, time.Now().Add(s.ttl))
		if err != nil {
			lock.releaseErr = err
		} else if !ok {
			lock.releaseErr = ErrOperationOwnershipLost
		}
	})
	return lock.releaseErr
}

// renewLoop updates lease and retention deadlines on its configured ticker until
// stopped or a store result reports ownership loss. Queries use two-second
// contexts derived from accepted work; a returned storage error is logged and retried.
//
// Storage errors do not establish replacement ownership and leave work cancellation
// unchanged, even when repeated delays let the lease expire. False does establish
// that the processing identity did not match the renewal update, so the loop
// cancels work and closes done on return. Cancellation of work alone does not
// select loop exit; Release also closes stopCh to stop further ticker attempts.
func (s *SystemOperationLockService) renewLoop(lock *SystemOperationLock) {
	defer close(lock.done)
	ticker := time.NewTicker(s.renewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			now := time.Now()
			ctx, cancel := context.WithTimeout(lock.ctx, 2*time.Second)
			ok, err := s.repo.RenewOperation(
				ctx,
				lock.recordID,
				lock.operationID,
				lock.ownership,
				now.Add(s.lease),
				now.Add(s.ttl),
			)
			cancel()
			if err != nil {
				s.log("service.system_operation_lock", "[SystemOperationLock] renew failed operation_id=%s err=%v", lock.operationID, err)
				// A query error leaves ownership uncertain. Retry on a later tick without
				// cancelling work, while lease expiry can still permit another claimant.
				continue
			}
			if !ok {
				// The store rejected this captured identity, so stop subsequent work before
				// reporting ownership loss and ending renewal; an earlier deadline alone does not trigger this branch.
				lock.cancel()
				s.log("service.system_operation_lock", "[SystemOperationLock] renew stopped operation_id=%s reason=ownership_lost", lock.operationID)
				return
			}
		case <-lock.stopCh:
			return
		}
	}
}

// busyError adds observed operation and retry-delay metadata to the shared busy
// error. Empty identity is omitted; an available deadline becomes whole seconds
// with a one-second minimum, including already passed or fractional remaining windows.
func (s *SystemOperationLockService) busyError(operationID string, lockedUntil *time.Time, now time.Time) error {
	metadata := make(map[string]string)
	if operationID != "" {
		metadata["operation_id"] = operationID
	}
	if lockedUntil != nil {
		// Express the observed lock window as usable HTTP retry metadata, keeping a
		// positive minimum when whole-second conversion drops a fraction or the sampled deadline has passed.
		sec := int(lockedUntil.Sub(now).Seconds())
		if sec <= 0 {
			sec = 1
		}
		metadata["retry_after"] = strconv.Itoa(sec)
	}
	if len(metadata) == 0 {
		return ErrSystemOperationBusy
	}
	return ErrSystemOperationBusy.WithMetadata(metadata)
}

// Context returns the accepted-work context of a nonnil acquired lock. It retains
// request values while omitting request cancellation and deadlines; release, service
// stop links, or observed renewal ownership loss can cancel later maintenance work.
func (l *SystemOperationLock) Context() context.Context { return l.ctx }
