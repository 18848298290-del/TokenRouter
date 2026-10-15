package creative

import (
	"context"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// queue.go defines creative queue leases, processing locks, and transient
// media storage operations.
// Creative task queue ownership is separate from user/workspace access to task
// metadata. A reservation carries a run identifier and queue lease token for
// acknowledgment, heartbeat, and requeue, while the job lock has its own
// ownership and renewal lifetime. Temporary payload and media operations keep
// plaintext and byte content outside persistent run rows.

// These queue errors distinguish an empty reserve, duplicate admission, lost
// ownership, and malformed task identity so workers can select acknowledgment,
// requeue, or recovery handling for the returned operation result.
var (
	ErrCreativeQueueEmpty    = infraerrors.New(infraerrors.CategoryNotFound, "CREATIVE_QUEUE_EMPTY", "creative queue is empty")
	ErrCreativeAlreadyQueued = infraerrors.New(infraerrors.CategoryConflict, "CREATIVE_ALREADY_QUEUED", "creative run is already queued")

	ErrCreativeLeaseLost           = infraerrors.New(infraerrors.CategoryConflict, "CREATIVE_LEASE_LOST", "creative run lease is no longer owned")
	ErrInvalidCreativeQueuePayload = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_QUEUE_INVALID_PAYLOAD", "invalid creative queue payload")
)

// ReservedCreativeRun identifies a reserved task and the lease token required
// for queue updates.
type ReservedCreativeRun struct {
	// RunID names the reserved task and LeaseToken fences updates to its active
	// queue entry. Workers carry both through heartbeat, acknowledgment, and
	// requeue so a stale reservation cannot change a later owner entry.
	RunID      string // reserved creative task identifier
	LeaseToken string // queue reservation ownership token
}

// CreativeRunJobLock is a task-level distributed lock that the holder releases
// after processing.
type CreativeRunJobLock interface {
	// Release relinquishes the held job lock under the supplied context.
	// Token-aware implementations leave expired or replacement locks unchanged,
	// while transport errors can leave cleanup to the lock lifetime.
	Release(ctx context.Context) error
}

// CreativeRunJobLockRefresher optionally renews a task lock during processing.
type CreativeRunJobLockRefresher interface {
	// Refresh renews a job lock when its stored token still matches and reports
	// ownership. A false result means renewal did not retain that lock; an error
	// leaves ownership uncertain for the worker.
	Refresh(ctx context.Context, ttl time.Duration) (bool, error)
}

// CreativeRunQueue coordinates ready, delayed, and active tasks using lease tokens.
// The Redis implementation is in internal/creative/rediscache/queue.go.
type CreativeRunQueue interface {
	// Enqueue admits a creative run identifier to ready work and its inflight
	// marker. Existing inflight work reports ErrCreativeAlreadyQueued, allowing
	// recovery callers to treat repeated queue admission as represented work.
	Enqueue(ctx context.Context, runID string) error
	// Reserve waits up to the supplied budget for ready work and returns its
	// active queue ownership. An empty budget still permits an immediate reserve
	// attempt, while cancellation and store failures remain errors. Workers use
	// the returned RunID and LeaseToken together for subsequent heartbeat,
	// acknowledgment, or requeue and acquire a separate job lock before
	// processing the task.
	Reserve(ctx context.Context, blockTimeout time.Duration) (ReservedCreativeRun, error)
	// RequeueAfter returns an owned active task to delayed or immediately ready
	// work. The lease token must match the active reservation; missing or
	// replaced ownership reports ErrCreativeLeaseLost instead of changing another
	// worker entry.
	RequeueAfter(ctx context.Context, runID, leaseToken string, delay time.Duration) error
	// Ack removes queue bookkeeping for the matching active run and lease token.
	// Missing or replaced ownership reports ErrCreativeLeaseLost, so workers
	// acknowledge only the reservation whose processing they completed.
	Ack(ctx context.Context, runID, leaseToken string) error
	// Heartbeat renews the active timestamp for a matching reservation and
	// reports whether it remains owned. A stale token or absent active entry
	// cannot recreate it after acknowledgment or requeue has removed that
	// membership.
	Heartbeat(ctx context.Context, runID, leaseToken string) (bool, error)
	// MoveDueDelayedToReady moves one bounded batch whose delayed availability is
	// due to ready work. Returned counts describe moved entries, and callers poll
	// again after nonempty batches or wait before another empty attempt.
	MoveDueDelayedToReady(ctx context.Context, limit int) (int, error)
	// RecoverStaleActive moves one bounded batch of old active timestamps back to
	// ready. Recovery clears their prior lease ownership, so previously reserved
	// workers cannot acknowledge a later reservation using stale tokens.
	RecoverStaleActive(ctx context.Context, staleAfter time.Duration, limit int) (int, error)
	// TryAcquireJobLock attempts a separate task lock and returns a handle only
	// after acquisition. Callers release an acquired handle after processing and
	// can renew it through CreativeRunJobLockRefresher while work remains active.
	TryAcquireJobLock(ctx context.Context, runID string, ttl time.Duration) (CreativeRunJobLock, bool, error)
}

// CreativeTransientStore stores task payloads and media with expiration.
// Payloads and image bytes expire with their TTL; the database stores task metadata.
type CreativeTransientStore interface {
	// SavePayload stores execution prompt and metadata with the transient store
	// lifetime. Source and mask bytes belong in their separate storage
	// operations; callers save those before marking provisioning transient_saved.
	SavePayload(ctx context.Context, runID string, payload *CreativeRunPayload) error
	// LoadPayload returns decoded execution metadata or an error distinguishing
	// absence, corruption, and store unavailability. Callers separately load
	// referenced input and mask bytes before dispatching a provider request.
	LoadPayload(ctx context.Context, runID string) (*CreativeRunPayload, error)
	// SaveInput stores nonempty source bytes under the run and supplied input
	// index. The bytes expire with the store lifetime; callers keep the original
	// index order for later payload SourceCount loading.
	SaveInput(ctx context.Context, runID string, idx int, data []byte) error
	// LoadInputs loads the requested indexed source sequence or reports a missing
	// source. Nonpositive counts yield no inputs in a configured store, and
	// storage failures remain distinct from confirmed absence.
	LoadInputs(ctx context.Context, runID string, count int) ([][]byte, error)
	// SaveMask stores nonempty mask bytes with the transient store lifetime.
	// Callers pair this write with payload HasMask metadata so worker input
	// loading can reconstruct the mask before provider execution.
	SaveMask(ctx context.Context, runID string, data []byte) error
	// LoadMask returns the stored mask bytes or a missing/unavailable storage
	// error. Callers infer or retain MIME separately because this byte operation
	// carries no mask metadata.
	LoadMask(ctx context.Context, runID string) ([]byte, error)
	// SaveOutput stores nonempty generated bytes for the supplied output index
	// and TTL. Nonpositive TTL uses the store default, while callers record an
	// aligned metadata deadline for public delivery checks.
	SaveOutput(ctx context.Context, runID string, index int, data []byte, ttl time.Duration) error
	// LoadOutput returns image bytes or distinguishes missing, corrupt, and
	// unavailable storage. Confirmed absence can classify delivery loss, while a
	// storage outage remains an error for result recovery.
	LoadOutput(ctx context.Context, runID string, index int) ([]byte, error)
	// DeleteOutput removes one transient image key after receipt or cleanup.
	// Missing keys succeed, allowing repeated acknowledgment and later cleanup to
	// retry a previously failed deletion safely.
	DeleteOutput(ctx context.Context, runID string, index int) error
	// DeleteRunTransient removes payload, mask, and the supplied input/output
	// indexes for a task. Nonpositive counts request key discovery;
	// implementations can leave undiscovered keys to their existing TTL when
	// discovery fails.
	DeleteRunTransient(ctx context.Context, runID string, inputCount, outputCount int) error
}
