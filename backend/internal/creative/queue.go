package creative

import (
	"context"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

var (
	ErrCreativeQueueEmpty    = infraerrors.New(infraerrors.CategoryNotFound, "CREATIVE_QUEUE_EMPTY", "creative queue is empty")
	ErrCreativeAlreadyQueued = infraerrors.New(infraerrors.CategoryConflict, "CREATIVE_ALREADY_QUEUED", "creative run is already queued")

	ErrCreativeLeaseLost           = infraerrors.New(infraerrors.CategoryConflict, "CREATIVE_LEASE_LOST", "creative run lease is no longer owned")
	ErrInvalidCreativeQueuePayload = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_QUEUE_INVALID_PAYLOAD", "invalid creative queue payload")
)

// ReservedCreativeRun identifies a reserved task and the lease token required for queue updates.
type ReservedCreativeRun struct {
	RunID      string
	LeaseToken string
}

// CreativeRunJobLock is a task-level distributed lock that the holder releases after processing.
type CreativeRunJobLock interface {
	Release(ctx context.Context) error
}

// CreativeRunJobLockRefresher optionally renews a task lock during processing.
type CreativeRunJobLockRefresher interface {
	Refresh(ctx context.Context, ttl time.Duration) (bool, error)
}

// CreativeRunQueue coordinates ready, delayed, and active tasks using lease tokens.
// The Redis implementation is in internal/creative/rediscache/queue.go.
type CreativeRunQueue interface {
	Enqueue(ctx context.Context, runID string) error
	Reserve(ctx context.Context, blockTimeout time.Duration) (ReservedCreativeRun, error)
	RequeueAfter(ctx context.Context, runID, leaseToken string, delay time.Duration) error
	Ack(ctx context.Context, runID, leaseToken string) error
	Heartbeat(ctx context.Context, runID, leaseToken string) (bool, error)
	MoveDueDelayedToReady(ctx context.Context, limit int) (int, error)
	RecoverStaleActive(ctx context.Context, staleAfter time.Duration, limit int) (int, error)
	TryAcquireJobLock(ctx context.Context, runID string, ttl time.Duration) (CreativeRunJobLock, bool, error)
}

// CreativeTransientStore stores task payloads and media with expiration.
// Payloads and image bytes expire with their TTL; the database stores task metadata.
type CreativeTransientStore interface {
	// SavePayload stores the prompt and payload metadata for the configured transient TTL.
	SavePayload(ctx context.Context, runID string, payload *CreativeRunPayload) error
	// LoadPayload returns the payload or ErrCreativeTransientNotFound when absent or expired.
	LoadPayload(ctx context.Context, runID string) (*CreativeRunPayload, error)
	// SaveInput stores source image bytes at the zero-based index.
	SaveInput(ctx context.Context, runID string, idx int, data []byte) error
	// LoadInputs returns count source images or an error if any image is missing.
	LoadInputs(ctx context.Context, runID string, count int) ([][]byte, error)
	// SaveMask stores mask bytes for the configured transient TTL.
	SaveMask(ctx context.Context, runID string, data []byte) error
	// LoadMask returns mask bytes or ErrCreativeTransientNotFound when absent.
	LoadMask(ctx context.Context, runID string) ([]byte, error)
	// SaveOutput stores one output image with the supplied TTL, using the default when nonpositive.
	SaveOutput(ctx context.Context, runID string, index int, data []byte, ttl time.Duration) error
	// LoadOutput returns image bytes or ErrCreativeTransientNotFound when absent or expired.
	LoadOutput(ctx context.Context, runID string, index int) ([]byte, error)
	// DeleteOutput removes one output after acknowledgment or during cleanup; missing keys are harmless.
	DeleteOutput(ctx context.Context, runID string, index int) error
	// DeleteRunTransient removes payload, inputs, mask, and outputs; nonpositive counts trigger key discovery.
	DeleteRunTransient(ctx context.Context, runID string, inputCount, outputCount int) error
}
