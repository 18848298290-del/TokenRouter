package batchimage

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// queue.go defines batch-image queue reservations, lease operations, and queue
// failure categories.
// Queue reservations identify persistent tasks by their public batch IDs. The
// queue coordinates ready, delayed and active records while a separate lease
// admits a worker's progression visit; PostgreSQL continues to own the
// lifecycle state consumed by that worker.
//
// Held-token operations compare ownership before changing queue activity,
// acknowledgement or retry scheduling. The queue-level variants are reserved
// for jobs with no current lock, which lets failed acquisition attempt
// recovery while a successor's token protects its records. Refresh is an
// optional lease capability used by the worker heartbeat. Ownership loss is
// reported distinctly from an empty queue or duplicate enqueue so RunOnce can
// choose the corresponding cancellation or retry path.

// Queue errors distinguish an ownership failure, idle reservation, duplicate
// inflight membership and invalid payload so worker visits can respond
// appropriately.
var (
	ErrBatchImageLeaseLost     = infraerrors.Conflict("BATCH_IMAGE_LEASE_LOST", "batch image job lease is no longer owned")
	ErrBatchImageQueueEmpty    = infraerrors.New(infraerrors.CategoryNotFound, "BATCH_IMAGE_QUEUE_EMPTY", "batch image queue is empty")
	ErrBatchImageAlreadyQueued = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_ALREADY_QUEUED", "batch image job is already queued")

	ErrInvalidBatchImageQueuePayload = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_QUEUE_INVALID_PAYLOAD", "invalid batch image queue payload")
)

// ReservedBatchImageJob carries the persistent batch identity reserved by a
// queue visit.
type ReservedBatchImageJob struct {
	BatchID string // public batch identifier
}

// BatchImageJobLock guards owned queue mutations for one worker lease and
// batch.
type BatchImageJobLock interface {
	// Release relinquishes this lock when its token still owns the job. A lost
	// token leaves a successor's lock intact, and worker cleanup can call it
	// after processing cancellation.
	Release(ctx context.Context) error
	// Heartbeat refreshes active queue activity while the held token still owns
	// the job. Ownership loss or an unavailable write returns an error so the
	// worker cancels further progression.
	Heartbeat(context.Context) error
	// Ack removes active, delayed and inflight bookkeeping while this held token
	// owns the task. Ownership comparison and mutation happen together, so a
	// stale worker receives ErrBatchImageLeaseLost.
	Ack(context.Context) error
	// RequeueAfter reschedules the task through this held token after its visit
	// finishes. Delays shorter than one millisecond become ready immediately; an
	// ownership mismatch returns ErrBatchImageLeaseLost and leaves successor
	// queue records intact.
	RequeueAfter(context.Context, time.Duration) error
}

// BatchImageQueue coordinates ready, delayed and active batch visits around
// persistent job state.
type BatchImageQueue interface {
	// Enqueue publishes a valid batch ID into the ready queue with an inflight
	// marker. Duplicate inflight membership returns ErrBatchImageAlreadyQueued
	// so idempotent submissions can accept the existing queue visit.
	Enqueue(ctx context.Context, batchID string) error
	// Reserve obtains a ready batch ID and records active activity before
	// returning it. The caller then acquires its job lock. Empty polls retry
	// within blockTimeout and then return ErrBatchImageQueueEmpty; Redis-call
	// latency and cancellation still follow ctx.
	Reserve(ctx context.Context, blockTimeout time.Duration) (ReservedBatchImageJob, error)
	// RequeueAfter reschedules a task with no current job lock. Delays of at
	// least one millisecond use delayed membership; shorter delays become ready,
	// and a current lock rejects mutation with ErrBatchImageLeaseLost.
	RequeueAfter(ctx context.Context, batchID string, delay time.Duration) error
	// Ack removes queue bookkeeping for a task with no current job lock. An
	// existing owner causes ErrBatchImageLeaseLost, so processing workers
	// acknowledge through their token-bearing lock handle instead.
	Ack(ctx context.Context, batchID string) error
	// Heartbeat updates active activity for a task with no current job lock. An
	// existing owner causes ErrBatchImageLeaseLost, so lease holders refresh
	// activity through their token-bearing lock handle.
	Heartbeat(ctx context.Context, batchID string) error
	// MoveDueDelayedToReady transfers due delayed entries into the ready queue
	// and returns the moved count. The limit bounds one scan, allowing
	// maintenance to drain positive batches before its next idle wait.
	MoveDueDelayedToReady(ctx context.Context, limit int) (int, error)
	// RecoverStaleActive transfers active entries older than staleAfter into the
	// ready queue and returns their count. The age must be positive; a later
	// worker still needs the task lock before progression.
	RecoverStaleActive(ctx context.Context, staleAfter time.Duration, limit int) (int, error)
	// TryAcquireJobLock attempts a new token-bearing lease for a valid batch ID
	// and duration. False means another lock already owns the task; a successful
	// caller eventually releases its returned handle.
	TryAcquireJobLock(ctx context.Context, batchID string, ttl time.Duration) (BatchImageJobLock, bool, error)
}

// BatchImageJobLockRefresher optionally extends a held lease while checking
// its current ownership.
type BatchImageJobLockRefresher interface {
	// Refresh extends this held lock's TTL when its token still matches the
	// stored owner. A mismatch returns ErrBatchImageLeaseLost, and the worker
	// treats other renewal errors as unconfirmed ownership.
	Refresh(context.Context, time.Duration) error
}

// IsValidBatchImageID checks the queue payload for an imgbatch_ prefix
// followed by at least one character. It accepts the suffix as supplied;
// identifier generation applies the stronger random hexadecimal format.
func IsValidBatchImageID(batchID string) bool {
	return strings.HasPrefix(batchID, "imgbatch_") && len(batchID) > len("imgbatch_")
}
